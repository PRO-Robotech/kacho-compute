// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package repo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	"github.com/PRO-Robotech/kacho-corelib/filter"
	"github.com/PRO-Robotech/kacho-corelib/validate"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/fgaintent"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// SnapshotRepo — реализация service.SnapshotRepo поверх pgxpool.
type SnapshotRepo struct {
	pool *pgxpool.Pool
}

// NewSnapshotRepo создаёт SnapshotRepo.
func NewSnapshotRepo(pool *pgxpool.Pool) *SnapshotRepo { return &SnapshotRepo{pool: pool} }

const snapshotCols = `id, project_id, created_at, name, description, labels, storage_size, disk_size, product_ids, ` +
	`status, source_disk_id, hardware_generation, kms_key`

// Get возвращает снапшот по id.
func (r *SnapshotRepo) Get(ctx context.Context, id string) (*domain.Snapshot, error) {
	q := fmt.Sprintf(`SELECT %s FROM snapshots WHERE id = $1`, snapshotCols)
	s, err := scanSnapshot(r.pool.QueryRow(ctx, q, id))
	if err != nil {
		return nil, wrapPgErr(err, "Snapshot", id)
	}
	return s, nil
}

// List возвращает снапшоты по folder с cursor-pagination.
func (r *SnapshotRepo) List(ctx context.Context, f service.SnapshotFilter, p service.Pagination) ([]*domain.Snapshot, string, error) {
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
	q := fmt.Sprintf(`SELECT %s FROM snapshots %s ORDER BY created_at ASC, id ASC LIMIT $%d`, snapshotCols, where, argIdx)
	args = append(args, pageSize+1)

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", wrapPgErr(err, "Snapshot", "")
	}
	defer rows.Close()
	var result []*domain.Snapshot
	for rows.Next() {
		s, serr := scanSnapshot(rows)
		if serr != nil {
			return nil, "", wrapPgErr(serr, "Snapshot", "")
		}
		result = append(result, s)
	}
	if err := rows.Err(); err != nil {
		return nil, "", wrapPgErr(err, "Snapshot", "")
	}
	var nextToken string
	if int64(len(result)) > pageSize {
		last := result[pageSize-1]
		nextToken = encodePageToken(last.CreatedAt, last.ID)
		result = result[:pageSize]
	}
	return result, nextToken, nil
}

// Insert вставляет снапшот + outbox-event Snapshot CREATED.
func (r *SnapshotRepo) Insert(ctx context.Context, s *domain.Snapshot) (*domain.Snapshot, error) {
	args, err := snapshotInsertArgs(s)
	if err != nil {
		return nil, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, service.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()
	const q = `INSERT INTO snapshots (id, project_id, created_at, name, description, labels, storage_size, disk_size, product_ids,
		status, source_disk_id, hardware_generation, kms_key) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING ` + snapshotCols
	result, err := scanSnapshot(tx.QueryRow(ctx, q, args...))
	if err != nil {
		return nil, wrapPgErr(err, "Snapshot", s.Name)
	}
	if err := emitCompute(ctx, tx, "Snapshot", result.ID, "CREATED", snapshotPayload(result)); err != nil {
		return nil, service.ErrInternal
	}
	// FGA owner-tuple register-intent in the SAME writer-tx (no dual-write).
	if err := emitFGARegisterIntent(ctx, tx, fgaintent.EventRegister, "Snapshot", result.ID, result.ProjectID, result.Labels); err != nil {
		return nil, service.ErrInternal
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapPgErr(err, "Snapshot", s.Name)
	}
	return result, nil
}

// Update обновляет mutable поля снапшота + outbox-event Snapshot UPDATED.
//
// emitLabelsRegister (parity с InstanceRepo.Update): когда true (use-case увидел
// "labels" в update-mask или full-PATCH) — в той же writer-tx эмитится свежий FGA
// register-intent (mirror.upsert) с текущими labels (atomic), чтобы IAM
// resource_mirror не протух и label-scoped грант ревокался при снятии/смене метки.
// Полное снятие меток → upsert {} (НЕ Unregister). false (name/description без labels) →
// register-intent НЕ эмитится.
func (r *SnapshotRepo) Update(ctx context.Context, s *domain.Snapshot, emitLabelsRegister bool) (*domain.Snapshot, error) {
	labelsJSON, err := marshalJSONB(s.Labels, "Snapshot.labels")
	if err != nil {
		return nil, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, service.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()
	const q = `UPDATE snapshots SET name=$2, description=$3, labels=$4 WHERE id=$1 RETURNING ` + snapshotCols
	result, err := scanSnapshot(tx.QueryRow(ctx, q, s.ID, s.Name, s.Description, labelsJSON))
	if err != nil {
		return nil, wrapPgErr(err, "Snapshot", s.ID)
	}
	if err := emitCompute(ctx, tx, "Snapshot", result.ID, "UPDATED", snapshotPayload(result)); err != nil {
		return nil, service.ErrInternal
	}
	// refresh the IAM resource_mirror only when labels were in the mask
	// (mirror.upsert, EventRegister) in the SAME writer-tx; empty labels → upsert {}.
	if emitLabelsRegister {
		if err := emitFGARegisterIntent(ctx, tx, fgaintent.EventRegister, "Snapshot", result.ID, result.ProjectID, result.Labels); err != nil {
			return nil, service.ErrInternal
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapPgErr(err, "Snapshot", s.ID)
	}
	return result, nil
}

// Delete удаляет снапшот + outbox-event Snapshot DELETED.
func (r *SnapshotRepo) Delete(ctx context.Context, id string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return service.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// DELETE … RETURNING project_id so the FGA unregister-intent can build the
	// project-hierarchy tuple of the just-deleted resource within the same tx.
	var projectID string
	err = tx.QueryRow(ctx, `DELETE FROM snapshots WHERE id = $1 RETURNING project_id`, id).Scan(&projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: Snapshot %s not found", service.ErrNotFound, id)
		}
		return wrapPgErr(err, "Snapshot", id)
	}
	if err := emitCompute(ctx, tx, "Snapshot", id, "DELETED", map[string]any{"id": id}); err != nil {
		return service.ErrInternal
	}
	// symmetric FGA unregister-intent in the SAME writer-tx.
	if err := emitFGARegisterIntent(ctx, tx, fgaintent.EventUnregister, "Snapshot", id, projectID, nil); err != nil {
		return service.ErrInternal
	}
	if err := tx.Commit(ctx); err != nil {
		return wrapPgErr(err, "Snapshot", id)
	}
	return nil
}

// ---- scan / args ----

func snapshotInsertArgs(s *domain.Snapshot) ([]any, error) {
	labelsJSON, err := marshalJSONB(s.Labels, "Snapshot.labels")
	if err != nil {
		return nil, err
	}
	prodJSON, err := marshalJSONB(orEmptySlice(s.ProductIDs), "Snapshot.product_ids")
	if err != nil {
		return nil, err
	}
	hgJSON, err := marshalProtoJSONB(s.HardwareGeneration, "Snapshot.hardware_generation")
	if err != nil {
		return nil, err
	}
	kmsJSON, err := marshalProtoJSONB(s.KMSKey, "Snapshot.kms_key")
	if err != nil {
		return nil, err
	}
	return []any{
		s.ID, s.ProjectID, s.CreatedAt, s.Name, s.Description, labelsJSON, s.StorageSize, s.DiskSize, prodJSON,
		snapshotStatusName(s.Status), s.SourceDiskID, hgJSON, kmsJSON,
	}, nil
}

func scanSnapshot(row scannable) (*domain.Snapshot, error) {
	var s domain.Snapshot
	var labelsJSON, prodJSON, hgJSON, kmsJSON []byte
	var statusName string
	if err := row.Scan(
		&s.ID, &s.ProjectID, &s.CreatedAt, &s.Name, &s.Description, &labelsJSON, &s.StorageSize, &s.DiskSize, &prodJSON,
		&statusName, &s.SourceDiskID, &hgJSON, &kmsJSON,
	); err != nil {
		return nil, err
	}
	if err := unmarshalJSONB(labelsJSON, &s.Labels, "Snapshot.labels"); err != nil {
		return nil, err
	}
	if err := unmarshalJSONB(prodJSON, &s.ProductIDs, "Snapshot.product_ids"); err != nil {
		return nil, err
	}
	s.Status = snapshotStatusFromName(statusName)
	if len(hgJSON) > 0 {
		s.HardwareGeneration = &computev1.HardwareGeneration{}
		if err := unmarshalProtoJSONB(hgJSON, s.HardwareGeneration, "Snapshot.hardware_generation"); err != nil {
			return nil, err
		}
	}
	if len(kmsJSON) > 0 {
		s.KMSKey = &computev1.KMSKey{}
		if err := unmarshalProtoJSONB(kmsJSON, s.KMSKey, "Snapshot.kms_key"); err != nil {
			return nil, err
		}
	}
	return &s, nil
}

func snapshotStatusName(s domain.SnapshotStatus) string {
	switch s {
	case domain.SnapshotStatusCreating:
		return "CREATING"
	case domain.SnapshotStatusReady:
		return "READY"
	case domain.SnapshotStatusError:
		return "ERROR"
	case domain.SnapshotStatusDeleting:
		return "DELETING"
	default:
		return "STATUS_UNSPECIFIED"
	}
}

func snapshotStatusFromName(s string) domain.SnapshotStatus {
	switch s {
	case "CREATING":
		return domain.SnapshotStatusCreating
	case "READY":
		return domain.SnapshotStatusReady
	case "ERROR":
		return domain.SnapshotStatusError
	case "DELETING":
		return domain.SnapshotStatusDeleting
	default:
		return domain.SnapshotStatusUnspecified
	}
}
