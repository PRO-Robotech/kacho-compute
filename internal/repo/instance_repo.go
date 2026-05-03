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

// InstanceRepo — реализация service.InstanceRepo поверх pgxpool.
type InstanceRepo struct {
	pool *pgxpool.Pool
}

// NewInstanceRepo создаёт InstanceRepo.
func NewInstanceRepo(pool *pgxpool.Pool) *InstanceRepo {
	return &InstanceRepo{pool: pool}
}

const instanceSelectCols = `
	id, folder_id, created_at, name, description, labels,
	zone_id, platform_id,
	resources_cores, resources_memory, resources_core_fraction, resources_gpus,
	status, fqdn, metadata,
	boot_disk_id, boot_disk_device_name, boot_disk_auto_delete,
	secondary_disks, network_interfaces,
	service_account_id, scheduling_preemptible, desired_power_state,
	generation, resource_version, observed_generation,
	status_last_transition_at, ips_internal, ips_external,
	last_restart_completed_at, deleted_at`

func (r *InstanceRepo) Get(ctx context.Context, id string) (*domain.Instance, error) {
	q := fmt.Sprintf(`SELECT %s FROM instances WHERE id = $1 AND deleted_at IS NULL`, instanceSelectCols)
	row := r.pool.QueryRow(ctx, q, id)
	inst, err := scanInstance(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, service.ErrNotFound
	}
	return inst, err
}

func (r *InstanceRepo) GetIncludingDeleted(ctx context.Context, id string) (*domain.Instance, error) {
	q := fmt.Sprintf(`SELECT %s FROM instances WHERE id = $1`, instanceSelectCols)
	row := r.pool.QueryRow(ctx, q, id)
	inst, err := scanInstance(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, service.ErrNotFound
	}
	return inst, err
}

func (r *InstanceRepo) List(ctx context.Context, f service.InstanceFilter, page service.Pagination) ([]*domain.Instance, string, error) {
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

	q := fmt.Sprintf(`SELECT %s FROM instances %s ORDER BY %s LIMIT $%d`,
		instanceSelectCols, where, orderClause, argIdx)
	args = append(args, pageSize+1)

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var result []*domain.Instance
	for rows.Next() {
		inst, err := scanInstance(rows)
		if err != nil {
			return nil, "", err
		}
		result = append(result, inst)
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

func (r *InstanceRepo) Insert(ctx context.Context, inst *domain.Instance) (*domain.Instance, error) {
	labelsJSON, _ := json.Marshal(inst.Labels)
	metaJSON, _ := json.Marshal(inst.Metadata)
	secondaryJSON, _ := json.Marshal(inst.SecondaryDisks)
	niJSON, _ := json.Marshal(inst.NetworkInterfaces)
	ipsIntJSON, _ := json.Marshal(inst.IPs.Internal)
	ipsExtJSON, _ := json.Marshal(inst.IPs.External)

	const q = `
		INSERT INTO instances (
			id, folder_id, created_at, name, description, labels,
			zone_id, platform_id,
			resources_cores, resources_memory, resources_core_fraction, resources_gpus,
			status, fqdn, metadata,
			boot_disk_id, boot_disk_device_name, boot_disk_auto_delete,
			secondary_disks, network_interfaces,
			service_account_id, scheduling_preemptible, desired_power_state,
			generation, resource_version, observed_generation,
			status_last_transition_at, ips_internal, ips_external
		) VALUES (
			$1,$2,$3,$4,$5,$6,
			$7,$8,
			$9,$10,$11,$12,
			$13,$14,$15,
			$16,$17,$18,
			$19,$20,
			$21,$22,$23,
			$24,$25,$26,
			$27,$28,$29
		)`

	_, err := r.pool.Exec(ctx, q,
		inst.ID, inst.FolderID, inst.CreatedAt, inst.Name, inst.Description, labelsJSON,
		inst.ZoneID, inst.PlatformID,
		inst.Resources.Cores, inst.Resources.Memory, inst.Resources.CoreFraction, inst.Resources.GPUs,
		int32(inst.Status), inst.FQDN, metaJSON,
		inst.BootDisk.DiskID, inst.BootDisk.DeviceName, inst.BootDisk.AutoDelete,
		secondaryJSON, niJSON,
		inst.ServiceAccountID, inst.SchedulingPolicy.Preemptible, int32(inst.DesiredPowerState),
		inst.Generation, inst.ResourceVersion, inst.ObservedGeneration,
		inst.StatusLastTransitionAt, ipsIntJSON, ipsExtJSON,
	)
	if err != nil {
		return nil, err
	}
	return r.GetIncludingDeleted(ctx, inst.ID)
}

func (r *InstanceRepo) Update(ctx context.Context, inst *domain.Instance) (*domain.Instance, error) {
	labelsJSON, _ := json.Marshal(inst.Labels)
	metaJSON, _ := json.Marshal(inst.Metadata)
	secondaryJSON, _ := json.Marshal(inst.SecondaryDisks)
	niJSON, _ := json.Marshal(inst.NetworkInterfaces)
	ipsIntJSON, _ := json.Marshal(inst.IPs.Internal)
	ipsExtJSON, _ := json.Marshal(inst.IPs.External)

	const q = `
		UPDATE instances SET
			name=$2, description=$3, labels=$4,
			resources_cores=$5, resources_memory=$6, resources_core_fraction=$7, resources_gpus=$8,
			status=$9, fqdn=$10, metadata=$11,
			boot_disk_id=$12, boot_disk_device_name=$13, boot_disk_auto_delete=$14,
			secondary_disks=$15, network_interfaces=$16,
			service_account_id=$17, scheduling_preemptible=$18, desired_power_state=$19,
			generation=$20, resource_version=$21, observed_generation=$22,
			status_last_transition_at=$23, ips_internal=$24, ips_external=$25,
			last_restart_completed_at=$26, deleted_at=$27
		WHERE id=$1`

	_, err := r.pool.Exec(ctx, q,
		inst.ID,
		inst.Name, inst.Description, labelsJSON,
		inst.Resources.Cores, inst.Resources.Memory, inst.Resources.CoreFraction, inst.Resources.GPUs,
		int32(inst.Status), inst.FQDN, metaJSON,
		inst.BootDisk.DiskID, inst.BootDisk.DeviceName, inst.BootDisk.AutoDelete,
		secondaryJSON, niJSON,
		inst.ServiceAccountID, inst.SchedulingPolicy.Preemptible, int32(inst.DesiredPowerState),
		inst.Generation, inst.ResourceVersion, inst.ObservedGeneration,
		inst.StatusLastTransitionAt, ipsIntJSON, ipsExtJSON,
		inst.LastRestartCompletedAt, inst.DeletedAt,
	)
	if err != nil {
		return nil, err
	}
	return r.GetIncludingDeleted(ctx, inst.ID)
}

func (r *InstanceRepo) ListPendingReconcile(ctx context.Context, limit int) ([]*domain.Instance, error) {
	q := fmt.Sprintf(`
		SELECT %s FROM instances
		WHERE status IN (1, 3, 5, 7)
		ORDER BY status_last_transition_at ASC
		LIMIT $1
		FOR UPDATE SKIP LOCKED`, instanceSelectCols)

	rows, err := r.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*domain.Instance
	for rows.Next() {
		inst, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, inst)
	}
	return result, rows.Err()
}

// HardDelete физически удаляет Instance (вызывается reconciler-ом после DELETING).
func (r *InstanceRepo) HardDelete(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, "DELETE FROM instances WHERE id = $1", id)
	return err
}

// ---- scan helpers ----

type scannable interface {
	Scan(dest ...any) error
}

func scanInstance(row scannable) (*domain.Instance, error) {
	var inst domain.Instance
	var labelsJSON, metaJSON []byte
	var secondaryJSON, niJSON []byte
	var ipsIntJSON, ipsExtJSON []byte
	var statusInt, powerStateInt int32

	err := row.Scan(
		&inst.ID, &inst.FolderID, &inst.CreatedAt, &inst.Name, &inst.Description, &labelsJSON,
		&inst.ZoneID, &inst.PlatformID,
		&inst.Resources.Cores, &inst.Resources.Memory, &inst.Resources.CoreFraction, &inst.Resources.GPUs,
		&statusInt, &inst.FQDN, &metaJSON,
		&inst.BootDisk.DiskID, &inst.BootDisk.DeviceName, &inst.BootDisk.AutoDelete,
		&secondaryJSON, &niJSON,
		&inst.ServiceAccountID, &inst.SchedulingPolicy.Preemptible, &powerStateInt,
		&inst.Generation, &inst.ResourceVersion, &inst.ObservedGeneration,
		&inst.StatusLastTransitionAt, &ipsIntJSON, &ipsExtJSON,
		&inst.LastRestartCompletedAt, &inst.DeletedAt,
	)
	if err != nil {
		return nil, err
	}
	inst.Status = domain.InstanceStatus(statusInt)
	inst.DesiredPowerState = domain.PowerState(powerStateInt)

	if labelsJSON != nil {
		_ = json.Unmarshal(labelsJSON, &inst.Labels)
	}
	if metaJSON != nil {
		_ = json.Unmarshal(metaJSON, &inst.Metadata)
	}
	if secondaryJSON != nil {
		_ = json.Unmarshal(secondaryJSON, &inst.SecondaryDisks)
	}
	if niJSON != nil {
		_ = json.Unmarshal(niJSON, &inst.NetworkInterfaces)
	}
	if ipsIntJSON != nil {
		_ = json.Unmarshal(ipsIntJSON, &inst.IPs.Internal)
	}
	if ipsExtJSON != nil {
		_ = json.Unmarshal(ipsExtJSON, &inst.IPs.External)
	}
	return &inst, nil
}
