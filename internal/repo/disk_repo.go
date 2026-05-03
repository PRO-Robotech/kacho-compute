package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// DiskRepo — реализация service.DiskRepo поверх pgxpool.
type DiskRepo struct {
	pool *pgxpool.Pool
}

// NewDiskRepo создаёт DiskRepo.
func NewDiskRepo(pool *pgxpool.Pool) *DiskRepo {
	return &DiskRepo{pool: pool}
}

const diskSelectCols = `
	id, folder_id, name, description, created_at, labels,
	disk_type_id, zone_id, size, image_id,
	status, attached_to_instance_id,
	generation, resource_version, observed_generation,
	status_last_transition_at, deleted_at`

func (r *DiskRepo) Get(ctx context.Context, id string) (*domain.Disk, error) {
	q := fmt.Sprintf(`SELECT %s FROM disks WHERE id = $1 AND deleted_at IS NULL`, diskSelectCols)
	row := r.pool.QueryRow(ctx, q, id)
	d, err := scanDisk(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, service.ErrNotFound
	}
	return d, err
}

func (r *DiskRepo) GetIncludingDeleted(ctx context.Context, id string) (*domain.Disk, error) {
	q := fmt.Sprintf(`SELECT %s FROM disks WHERE id = $1`, diskSelectCols)
	row := r.pool.QueryRow(ctx, q, id)
	d, err := scanDisk(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, service.ErrNotFound
	}
	return d, err
}

func (r *DiskRepo) List(ctx context.Context, f service.DiskFilter, page service.Pagination) ([]*domain.Disk, string, error) {
	pageSize := page.PageSize
	if pageSize <= 0 || pageSize > 1000 {
		pageSize = 50
	}

	args := []any{}
	conditions := []string{"deleted_at IS NULL"}
	argIdx := 1

	if f.FolderID != "" {
		conditions = append(conditions, fmt.Sprintf("folder_id = $%d", argIdx))
		args = append(args, f.FolderID)
		argIdx++
	}
	if page.PageToken != "" {
		ts, id, err := decodePageToken(page.PageToken)
		if err != nil {
			return nil, "", fmt.Errorf("invalid page_token: %w", err)
		}
		conditions = append(conditions, fmt.Sprintf("(created_at, id) > ($%d, $%d)", argIdx, argIdx+1))
		args = append(args, ts, id)
		argIdx += 2
	}

	where := "WHERE " + strings.Join(conditions, " AND ")
	orderClause := "created_at ASC, id ASC"
	if f.OrderBy != "" {
		orderClause = sanitizeOrderBy(f.OrderBy)
	}

	q := fmt.Sprintf(`SELECT %s FROM disks %s ORDER BY %s LIMIT $%d`,
		diskSelectCols, where, orderClause, argIdx)
	args = append(args, pageSize+1)

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var result []*domain.Disk
	for rows.Next() {
		d, err := scanDisk(rows)
		if err != nil {
			return nil, "", err
		}
		result = append(result, d)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var nextToken string
	if int64(len(result)) > pageSize {
		last := result[pageSize-1]
		nextToken = encodePageToken(last.CreatedAt, last.ID)
		result = result[:pageSize]
	}
	return result, nextToken, nil
}

func (r *DiskRepo) Insert(ctx context.Context, d *domain.Disk) (*domain.Disk, error) {
	labelsJSON, _ := json.Marshal(d.Labels)

	const q = `
		INSERT INTO disks (
			id, folder_id, name, description, created_at, labels,
			disk_type_id, zone_id, size, image_id,
			status, attached_to_instance_id,
			generation, resource_version, observed_generation,
			status_last_transition_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,
			$7,$8,$9,$10,
			$11,$12,
			$13,$14,$15,
			$16
		)`

	_, err := r.pool.Exec(ctx, q,
		d.ID, d.FolderID, d.Name, d.Description, d.CreatedAt, labelsJSON,
		d.DiskTypeID, d.ZoneID, d.Size, nullStr(d.ImageID),
		int32(d.Status), nullStr(d.AttachedToInstanceID),
		d.Generation, d.ResourceVersion, d.ObservedGeneration,
		d.StatusLastTransitionAt,
	)
	if err != nil {
		return nil, err
	}
	return r.GetIncludingDeleted(ctx, d.ID)
}

func (r *DiskRepo) Update(ctx context.Context, d *domain.Disk) (*domain.Disk, error) {
	labelsJSON, _ := json.Marshal(d.Labels)

	const q = `
		UPDATE disks SET
			name=$2, description=$3, labels=$4,
			disk_type_id=$5, zone_id=$6, size=$7, image_id=$8,
			status=$9, attached_to_instance_id=$10,
			generation=$11, resource_version=$12, observed_generation=$13,
			status_last_transition_at=$14, deleted_at=$15
		WHERE id=$1`

	_, err := r.pool.Exec(ctx, q,
		d.ID,
		d.Name, d.Description, labelsJSON,
		d.DiskTypeID, d.ZoneID, d.Size, nullStr(d.ImageID),
		int32(d.Status), nullStr(d.AttachedToInstanceID),
		d.Generation, d.ResourceVersion, d.ObservedGeneration,
		d.StatusLastTransitionAt, d.DeletedAt,
	)
	if err != nil {
		return nil, err
	}
	return r.GetIncludingDeleted(ctx, d.ID)
}

func (r *DiskRepo) ListPendingReconcile(ctx context.Context, limit int) ([]*domain.Disk, error) {
	q := fmt.Sprintf(`
		SELECT %s FROM disks
		WHERE status IN (1, 6)
		ORDER BY status_last_transition_at ASC
		LIMIT $1
		FOR UPDATE SKIP LOCKED`, diskSelectCols)

	rows, err := r.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*domain.Disk
	for rows.Next() {
		d, err := scanDisk(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

// HardDelete физически удаляет Disk (вызывается reconciler-ом).
func (r *DiskRepo) HardDelete(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, "DELETE FROM disks WHERE id = $1", id)
	return err
}

// ---- scan helpers ----

func scanDisk(row scannable) (*domain.Disk, error) {
	var d domain.Disk
	var labelsJSON []byte
	var statusInt int32
	var imageID, attachedTo *string

	err := row.Scan(
		&d.ID, &d.FolderID, &d.Name, &d.Description, &d.CreatedAt, &labelsJSON,
		&d.DiskTypeID, &d.ZoneID, &d.Size, &imageID,
		&statusInt, &attachedTo,
		&d.Generation, &d.ResourceVersion, &d.ObservedGeneration,
		&d.StatusLastTransitionAt, &d.DeletedAt,
	)
	if err != nil {
		return nil, err
	}
	d.Status = domain.DiskStatus(statusInt)
	if imageID != nil {
		d.ImageID = *imageID
	}
	if attachedTo != nil {
		d.AttachedToInstanceID = *attachedTo
	}
	if labelsJSON != nil {
		_ = json.Unmarshal(labelsJSON, &d.Labels)
	}
	return &d, nil
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
