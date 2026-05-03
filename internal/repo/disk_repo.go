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
	"google.golang.org/protobuf/proto"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	coreerrors "github.com/PRO-Robotech/kacho-corelib/errors"
	"github.com/PRO-Robotech/kacho-corelib/outbox"
	"github.com/PRO-Robotech/kacho-corelib/selector"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	commonv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/common/v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	sqlcgen "github.com/PRO-Robotech/kacho-compute/internal/repo/gen"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// diskSpec хранит spec в JSONB.
type diskSpec struct {
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	DiskTypeID  string `json:"disk_type_id"`
	ZoneID      string `json:"zone_id"`
	Size        string `json:"size"`
	ImageID     string `json:"image_id,omitempty"`
}

// diskStatus хранит status в JSONB.
type diskStatus struct {
	State                 string `json:"state"`
	StateLastTransitionAt string `json:"state_last_transition_at,omitempty"`
	AttachedToInstanceID  string `json:"attached_to_instance_id,omitempty"`
	DeviceName            string `json:"device_name,omitempty"`
	ObservedGeneration    int64  `json:"observed_generation,omitempty"`
}

// DiskRepo реализует service.DiskRepo.
type DiskRepo struct {
	pool         *pgxpool.Pool
	transactor   *coredb.Transactor
	outboxWriter *outbox.Writer
}

// NewDiskRepo создаёт DiskRepo.
func NewDiskRepo(pool *pgxpool.Pool, transactor *coredb.Transactor, outboxWriter *outbox.Writer) *DiskRepo {
	return &DiskRepo{pool: pool, transactor: transactor, outboxWriter: outboxWriter}
}

func (r *DiskRepo) GetByUID(ctx context.Context, uid string) (*domain.Disk, error) {
	pgUID, err := strToUUID(uid)
	if err != nil {
		return nil, coreerrors.InvalidArgument().AddFieldViolation("uid", "invalid uuid").Err()
	}
	q := sqlcgen.New(r.pool)
	row, err := q.GetDiskByUID(ctx, pgUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return sqlcDiskToDomain(row), nil
}

func (r *DiskRepo) GetByFolderAndName(ctx context.Context, folderID, name string) (*domain.Disk, error) {
	pgFolderID, err := strToUUID(folderID)
	if err != nil {
		return nil, coreerrors.InvalidArgument().AddFieldViolation("folder_id", "invalid uuid").Err()
	}
	q := sqlcgen.New(r.pool)
	row, err := q.GetDiskByFolderAndName(ctx, sqlcgen.GetDiskByFolderAndNameParams{
		FolderID: pgFolderID,
		Name:     name,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return sqlcDiskToDomain(row), nil
}

func (r *DiskRepo) List(ctx context.Context, selectors []service.Selector, page service.Pagination) ([]*domain.Disk, string, int64, error) {
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

	query := buildListQueryDisks(br, pageClause)
	args := append(br.Args, pageArgs...)
	args = append(args, pageSize+1)
	limitParam := fmt.Sprintf("$%d", paramBase)
	query += fmt.Sprintf(" ORDER BY resource_version ASC, uid ASC LIMIT %s", limitParam)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", 0, err
	}
	defer rows.Close()

	var disks []*domain.Disk
	for rows.Next() {
		var row sqlcgen.Disk
		if scanErr := rows.Scan(
			&row.Uid, &row.FolderID, &row.CloudID, &row.OrganizationID, &row.Name,
			&row.Labels, &row.Annotations, &row.CreationTimestamp, &row.ResourceVersion,
			&row.Generation, &row.DeletionTimestamp, &row.Finalizers, &row.Spec, &row.Status,
		); scanErr != nil {
			return nil, "", 0, scanErr
		}
		disks = append(disks, sqlcDiskToDomain(row))
	}
	if err := rows.Err(); err != nil {
		return nil, "", 0, err
	}

	var nextToken string
	if len(disks) > pageSize {
		disks = disks[:pageSize]
		last := disks[len(disks)-1]
		tok := selector.PageToken{LastResourceVersion: last.ResourceVersion, LastUID: last.UID}
		b, _ := json.Marshal(tok)
		nextToken = string(b)
	}

	return disks, nextToken, snapshotRV, nil
}

func (r *DiskRepo) SnapshotResourceVersion(ctx context.Context) (int64, error) {
	q := sqlcgen.New(r.pool)
	return q.SnapshotResourceVersion(ctx)
}

func (r *DiskRepo) Insert(ctx context.Context, disk *domain.Disk) (*domain.Disk, error) {
	pgUID, err := strToUUID(disk.UID)
	if err != nil {
		return nil, err
	}
	pgFolderID, err := strToUUID(disk.FolderID)
	if err != nil {
		return nil, err
	}
	pgCloudID := strToUUIDOrZero(disk.CloudID)
	pgOrgID := strToUUIDOrZero(disk.OrganizationID)

	specJSON := anyToJSONB(diskSpec{
		DisplayName: disk.DisplayName,
		Description: disk.Description,
		DiskTypeID:  disk.DiskTypeID,
		ZoneID:      disk.ZoneID,
		Size:        disk.Size,
		ImageID:     disk.ImageID,
	})
	statusJSON := anyToJSONB(diskStatus{
		State:                 diskStateToString(disk.State),
		StateLastTransitionAt: time.Now().UTC().Format(time.RFC3339Nano),
	})

	var result *domain.Disk
	txErr := r.transactor.InTx(ctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		row, insertErr := q.InsertDisk(ctx, sqlcgen.InsertDiskParams{
			Uid:            pgUID,
			FolderID:       pgFolderID,
			CloudID:        pgCloudID,
			OrganizationID: pgOrgID,
			Name:           disk.Name,
			Labels:         mapToJSONB(disk.Labels),
			Annotations:    mapToJSONB(disk.Annotations),
			Finalizers:     nonNilStrings(disk.Finalizers),
			Spec:           specJSON,
			Status:         statusJSON,
		})
		if insertErr != nil {
			return insertErr
		}
		result = sqlcDiskToDomain(row)

		data, _ := proto.Marshal(domainDiskToProto(result))
		_, evtErr := r.outboxWriter.WriteEvent(ctx, tx, outbox.Event{
			EventType:    "ADDED",
			ResourceKind: "Disk",
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

func (r *DiskRepo) Update(ctx context.Context, disk *domain.Disk) (*domain.Disk, error) {
	pgUID, err := strToUUID(disk.UID)
	if err != nil {
		return nil, err
	}
	specJSON := anyToJSONB(diskSpec{
		DisplayName: disk.DisplayName,
		Description: disk.Description,
		DiskTypeID:  disk.DiskTypeID,
		ZoneID:      disk.ZoneID,
		Size:        disk.Size,
		ImageID:     disk.ImageID,
	})

	var result *domain.Disk
	txErr := r.transactor.InTx(ctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		row, updateErr := q.UpdateDisk(ctx, sqlcgen.UpdateDiskParams{
			Uid:         pgUID,
			Labels:      mapToJSONB(disk.Labels),
			Annotations: mapToJSONB(disk.Annotations),
			Spec:        specJSON,
		})
		if updateErr != nil {
			return updateErr
		}
		result = sqlcDiskToDomain(row)

		data, _ := proto.Marshal(domainDiskToProto(result))
		_, evtErr := r.outboxWriter.WriteEvent(ctx, tx, outbox.Event{
			EventType:    "MODIFIED",
			ResourceKind: "Disk",
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

func (r *DiskRepo) UpdateStatus(ctx context.Context, disk *domain.Disk) (*domain.Disk, error) {
	pgUID, err := strToUUID(disk.UID)
	if err != nil {
		return nil, err
	}
	statusJSON := anyToJSONB(diskStatus{
		State:                 diskStateToString(disk.State),
		StateLastTransitionAt: disk.StateLastTransitionAt.UTC().Format(time.RFC3339Nano),
		AttachedToInstanceID:  disk.AttachedToInstanceID,
		DeviceName:            disk.DeviceName,
		ObservedGeneration:    disk.ObservedGeneration,
	})

	var result *domain.Disk
	txErr := r.transactor.InTx(ctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		row, updateErr := q.UpdateDiskStatus(ctx, sqlcgen.UpdateDiskStatusParams{
			Uid:    pgUID,
			Status: statusJSON,
		})
		if updateErr != nil {
			return updateErr
		}
		result = sqlcDiskToDomain(row)

		data, _ := proto.Marshal(domainDiskToProto(result))
		_, evtErr := r.outboxWriter.WriteEvent(ctx, tx, outbox.Event{
			EventType:    "MODIFIED",
			ResourceKind: "Disk",
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

func (r *DiskRepo) SoftDelete(ctx context.Context, uid string) error {
	pgUID, err := strToUUID(uid)
	if err != nil {
		return err
	}
	return r.transactor.InTx(ctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if err := q.SoftDeleteDisk(ctx, pgUID); err != nil {
			return err
		}
		_, evtErr := r.outboxWriter.WriteEvent(ctx, tx, outbox.Event{
			EventType:    "DELETED",
			ResourceKind: "Disk",
			ResourceUID:  uid,
			Data:         nil,
		})
		return evtErr
	})
}

func (r *DiskRepo) HardDelete(ctx context.Context, uid string) error {
	pgUID, err := strToUUID(uid)
	if err != nil {
		return err
	}
	txErr := r.transactor.InTx(ctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if err := q.DeleteDisk(ctx, pgUID); err != nil {
			return err
		}
		_, evtErr := r.outboxWriter.WriteEvent(ctx, tx, outbox.Event{
			EventType:    "DELETED",
			ResourceKind: "Disk",
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

func (r *DiskRepo) HasSnapshots(ctx context.Context, uid string) (bool, error) {
	q := sqlcgen.New(r.pool)
	return q.DiskHasSnapshots(ctx, uid)
}

func (r *DiskRepo) ListPendingReconcile(ctx context.Context) ([]*domain.Disk, error) {
	q := sqlcgen.New(r.pool)
	rows, err := q.ListDisksPendingReconcile(ctx)
	if err != nil {
		return nil, err
	}
	var disks []*domain.Disk
	for _, row := range rows {
		disks = append(disks, sqlcDiskToDomain(row))
	}
	return disks, nil
}

func (r *DiskRepo) ListAttachedToInstance(ctx context.Context, instanceUID string) ([]*domain.Disk, error) {
	q := sqlcgen.New(r.pool)
	rows, err := q.ListDisksAttachedToInstance(ctx, instanceUID)
	if err != nil {
		return nil, err
	}
	var disks []*domain.Disk
	for _, row := range rows {
		disks = append(disks, sqlcDiskToDomain(row))
	}
	return disks, nil
}

// sqlcDiskToDomain конвертирует sqlc-модель в domain.Disk.
func sqlcDiskToDomain(row sqlcgen.Disk) *domain.Disk {
	var sp diskSpec
	jsonbToAny(row.Spec, &sp)

	var st diskStatus
	jsonbToAny(row.Status, &st)

	disk := &domain.Disk{
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

		DisplayName: sp.DisplayName,
		Description: sp.Description,
		DiskTypeID:  sp.DiskTypeID,
		ZoneID:      sp.ZoneID,
		Size:        sp.Size,
		ImageID:     sp.ImageID,

		State:                diskStateFromString(st.State),
		AttachedToInstanceID: st.AttachedToInstanceID,
		DeviceName:           st.DeviceName,
		ObservedGeneration:   st.ObservedGeneration,
	}
	if st.StateLastTransitionAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, st.StateLastTransitionAt); err == nil {
			disk.StateLastTransitionAt = t
		}
	}
	return disk
}

func diskStateToString(s domain.DiskState) string {
	switch s {
	case domain.DiskStateCreating:
		return "CREATING"
	case domain.DiskStateReady:
		return "READY"
	case domain.DiskStateAttaching:
		return "ATTACHING"
	case domain.DiskStateDetaching:
		return "DETACHING"
	case domain.DiskStateError:
		return "ERROR"
	case domain.DiskStateDeleting:
		return "DELETING"
	default:
		return "UNSPECIFIED"
	}
}

func diskStateFromString(s string) domain.DiskState {
	switch s {
	case "CREATING":
		return domain.DiskStateCreating
	case "READY":
		return domain.DiskStateReady
	case "ATTACHING":
		return domain.DiskStateAttaching
	case "DETACHING":
		return domain.DiskStateDetaching
	case "ERROR":
		return domain.DiskStateError
	case "DELETING":
		return domain.DiskStateDeleting
	default:
		return domain.DiskStateUnspecified
	}
}

func domainDiskToProto(disk *domain.Disk) *computev1.Disk {
	meta := &commonv1.ResourceMeta{
		Uid:             disk.UID,
		Name:            disk.Name,
		FolderId:        disk.FolderID,
		CloudId:         disk.CloudID,
		OrganizationId:  disk.OrganizationID,
		Labels:          disk.Labels,
		Annotations:     disk.Annotations,
		ResourceVersion: fmt.Sprintf("%d", disk.ResourceVersion),
		Generation:      disk.Generation,
		Finalizers:      disk.Finalizers,
	}
	if !disk.CreationTimestamp.IsZero() {
		meta.CreationTimestamp = timestamppb.New(disk.CreationTimestamp)
	}
	if disk.DeletionTimestamp != nil {
		meta.DeletionTimestamp = timestamppb.New(*disk.DeletionTimestamp)
	}

	pbState := computev1.DiskStatus_STATE_UNSPECIFIED
	switch disk.State {
	case domain.DiskStateCreating:
		pbState = computev1.DiskStatus_STATE_CREATING
	case domain.DiskStateReady:
		pbState = computev1.DiskStatus_STATE_READY
	case domain.DiskStateAttaching:
		pbState = computev1.DiskStatus_STATE_ATTACHING
	case domain.DiskStateDetaching:
		pbState = computev1.DiskStatus_STATE_DETACHING
	case domain.DiskStateError:
		pbState = computev1.DiskStatus_STATE_ERROR
	case domain.DiskStateDeleting:
		pbState = computev1.DiskStatus_STATE_DELETING
	}

	st := &computev1.DiskStatus{
		State:                pbState,
		AttachedToInstanceId: disk.AttachedToInstanceID,
		DeviceName:           disk.DeviceName,
		ObservedGeneration:   disk.ObservedGeneration,
	}
	if !disk.StateLastTransitionAt.IsZero() {
		st.StateLastTransitionAt = timestamppb.New(disk.StateLastTransitionAt)
	}

	return &computev1.Disk{
		Metadata: meta,
		Spec: &computev1.DiskSpec{
			DisplayName: disk.DisplayName,
			Description: disk.Description,
			DiskTypeId:  disk.DiskTypeID,
			ZoneId:      disk.ZoneID,
			Size:        disk.Size,
			ImageId:     disk.ImageID,
		},
		Status: st,
	}
}

func buildListQueryDisks(br selector.BuildResult, pageClause string) string {
	var sb strings.Builder
	sb.WriteString(
		"SELECT uid, folder_id, cloud_id, organization_id, name, labels, annotations, creation_timestamp, resource_version, generation, deletion_timestamp, finalizers, spec, status FROM disks WHERE deletion_timestamp IS NULL",
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
