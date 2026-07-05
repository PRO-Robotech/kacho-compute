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
	"github.com/PRO-Robotech/kacho-compute/internal/ports"
)

// DiskRepo — реализация ports.DiskRepo поверх pgxpool.
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
func (r *DiskRepo) List(ctx context.Context, f ports.DiskFilter, p ports.Pagination) ([]*domain.Disk, string, error) {
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
		// FGA filter. Empty allow-list must be short-circuited
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
		return nil, ports.ErrInternal
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
		return nil, ports.ErrInternal
	}
	// FGA owner-tuple register-intent in the SAME writer-tx (no dual-write).
	if err := emitFGARegisterIntent(ctx, tx, fgaintent.EventRegister, "Disk", result.ID, result.ProjectID, result.Labels); err != nil {
		return nil, ports.ErrInternal
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapPgErr(err, "Disk", d.Name)
	}
	return result, nil
}

// Update обновляет mutable поля диска + outbox-event Disk UPDATED.
//
// emitLabelsRegister (parity с InstanceRepo.Update): когда true (use-case увидел
// "labels" в update-mask, либо full-object PATCH применяет labels) — в той же
// writer-tx, что и UPDATE, эмитится свежий FGA register-intent с текущими labels +
// parent-scope (atomic), чтобы IAM resource_mirror не протух и label-scoped грант
// ревокался при снятии/смене метки. Полное снятие меток (labels={}) эмитит mirror.upsert
// с пустыми labels (НЕ Unregister — ресурс жив). Когда false (name/description/size
// без labels) — register-intent НЕ эмитится (меньше reconcile-шума).
func (r *DiskRepo) Update(ctx context.Context, d *domain.Disk, emitLabelsRegister bool, changed []string) (*domain.Disk, error) {
	// column-scoped SET: пишем только изменённые колонки (см. updateSet) — иначе
	// конкурентный Update по другому полю затирается устаревшим снимком (lost update).
	ch := changedSet(changed)
	us := newUpdateSet(d.ID)
	if _, ok := ch["name"]; ok {
		us.add("name", d.Name)
	}
	if _, ok := ch["description"]; ok {
		us.add("description", d.Description)
	}
	if _, ok := ch["labels"]; ok {
		labelsJSON, err := marshalJSONB(d.Labels, "Disk.labels")
		if err != nil {
			return nil, err
		}
		us.add("labels", labelsJSON)
	}
	// sizeParam — placeholder-индекс колонки size, если она в update-mask. > 0
	// включает атомарный монотонный CAS-предикат `AND size <= $sizeParam` в WHERE
	// (см. ниже): усадка размера отбивается на DB-уровне, а не software check-then-act.
	sizeParam := 0
	if _, ok := ch["size"]; ok {
		us.add("size", d.Size)
		sizeParam = len(us.args)
	}
	if _, ok := ch["disk_placement_policy"]; ok {
		dppJSON, err := marshalProtoJSONB(d.DiskPlacementPolicy, "Disk.disk_placement_policy")
		if err != nil {
			return nil, err
		}
		us.add("disk_placement_policy", dppJSON)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, ports.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var result *domain.Disk
	switch {
	case us.empty():
		result, err = scanDisk(tx.QueryRow(ctx, `SELECT `+diskCols+` FROM disks WHERE id = $1`, d.ID))
	case sizeParam > 0:
		// Монотонный размер — DB-level CAS: SET … WHERE id = $1 AND size <= $sizeParam.
		// Single-statement UPDATE на одной строке защищён row-lock'ом Postgres —
		// конкурентный writer ждёт commit'а первого и видит уже увеличенный size,
		// поэтому усадка (size > $new) не проходит (0 строк). Заменяет запрещённый
		// software stale-read + безусловный UPDATE (проект-правило 10, KAC TOCTOU).
		q := fmt.Sprintf(`UPDATE disks %s WHERE id = $1 AND size <= $%d RETURNING %s`, us.clause(), sizeParam, diskCols)
		result, err = scanDisk(tx.QueryRow(ctx, q, us.args...))
		if errors.Is(err, pgx.ErrNoRows) {
			// 0 строк: либо диска нет (NotFound), либо CAS отбил усадку
			// (FailedPrecondition). Различаем EXISTS-пробой в той же tx.
			var exists bool
			if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM disks WHERE id = $1)`, d.ID).Scan(&exists); e != nil {
				return nil, wrapPgErr(e, "Disk", d.ID)
			}
			if exists {
				return nil, fmt.Errorf("%w: Disk size can only be increased", ports.ErrFailedPrecondition)
			}
			return nil, fmt.Errorf("%w: Disk %s not found", ports.ErrNotFound, d.ID)
		}
	default:
		q := `UPDATE disks ` + us.clause() + ` WHERE id = $1 RETURNING ` + diskCols
		result, err = scanDisk(tx.QueryRow(ctx, q, us.args...))
	}
	if err != nil {
		return nil, wrapPgErr(err, "Disk", d.ID)
	}
	if err := emitCompute(ctx, tx, "Disk", result.ID, "UPDATED", diskPayload(result)); err != nil {
		return nil, ports.ErrInternal
	}
	// refresh the IAM resource_mirror only when labels were in the update-mask.
	// mirror.upsert (EventRegister) with the CURRENT labels in the SAME writer-tx
	// (atomic); empty labels → upsert {}, NOT unregister.
	if emitLabelsRegister {
		if err := emitFGARegisterIntent(ctx, tx, fgaintent.EventRegister, "Disk", result.ID, result.ProjectID, result.Labels); err != nil {
			return nil, ports.ErrInternal
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapPgErr(err, "Disk", d.ID)
	}
	return result, nil
}

// SetZoneIfDetached атомарно переносит диск в другую зону, но только если он не
// attached (Relocate). Инвариант «не релоцировать attached-диск» обеспечивается на
// DB-уровне, а не software check-then-act.
//
// Сериализация с конкурентным AttachDisk: `SELECT … FOR UPDATE` берёт на строке
// disks row-lock `FOR UPDATE`, который конфликтует с `FOR KEY SHARE` — его берёт
// INSERT в attached_disks по FK disk_id. Поэтому attach и relocate упорядочиваются:
//   - relocate первым: attach-INSERT ждёт commit relocate, потом видит обновлённую
//     зону (его собственный zone-guard отбивает несоответствие);
//   - attach первым: relocate после его commit видит строку attached_disks и
//     отдаёт FailedPrecondition.
//
// Один UPDATE с подзапросом `WHERE NOT EXISTS(…)` здесь недостаточен: он берёт лишь
// `FOR NO KEY UPDATE`, который НЕ конфликтует с `FOR KEY SHARE` attach'а, и гонка
// остаётся открытой. Нет диска → ErrNotFound; attached → ErrFailedPrecondition.
func (r *DiskRepo) SetZoneIfDetached(ctx context.Context, id, zoneID string) (*domain.Disk, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, ports.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var locked int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM disks WHERE id = $1 FOR UPDATE`, id).Scan(&locked); err != nil {
		return nil, wrapPgErr(err, "Disk", id)
	}
	var attached bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM attached_disks WHERE disk_id = $1)`, id).Scan(&attached); err != nil {
		return nil, wrapPgErr(err, "Disk", id)
	}
	if attached {
		return nil, fmt.Errorf("%w: Disk is in use", ports.ErrFailedPrecondition)
	}
	result, err := scanDisk(tx.QueryRow(ctx,
		`UPDATE disks SET zone_id = $2 WHERE id = $1 RETURNING `+diskCols, id, zoneID))
	if err != nil {
		return nil, wrapPgErr(err, "Disk", id)
	}
	if err := emitCompute(ctx, tx, "Disk", result.ID, "UPDATED", diskPayload(result)); err != nil {
		return nil, ports.ErrInternal
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
		return ports.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// DELETE … RETURNING project_id so the FGA unregister-intent (below) can build
	// the project-hierarchy tuple of the just-deleted resource within the same tx.
	var projectID string
	err = tx.QueryRow(ctx, `DELETE FROM disks WHERE id = $1 RETURNING project_id`, id).Scan(&projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: Disk %s not found", ports.ErrNotFound, id)
		}
		if isFKViolation(err) {
			return fmt.Errorf("%w: The disk %s is being used", ports.ErrFailedPrecondition, id)
		}
		return wrapPgErr(err, "Disk", id)
	}
	if err := emitCompute(ctx, tx, "Disk", id, "DELETED", map[string]any{"id": id}); err != nil {
		return ports.ErrInternal
	}
	// symmetric FGA unregister-intent in the SAME writer-tx.
	if err := emitFGARegisterIntent(ctx, tx, fgaintent.EventUnregister, "Disk", id, projectID, nil); err != nil {
		return ports.ErrInternal
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
