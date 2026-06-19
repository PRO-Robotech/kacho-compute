package repo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PRO-Robotech/kacho-corelib/filter"
	"github.com/PRO-Robotech/kacho-corelib/validate"
	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/fgaintent"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// ImageRepo — реализация service.ImageRepo поверх pgxpool.
type ImageRepo struct {
	pool *pgxpool.Pool
}

// NewImageRepo создаёт ImageRepo.
func NewImageRepo(pool *pgxpool.Pool) *ImageRepo { return &ImageRepo{pool: pool} }

const imageCols = `id, project_id, created_at, name, description, labels, family, storage_size, min_disk_size, product_ids, ` +
	`status, os_type, os_nvidia_driver, pooled, hardware_generation, kms_key, source_image_id, source_snapshot_id, source_disk_id, source_uri`

// Get возвращает образ по id.
func (r *ImageRepo) Get(ctx context.Context, id string) (*domain.Image, error) {
	q := fmt.Sprintf(`SELECT %s FROM images WHERE id = $1`, imageCols)
	i, err := scanImage(r.pool.QueryRow(ctx, q, id))
	if err != nil {
		return nil, wrapPgErr(err, "Image", id)
	}
	return i, nil
}

// GetLatestByFamily возвращает образ с max created_at в family внутри folder.
func (r *ImageRepo) GetLatestByFamily(ctx context.Context, folderID, family string) (*domain.Image, error) {
	q := fmt.Sprintf(`SELECT %s FROM images WHERE project_id = $1 AND family = $2 ORDER BY created_at DESC, id DESC LIMIT 1`, imageCols)
	i, err := scanImage(r.pool.QueryRow(ctx, q, folderID, family))
	if err != nil {
		return nil, wrapPgErr(err, "Image", family)
	}
	return i, nil
}

// List возвращает образы по folder с cursor-pagination.
func (r *ImageRepo) List(ctx context.Context, f service.ImageFilter, p service.Pagination) ([]*domain.Image, string, error) {
	pageSize, err := validate.PageSize("page_size", p.PageSize)
	if err != nil {
		return nil, "", err
	}
	var args []any
	var conditions []string
	argIdx := 1
	if f.ProjectID != "" {
		conditions = append(conditions, fmt.Sprintf("project_id = $%d", argIdx))
		args = append(args, f.ProjectID)
		argIdx++
	}
	if f.AllowedIDs != nil {
		if len(f.AllowedIDs) == 0 {
			return nil, "", nil
		}
		conditions = append(conditions, fmt.Sprintf("id = ANY($%d::text[])", argIdx))
		args = append(args, f.AllowedIDs)
		argIdx++
	}
	if f.Filter != "" {
		ast, perr := filter.Parse(f.Filter, []string{"name"})
		if perr != nil {
			return nil, "", invalidFilterErr(perr)
		}
		if ast != nil {
			frag, fargs := ast.ToSQL(argIdx)
			conditions = append(conditions, frag)
			args = append(args, fargs...)
			argIdx += len(fargs)
		}
	}
	if p.PageToken != "" {
		tsv, id, derr := decodePageToken(p.PageToken)
		if derr != nil {
			return nil, "", invalidPageTokenErr(derr)
		}
		conditions = append(conditions, fmt.Sprintf("(created_at, id) > ($%d, $%d)", argIdx, argIdx+1))
		args = append(args, tsv, id)
		argIdx += 2
	}
	var where string
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}
	q := fmt.Sprintf(`SELECT %s FROM images %s ORDER BY created_at ASC, id ASC LIMIT $%d`, imageCols, where, argIdx)
	args = append(args, pageSize+1)

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", wrapPgErr(err, "Image", "")
	}
	defer rows.Close()
	var result []*domain.Image
	for rows.Next() {
		i, serr := scanImage(rows)
		if serr != nil {
			return nil, "", wrapPgErr(serr, "Image", "")
		}
		result = append(result, i)
	}
	if err := rows.Err(); err != nil {
		return nil, "", wrapPgErr(err, "Image", "")
	}
	var nextToken string
	if int64(len(result)) > pageSize {
		last := result[pageSize-1]
		nextToken = encodePageToken(last.CreatedAt, last.ID)
		result = result[:pageSize]
	}
	return result, nextToken, nil
}

// Insert вставляет образ + outbox-event Image CREATED.
func (r *ImageRepo) Insert(ctx context.Context, i *domain.Image) (*domain.Image, error) {
	args, err := imageInsertArgs(i)
	if err != nil {
		return nil, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, service.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()
	const q = `INSERT INTO images (id, project_id, created_at, name, description, labels, family, storage_size, min_disk_size, product_ids,
		status, os_type, os_nvidia_driver, pooled, hardware_generation, kms_key, source_image_id, source_snapshot_id, source_disk_id, source_uri)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20) RETURNING ` + imageCols
	result, err := scanImage(tx.QueryRow(ctx, q, args...))
	if err != nil {
		return nil, wrapPgErr(err, "Image", i.Name)
	}
	if err := emitCompute(ctx, tx, "Image", result.ID, "CREATED", imagePayload(result)); err != nil {
		return nil, service.ErrInternal
	}
	// SEC-D: FGA owner-tuple register-intent in the SAME writer-tx (no dual-write).
	if err := emitFGARegisterIntent(ctx, tx, fgaintent.EventRegister, "Image", result.ID, result.ProjectID, result.Labels); err != nil {
		return nil, service.ErrInternal
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapPgErr(err, "Image", i.Name)
	}
	return result, nil
}

// Update обновляет mutable поля образа + outbox-event Image UPDATED.
func (r *ImageRepo) Update(ctx context.Context, i *domain.Image) (*domain.Image, error) {
	labelsJSON, err := marshalJSONB(i.Labels, "Image.labels")
	if err != nil {
		return nil, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, service.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()
	const q = `UPDATE images SET name=$2, description=$3, labels=$4, min_disk_size=$5 WHERE id=$1 RETURNING ` + imageCols
	result, err := scanImage(tx.QueryRow(ctx, q, i.ID, i.Name, i.Description, labelsJSON, i.MinDiskSize))
	if err != nil {
		return nil, wrapPgErr(err, "Image", i.ID)
	}
	if err := emitCompute(ctx, tx, "Image", result.ID, "UPDATED", imagePayload(result)); err != nil {
		return nil, service.ErrInternal
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapPgErr(err, "Image", i.ID)
	}
	return result, nil
}

// Delete удаляет образ + outbox-event Image DELETED.
func (r *ImageRepo) Delete(ctx context.Context, id string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return service.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// DELETE … RETURNING project_id so the FGA unregister-intent can build the
	// project-hierarchy tuple of the just-deleted resource within the same tx.
	var projectID string
	err = tx.QueryRow(ctx, `DELETE FROM images WHERE id = $1 RETURNING project_id`, id).Scan(&projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: Image %s not found", service.ErrNotFound, id)
		}
		return wrapPgErr(err, "Image", id)
	}
	if err := emitCompute(ctx, tx, "Image", id, "DELETED", map[string]any{"id": id}); err != nil {
		return service.ErrInternal
	}
	// SEC-D: symmetric FGA unregister-intent in the SAME writer-tx.
	if err := emitFGARegisterIntent(ctx, tx, fgaintent.EventUnregister, "Image", id, projectID, nil); err != nil {
		return service.ErrInternal
	}
	if err := tx.Commit(ctx); err != nil {
		return wrapPgErr(err, "Image", id)
	}
	return nil
}

// ---- scan / args ----

func imageInsertArgs(i *domain.Image) ([]any, error) {
	labelsJSON, err := marshalJSONB(i.Labels, "Image.labels")
	if err != nil {
		return nil, err
	}
	prodJSON, err := marshalJSONB(orEmptySlice(i.ProductIDs), "Image.product_ids")
	if err != nil {
		return nil, err
	}
	hgJSON, err := marshalProtoJSONB(i.HardwareGeneration, "Image.hardware_generation")
	if err != nil {
		return nil, err
	}
	kmsJSON, err := marshalProtoJSONB(i.KMSKey, "Image.kms_key")
	if err != nil {
		return nil, err
	}
	return []any{
		i.ID, i.ProjectID, i.CreatedAt, i.Name, i.Description, labelsJSON, i.Family, i.StorageSize, i.MinDiskSize, prodJSON,
		imageStatusName(i.Status), osTypeName(i.OsType), i.OsNvidiaDriver, i.Pooled, hgJSON, kmsJSON,
		i.SourceImageID, i.SourceSnapshotID, i.SourceDiskID, i.SourceURI,
	}, nil
}

func scanImage(row scannable) (*domain.Image, error) {
	var i domain.Image
	var labelsJSON, prodJSON, hgJSON, kmsJSON []byte
	var statusName, osTypeStr string
	if err := row.Scan(
		&i.ID, &i.ProjectID, &i.CreatedAt, &i.Name, &i.Description, &labelsJSON, &i.Family, &i.StorageSize, &i.MinDiskSize, &prodJSON,
		&statusName, &osTypeStr, &i.OsNvidiaDriver, &i.Pooled, &hgJSON, &kmsJSON,
		&i.SourceImageID, &i.SourceSnapshotID, &i.SourceDiskID, &i.SourceURI,
	); err != nil {
		return nil, err
	}
	if err := unmarshalJSONB(labelsJSON, &i.Labels, "Image.labels"); err != nil {
		return nil, err
	}
	if err := unmarshalJSONB(prodJSON, &i.ProductIDs, "Image.product_ids"); err != nil {
		return nil, err
	}
	i.Status = imageStatusFromName(statusName)
	i.OsType = osTypeFromName(osTypeStr)
	if len(hgJSON) > 0 {
		i.HardwareGeneration = &computev1.HardwareGeneration{}
		if err := unmarshalProtoJSONB(hgJSON, i.HardwareGeneration, "Image.hardware_generation"); err != nil {
			return nil, err
		}
	}
	if len(kmsJSON) > 0 {
		i.KMSKey = &computev1.KMSKey{}
		if err := unmarshalProtoJSONB(kmsJSON, i.KMSKey, "Image.kms_key"); err != nil {
			return nil, err
		}
	}
	return &i, nil
}

func imageStatusName(s domain.ImageStatus) string {
	switch s {
	case domain.ImageStatusCreating:
		return "CREATING"
	case domain.ImageStatusReady:
		return "READY"
	case domain.ImageStatusError:
		return "ERROR"
	case domain.ImageStatusDeleting:
		return "DELETING"
	default:
		return "STATUS_UNSPECIFIED"
	}
}

func imageStatusFromName(s string) domain.ImageStatus {
	switch s {
	case "CREATING":
		return domain.ImageStatusCreating
	case "READY":
		return domain.ImageStatusReady
	case "ERROR":
		return domain.ImageStatusError
	case "DELETING":
		return domain.ImageStatusDeleting
	default:
		return domain.ImageStatusUnspecified
	}
}

func osTypeName(t domain.OsType) string {
	switch t {
	case domain.OsTypeLinux:
		return "LINUX"
	case domain.OsTypeWindows:
		return "WINDOWS"
	default:
		return "TYPE_UNSPECIFIED"
	}
}

func osTypeFromName(s string) domain.OsType {
	switch s {
	case "LINUX":
		return domain.OsTypeLinux
	case "WINDOWS":
		return domain.OsTypeWindows
	default:
		return domain.OsTypeUnspecified
	}
}
