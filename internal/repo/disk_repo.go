package repo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	computev1 "github.com/PRO-Robotech/kacho-compute/proto/gen/go/kacho/cloud/compute/v1"
	"github.com/PRO-Robotech/kacho-corelib/filter"
	"github.com/PRO-Robotech/kacho-corelib/validate"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/fgaintent"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// DiskRepo — реализация service.DiskRepo поверх pgxpool.
type DiskRepo struct {
	pool *pgxpool.Pool
}

// NewDiskRepo создаёт DiskRepo.
func NewDiskRepo(pool *pgxpool.Pool) *DiskRepo { return &DiskRepo{pool: pool} }

const diskCols = `id, project_id, created_at, name, description, labels, type_id, zone_id, size, block_size, ` +
	`product_ids, status, source_image_id, source_snapshot_id, disk_placement_policy, hardware_generation, kms_key`

// Get возвращает диск по id (+ instance_ids из attached_disks).
func (r *DiskRepo) Get(ctx context.Context, id string) (*domain.Disk, error) {
	q := fmt.Sprintf(`SELECT %s FROM disks WHERE id = $1`, diskCols)
	d, err := scanDisk(r.pool.QueryRow(ctx, q, id))
	if err != nil {
		return nil, wrapPgErr(err, "Disk", id)
	}
	if err := r.fillInstanceIDs(ctx, d); err != nil {
		return nil, err
	}
	return d, nil
}

// List возвращает диски по folder с cursor-pagination.
func (r *DiskRepo) List(ctx context.Context, f service.DiskFilter, p service.Pagination) ([]*domain.Disk, string, error) {
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
		// FGA filter (KAC-127 Phase 4). Empty allow-list must be short-circuited
		// in the service-layer (no DB query); we defend here too.
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
	q := fmt.Sprintf(`SELECT %s FROM disks %s ORDER BY created_at ASC, id ASC LIMIT $%d`, diskCols, where, argIdx)
	args = append(args, pageSize+1)

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", wrapPgErr(err, "Disk", "")
	}
	defer rows.Close()
	var result []*domain.Disk
	for rows.Next() {
		d, serr := scanDisk(rows)
		if serr != nil {
			return nil, "", wrapPgErr(serr, "Disk", "")
		}
		result = append(result, d)
	}
	if err := rows.Err(); err != nil {
		return nil, "", wrapPgErr(err, "Disk", "")
	}
	var nextToken string
	if int64(len(result)) > pageSize {
		last := result[pageSize-1]
		nextToken = encodePageToken(last.CreatedAt, last.ID)
		result = result[:pageSize]
	}
	for _, d := range result {
		if err := r.fillInstanceIDs(ctx, d); err != nil {
			return nil, "", err
		}
	}
	return result, nextToken, nil
}

// Insert вставляет диск + outbox-event Disk CREATED.
func (r *DiskRepo) Insert(ctx context.Context, d *domain.Disk) (*domain.Disk, error) {
	args, err := diskInsertArgs(d)
	if err != nil {
		return nil, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, service.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const q = `INSERT INTO disks (id, project_id, created_at, name, description, labels, type_id, zone_id, size, block_size,
		product_ids, status, source_image_id, source_snapshot_id, disk_placement_policy, hardware_generation, kms_key)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17) RETURNING ` + diskCols
	result, err := scanDisk(tx.QueryRow(ctx, q, args...))
	if err != nil {
		return nil, wrapPgErr(err, "Disk", d.Name)
	}
	if err := emitCompute(ctx, tx, "Disk", result.ID, "CREATED", diskPayload(result)); err != nil {
		return nil, service.ErrInternal
	}
	// SEC-D: FGA owner-tuple register-intent in the SAME writer-tx (no dual-write).
	if err := emitFGARegisterIntent(ctx, tx, fgaintent.EventRegister, "Disk", result.ID, result.ProjectID, result.Labels); err != nil {
		return nil, service.ErrInternal
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapPgErr(err, "Disk", d.Name)
	}
	return result, nil
}

// Update обновляет mutable поля диска + outbox-event Disk UPDATED.
//
// emitLabelsRegister (#113 / T3.1, parity с InstanceRepo.Update): когда true (use-case
// увидел "labels" в update-mask, либо full-object PATCH применяет labels) — в той же
// writer-tx, что и UPDATE, эмитится свежий FGA register-intent с текущими labels +
// parent-scope (atomic, ban #10), чтобы IAM resource_mirror не протух и ARM_LABELS-грант
// ревокался при снятии/смене метки. Полное снятие меток (labels={}) эмитит mirror.upsert
// с пустыми labels (НЕ Unregister — ресурс жив, G-3). Когда false (name/description/size
// без labels) — register-intent НЕ эмитится (G-2: меньше reconcile-шума).
func (r *DiskRepo) Update(ctx context.Context, d *domain.Disk, emitLabelsRegister bool) (*domain.Disk, error) {
	labelsJSON, err := marshalJSONB(d.Labels, "Disk.labels")
	if err != nil {
		return nil, err
	}
	dppJSON, err := marshalProtoJSONB(d.DiskPlacementPolicy, "Disk.disk_placement_policy")
	if err != nil {
		return nil, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, service.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const q = `UPDATE disks SET name=$2, description=$3, labels=$4, size=$5, disk_placement_policy=$6 WHERE id=$1 RETURNING ` + diskCols
	result, err := scanDisk(tx.QueryRow(ctx, q, d.ID, d.Name, d.Description, labelsJSON, d.Size, dppJSON))
	if err != nil {
		return nil, wrapPgErr(err, "Disk", d.ID)
	}
	if err := emitCompute(ctx, tx, "Disk", result.ID, "UPDATED", diskPayload(result)); err != nil {
		return nil, service.ErrInternal
	}
	// #113 / T3.1: refresh the IAM resource_mirror only when labels were in the
	// update-mask. mirror.upsert (EventRegister) with the CURRENT labels in the SAME
	// writer-tx (atomic, ban #10); empty labels → upsert {}, NOT unregister (G-3).
	if emitLabelsRegister {
		if err := emitFGARegisterIntent(ctx, tx, fgaintent.EventRegister, "Disk", result.ID, result.ProjectID, result.Labels); err != nil {
			return nil, service.ErrInternal
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapPgErr(err, "Disk", d.ID)
	}
	return result, nil
}

// SetZoneID меняет zone_id (для Relocate).
func (r *DiskRepo) SetZoneID(ctx context.Context, id, zoneID string) (*domain.Disk, error) {
	return r.simpleSet(ctx, id, "zone_id", zoneID)
}

func (r *DiskRepo) simpleSet(ctx context.Context, id, col, val string) (*domain.Disk, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, service.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := fmt.Sprintf(`UPDATE disks SET %s = $2 WHERE id = $1 RETURNING %s`, col, diskCols)
	result, err := scanDisk(tx.QueryRow(ctx, q, id, val))
	if err != nil {
		return nil, wrapPgErr(err, "Disk", id)
	}
	if err := emitCompute(ctx, tx, "Disk", result.ID, "UPDATED", diskPayload(result)); err != nil {
		return nil, service.ErrInternal
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapPgErr(err, "Disk", id)
	}
	return result, nil
}

// Delete удаляет диск (23503 → FailedPrecondition если attached) + outbox DELETED.
func (r *DiskRepo) Delete(ctx context.Context, id string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return service.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// DELETE … RETURNING project_id so the FGA unregister-intent (below) can build
	// the project-hierarchy tuple of the just-deleted resource within the same tx.
	var projectID string
	err = tx.QueryRow(ctx, `DELETE FROM disks WHERE id = $1 RETURNING project_id`, id).Scan(&projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: Disk %s not found", service.ErrNotFound, id)
		}
		if isFKViolation(err) {
			return fmt.Errorf("%w: The disk %s is being used", service.ErrFailedPrecondition, id)
		}
		return wrapPgErr(err, "Disk", id)
	}
	if err := emitCompute(ctx, tx, "Disk", id, "DELETED", map[string]any{"id": id}); err != nil {
		return service.ErrInternal
	}
	// SEC-D: symmetric FGA unregister-intent in the SAME writer-tx.
	if err := emitFGARegisterIntent(ctx, tx, fgaintent.EventUnregister, "Disk", id, projectID, nil); err != nil {
		return service.ErrInternal
	}
	if err := tx.Commit(ctx); err != nil {
		return wrapPgErr(err, "Disk", id)
	}
	return nil
}

// IsAttached — true если есть строка attached_disks для disk_id.
func (r *DiskRepo) IsAttached(ctx context.Context, id string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM attached_disks WHERE disk_id = $1)`, id).Scan(&exists)
	if err != nil {
		return false, wrapPgErr(err, "Disk", id)
	}
	return exists, nil
}

func (r *DiskRepo) fillInstanceIDs(ctx context.Context, d *domain.Disk) error {
	rows, err := r.pool.Query(ctx, `SELECT instance_id FROM attached_disks WHERE disk_id = $1 ORDER BY instance_id`, d.ID)
	if err != nil {
		return wrapPgErr(err, "Disk", d.ID)
	}
	defer rows.Close()
	for rows.Next() {
		var iid string
		if err := rows.Scan(&iid); err != nil {
			return wrapPgErr(err, "Disk", d.ID)
		}
		d.InstanceIDs = append(d.InstanceIDs, iid)
	}
	return rows.Err()
}

// ---- scan / args ----

func diskInsertArgs(d *domain.Disk) ([]any, error) {
	labelsJSON, err := marshalJSONB(d.Labels, "Disk.labels")
	if err != nil {
		return nil, err
	}
	prodJSON, err := marshalJSONB(orEmptySlice(d.ProductIDs), "Disk.product_ids")
	if err != nil {
		return nil, err
	}
	dppJSON, err := marshalProtoJSONB(d.DiskPlacementPolicy, "Disk.disk_placement_policy")
	if err != nil {
		return nil, err
	}
	hgJSON, err := marshalProtoJSONB(d.HardwareGeneration, "Disk.hardware_generation")
	if err != nil {
		return nil, err
	}
	kmsJSON, err := marshalProtoJSONB(d.KMSKey, "Disk.kms_key")
	if err != nil {
		return nil, err
	}
	return []any{
		d.ID, d.ProjectID, d.CreatedAt, d.Name, d.Description, labelsJSON, d.TypeID, d.ZoneID, d.Size, d.BlockSize,
		prodJSON, diskStatusName(d.Status), d.SourceImageID, d.SourceSnapshotID, dppJSON, hgJSON, kmsJSON,
	}, nil
}

func scanDisk(row scannable) (*domain.Disk, error) {
	var d domain.Disk
	var labelsJSON, prodJSON, dppJSON, hgJSON, kmsJSON []byte
	var statusName string
	if err := row.Scan(
		&d.ID, &d.ProjectID, &d.CreatedAt, &d.Name, &d.Description, &labelsJSON, &d.TypeID, &d.ZoneID, &d.Size, &d.BlockSize,
		&prodJSON, &statusName, &d.SourceImageID, &d.SourceSnapshotID, &dppJSON, &hgJSON, &kmsJSON,
	); err != nil {
		return nil, err
	}
	if err := unmarshalJSONB(labelsJSON, &d.Labels, "Disk.labels"); err != nil {
		return nil, err
	}
	if err := unmarshalJSONB(prodJSON, &d.ProductIDs, "Disk.product_ids"); err != nil {
		return nil, err
	}
	d.Status = diskStatusFromName(statusName)
	if len(dppJSON) > 0 {
		d.DiskPlacementPolicy = &computev1.DiskPlacementPolicy{}
		if err := unmarshalProtoJSONB(dppJSON, d.DiskPlacementPolicy, "Disk.disk_placement_policy"); err != nil {
			return nil, err
		}
	}
	if len(hgJSON) > 0 {
		d.HardwareGeneration = &computev1.HardwareGeneration{}
		if err := unmarshalProtoJSONB(hgJSON, d.HardwareGeneration, "Disk.hardware_generation"); err != nil {
			return nil, err
		}
	}
	if len(kmsJSON) > 0 {
		d.KMSKey = &computev1.KMSKey{}
		if err := unmarshalProtoJSONB(kmsJSON, d.KMSKey, "Disk.kms_key"); err != nil {
			return nil, err
		}
	}
	return &d, nil
}

func diskStatusName(s domain.DiskStatus) string {
	switch s {
	case domain.DiskStatusCreating:
		return "CREATING"
	case domain.DiskStatusReady:
		return "READY"
	case domain.DiskStatusError:
		return "ERROR"
	case domain.DiskStatusDeleting:
		return "DELETING"
	default:
		return "STATUS_UNSPECIFIED"
	}
}

func diskStatusFromName(s string) domain.DiskStatus {
	switch s {
	case "CREATING":
		return domain.DiskStatusCreating
	case "READY":
		return domain.DiskStatusReady
	case "ERROR":
		return domain.DiskStatusError
	case "DELETING":
		return domain.DiskStatusDeleting
	default:
		return domain.DiskStatusUnspecified
	}
}

func orEmptySlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
