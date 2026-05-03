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

// SnapshotRepo — реализация service.SnapshotRepo поверх pgxpool.
type SnapshotRepo struct {
	pool *pgxpool.Pool
}

// NewSnapshotRepo создаёт SnapshotRepo.
func NewSnapshotRepo(pool *pgxpool.Pool) *SnapshotRepo {
	return &SnapshotRepo{pool: pool}
}

const snapshotSelectCols = `
	id, folder_id, name, description, created_at, labels,
	disk_id, size, status, progress_percent,
	generation, resource_version, observed_generation, deleted_at`

func (r *SnapshotRepo) Get(ctx context.Context, id string) (*domain.Snapshot, error) {
	q := fmt.Sprintf(`SELECT %s FROM snapshots WHERE id = $1 AND deleted_at IS NULL`, snapshotSelectCols)
	row := r.pool.QueryRow(ctx, q, id)
	s, err := scanSnapshot(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, service.ErrNotFound
	}
	return s, err
}

func (r *SnapshotRepo) GetIncludingDeleted(ctx context.Context, id string) (*domain.Snapshot, error) {
	q := fmt.Sprintf(`SELECT %s FROM snapshots WHERE id = $1`, snapshotSelectCols)
	row := r.pool.QueryRow(ctx, q, id)
	s, err := scanSnapshot(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, service.ErrNotFound
	}
	return s, err
}

func (r *SnapshotRepo) List(ctx context.Context, f service.SnapshotFilter, page service.Pagination) ([]*domain.Snapshot, string, error) {
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

	q := fmt.Sprintf(`SELECT %s FROM snapshots %s ORDER BY %s LIMIT $%d`,
		snapshotSelectCols, where, orderClause, argIdx)
	args = append(args, pageSize+1)

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var result []*domain.Snapshot
	for rows.Next() {
		s, err := scanSnapshot(rows)
		if err != nil {
			return nil, "", err
		}
		result = append(result, s)
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

func (r *SnapshotRepo) Insert(ctx context.Context, s *domain.Snapshot) (*domain.Snapshot, error) {
	labelsJSON, _ := json.Marshal(s.Labels)

	const q = `
		INSERT INTO snapshots (
			id, folder_id, name, description, created_at, labels,
			disk_id, size, status, progress_percent,
			generation, resource_version, observed_generation
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`

	_, err := r.pool.Exec(ctx, q,
		s.ID, s.FolderID, s.Name, s.Description, s.CreatedAt, labelsJSON,
		s.DiskID, s.Size, int32(s.Status), s.ProgressPercent,
		s.Generation, s.ResourceVersion, s.ObservedGeneration,
	)
	if err != nil {
		return nil, err
	}
	return r.GetIncludingDeleted(ctx, s.ID)
}

func (r *SnapshotRepo) Update(ctx context.Context, s *domain.Snapshot) (*domain.Snapshot, error) {
	labelsJSON, _ := json.Marshal(s.Labels)

	const q = `
		UPDATE snapshots SET
			name=$2, description=$3, labels=$4,
			disk_id=$5, size=$6, status=$7, progress_percent=$8,
			generation=$9, resource_version=$10, observed_generation=$11,
			deleted_at=$12
		WHERE id=$1`

	_, err := r.pool.Exec(ctx, q,
		s.ID,
		s.Name, s.Description, labelsJSON,
		s.DiskID, s.Size, int32(s.Status), s.ProgressPercent,
		s.Generation, s.ResourceVersion, s.ObservedGeneration,
		s.DeletedAt,
	)
	if err != nil {
		return nil, err
	}
	return r.GetIncludingDeleted(ctx, s.ID)
}

func (r *SnapshotRepo) ListPendingReconcile(ctx context.Context, limit int) ([]*domain.Snapshot, error) {
	q := fmt.Sprintf(`
		SELECT %s FROM snapshots
		WHERE status IN (1, 4)
		ORDER BY created_at ASC
		LIMIT $1
		FOR UPDATE SKIP LOCKED`, snapshotSelectCols)

	rows, err := r.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*domain.Snapshot
	for rows.Next() {
		s, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// HardDelete физически удаляет Snapshot.
func (r *SnapshotRepo) HardDelete(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, "DELETE FROM snapshots WHERE id = $1", id)
	return err
}

// ---- scan helpers ----

func scanSnapshot(row scannable) (*domain.Snapshot, error) {
	var s domain.Snapshot
	var labelsJSON []byte
	var statusInt int32

	err := row.Scan(
		&s.ID, &s.FolderID, &s.Name, &s.Description, &s.CreatedAt, &labelsJSON,
		&s.DiskID, &s.Size, &statusInt, &s.ProgressPercent,
		&s.Generation, &s.ResourceVersion, &s.ObservedGeneration, &s.DeletedAt,
	)
	if err != nil {
		return nil, err
	}
	s.Status = domain.SnapshotStatus(statusInt)
	if labelsJSON != nil {
		_ = json.Unmarshal(labelsJSON, &s.Labels)
	}
	return &s, nil
}
