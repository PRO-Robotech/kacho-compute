package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
	"github.com/PRO-Robotech/kacho-corelib/validate"
)

// ---- DiskTypeRepo ----

// DiskTypeRepo — реализация service.DiskTypeRepo поверх pgxpool.
type DiskTypeRepo struct {
	pool *pgxpool.Pool
}

// NewDiskTypeRepo создаёт DiskTypeRepo.
func NewDiskTypeRepo(pool *pgxpool.Pool) *DiskTypeRepo { return &DiskTypeRepo{pool: pool} }

// Get возвращает тип диска по id.
func (r *DiskTypeRepo) Get(ctx context.Context, id string) (*domain.DiskType, error) {
	var t domain.DiskType
	var zoneIDsJSON []byte
	err := r.pool.QueryRow(ctx, `SELECT id, description, zone_ids, created_at FROM disk_types WHERE id = $1`, id).
		Scan(&t.ID, &t.Description, &zoneIDsJSON, &t.CreatedAt)
	if err != nil {
		return nil, wrapPgErr(err, "Disk type", id)
	}
	if err := unmarshalJSONB(zoneIDsJSON, &t.ZoneIDs, "DiskType.zone_ids"); err != nil {
		return nil, err
	}
	return &t, nil
}

// List возвращает типы дисков с cursor-пагинацией по id (verbatim YC:
// page_size валидируется через corevalidate.PageSize, garbage page_token → InvalidArgument).
func (r *DiskTypeRepo) List(ctx context.Context, p service.Pagination) ([]*domain.DiskType, string, error) {
	pageSize, err := validate.PageSize("page_size", p.PageSize)
	if err != nil {
		return nil, "", err
	}
	args := []any{}
	where := ""
	if p.PageToken != "" {
		_, cursorID, derr := decodePageToken(p.PageToken)
		if derr != nil {
			return nil, "", invalidPageTokenErr(derr)
		}
		where = "WHERE id > $1"
		args = append(args, cursorID)
	}
	q := fmt.Sprintf(`SELECT id, description, zone_ids, created_at FROM disk_types %s ORDER BY id ASC LIMIT $%d`, where, len(args)+1)
	args = append(args, pageSize+1)
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", wrapPgErr(err, "Disk type", "")
	}
	defer rows.Close()
	var out []*domain.DiskType
	for rows.Next() {
		var t domain.DiskType
		var zoneIDsJSON []byte
		if err := rows.Scan(&t.ID, &t.Description, &zoneIDsJSON, &t.CreatedAt); err != nil {
			return nil, "", wrapPgErr(err, "Disk type", "")
		}
		if err := unmarshalJSONB(zoneIDsJSON, &t.ZoneIDs, "DiskType.zone_ids"); err != nil {
			return nil, "", err
		}
		out = append(out, &t)
	}
	if err := rows.Err(); err != nil {
		return nil, "", wrapPgErr(err, "Disk type", "")
	}
	var next string
	if int64(len(out)) > pageSize {
		last := out[pageSize-1]
		next = encodePageToken(last.CreatedAt, last.ID)
		out = out[:pageSize]
	}
	return out, next, nil
}

// Insert вставляет тип диска (admin-only).
func (r *DiskTypeRepo) Insert(ctx context.Context, t *domain.DiskType) (*domain.DiskType, error) {
	zoneIDsJSON, err := marshalJSONB(orEmptySlice(t.ZoneIDs), "DiskType.zone_ids")
	if err != nil {
		return nil, err
	}
	var created domain.DiskType
	var outJSON []byte
	err = r.pool.QueryRow(ctx,
		`INSERT INTO disk_types (id, description, zone_ids, created_at) VALUES ($1,$2,$3,$4) RETURNING id, description, zone_ids, created_at`,
		t.ID, t.Description, zoneIDsJSON, time.Now().UTC()).
		Scan(&created.ID, &created.Description, &outJSON, &created.CreatedAt)
	if err != nil {
		return nil, wrapPgErr(err, "Disk type", t.ID)
	}
	if err := unmarshalJSONB(outJSON, &created.ZoneIDs, "DiskType.zone_ids"); err != nil {
		return nil, err
	}
	return &created, nil
}

// Update обновляет тип диска (admin-only).
func (r *DiskTypeRepo) Update(ctx context.Context, t *domain.DiskType) (*domain.DiskType, error) {
	zoneIDsJSON, err := marshalJSONB(orEmptySlice(t.ZoneIDs), "DiskType.zone_ids")
	if err != nil {
		return nil, err
	}
	var updated domain.DiskType
	var outJSON []byte
	err = r.pool.QueryRow(ctx,
		`UPDATE disk_types SET description=$2, zone_ids=$3 WHERE id=$1 RETURNING id, description, zone_ids, created_at`,
		t.ID, t.Description, zoneIDsJSON).
		Scan(&updated.ID, &updated.Description, &outJSON, &updated.CreatedAt)
	if err != nil {
		return nil, wrapPgErr(err, "Disk type", t.ID)
	}
	if err := unmarshalJSONB(outJSON, &updated.ZoneIDs, "DiskType.zone_ids"); err != nil {
		return nil, err
	}
	return &updated, nil
}

// Delete удаляет тип диска (admin-only).
func (r *DiskTypeRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM disk_types WHERE id = $1`, id)
	if err != nil {
		return wrapPgErr(err, "Disk type", id)
	}
	if tag.RowsAffected() == 0 {
		return service.ErrNotFound
	}
	return nil
}

// ---- ZoneRepo ----

// ZoneRepo — реализация service.ZoneRepo поверх pgxpool.
type ZoneRepo struct {
	pool *pgxpool.Pool
}

// NewZoneRepo создаёт ZoneRepo.
func NewZoneRepo(pool *pgxpool.Pool) *ZoneRepo { return &ZoneRepo{pool: pool} }

// Get возвращает зону по id.
func (r *ZoneRepo) Get(ctx context.Context, id string) (*domain.Zone, error) {
	var z domain.Zone
	var statusName string
	err := r.pool.QueryRow(ctx, `SELECT id, region_id, name, status, created_at FROM zones WHERE id = $1`, id).
		Scan(&z.ID, &z.RegionID, &z.Name, &statusName, &z.CreatedAt)
	if err != nil {
		return nil, wrapPgErr(err, "Zone", id)
	}
	z.Status = zoneStatusFromName(statusName)
	return &z, nil
}

// List возвращает зоны с cursor-пагинацией по id (verbatim YC: page_size валидируется,
// garbage page_token → InvalidArgument).
func (r *ZoneRepo) List(ctx context.Context, p service.Pagination) ([]*domain.Zone, string, error) {
	pageSize, err := validate.PageSize("page_size", p.PageSize)
	if err != nil {
		return nil, "", err
	}
	args := []any{}
	where := ""
	if p.PageToken != "" {
		_, cursorID, derr := decodePageToken(p.PageToken)
		if derr != nil {
			return nil, "", invalidPageTokenErr(derr)
		}
		where = "WHERE id > $1"
		args = append(args, cursorID)
	}
	q := fmt.Sprintf(`SELECT id, region_id, name, status, created_at FROM zones %s ORDER BY id ASC LIMIT $%d`, where, len(args)+1)
	args = append(args, pageSize+1)
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", wrapPgErr(err, "Zone", "")
	}
	defer rows.Close()
	var out []*domain.Zone
	for rows.Next() {
		var z domain.Zone
		var statusName string
		if err := rows.Scan(&z.ID, &z.RegionID, &z.Name, &statusName, &z.CreatedAt); err != nil {
			return nil, "", wrapPgErr(err, "Zone", "")
		}
		z.Status = zoneStatusFromName(statusName)
		out = append(out, &z)
	}
	if err := rows.Err(); err != nil {
		return nil, "", wrapPgErr(err, "Zone", "")
	}
	var next string
	if int64(len(out)) > pageSize {
		last := out[pageSize-1]
		next = encodePageToken(last.CreatedAt, last.ID)
		out = out[:pageSize]
	}
	return out, next, nil
}

// Insert вставляет зону (admin-only).
func (r *ZoneRepo) Insert(ctx context.Context, z *domain.Zone) (*domain.Zone, error) {
	var created domain.Zone
	var statusName string
	err := r.pool.QueryRow(ctx,
		`INSERT INTO zones (id, region_id, name, status, created_at) VALUES ($1,$2,$3,$4,$5) RETURNING id, region_id, name, status, created_at`,
		z.ID, z.RegionID, z.Name, zoneStatusName(z.Status), time.Now().UTC()).
		Scan(&created.ID, &created.RegionID, &created.Name, &statusName, &created.CreatedAt)
	if err != nil {
		return nil, wrapPgErr(err, "Zone", z.ID)
	}
	created.Status = zoneStatusFromName(statusName)
	return &created, nil
}

// Update обновляет зону (admin-only).
func (r *ZoneRepo) Update(ctx context.Context, z *domain.Zone) (*domain.Zone, error) {
	var updated domain.Zone
	var statusName string
	err := r.pool.QueryRow(ctx,
		`UPDATE zones SET region_id=$2, name=$3, status=$4 WHERE id=$1 RETURNING id, region_id, name, status, created_at`,
		z.ID, z.RegionID, z.Name, zoneStatusName(z.Status)).
		Scan(&updated.ID, &updated.RegionID, &updated.Name, &statusName, &updated.CreatedAt)
	if err != nil {
		return nil, wrapPgErr(err, "Zone", z.ID)
	}
	updated.Status = zoneStatusFromName(statusName)
	return &updated, nil
}

// Delete удаляет зону (admin-only).
func (r *ZoneRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM zones WHERE id = $1`, id)
	if err != nil {
		return wrapPgErr(err, "Zone", id)
	}
	if tag.RowsAffected() == 0 {
		return service.ErrNotFound
	}
	return nil
}

// ---- RegionRepo ----

// RegionRepo — реализация service.RegionRepo поверх pgxpool.
type RegionRepo struct {
	pool *pgxpool.Pool
}

// NewRegionRepo создаёт RegionRepo.
func NewRegionRepo(pool *pgxpool.Pool) *RegionRepo { return &RegionRepo{pool: pool} }

// Get возвращает регион по id.
func (r *RegionRepo) Get(ctx context.Context, id string) (*domain.Region, error) {
	var rg domain.Region
	err := r.pool.QueryRow(ctx, `SELECT id, name, created_at FROM regions WHERE id = $1`, id).
		Scan(&rg.ID, &rg.Name, &rg.CreatedAt)
	if err != nil {
		return nil, wrapPgErr(err, "Region", id)
	}
	return &rg, nil
}

// List возвращает регионы с cursor-пагинацией по id.
func (r *RegionRepo) List(ctx context.Context, p service.Pagination) ([]*domain.Region, string, error) {
	pageSize, err := validate.PageSize("page_size", p.PageSize)
	if err != nil {
		return nil, "", err
	}
	args := []any{}
	where := ""
	if p.PageToken != "" {
		_, cursorID, derr := decodePageToken(p.PageToken)
		if derr != nil {
			return nil, "", invalidPageTokenErr(derr)
		}
		where = "WHERE id > $1"
		args = append(args, cursorID)
	}
	q := fmt.Sprintf(`SELECT id, name, created_at FROM regions %s ORDER BY id ASC LIMIT $%d`, where, len(args)+1)
	args = append(args, pageSize+1)
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", wrapPgErr(err, "Region", "")
	}
	defer rows.Close()
	var out []*domain.Region
	for rows.Next() {
		var rg domain.Region
		if err := rows.Scan(&rg.ID, &rg.Name, &rg.CreatedAt); err != nil {
			return nil, "", wrapPgErr(err, "Region", "")
		}
		out = append(out, &rg)
	}
	if err := rows.Err(); err != nil {
		return nil, "", wrapPgErr(err, "Region", "")
	}
	var next string
	if int64(len(out)) > pageSize {
		last := out[pageSize-1]
		next = encodePageToken(last.CreatedAt, last.ID)
		out = out[:pageSize]
	}
	return out, next, nil
}

// Insert вставляет регион (admin-only).
func (r *RegionRepo) Insert(ctx context.Context, rg *domain.Region) (*domain.Region, error) {
	var created domain.Region
	err := r.pool.QueryRow(ctx,
		`INSERT INTO regions (id, name, created_at) VALUES ($1,$2,$3) RETURNING id, name, created_at`,
		rg.ID, rg.Name, time.Now().UTC()).
		Scan(&created.ID, &created.Name, &created.CreatedAt)
	if err != nil {
		return nil, wrapPgErr(err, "Region", rg.ID)
	}
	return &created, nil
}

// Update обновляет регион (admin-only).
func (r *RegionRepo) Update(ctx context.Context, rg *domain.Region) (*domain.Region, error) {
	var updated domain.Region
	err := r.pool.QueryRow(ctx,
		`UPDATE regions SET name=$2 WHERE id=$1 RETURNING id, name, created_at`,
		rg.ID, rg.Name).
		Scan(&updated.ID, &updated.Name, &updated.CreatedAt)
	if err != nil {
		return nil, wrapPgErr(err, "Region", rg.ID)
	}
	return &updated, nil
}

// Delete удаляет регион (admin-only). Если у региона есть зоны — FK RESTRICT
// (SQLSTATE 23503) → wrapPgErr вернёт service.ErrFailedPrecondition → handler
// маппит в gRPC FailedPrecondition.
func (r *RegionRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM regions WHERE id = $1`, id)
	if err != nil {
		return wrapPgErr(err, "Region", id)
	}
	if tag.RowsAffected() == 0 {
		return service.ErrNotFound
	}
	return nil
}

// CountZones — число зон в регионе.
func (r *RegionRepo) CountZones(ctx context.Context, regionID string) (int, error) {
	var n int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM zones WHERE region_id = $1`, regionID).Scan(&n); err != nil {
		return 0, wrapPgErr(err, "Region", regionID)
	}
	return n, nil
}

// ---- ZoneRepoSource: адаптер ZoneRepo → service.ZoneSource ----

// ZoneRepoSource адаптирует локальную таблицу `zones` к service.ZoneSource
// (а значит и к service.ZoneRegistry). kacho-compute — owner Geography:
// это единственный источник зон (для ZoneService.Get/List и для existence-check
// zone_id в Disk/Instance Create). См. workspace CLAUDE.md §«Кросс-доменные ссылки».
type ZoneRepoSource struct{ repo *ZoneRepo }

// NewZoneRepoSource создаёт ZoneRepoSource поверх ZoneRepo.
func NewZoneRepoSource(repo *ZoneRepo) *ZoneRepoSource { return &ZoneRepoSource{repo: repo} }

// GetZone возвращает зону по id (service.ErrNotFound при отсутствии).
func (s *ZoneRepoSource) GetZone(ctx context.Context, zoneID string) (service.ZoneInfo, error) {
	z, err := s.repo.Get(ctx, zoneID)
	if err != nil {
		return service.ZoneInfo{}, err
	}
	return service.ZoneInfo{ID: z.ID, RegionID: z.RegionID}, nil
}

// ListZones возвращает зоны с cursor-пагинацией (как ZoneRepo.List).
func (s *ZoneRepoSource) ListZones(ctx context.Context, pageSize int64, pageToken string) ([]service.ZoneInfo, string, error) {
	zones, next, err := s.repo.List(ctx, service.Pagination{PageSize: pageSize, PageToken: pageToken})
	if err != nil {
		return nil, "", err
	}
	out := make([]service.ZoneInfo, 0, len(zones))
	for _, z := range zones {
		out = append(out, service.ZoneInfo{ID: z.ID, RegionID: z.RegionID})
	}
	return out, next, nil
}

func zoneStatusName(s domain.ZoneStatus) string {
	switch s {
	case domain.ZoneStatusUp:
		return "UP"
	case domain.ZoneStatusDown:
		return "DOWN"
	default:
		return "STATUS_UNSPECIFIED"
	}
}

func zoneStatusFromName(s string) domain.ZoneStatus {
	switch s {
	case "UP":
		return domain.ZoneStatusUp
	case "DOWN":
		return domain.ZoneStatusDown
	default:
		return domain.ZoneStatusUnspecified
	}
}
