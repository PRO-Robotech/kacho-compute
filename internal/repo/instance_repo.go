package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	computev1 "github.com/PRO-Robotech/kacho-compute/proto/gen/go/kacho/cloud/compute/v1"
	"github.com/PRO-Robotech/kacho-corelib/filter"
	"github.com/PRO-Robotech/kacho-corelib/validate"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/fgaintent"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// InstanceRepo — реализация service.InstanceRepo поверх pgxpool (multi-table).
type InstanceRepo struct {
	pool *pgxpool.Pool
}

// NewInstanceRepo создаёт InstanceRepo.
func NewInstanceRepo(pool *pgxpool.Pool) *InstanceRepo { return &InstanceRepo{pool: pool} }

const instanceCols = `id, project_id, created_at, name, description, labels, zone_id, platform_id, cores, memory, core_fraction, gpus, ` +
	`status, metadata, metadata_options, service_account_id, hostname, fqdn, network_settings_type, scheduling_preemptible, ` +
	`placement_policy, serial_port_ssh_authorization, gpu_cluster_id, hardware_generation, maintenance_policy, ` +
	`maintenance_grace_period_seconds, reserved_instance_pool_id, host_group_id, host_id, application`

// Get возвращает ВМ по id (+ NIC-и + attached_disks).
func (r *InstanceRepo) Get(ctx context.Context, id string) (*domain.Instance, error) {
	q := fmt.Sprintf(`SELECT %s FROM instances WHERE id = $1`, instanceCols)
	in, err := scanInstance(r.pool.QueryRow(ctx, q, id))
	if err != nil {
		return nil, wrapPgErr(err, "Instance", id)
	}
	if err := r.fillChildren(ctx, r.pool, in); err != nil {
		return nil, err
	}
	return in, nil
}

// List возвращает ВМ по folder с cursor-pagination.
func (r *InstanceRepo) List(ctx context.Context, f service.InstanceFilter, p service.Pagination) ([]*domain.Instance, string, error) {
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
	q := fmt.Sprintf(`SELECT %s FROM instances %s ORDER BY created_at ASC, id ASC LIMIT $%d`, instanceCols, where, argIdx)
	args = append(args, pageSize+1)

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", wrapPgErr(err, "Instance", "")
	}
	defer rows.Close()
	var result []*domain.Instance
	for rows.Next() {
		in, serr := scanInstance(rows)
		if serr != nil {
			return nil, "", wrapPgErr(serr, "Instance", "")
		}
		result = append(result, in)
	}
	if err := rows.Err(); err != nil {
		return nil, "", wrapPgErr(err, "Instance", "")
	}
	var nextToken string
	if int64(len(result)) > pageSize {
		last := result[pageSize-1]
		nextToken = encodePageToken(last.CreatedAt, last.ID)
		result = result[:pageSize]
	}
	for _, in := range result {
		if err := r.fillChildren(ctx, r.pool, in); err != nil {
			return nil, "", err
		}
	}
	return result, nextToken, nil
}

// Insert вставляет ВМ + NIC-и + attached_disks + inline-диски в одной TX.
func (r *InstanceRepo) Insert(ctx context.Context, in *domain.Instance, inlineDisks []*domain.Disk) (*domain.Instance, error) {
	insertArgs, err := instanceInsertArgs(in)
	if err != nil {
		return nil, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, service.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// inline disks first (FK attached_disks.disk_id → disks).
	for _, d := range inlineDisks {
		if err := insertDiskTx(ctx, tx, d); err != nil {
			return nil, err
		}
	}

	const qIns = `INSERT INTO instances (` + instanceCols + `)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30) RETURNING ` + instanceCols
	created, err := scanInstance(tx.QueryRow(ctx, qIns, insertArgs...))
	if err != nil {
		return nil, wrapPgErr(err, "Instance", in.Name)
	}
	for _, nic := range in.NetworkInterfaces {
		if err := insertNICTx(ctx, tx, in.ID, nic); err != nil {
			return nil, err
		}
	}
	for _, ad := range in.AttachedDisks {
		if err := insertAttachedDiskTx(ctx, tx, in.ID, ad); err != nil {
			// KAC-90: UNIQUE на attached_disks.disk_id — диск уже attached другой Instance.
			if isAttachedDisksDiskIDUniqViolation(err) {
				return nil, fmt.Errorf("%w: disk already attached to another instance", service.ErrFailedPrecondition)
			}
			return nil, err
		}
	}
	for _, d := range inlineDisks {
		if err := emitCompute(ctx, tx, "Disk", d.ID, "CREATED", diskPayload(d)); err != nil {
			return nil, service.ErrInternal
		}
		// SEC-D: inline boot/secondary disks are created resources → register their
		// owner-tuple too, in the same writer-tx. RSAB β: carry the disk labels.
		if err := emitFGARegisterIntent(ctx, tx, fgaintent.EventRegister, "Disk", d.ID, d.ProjectID, d.Labels); err != nil {
			return nil, service.ErrInternal
		}
	}
	if err := r.fillChildrenTx(ctx, tx, created); err != nil {
		return nil, err
	}
	if err := emitCompute(ctx, tx, "Instance", created.ID, "CREATED", instancePayload(created)); err != nil {
		return nil, service.ErrInternal
	}
	// SEC-D: FGA owner-tuple register-intent for the Instance in the SAME writer-tx.
	// RSAB β: carry the instance labels + parent-scope to feed IAM resource_mirror.
	if err := emitFGARegisterIntent(ctx, tx, fgaintent.EventRegister, "Instance", created.ID, created.ProjectID, created.Labels); err != nil {
		return nil, service.ErrInternal
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapPgErr(err, "Instance", in.Name)
	}
	return created, nil
}

// Update обновляет mutable поля ВМ + status + outbox UPDATED.
//
// emitLabelsRegister (epic RSAB β, D-β6): when true (the use-case saw "labels" in
// the update-mask, or a full-object PATCH that applies labels) a fresh FGA
// register-intent carrying the updated labels + parent-scope is emitted IN THE
// SAME writer-tx as the UPDATE (atomic, ban #10) so the IAM resource_mirror stays
// in sync (dev→prod label dynamics). When false (name/description/… without
// labels) NO register-intent is emitted — labels-membership and the immutable
// parent are unchanged, so a refresh would be pointless traffic (β-04b).
func (r *InstanceRepo) Update(ctx context.Context, in *domain.Instance, emitLabelsRegister bool) (*domain.Instance, error) {
	labelsJSON, err := marshalJSONB(in.Labels, "Instance.labels")
	if err != nil {
		return nil, err
	}
	ppJSON, err := marshalProtoJSONB(in.PlacementPolicy, "Instance.placement_policy")
	if err != nil {
		return nil, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, service.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()
	const q = `UPDATE instances SET name=$2, description=$3, labels=$4, service_account_id=$5, cores=$6, memory=$7,
		core_fraction=$8, gpus=$9, platform_id=$10, status=$11, network_settings_type=$12, placement_policy=$13
		WHERE id=$1 RETURNING ` + instanceCols
	updated, err := scanInstance(tx.QueryRow(ctx, q, in.ID, in.Name, in.Description, labelsJSON, in.ServiceAccountID,
		in.Cores, in.Memory, in.CoreFraction, in.GPUs, in.PlatformID, instanceStatusName(in.Status), in.NetworkSettingsType, ppJSON))
	if err != nil {
		return nil, wrapPgErr(err, "Instance", in.ID)
	}
	if err := r.fillChildrenTx(ctx, tx, updated); err != nil {
		return nil, err
	}
	if err := emitCompute(ctx, tx, "Instance", updated.ID, "UPDATED", instancePayload(updated)); err != nil {
		return nil, service.ErrInternal
	}
	// RSAB β (D-β6): refresh the IAM resource_mirror only when labels were in the
	// update-mask. Emitted in the SAME writer-tx as the UPDATE (atomic, ban #10).
	if emitLabelsRegister {
		if err := emitFGARegisterIntent(ctx, tx, fgaintent.EventRegister, "Instance", updated.ID, updated.ProjectID, updated.Labels); err != nil {
			return nil, service.ErrInternal
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapPgErr(err, "Instance", in.ID)
	}
	return updated, nil
}

// SetStatusCAS атомарно переводит instance из expected-status в next-status
// (workspace CLAUDE.md §«Within-service refs — DB-уровень обязателен»).
//
// Conditional UPDATE: `WHERE id=$1 AND status=$expected` — Postgres row-level
// lock сериализует concurrent writer'ов на одной row; второй writer ждёт
// commit'а первого, после чего видит уже обновлённый status, WHERE не
// matches, 0 rows → FailedPrecondition. Различаем NotFound vs
// FailedPrecondition дополнительным `SELECT EXISTS` в той же TX. Закрывает
// G2 audit KAC-85 (TOCTOU `Get→check→SetStatus`), parity c kacho-vpc KAC-52.
func (r *InstanceRepo) SetStatusCAS(ctx context.Context, id string, expected, next domain.InstanceStatus) (*domain.Instance, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, service.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `UPDATE instances SET status = $3 WHERE id = $1 AND status = $2`,
		id, instanceStatusName(expected), instanceStatusName(next))
	if err != nil {
		return nil, wrapPgErr(err, "Instance", id)
	}
	if tag.RowsAffected() == 0 {
		// Различаем «instance не существует» vs «instance в другом state».
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM instances WHERE id = $1)`, id).Scan(&exists); err != nil {
			return nil, wrapPgErr(err, "Instance", id)
		}
		if !exists {
			return nil, fmt.Errorf("%w: Instance %s not found", service.ErrNotFound, id)
		}
		return nil, fmt.Errorf("%w: state transition not allowed from current status", service.ErrFailedPrecondition)
	}
	q := fmt.Sprintf(`SELECT %s FROM instances WHERE id = $1`, instanceCols)
	in, err := scanInstance(tx.QueryRow(ctx, q, id))
	if err != nil {
		return nil, wrapPgErr(err, "Instance", id)
	}
	if err := r.fillChildrenTx(ctx, tx, in); err != nil {
		return nil, err
	}
	if err := emitCompute(ctx, tx, "Instance", in.ID, "UPDATED", instancePayload(in)); err != nil {
		return nil, service.ErrInternal
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapPgErr(err, "Instance", id)
	}
	return in, nil
}

// AttachDisk добавляет строку attached_disks + outbox UPDATED.
func (r *InstanceRepo) AttachDisk(ctx context.Context, id string, ad domain.AttachedDisk) (*domain.Instance, error) {
	return r.mutateAndReload(ctx, id, "UPDATED", func(ctx context.Context, tx pgx.Tx) error {
		return insertAttachedDiskTx(ctx, tx, id, ad)
	})
}

// DetachDisk удаляет строку attached_disks по disk_id + outbox UPDATED.
func (r *InstanceRepo) DetachDisk(ctx context.Context, id, diskID string) (*domain.Instance, error) {
	return r.mutateAndReload(ctx, id, "UPDATED", func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM attached_disks WHERE instance_id = $1 AND disk_id = $2`, id, diskID)
		return err
	})
}

// ReplaceNIC заменяет одну строку instance_network_interfaces + outbox UPDATED.
func (r *InstanceRepo) ReplaceNIC(ctx context.Context, id string, nic domain.NetworkInterface) (*domain.Instance, error) {
	return r.mutateAndReload(ctx, id, "UPDATED", func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM instance_network_interfaces WHERE instance_id = $1 AND idx = $2`, id, nic.Index); err != nil {
			return err
		}
		return insertNICTx(ctx, tx, id, nic)
	})
}

// SetMetadata заменяет map metadata + outbox UPDATED.
func (r *InstanceRepo) SetMetadata(ctx context.Context, id string, metadata map[string]string) (*domain.Instance, error) {
	mdJSON, err := marshalJSONB(orEmptyMap(metadata), "Instance.metadata")
	if err != nil {
		return nil, err
	}
	return r.mutateAndReload(ctx, id, "UPDATED", func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE instances SET metadata = $2 WHERE id = $1`, id, mdJSON)
		return err
	})
}

// Delete удаляет ВМ; autoDeleteDiskIDs — диски с auto_delete=true (удаляются до
// DELETE instance; остальные строки attached_disks/NIC чистит FK CASCADE).
func (r *InstanceRepo) Delete(ctx context.Context, id string, autoDeleteDiskIDs []string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return service.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// detach all disks first (чтобы FK attached_disks.disk_id RESTRICT не блокировал
	// удаление auto-delete дисков; CASCADE на instance_id уберёт строки при DELETE instance,
	// но мы делаем явный DELETE строк перед DELETE дисков).
	if _, err := tx.Exec(ctx, `DELETE FROM attached_disks WHERE instance_id = $1`, id); err != nil {
		return wrapPgErr(err, "Instance", id)
	}
	// DELETE … RETURNING project_id (auto-delete disks share the instance's
	// project) so the FGA unregister-intents (below) can build the project-hierarchy
	// tuples of the just-deleted resources within the same writer-tx.
	for _, did := range autoDeleteDiskIDs {
		var diskProject string
		err := tx.QueryRow(ctx, `DELETE FROM disks WHERE id = $1 RETURNING project_id`, did).Scan(&diskProject)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// already gone — nothing to unregister; carry on (idempotent delete).
				continue
			}
			if isFKViolation(err) {
				return fmt.Errorf("%w: The disk %s is being used", service.ErrFailedPrecondition, did)
			}
			return wrapPgErr(err, "Disk", did)
		}
		if err := emitCompute(ctx, tx, "Disk", did, "DELETED", map[string]any{"id": did}); err != nil {
			return service.ErrInternal
		}
		// SEC-D: symmetric FGA unregister-intent for the auto-deleted disk.
		// Unregister removes the mirror row by object → labels are irrelevant (nil).
		if err := emitFGARegisterIntent(ctx, tx, fgaintent.EventUnregister, "Disk", did, diskProject, nil); err != nil {
			return service.ErrInternal
		}
	}
	var projectID string
	err = tx.QueryRow(ctx, `DELETE FROM instances WHERE id = $1 RETURNING project_id`, id).Scan(&projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: Instance %s not found", service.ErrNotFound, id)
		}
		return wrapPgErr(err, "Instance", id)
	}
	if err := emitCompute(ctx, tx, "Instance", id, "DELETED", map[string]any{"id": id}); err != nil {
		return service.ErrInternal
	}
	// SEC-D: symmetric FGA unregister-intent for the instance in the SAME writer-tx.
	// Unregister removes the mirror row by object → labels are irrelevant (nil).
	if err := emitFGARegisterIntent(ctx, tx, fgaintent.EventUnregister, "Instance", id, projectID, nil); err != nil {
		return service.ErrInternal
	}
	if err := tx.Commit(ctx); err != nil {
		return wrapPgErr(err, "Instance", id)
	}
	return nil
}

// ---- internal helpers ----

func (r *InstanceRepo) mutateAndReload(ctx context.Context, id, eventType string, mutate func(context.Context, pgx.Tx) error) (*domain.Instance, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, service.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// ensure instance exists.
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM instances WHERE id = $1)`, id).Scan(&exists); err != nil {
		return nil, wrapPgErr(err, "Instance", id)
	}
	if !exists {
		return nil, fmt.Errorf("%w: Instance %s not found", service.ErrNotFound, id)
	}
	if err := mutate(ctx, tx); err != nil {
		if isFKViolation(err) {
			return nil, fmt.Errorf("%w: Instance %s has dependent resources", service.ErrFailedPrecondition, id)
		}
		// KAC-90: UNIQUE на attached_disks.disk_id — диск уже attached другой Instance.
		// Отделяем от generic AlreadyExists (мапит в FailedPrecondition, не AlreadyExists).
		if isAttachedDisksDiskIDUniqViolation(err) {
			return nil, fmt.Errorf("%w: disk already attached to another instance", service.ErrFailedPrecondition)
		}
		if isUniqueViolation(err) {
			return nil, service.ErrAlreadyExists
		}
		return nil, wrapPgErr(err, "Instance", id)
	}
	q := fmt.Sprintf(`SELECT %s FROM instances WHERE id = $1`, instanceCols)
	in, err := scanInstance(tx.QueryRow(ctx, q, id))
	if err != nil {
		return nil, wrapPgErr(err, "Instance", id)
	}
	if err := r.fillChildrenTx(ctx, tx, in); err != nil {
		return nil, err
	}
	if err := emitCompute(ctx, tx, "Instance", in.ID, eventType, instancePayload(in)); err != nil {
		return nil, service.ErrInternal
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapPgErr(err, "Instance", id)
	}
	return in, nil
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func (r *InstanceRepo) fillChildren(ctx context.Context, q querier, in *domain.Instance) error {
	return r.fillChildrenGeneric(ctx, q, in)
}

func (r *InstanceRepo) fillChildrenTx(ctx context.Context, tx pgx.Tx, in *domain.Instance) error {
	return r.fillChildrenGeneric(ctx, tx, in)
}

func (r *InstanceRepo) fillChildrenGeneric(ctx context.Context, q querier, in *domain.Instance) error {
	// NIC-и.
	nicRows, err := q.Query(ctx, `SELECT idx, nic_id, mac_address, subnet_id, primary_v4_address, primary_v4_address_id, primary_v4_nat, primary_v6_address, primary_v6_nat, security_group_ids
		FROM instance_network_interfaces WHERE instance_id = $1 ORDER BY idx`, in.ID)
	if err != nil {
		return wrapPgErr(err, "Instance", in.ID)
	}
	for nicRows.Next() {
		var nic domain.NetworkInterface
		var v4NatJSON, v6NatJSON, sgJSON []byte
		if err := nicRows.Scan(&nic.Index, &nic.NICID, &nic.MACAddress, &nic.SubnetID, &nic.PrimaryV4Address, &nic.PrimaryV4AddressID, &v4NatJSON, &nic.PrimaryV6Address, &v6NatJSON, &sgJSON); err != nil {
			nicRows.Close()
			return wrapPgErr(err, "Instance", in.ID)
		}
		if len(v4NatJSON) > 0 {
			nic.PrimaryV4Nat = &domain.OneToOneNat{}
			if err := json.Unmarshal(v4NatJSON, nic.PrimaryV4Nat); err != nil {
				nic.PrimaryV4Nat = nil
			}
		}
		if len(v6NatJSON) > 0 {
			nic.PrimaryV6Nat = &domain.OneToOneNat{}
			if err := json.Unmarshal(v6NatJSON, nic.PrimaryV6Nat); err != nil {
				nic.PrimaryV6Nat = nil
			}
		}
		_ = json.Unmarshal(sgJSON, &nic.SecurityGroupIDs)
		in.NetworkInterfaces = append(in.NetworkInterfaces, nic)
	}
	nicRows.Close()
	if err := nicRows.Err(); err != nil {
		return wrapPgErr(err, "Instance", in.ID)
	}
	// attached_disks.
	adRows, err := q.Query(ctx, `SELECT disk_id, is_boot, mode, device_name, auto_delete, attached_at FROM attached_disks WHERE instance_id = $1 ORDER BY is_boot DESC, attached_at`, in.ID)
	if err != nil {
		return wrapPgErr(err, "Instance", in.ID)
	}
	for adRows.Next() {
		var ad domain.AttachedDisk
		var modeName string
		if err := adRows.Scan(&ad.DiskID, &ad.IsBoot, &modeName, &ad.DeviceName, &ad.AutoDelete, &ad.AttachedAt); err != nil {
			adRows.Close()
			return wrapPgErr(err, "Instance", in.ID)
		}
		ad.Mode = attachedDiskModeFromName(modeName)
		in.AttachedDisks = append(in.AttachedDisks, ad)
	}
	adRows.Close()
	if err := adRows.Err(); err != nil {
		return wrapPgErr(err, "Instance", in.ID)
	}
	return nil
}

func insertNICTx(ctx context.Context, tx pgx.Tx, instanceID string, nic domain.NetworkInterface) error {
	v4NatJSON, err := marshalNilable(nic.PrimaryV4Nat, "NIC.primary_v4_nat")
	if err != nil {
		return err
	}
	v6NatJSON, err := marshalNilable(nic.PrimaryV6Nat, "NIC.primary_v6_nat")
	if err != nil {
		return err
	}
	sgJSON, err := marshalJSONB(orEmptySlice(nic.SecurityGroupIDs), "NIC.security_group_ids")
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO instance_network_interfaces
		(instance_id, idx, nic_id, mac_address, subnet_id, primary_v4_address, primary_v4_address_id, primary_v4_nat, primary_v4_dns_records, primary_v6_address, primary_v6_nat, primary_v6_dns_records, security_group_ids)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'[]'::jsonb,$9,$10,'[]'::jsonb,$11)`,
		instanceID, nic.Index, nic.NICID, nic.MACAddress, nic.SubnetID, nic.PrimaryV4Address, nic.PrimaryV4AddressID, v4NatJSON, nic.PrimaryV6Address, v6NatJSON, sgJSON)
	return err
}

func insertAttachedDiskTx(ctx context.Context, tx pgx.Tx, instanceID string, ad domain.AttachedDisk) error {
	at := ad.AttachedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := tx.Exec(ctx, `INSERT INTO attached_disks (instance_id, disk_id, is_boot, mode, device_name, auto_delete, attached_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		instanceID, ad.DiskID, ad.IsBoot, attachedDiskModeName(ad.Mode), ad.DeviceName, ad.AutoDelete, at)
	return err
}

// insertDiskTx вставляет диск внутри переданной TX (для inline-дисков Instance.Create).
func insertDiskTx(ctx context.Context, tx pgx.Tx, d *domain.Disk) error {
	args, err := diskInsertArgs(d)
	if err != nil {
		return err
	}
	const q = `INSERT INTO disks (id, project_id, created_at, name, description, labels, type_id, zone_id, size, block_size,
		product_ids, status, source_image_id, source_snapshot_id, disk_placement_policy, hardware_generation, kms_key)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`
	_, err = tx.Exec(ctx, q, args...)
	if err != nil {
		return wrapPgErr(err, "Disk", d.ID)
	}
	return nil
}

// ---- scan / args ----

func instanceInsertArgs(in *domain.Instance) ([]any, error) {
	labelsJSON, err := marshalJSONB(orEmptyMap(in.Labels), "Instance.labels")
	if err != nil {
		return nil, err
	}
	mdJSON, err := marshalJSONB(orEmptyMap(in.Metadata), "Instance.metadata")
	if err != nil {
		return nil, err
	}
	mdOptJSON, err := marshalProtoJSONB(in.MetadataOptions, "Instance.metadata_options")
	if err != nil {
		return nil, err
	}
	ppJSON, err := marshalProtoJSONB(in.PlacementPolicy, "Instance.placement_policy")
	if err != nil {
		return nil, err
	}
	hgJSON, err := marshalProtoJSONB(in.HardwareGeneration, "Instance.hardware_generation")
	if err != nil {
		return nil, err
	}
	appJSON, err := marshalProtoJSONB(in.Application, "Instance.application")
	if err != nil {
		return nil, err
	}
	return []any{
		in.ID, in.ProjectID, in.CreatedAt, in.Name, in.Description, labelsJSON, in.ZoneID, in.PlatformID, in.Cores, in.Memory, in.CoreFraction, in.GPUs,
		instanceStatusName(in.Status), mdJSON, mdOptJSON, in.ServiceAccountID, in.Hostname, in.FQDN, in.NetworkSettingsType, in.SchedulingPreemptible,
		ppJSON, in.SerialPortSSHAuthorization, in.GPUClusterID, hgJSON, in.MaintenancePolicy,
		in.MaintenanceGracePeriodSeconds, in.ReservedInstancePoolID, in.HostGroupID, in.HostID, appJSON,
	}, nil
}

func scanInstance(row scannable) (*domain.Instance, error) {
	var in domain.Instance
	var labelsJSON, mdJSON, mdOptJSON, ppJSON, hgJSON, appJSON []byte
	var statusName string
	if err := row.Scan(
		&in.ID, &in.ProjectID, &in.CreatedAt, &in.Name, &in.Description, &labelsJSON, &in.ZoneID, &in.PlatformID, &in.Cores, &in.Memory, &in.CoreFraction, &in.GPUs,
		&statusName, &mdJSON, &mdOptJSON, &in.ServiceAccountID, &in.Hostname, &in.FQDN, &in.NetworkSettingsType, &in.SchedulingPreemptible,
		&ppJSON, &in.SerialPortSSHAuthorization, &in.GPUClusterID, &hgJSON, &in.MaintenancePolicy,
		&in.MaintenanceGracePeriodSeconds, &in.ReservedInstancePoolID, &in.HostGroupID, &in.HostID, &appJSON,
	); err != nil {
		return nil, err
	}
	if err := unmarshalJSONB(labelsJSON, &in.Labels, "Instance.labels"); err != nil {
		return nil, err
	}
	if err := unmarshalJSONB(mdJSON, &in.Metadata, "Instance.metadata"); err != nil {
		return nil, err
	}
	in.Status = instanceStatusFromName(statusName)
	if len(mdOptJSON) > 0 {
		in.MetadataOptions = &computev1.MetadataOptions{}
		if err := unmarshalProtoJSONB(mdOptJSON, in.MetadataOptions, "Instance.metadata_options"); err != nil {
			return nil, err
		}
	}
	if len(ppJSON) > 0 {
		in.PlacementPolicy = &computev1.PlacementPolicy{}
		if err := unmarshalProtoJSONB(ppJSON, in.PlacementPolicy, "Instance.placement_policy"); err != nil {
			return nil, err
		}
	}
	if len(hgJSON) > 0 {
		in.HardwareGeneration = &computev1.HardwareGeneration{}
		if err := unmarshalProtoJSONB(hgJSON, in.HardwareGeneration, "Instance.hardware_generation"); err != nil {
			return nil, err
		}
	}
	if len(appJSON) > 0 {
		in.Application = &computev1.Application{}
		if err := unmarshalProtoJSONB(appJSON, in.Application, "Instance.application"); err != nil {
			return nil, err
		}
	}
	return &in, nil
}

func instanceStatusName(s domain.InstanceStatus) string {
	if v, ok := computev1.Instance_Status_name[int32(s)]; ok {
		return v
	}
	return "STATUS_UNSPECIFIED"
}

func instanceStatusFromName(s string) domain.InstanceStatus {
	if v, ok := computev1.Instance_Status_value[s]; ok {
		return domain.InstanceStatus(v)
	}
	return domain.InstanceStatusUnspecified
}

func attachedDiskModeName(m domain.AttachedDiskMode) string {
	switch m {
	case domain.AttachedDiskModeReadOnly:
		return "READ_ONLY"
	case domain.AttachedDiskModeReadWrite:
		return "READ_WRITE"
	default:
		return "MODE_UNSPECIFIED"
	}
}

func attachedDiskModeFromName(s string) domain.AttachedDiskMode {
	switch s {
	case "READ_ONLY":
		return domain.AttachedDiskModeReadOnly
	case "READ_WRITE":
		return domain.AttachedDiskModeReadWrite
	default:
		return domain.AttachedDiskModeUnspecified
	}
}

func marshalNilable(v *domain.OneToOneNat, field string) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return marshalJSONB(v, field)
}

func orEmptyMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
