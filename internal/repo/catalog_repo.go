// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

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

// List возвращает типы дисков с cursor-пагинацией по id (конвенция Kachō:
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

// NOTE: Region/Zone serving (ZoneRepo/RegionRepo поверх локальных таблиц
// `zones`/`regions`) удалён — Geography (Region/Zone) принадлежит kacho-geo.
// Локальные таблицы сняты миграцией 0011_drop_geography. zone_id-валидация
// Instance/Disk идёт в kacho-geo через clients.GeoClient.
