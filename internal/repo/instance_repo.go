package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	coreerrors "github.com/PRO-Robotech/kacho-corelib/errors"
	"github.com/PRO-Robotech/kacho-corelib/outbox"
	"github.com/PRO-Robotech/kacho-corelib/selector"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	commonv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/common/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	sqlcgen "github.com/PRO-Robotech/kacho-compute/internal/repo/gen"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// instanceSpec хранит spec инстанса в JSONB.
type instanceSpec struct {
	DisplayName       string                    `json:"display_name"`
	Description       string                    `json:"description"`
	PlatformID        string                    `json:"platform_id"`
	ZoneID            string                    `json:"zone_id"`
	Resources         *domain.ResourceSpec      `json:"resources,omitempty"`
	BootDisk          *domain.AttachedDisk      `json:"boot_disk,omitempty"`
	SecondaryDisks    []*domain.AttachedDisk    `json:"secondary_disks,omitempty"`
	NetworkInterfaces []*domain.NetworkInterface `json:"network_interfaces,omitempty"`
	SchedulingPolicy  *domain.SchedulingPolicy  `json:"scheduling_policy,omitempty"`
	Metadata          map[string]string         `json:"metadata,omitempty"`
	FQDN              string                    `json:"fqdn,omitempty"`
	DesiredPowerState int32                     `json:"desired_power_state"`
}

// instanceStatus хранит status инстанса в JSONB.
type instanceStatus struct {
	State                  string `json:"state"`
	StateLastTransitionAt  string `json:"state_last_transition_at,omitempty"`
	InternalIP             string `json:"internal_ip,omitempty"`
	ExternalIP             string `json:"external_ip,omitempty"`
	StatusFQDN             string `json:"fqdn,omitempty"`
	HostID                 string `json:"host_id,omitempty"`
	LastRestartCompletedAt string `json:"last_restart_completed_at,omitempty"`
	ObservedGeneration     int64  `json:"observed_generation,omitempty"`
}

// InstanceRepo реализует service.InstanceRepo.
type InstanceRepo struct {
	pool         *pgxpool.Pool
	transactor   *coredb.Transactor
	outboxWriter *outbox.Writer
}

// NewInstanceRepo создаёт InstanceRepo.
func NewInstanceRepo(pool *pgxpool.Pool, transactor *coredb.Transactor, outboxWriter *outbox.Writer) *InstanceRepo {
	return &InstanceRepo{pool: pool, transactor: transactor, outboxWriter: outboxWriter}
}

func (r *InstanceRepo) GetByUID(ctx context.Context, uid string) (*domain.Instance, error) {
	pgUID, err := strToUUID(uid)
	if err != nil {
		return nil, coreerrors.InvalidArgument().AddFieldViolation("uid", "invalid uuid").Err()
	}
	q := sqlcgen.New(r.pool)
	row, err := q.GetInstanceByUID(ctx, pgUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return sqlcInstanceToDomain(row), nil
}

func (r *InstanceRepo) GetByFolderAndName(ctx context.Context, folderID, name string) (*domain.Instance, error) {
	pgFolderID, err := strToUUID(folderID)
	if err != nil {
		return nil, coreerrors.InvalidArgument().AddFieldViolation("folder_id", "invalid uuid").Err()
	}
	q := sqlcgen.New(r.pool)
	row, err := q.GetInstanceByFolderAndName(ctx, sqlcgen.GetInstanceByFolderAndNameParams{
		FolderID: pgFolderID,
		Name:     name,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return sqlcInstanceToDomain(row), nil
}

func (r *InstanceRepo) List(ctx context.Context, selectors []service.Selector, page service.Pagination) ([]*domain.Instance, string, int64, error) {
	pageSize := int(page.PageSize)
	if pageSize <= 0 || pageSize > 1000 {
		pageSize = 100
	}

	snapshotRV, err := r.SnapshotResourceVersion(ctx)
	if err != nil {
		return nil, "", 0, err
	}

	var coreSelectors []selector.Selector
	for _, s := range selectors {
		cs := selector.Selector{Labels: s.Labels}
		if s.Name != "" || s.FolderID != "" {
			cs.Field = &selector.FieldFilter{
				Name:     s.Name,
				FolderID: s.FolderID,
			}
		}
		coreSelectors = append(coreSelectors, cs)
	}

	br, err := selector.Build(coreSelectors)
	if err != nil {
		return nil, "", 0, err
	}

	var pageClause string
	var pageArgs []any
	var pageToken *selector.PageToken
	if page.PageToken != "" {
		var tok selector.PageToken
		if jsonErr := json.Unmarshal([]byte(page.PageToken), &tok); jsonErr == nil {
			pageToken = &tok
		}
	}

	paramBase := len(br.Args) + 1
	if pageToken != nil {
		pageClause, pageArgs = selector.BuildPageClause(pageToken, paramBase)
		paramBase += len(pageArgs)
	}

	query := buildListQueryInstances(br, pageClause)
	args := append(br.Args, pageArgs...)
	args = append(args, pageSize+1)
	limitParam := fmt.Sprintf("$%d", paramBase)
	query += fmt.Sprintf(" ORDER BY resource_version ASC, uid ASC LIMIT %s", limitParam)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", 0, err
	}
	defer rows.Close()

	var instances []*domain.Instance
	for rows.Next() {
		var row sqlcgen.Instance
		if scanErr := rows.Scan(
			&row.Uid, &row.FolderID, &row.CloudID, &row.OrganizationID, &row.Name,
			&row.Labels, &row.Annotations, &row.CreationTimestamp, &row.ResourceVersion,
			&row.Generation, &row.DeletionTimestamp, &row.Finalizers, &row.RestartedAt,
			&row.Spec, &row.Status,
		); scanErr != nil {
			return nil, "", 0, scanErr
		}
		instances = append(instances, sqlcInstanceToDomain(row))
	}
	if err := rows.Err(); err != nil {
		return nil, "", 0, err
	}

	var nextToken string
	if len(instances) > pageSize {
		instances = instances[:pageSize]
		last := instances[len(instances)-1]
		tok := selector.PageToken{LastResourceVersion: last.ResourceVersion, LastUID: last.UID}
		b, _ := json.Marshal(tok)
		nextToken = string(b)
	}

	return instances, nextToken, snapshotRV, nil
}

func (r *InstanceRepo) SnapshotResourceVersion(ctx context.Context) (int64, error) {
	q := sqlcgen.New(r.pool)
	return q.SnapshotResourceVersion(ctx)
}

func (r *InstanceRepo) Insert(ctx context.Context, inst *domain.Instance) (*domain.Instance, error) {
	pgUID, err := strToUUID(inst.UID)
	if err != nil {
		return nil, err
	}
	pgFolderID, err := strToUUID(inst.FolderID)
	if err != nil {
		return nil, err
	}
	pgCloudID := strToUUIDOrZero(inst.CloudID)
	pgOrgID := strToUUIDOrZero(inst.OrganizationID)

	specJSON := anyToJSONB(instanceSpec{
		DisplayName:       inst.DisplayName,
		Description:       inst.Description,
		PlatformID:        inst.PlatformID,
		ZoneID:            inst.ZoneID,
		Resources:         inst.Resources,
		BootDisk:          inst.BootDisk,
		SecondaryDisks:    inst.SecondaryDisks,
		NetworkInterfaces: inst.NetworkInterfaces,
		SchedulingPolicy:  inst.SchedulingPolicy,
		Metadata:          inst.Metadata,
		FQDN:              inst.FQDN,
		DesiredPowerState: int32(inst.DesiredPowerState),
	})
	statusJSON := anyToJSONB(instanceStatus{
		State:                 instanceStateToString(inst.State),
		StateLastTransitionAt: time.Now().UTC().Format(time.RFC3339Nano),
	})

	var result *domain.Instance
	txErr := r.transactor.InTx(ctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		row, insertErr := q.InsertInstance(ctx, sqlcgen.InsertInstanceParams{
			Uid:            pgUID,
			FolderID:       pgFolderID,
			CloudID:        pgCloudID,
			OrganizationID: pgOrgID,
			Name:           inst.Name,
			Labels:         mapToJSONB(inst.Labels),
			Annotations:    mapToJSONB(inst.Annotations),
			Finalizers:     nonNilStrings(inst.Finalizers),
			Spec:           specJSON,
			Status:         statusJSON,
		})
		if insertErr != nil {
			return insertErr
		}
		result = sqlcInstanceToDomain(row)

		data, _ := proto.Marshal(domainInstanceToProto(result))
		_, evtErr := r.outboxWriter.WriteEvent(ctx, tx, outbox.Event{
			EventType:    "ADDED",
			ResourceKind: "Instance",
			ResourceUID:  result.UID,
			Data:         data,
		})
		return evtErr
	})
	if txErr != nil {
		return nil, txErr
	}
	_ = r.outboxWriter.Notify(ctx, r.pool)
	return result, nil
}

func (r *InstanceRepo) Update(ctx context.Context, inst *domain.Instance) (*domain.Instance, error) {
	pgUID, err := strToUUID(inst.UID)
	if err != nil {
		return nil, err
	}
	specJSON := anyToJSONB(instanceSpec{
		DisplayName:       inst.DisplayName,
		Description:       inst.Description,
		PlatformID:        inst.PlatformID,
		ZoneID:            inst.ZoneID,
		Resources:         inst.Resources,
		BootDisk:          inst.BootDisk,
		SecondaryDisks:    inst.SecondaryDisks,
		NetworkInterfaces: inst.NetworkInterfaces,
		SchedulingPolicy:  inst.SchedulingPolicy,
		Metadata:          inst.Metadata,
		FQDN:              inst.FQDN,
		DesiredPowerState: int32(inst.DesiredPowerState),
	})

	var result *domain.Instance
	txErr := r.transactor.InTx(ctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		row, updateErr := q.UpdateInstance(ctx, sqlcgen.UpdateInstanceParams{
			Uid:         pgUID,
			Labels:      mapToJSONB(inst.Labels),
			Annotations: mapToJSONB(inst.Annotations),
			Spec:        specJSON,
		})
		if updateErr != nil {
			return updateErr
		}
		result = sqlcInstanceToDomain(row)

		data, _ := proto.Marshal(domainInstanceToProto(result))
		_, evtErr := r.outboxWriter.WriteEvent(ctx, tx, outbox.Event{
			EventType:    "MODIFIED",
			ResourceKind: "Instance",
			ResourceUID:  result.UID,
			Data:         data,
		})
		return evtErr
	})
	if txErr != nil {
		return nil, txErr
	}
	_ = r.outboxWriter.Notify(ctx, r.pool)
	return result, nil
}

func (r *InstanceRepo) UpdateStatus(ctx context.Context, inst *domain.Instance) (*domain.Instance, error) {
	pgUID, err := strToUUID(inst.UID)
	if err != nil {
		return nil, err
	}

	st := instanceStatus{
		State:                 instanceStateToString(inst.State),
		StateLastTransitionAt: inst.StateLastTransitionAt.UTC().Format(time.RFC3339Nano),
		StatusFQDN:            inst.StatusFQDN,
		HostID:                inst.HostID,
		ObservedGeneration:    inst.ObservedGeneration,
	}
	if inst.IPs != nil {
		st.InternalIP = inst.IPs.Internal
		st.ExternalIP = inst.IPs.External
	}
	if inst.LastRestartCompletedAt != nil {
		st.LastRestartCompletedAt = inst.LastRestartCompletedAt.UTC().Format(time.RFC3339Nano)
	}
	statusJSON := anyToJSONB(st)

	var result *domain.Instance
	txErr := r.transactor.InTx(ctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		row, updateErr := q.UpdateInstanceStatus(ctx, sqlcgen.UpdateInstanceStatusParams{
			Uid:    pgUID,
			Status: statusJSON,
		})
		if updateErr != nil {
			return updateErr
		}
		result = sqlcInstanceToDomain(row)

		data, _ := proto.Marshal(domainInstanceToProto(result))
		_, evtErr := r.outboxWriter.WriteEvent(ctx, tx, outbox.Event{
			EventType:    "MODIFIED",
			ResourceKind: "Instance",
			ResourceUID:  result.UID,
			Data:         data,
		})
		return evtErr
	})
	if txErr != nil {
		return nil, txErr
	}
	_ = r.outboxWriter.Notify(ctx, r.pool)
	return result, nil
}

func (r *InstanceRepo) UpdateMetadata(ctx context.Context, uid string, finalizers []string, updateFinalizers bool, restartedAt *string) (*domain.Instance, error) {
	pgUID, err := strToUUID(uid)
	if err != nil {
		return nil, err
	}

	var result *domain.Instance
	txErr := r.transactor.InTx(ctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		var row sqlcgen.Instance
		var updateErr error

		if updateFinalizers {
			row, updateErr = q.UpdateInstanceFinalizers(ctx, sqlcgen.UpdateInstanceFinalizersParams{
				Uid:        pgUID,
				Finalizers: finalizers,
			})
			if updateErr != nil {
				return updateErr
			}
			result = sqlcInstanceToDomain(row)
		}

		if restartedAt != nil && *restartedAt != "" {
			row, updateErr = q.UpdateInstanceRestartedAt(ctx, sqlcgen.UpdateInstanceRestartedAtParams{
				Uid:     pgUID,
				Column2: pgtype.Timestamptz{Time: parseRFC3339(*restartedAt), Valid: true},
			})
			if updateErr != nil {
				return updateErr
			}
			result = sqlcInstanceToDomain(row)
		}

		if result == nil {
			// nothing changed, fetch current
			row, updateErr = q.GetInstanceByUID(ctx, pgUID)
			if updateErr != nil {
				return updateErr
			}
			result = sqlcInstanceToDomain(row)
			return nil
		}

		data, _ := proto.Marshal(domainInstanceToProto(result))
		_, evtErr := r.outboxWriter.WriteEvent(ctx, tx, outbox.Event{
			EventType:    "MODIFIED",
			ResourceKind: "Instance",
			ResourceUID:  result.UID,
			Data:         data,
		})
		return evtErr
	})
	if txErr != nil {
		return nil, txErr
	}
	if result != nil {
		_ = r.outboxWriter.Notify(ctx, r.pool)
	}
	return result, nil
}

func parseRFC3339(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func (r *InstanceRepo) SetRestart(ctx context.Context, uid string) (*domain.Instance, error) {
	pgUID, err := strToUUID(uid)
	if err != nil {
		return nil, err
	}

	var result *domain.Instance
	txErr := r.transactor.InTx(ctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		row, updateErr := q.SetInstanceRestart(ctx, pgUID)
		if updateErr != nil {
			return updateErr
		}
		result = sqlcInstanceToDomain(row)

		data, _ := proto.Marshal(domainInstanceToProto(result))
		_, evtErr := r.outboxWriter.WriteEvent(ctx, tx, outbox.Event{
			EventType:    "MODIFIED",
			ResourceKind: "Instance",
			ResourceUID:  result.UID,
			Data:         data,
		})
		return evtErr
	})
	if txErr != nil {
		return nil, txErr
	}
	_ = r.outboxWriter.Notify(ctx, r.pool)
	return result, nil
}

func (r *InstanceRepo) SoftDelete(ctx context.Context, uid string) error {
	pgUID, err := strToUUID(uid)
	if err != nil {
		return err
	}
	txErr := r.transactor.InTx(ctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if err := q.SoftDeleteInstance(ctx, pgUID); err != nil {
			return err
		}
		_, evtErr := r.outboxWriter.WriteEvent(ctx, tx, outbox.Event{
			EventType:    "MODIFIED",
			ResourceKind: "Instance",
			ResourceUID:  uid,
			Data:         nil,
		})
		return evtErr
	})
	if txErr != nil {
		return txErr
	}
	_ = r.outboxWriter.Notify(ctx, r.pool)
	return nil
}

func (r *InstanceRepo) HardDelete(ctx context.Context, uid string) error {
	pgUID, err := strToUUID(uid)
	if err != nil {
		return err
	}
	txErr := r.transactor.InTx(ctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if err := q.DeleteInstance(ctx, pgUID); err != nil {
			return err
		}
		_, evtErr := r.outboxWriter.WriteEvent(ctx, tx, outbox.Event{
			EventType:    "DELETED",
			ResourceKind: "Instance",
			ResourceUID:  uid,
			Data:         nil,
		})
		return evtErr
	})
	if txErr != nil {
		return txErr
	}
	_ = r.outboxWriter.Notify(ctx, r.pool)
	return nil
}

func (r *InstanceRepo) ListPendingReconcile(ctx context.Context) ([]*domain.Instance, error) {
	q := sqlcgen.New(r.pool)
	rows, err := q.ListInstancesPendingReconcile(ctx)
	if err != nil {
		return nil, err
	}
	var instances []*domain.Instance
	for _, row := range rows {
		instances = append(instances, sqlcInstanceToDomain(row))
	}
	return instances, nil
}

// sqlcInstanceToDomain конвертирует sqlc-модель в domain.Instance.
func sqlcInstanceToDomain(row sqlcgen.Instance) *domain.Instance {
	var sp instanceSpec
	jsonbToAny(row.Spec, &sp)

	var st instanceStatus
	jsonbToAny(row.Status, &st)

	inst := &domain.Instance{
		UID:               uuidToStr(row.Uid),
		FolderID:          uuidToStr(row.FolderID),
		CloudID:           uuidToStr(row.CloudID),
		OrganizationID:    uuidToStr(row.OrganizationID),
		Name:              row.Name,
		Labels:            jsonbToMap(row.Labels),
		Annotations:       jsonbToMap(row.Annotations),
		CreationTimestamp: tsToTime(row.CreationTimestamp),
		ResourceVersion:   row.ResourceVersion,
		Generation:        row.Generation,
		DeletionTimestamp: tsToTimePtr(row.DeletionTimestamp),
		Finalizers:        row.Finalizers,
		RestartedAt:       tsToTimePtr(row.RestartedAt),

		DisplayName:       sp.DisplayName,
		Description:       sp.Description,
		PlatformID:        sp.PlatformID,
		ZoneID:            sp.ZoneID,
		Resources:         sp.Resources,
		BootDisk:          sp.BootDisk,
		SecondaryDisks:    sp.SecondaryDisks,
		NetworkInterfaces: sp.NetworkInterfaces,
		SchedulingPolicy:  sp.SchedulingPolicy,
		Metadata:          sp.Metadata,
		FQDN:              sp.FQDN,
		DesiredPowerState: domain.DesiredPowerState(sp.DesiredPowerState),

		State:              instanceStateFromString(st.State),
		StatusFQDN:         st.StatusFQDN,
		HostID:             st.HostID,
		ObservedGeneration: st.ObservedGeneration,
	}
	if st.StateLastTransitionAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, st.StateLastTransitionAt); err == nil {
			inst.StateLastTransitionAt = t
		}
	}
	if st.LastRestartCompletedAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, st.LastRestartCompletedAt); err == nil {
			inst.LastRestartCompletedAt = &t
		}
	}
	if st.InternalIP != "" || st.ExternalIP != "" {
		inst.IPs = &domain.IPs{
			Internal: st.InternalIP,
			External: st.ExternalIP,
		}
	}
	return inst
}

func instanceStateToString(s domain.InstanceState) string {
	switch s {
	case domain.InstanceStateProvisioning:
		return "PROVISIONING"
	case domain.InstanceStateRunning:
		return "RUNNING"
	case domain.InstanceStateStopping:
		return "STOPPING"
	case domain.InstanceStateStopped:
		return "STOPPED"
	case domain.InstanceStateStarting:
		return "STARTING"
	case domain.InstanceStateUpdating:
		return "UPDATING"
	case domain.InstanceStateError:
		return "ERROR"
	case domain.InstanceStateDeleting:
		return "DELETING"
	default:
		return "UNSPECIFIED"
	}
}

func instanceStateFromString(s string) domain.InstanceState {
	switch s {
	case "PROVISIONING":
		return domain.InstanceStateProvisioning
	case "RUNNING":
		return domain.InstanceStateRunning
	case "STOPPING":
		return domain.InstanceStateStopping
	case "STOPPED":
		return domain.InstanceStateStopped
	case "STARTING":
		return domain.InstanceStateStarting
	case "UPDATING":
		return domain.InstanceStateUpdating
	case "ERROR":
		return domain.InstanceStateError
	case "DELETING":
		return domain.InstanceStateDeleting
	default:
		return domain.InstanceStateUnspecified
	}
}

func domainInstanceToProto(inst *domain.Instance) *computev1.Instance {
	meta := &commonv1.ResourceMeta{
		Uid:             inst.UID,
		Name:            inst.Name,
		FolderId:        inst.FolderID,
		CloudId:         inst.CloudID,
		OrganizationId:  inst.OrganizationID,
		Labels:          inst.Labels,
		Annotations:     inst.Annotations,
		ResourceVersion: fmt.Sprintf("%d", inst.ResourceVersion),
		Generation:      inst.Generation,
		Finalizers:      inst.Finalizers,
	}
	if !inst.CreationTimestamp.IsZero() {
		meta.CreationTimestamp = timestamppb.New(inst.CreationTimestamp)
	}
	if inst.DeletionTimestamp != nil {
		meta.DeletionTimestamp = timestamppb.New(*inst.DeletionTimestamp)
	}
	if inst.RestartedAt != nil {
		meta.RestartedAt = timestamppb.New(*inst.RestartedAt)
	}

	spec := &computev1.InstanceSpec{
		DisplayName:       inst.DisplayName,
		Description:       inst.Description,
		PlatformId:        inst.PlatformID,
		ZoneId:            inst.ZoneID,
		Fqdn:              inst.FQDN,
		DesiredPowerState: computev1.DesiredPowerState(inst.DesiredPowerState),
		Metadata:          inst.Metadata,
	}
	if inst.Resources != nil {
		spec.Resources = &computev1.ResourceSpec{
			Cores:        inst.Resources.Cores,
			Memory:       inst.Resources.Memory,
			CoreFraction: inst.Resources.CoreFraction,
		}
	}
	if inst.BootDisk != nil {
		spec.BootDisk = &computev1.AttachedDisk{
			DiskId:     inst.BootDisk.DiskID,
			DeviceName: inst.BootDisk.DeviceName,
			AutoDelete: inst.BootDisk.AutoDelete,
		}
	}
	for _, sd := range inst.SecondaryDisks {
		spec.SecondaryDisks = append(spec.SecondaryDisks, &computev1.AttachedDisk{
			DiskId:     sd.DiskID,
			DeviceName: sd.DeviceName,
			AutoDelete: sd.AutoDelete,
		})
	}
	for _, ni := range inst.NetworkInterfaces {
		pbNI := &computev1.NetworkInterface{
			SubnetId:         ni.SubnetID,
			SecurityGroupIds: ni.SecurityGroupIDs,
		}
		if ni.PrimaryV4Address != nil {
			pbNI.PrimaryV4Address = &computev1.PrimaryV4Address{
				Address: ni.PrimaryV4Address.Address,
			}
		}
		spec.NetworkInterfaces = append(spec.NetworkInterfaces, pbNI)
	}
	if inst.SchedulingPolicy != nil {
		spec.SchedulingPolicy = &computev1.SchedulingPolicy{
			Preemptible: inst.SchedulingPolicy.Preemptible,
		}
	}

	pbState := computev1.InstanceStatus_STATE_UNSPECIFIED
	switch inst.State {
	case domain.InstanceStateProvisioning:
		pbState = computev1.InstanceStatus_STATE_PROVISIONING
	case domain.InstanceStateRunning:
		pbState = computev1.InstanceStatus_STATE_RUNNING
	case domain.InstanceStateStopping:
		pbState = computev1.InstanceStatus_STATE_STOPPING
	case domain.InstanceStateStopped:
		pbState = computev1.InstanceStatus_STATE_STOPPED
	case domain.InstanceStateStarting:
		pbState = computev1.InstanceStatus_STATE_STARTING
	case domain.InstanceStateUpdating:
		pbState = computev1.InstanceStatus_STATE_UPDATING
	case domain.InstanceStateError:
		pbState = computev1.InstanceStatus_STATE_ERROR
	case domain.InstanceStateDeleting:
		pbState = computev1.InstanceStatus_STATE_DELETING
	}

	st := &computev1.InstanceStatus{
		State:  pbState,
		Fqdn:   inst.StatusFQDN,
		HostId: inst.HostID,
	}
	if !inst.StateLastTransitionAt.IsZero() {
		st.StateLastTransitionAt = timestamppb.New(inst.StateLastTransitionAt)
	}
	if inst.IPs != nil {
		st.Ips = &computev1.IPs{
			Internal: inst.IPs.Internal,
			External: inst.IPs.External,
		}
	}
	if inst.LastRestartCompletedAt != nil {
		st.LastRestartCompletedAt = timestamppb.New(*inst.LastRestartCompletedAt)
	}

	return &computev1.Instance{
		Metadata: meta,
		Spec:     spec,
		Status:   st,
	}
}

func buildListQueryInstances(br selector.BuildResult, pageClause string) string {
	var sb strings.Builder
	sb.WriteString(
		"SELECT uid, folder_id, cloud_id, organization_id, name, labels, annotations, creation_timestamp, resource_version, generation, deletion_timestamp, finalizers, restarted_at, spec, status FROM instances WHERE deletion_timestamp IS NULL",
	)
	if br.WhereClause != "" {
		clause := strings.TrimPrefix(br.WhereClause, "WHERE ")
		sb.WriteString(" AND (")
		sb.WriteString(clause)
		sb.WriteString(")")
	}
	if pageClause != "" {
		sb.WriteString(" AND (")
		sb.WriteString(pageClause)
		sb.WriteString(")")
	}
	return sb.String()
}
