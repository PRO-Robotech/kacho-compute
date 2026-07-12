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

	"github.com/PRO-Robotech/kacho-corelib/filter"
	"github.com/PRO-Robotech/kacho-corelib/validate"
	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/fgaintent"
	"github.com/PRO-Robotech/kacho-compute/internal/ports"
)

// InstanceRepo — реализация ports.InstanceRepo поверх pgxpool.
//
// Storage-split cutover: compute больше НЕ держит local attach-state — таблица
// `attached_disks` удалена (миграция 0013). Том↔Instance-привязка живёт в
// kacho-storage; `Instance.boot_volume`/`secondary_volumes` — read-only зеркало,
// заполняемое use-case'ом на чтении из storage. Здесь остаётся только строка
// `instances` (+ same-DB NIC-mirror child таблица, cascade).
type InstanceRepo struct {
	pool *pgxpool.Pool
}

// NewInstanceRepo создаёт InstanceRepo.
func NewInstanceRepo(pool *pgxpool.Pool) *InstanceRepo { return &InstanceRepo{pool: pool} }

const instanceCols = `id, project_id, created_at, name, description, labels, zone_id, platform_id, cores, memory, core_fraction, gpus, ` +
	`status, metadata, metadata_options, service_account_id, hostname, fqdn, network_settings_type, scheduling_preemptible, ` +
	`placement_policy, serial_port_ssh_authorization, gpu_cluster_id, hardware_generation, maintenance_policy, ` +
	`maintenance_grace_period_seconds, reserved_instance_pool_id, host_group_id, host_id, application`

// Get возвращает ВМ по id. AttachedDisks НЕ заполняются здесь — это зеркало из
// kacho-storage, use-case подтягивает его на чтении (graceful-degrade).
func (r *InstanceRepo) Get(ctx context.Context, id string) (*domain.Instance, error) {
	q := fmt.Sprintf(`SELECT %s FROM instances WHERE id = $1`, instanceCols)
	in, err := scanInstance(r.pool.QueryRow(ctx, q, id))
	if err != nil {
		return nil, wrapPgErr(err, "Instance", id)
	}
	return in, nil
}

// List возвращает ВМ по project с cursor-pagination.
func (r *InstanceRepo) List(ctx context.Context, f ports.InstanceFilter, p ports.Pagination) ([]*domain.Instance, string, error) {
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
	return result, nextToken, nil
}

// Insert вставляет строку ВМ + outbox CREATED + FGA register-intent в одной
// writer-tx. Никаких attached_disks / inline-дисков — compute local attach-state
// упразднён (storage-split).
func (r *InstanceRepo) Insert(ctx context.Context, in *domain.Instance) (*domain.Instance, error) {
	insertArgs, err := instanceInsertArgs(in)
	if err != nil {
		return nil, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, ports.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const qIns = `INSERT INTO instances (` + instanceCols + `)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30) RETURNING ` + instanceCols
	created, err := scanInstance(tx.QueryRow(ctx, qIns, insertArgs...))
	if err != nil {
		return nil, wrapPgErr(err, "Instance", in.Name)
	}
	if err := emitCompute(ctx, tx, "Instance", created.ID, "CREATED", instancePayload(created)); err != nil {
		return nil, ports.ErrInternal
	}
	// FGA owner-tuple register-intent for the Instance in the SAME writer-tx,
	// carrying the instance labels + parent-scope to feed IAM resource_mirror.
	if err := emitFGARegisterIntent(ctx, tx, fgaintent.EventRegister, "Instance", created.ID, created.ProjectID, created.Labels); err != nil {
		return nil, ports.ErrInternal
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapPgErr(err, "Instance", in.Name)
	}
	return created, nil
}

// Update обновляет mutable descriptive/resource поля ВМ + outbox UPDATED.
// status НЕ трогается — им владеет исключительно SetStatusCAS/MarkDeleting.
//
// emitLabelsRegister: when true a fresh FGA register-intent carrying the updated
// labels + parent-scope is emitted IN THE SAME writer-tx as the UPDATE (atomic) so
// the IAM resource_mirror stays in sync.
func (r *InstanceRepo) Update(ctx context.Context, in *domain.Instance, emitLabelsRegister bool, changed []string) (*domain.Instance, error) {
	ch := changedSet(changed)
	us := newUpdateSet(in.ID)
	if _, ok := ch["name"]; ok {
		us.add("name", in.Name)
	}
	if _, ok := ch["description"]; ok {
		us.add("description", in.Description)
	}
	if _, ok := ch["labels"]; ok {
		labelsJSON, err := marshalJSONB(in.Labels, "Instance.labels")
		if err != nil {
			return nil, err
		}
		us.add("labels", labelsJSON)
	}
	if _, ok := ch["service_account_id"]; ok {
		us.add("service_account_id", in.ServiceAccountID)
	}
	if _, ok := ch["placement_policy"]; ok {
		ppJSON, err := marshalProtoJSONB(in.PlacementPolicy, "Instance.placement_policy")
		if err != nil {
			return nil, err
		}
		us.add("placement_policy", ppJSON)
	}
	if _, ok := ch["network_settings"]; ok {
		us.add("network_settings_type", in.NetworkSettingsType)
	}
	// requireStopped: resize (resources_spec) и replatform (platform_id) разрешены
	// ТОЛЬКО пока instance STOPPED. Инвариант закрывается на DB-уровне атомарным CAS
	// `AND status='STOPPED'` в самом UPDATE — НЕ software Get→check→UPDATE.
	requireStopped := false
	if _, ok := ch["resources_spec"]; ok {
		us.add("cores", in.Cores)
		us.add("memory", in.Memory)
		us.add("core_fraction", in.CoreFraction)
		us.add("gpus", in.GPUs)
		requireStopped = true
	}
	if _, ok := ch["platform_id"]; ok {
		us.add("platform_id", in.PlatformID)
		requireStopped = true
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, ports.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var updated *domain.Instance
	if us.empty() {
		// mask не задел ни одной mutable-колонки — no-op: перечитываем строку
		// (NotFound если её нет) и всё равно эмитим UPDATED (behaviour-preserving).
		updated, err = scanInstance(tx.QueryRow(ctx, fmt.Sprintf(`SELECT %s FROM instances WHERE id = $1`, instanceCols), in.ID))
	} else {
		where := ` WHERE id = $1`
		if requireStopped {
			us.args = append(us.args, instanceStatusName(domain.InstanceStatusStopped))
			where += fmt.Sprintf(` AND status = $%d`, len(us.args))
		}
		q := `UPDATE instances ` + us.clause() + where + ` RETURNING ` + instanceCols
		updated, err = scanInstance(tx.QueryRow(ctx, q, us.args...))
	}
	if err != nil {
		if requireStopped && errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			if e2 := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM instances WHERE id = $1)`, in.ID).Scan(&exists); e2 != nil {
				return nil, wrapPgErr(e2, "Instance", in.ID)
			}
			if !exists {
				return nil, fmt.Errorf("%w: Instance %s not found", ports.ErrNotFound, in.ID)
			}
			return nil, fmt.Errorf("%w: Instance must be stopped", ports.ErrFailedPrecondition)
		}
		return nil, wrapPgErr(err, "Instance", in.ID)
	}
	if err := emitCompute(ctx, tx, "Instance", updated.ID, "UPDATED", instancePayload(updated)); err != nil {
		return nil, ports.ErrInternal
	}
	if emitLabelsRegister {
		if err := emitFGARegisterIntent(ctx, tx, fgaintent.EventRegister, "Instance", updated.ID, updated.ProjectID, updated.Labels); err != nil {
			return nil, ports.ErrInternal
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapPgErr(err, "Instance", in.ID)
	}
	return updated, nil
}

// SetStatusCAS атомарно переводит instance из expected-status в next-status
// (within-service-инвариант на DB-уровне, conditional UPDATE WHERE id AND status).
func (r *InstanceRepo) SetStatusCAS(ctx context.Context, id string, expected, next domain.InstanceStatus) (*domain.Instance, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, ports.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `UPDATE instances SET status = $3 WHERE id = $1 AND status = $2`,
		id, instanceStatusName(expected), instanceStatusName(next))
	if err != nil {
		return nil, wrapPgErr(err, "Instance", id)
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM instances WHERE id = $1)`, id).Scan(&exists); err != nil {
			return nil, wrapPgErr(err, "Instance", id)
		}
		if !exists {
			return nil, fmt.Errorf("%w: Instance %s not found", ports.ErrNotFound, id)
		}
		return nil, fmt.Errorf("%w: state transition not allowed from current status", ports.ErrFailedPrecondition)
	}
	q := fmt.Sprintf(`SELECT %s FROM instances WHERE id = $1`, instanceCols)
	in, err := scanInstance(tx.QueryRow(ctx, q, id))
	if err != nil {
		return nil, wrapPgErr(err, "Instance", id)
	}
	if err := emitCompute(ctx, tx, "Instance", in.ID, "UPDATED", instancePayload(in)); err != nil {
		return nil, ports.ErrInternal
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapPgErr(err, "Instance", id)
	}
	return in, nil
}

// GateForAttach — compute-local CAS-гейт attach-саги (disk/NIC): атомарно
// проверяет, что инстанс в {RUNNING, STOPPED}, и возвращает self-describing payload
// (zone_id/project_id/name) для форварда в storage/vpc. Реализован conditional
// SELECT `WHERE status IN (...)` — 0 rows означает либо отсутствие инстанса
// (NotFound), либо недопустимое состояние (FailedPrecondition). Гейт закрывает
// attach-vs-delete гонку (Delete ставит DELETING первым → status IN(...) не сматчит).
func (r *InstanceRepo) GateForAttach(ctx context.Context, id string) (string, string, string, error) {
	var zoneID, projectID, name string
	err := r.pool.QueryRow(ctx,
		`SELECT zone_id, project_id, name FROM instances
		  WHERE id = $1 AND status IN ('RUNNING','STOPPED')`, id).
		Scan(&zoneID, &projectID, &name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Различаем «нет инстанса» vs «в недопустимом состоянии».
			var exists bool
			if e2 := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM instances WHERE id = $1)`, id).Scan(&exists); e2 != nil {
				return "", "", "", wrapPgErr(e2, "Instance", id)
			}
			if !exists {
				return "", "", "", fmt.Errorf("%w: Instance %s not found", ports.ErrNotFound, id)
			}
			return "", "", "", fmt.Errorf("%w: Instance must be RUNNING or STOPPED", ports.ErrFailedPrecondition)
		}
		return "", "", "", wrapPgErr(err, "Instance", id)
	}
	return zoneID, projectID, name, nil
}

// MarkDeleting атомарно переводит инстанс в DELETING (идемпотентно). Ставится ПЕРЕД
// release'ом привязок в delete-саге, чтобы конкурентный AttachDisk-гейт видел
// DELETING и падал (attach-vs-delete race). Повтор на уже-DELETING — no-op OK.
func (r *InstanceRepo) MarkDeleting(ctx context.Context, id string) (*domain.Instance, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, ports.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `UPDATE instances SET status = 'DELETING' WHERE id = $1 AND status <> 'DELETING'`, id)
	if err != nil {
		return nil, wrapPgErr(err, "Instance", id)
	}
	q := fmt.Sprintf(`SELECT %s FROM instances WHERE id = $1`, instanceCols)
	in, err := scanInstance(tx.QueryRow(ctx, q, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: Instance %s not found", ports.ErrNotFound, id)
		}
		return nil, wrapPgErr(err, "Instance", id)
	}
	if tag.RowsAffected() > 0 {
		// эмитим UPDATED только на фактическом переходе (не на идемпотентном повторе).
		if err := emitCompute(ctx, tx, "Instance", in.ID, "UPDATED", instancePayload(in)); err != nil {
			return nil, ports.ErrInternal
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapPgErr(err, "Instance", id)
	}
	return in, nil
}

// MergeMetadata атомарно применяет delete+upsert дельту к map metadata одним
// SQL-statement'ом + outbox UPDATED (within-service-инвариант на DB-уровне).
func (r *InstanceRepo) MergeMetadata(ctx context.Context, id string, del []string, upsert map[string]string) (*domain.Instance, error) {
	upsertJSON, err := marshalJSONB(orEmptyMap(upsert), "Instance.metadata.upsert")
	if err != nil {
		return nil, err
	}
	delKeys := del
	if delKeys == nil {
		delKeys = []string{}
	}
	return r.mutateAndReload(ctx, id, "UPDATED", func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE instances
			    SET metadata = (COALESCE(metadata, '{}'::jsonb) - $2::text[]) || $3::jsonb
			  WHERE id = $1`,
			id, delKeys, upsertJSON)
		return err
	})
}

// Delete удаляет строку ВМ + outbox DELETED + FGA unregister-intent в одной
// writer-tx. ФИНАЛЬНЫЙ шаг delete-саги — том/NIC-привязки уже сняты в use-case
// (storage.Detach/vpc.Detach) ДО этого вызова; строка инстанса удаляется ПОСЛЕДНЕЙ,
// чтобы crash не осиротил привязки. Никакого attached_disks-sweep (таблицы нет).
func (r *InstanceRepo) Delete(ctx context.Context, id string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return ports.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var projectID string
	err = tx.QueryRow(ctx, `DELETE FROM instances WHERE id = $1 RETURNING project_id`, id).Scan(&projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: Instance %s not found", ports.ErrNotFound, id)
		}
		return wrapPgErr(err, "Instance", id)
	}
	// instance_network_interfaces (same-DB cascade child) снимается FK CASCADE.
	if err := emitCompute(ctx, tx, "Instance", id, "DELETED", map[string]any{"id": id}); err != nil {
		return ports.ErrInternal
	}
	if err := emitFGARegisterIntent(ctx, tx, fgaintent.EventUnregister, "Instance", id, projectID, nil); err != nil {
		return ports.ErrInternal
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
		return nil, ports.ErrInternal
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM instances WHERE id = $1)`, id).Scan(&exists); err != nil {
		return nil, wrapPgErr(err, "Instance", id)
	}
	if !exists {
		return nil, fmt.Errorf("%w: Instance %s not found", ports.ErrNotFound, id)
	}
	if err := mutate(ctx, tx); err != nil {
		return nil, wrapPgErr(err, "Instance", id)
	}
	q := fmt.Sprintf(`SELECT %s FROM instances WHERE id = $1`, instanceCols)
	in, err := scanInstance(tx.QueryRow(ctx, q, id))
	if err != nil {
		return nil, wrapPgErr(err, "Instance", id)
	}
	if err := emitCompute(ctx, tx, "Instance", in.ID, eventType, instancePayload(in)); err != nil {
		return nil, ports.ErrInternal
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapPgErr(err, "Instance", id)
	}
	return in, nil
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

// orEmptyMap возвращает пустую map вместо nil (JSONB-колонки NOT NULL DEFAULT '{}').
func orEmptyMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
