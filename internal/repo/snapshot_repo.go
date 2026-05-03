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

type snapshotSpec struct {
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	DiskID      string `json:"disk_id"`
}

type snapshotStatus struct {
	State                 string `json:"state"`
	StateLastTransitionAt string `json:"state_last_transition_at,omitempty"`
	ProgressPercent       int32  `json:"progress_percent"`
	ObservedGeneration    int64  `json:"observed_generation,omitempty"`
}

// SnapshotRepo реализует service.SnapshotRepo.
type SnapshotRepo struct {
	pool         *pgxpool.Pool
	transactor   *coredb.Transactor
	outboxWriter *outbox.Writer
}

// NewSnapshotRepo создаёт SnapshotRepo.
func NewSnapshotRepo(pool *pgxpool.Pool, transactor *coredb.Transactor, outboxWriter *outbox.Writer) *SnapshotRepo {
	return &SnapshotRepo{pool: pool, transactor: transactor, outboxWriter: outboxWriter}
}

func (r *SnapshotRepo) GetByUID(ctx context.Context, uid string) (*domain.Snapshot, error) {
	pgUID, err := strToUUID(uid)
	if err != nil {
		return nil, coreerrors.InvalidArgument().AddFieldViolation("uid", "invalid uuid").Err()
	}
	q := sqlcgen.New(r.pool)
	row, err := q.GetSnapshotByUID(ctx, pgUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return sqlcSnapshotToDomain(row), nil
}

func (r *SnapshotRepo) GetByFolderAndName(ctx context.Context, folderID, name string) (*domain.Snapshot, error) {
	pgFolderID, err := strToUUID(folderID)
	if err != nil {
		return nil, coreerrors.InvalidArgument().AddFieldViolation("folder_id", "invalid uuid").Err()
	}
	q := sqlcgen.New(r.pool)
	row, err := q.GetSnapshotByFolderAndName(ctx, sqlcgen.GetSnapshotByFolderAndNameParams{
		FolderID: pgFolderID,
		Name:     name,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return sqlcSnapshotToDomain(row), nil
}

func (r *SnapshotRepo) List(ctx context.Context, selectors []service.Selector, page service.Pagination) ([]*domain.Snapshot, string, int64, error) {
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

	query := buildListQuerySnapshots(br, pageClause)
	args := append(br.Args, pageArgs...)
	args = append(args, pageSize+1)
	limitParam := fmt.Sprintf("$%d", paramBase)
	query += fmt.Sprintf(" ORDER BY resource_version ASC, uid ASC LIMIT %s", limitParam)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", 0, err
	}
	defer rows.Close()

	var snaps []*domain.Snapshot
	for rows.Next() {
		var row sqlcgen.Snapshot
		if scanErr := rows.Scan(
			&row.Uid, &row.FolderID, &row.CloudID, &row.OrganizationID, &row.Name,
			&row.Labels, &row.Annotations, &row.CreationTimestamp, &row.ResourceVersion,
			&row.Generation, &row.DeletionTimestamp, &row.Finalizers, &row.Spec, &row.Status,
		); scanErr != nil {
			return nil, "", 0, scanErr
		}
		snaps = append(snaps, sqlcSnapshotToDomain(row))
	}
	if err := rows.Err(); err != nil {
		return nil, "", 0, err
	}

	var nextToken string
	if len(snaps) > pageSize {
		snaps = snaps[:pageSize]
		last := snaps[len(snaps)-1]
		tok := selector.PageToken{LastResourceVersion: last.ResourceVersion, LastUID: last.UID}
		b, _ := json.Marshal(tok)
		nextToken = string(b)
	}

	return snaps, nextToken, snapshotRV, nil
}

func (r *SnapshotRepo) SnapshotResourceVersion(ctx context.Context) (int64, error) {
	q := sqlcgen.New(r.pool)
	return q.SnapshotResourceVersion(ctx)
}

func (r *SnapshotRepo) Insert(ctx context.Context, snap *domain.Snapshot) (*domain.Snapshot, error) {
	pgUID, err := strToUUID(snap.UID)
	if err != nil {
		return nil, err
	}
	pgFolderID, err := strToUUID(snap.FolderID)
	if err != nil {
		return nil, err
	}
	pgCloudID := strToUUIDOrZero(snap.CloudID)
	pgOrgID := strToUUIDOrZero(snap.OrganizationID)

	specJSON := anyToJSONB(snapshotSpec{
		DisplayName: snap.DisplayName,
		Description: snap.Description,
		DiskID:      snap.DiskID,
	})
	statusJSON := anyToJSONB(snapshotStatus{
		State:                 snapshotStateToString(snap.State),
		StateLastTransitionAt: time.Now().UTC().Format(time.RFC3339Nano),
		ProgressPercent:       snap.ProgressPercent,
	})

	var result *domain.Snapshot
	txErr := r.transactor.InTx(ctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		row, insertErr := q.InsertSnapshot(ctx, sqlcgen.InsertSnapshotParams{
			Uid:            pgUID,
			FolderID:       pgFolderID,
			CloudID:        pgCloudID,
			OrganizationID: pgOrgID,
			Name:           snap.Name,
			Labels:         mapToJSONB(snap.Labels),
			Annotations:    mapToJSONB(snap.Annotations),
			Finalizers:     nonNilStrings(snap.Finalizers),
			Spec:           specJSON,
			Status:         statusJSON,
		})
		if insertErr != nil {
			return insertErr
		}
		result = sqlcSnapshotToDomain(row)

		data, _ := proto.Marshal(domainSnapshotToProto(result))
		_, evtErr := r.outboxWriter.WriteEvent(ctx, tx, outbox.Event{
			EventType:    "ADDED",
			ResourceKind: "Snapshot",
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

func (r *SnapshotRepo) Update(ctx context.Context, snap *domain.Snapshot) (*domain.Snapshot, error) {
	pgUID, err := strToUUID(snap.UID)
	if err != nil {
		return nil, err
	}
	specJSON := anyToJSONB(snapshotSpec{
		DisplayName: snap.DisplayName,
		Description: snap.Description,
		DiskID:      snap.DiskID,
	})

	var result *domain.Snapshot
	txErr := r.transactor.InTx(ctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		row, updateErr := q.UpdateSnapshot(ctx, sqlcgen.UpdateSnapshotParams{
			Uid:         pgUID,
			Labels:      mapToJSONB(snap.Labels),
			Annotations: mapToJSONB(snap.Annotations),
			Spec:        specJSON,
		})
		if updateErr != nil {
			return updateErr
		}
		result = sqlcSnapshotToDomain(row)

		data, _ := proto.Marshal(domainSnapshotToProto(result))
		_, evtErr := r.outboxWriter.WriteEvent(ctx, tx, outbox.Event{
			EventType:    "MODIFIED",
			ResourceKind: "Snapshot",
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

func (r *SnapshotRepo) UpdateStatus(ctx context.Context, snap *domain.Snapshot) (*domain.Snapshot, error) {
	pgUID, err := strToUUID(snap.UID)
	if err != nil {
		return nil, err
	}
	statusJSON := anyToJSONB(snapshotStatus{
		State:                 snapshotStateToString(snap.State),
		StateLastTransitionAt: snap.StateLastTransitionAt.UTC().Format(time.RFC3339Nano),
		ProgressPercent:       snap.ProgressPercent,
		ObservedGeneration:    snap.ObservedGeneration,
	})

	var result *domain.Snapshot
	txErr := r.transactor.InTx(ctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		row, updateErr := q.UpdateSnapshotStatus(ctx, sqlcgen.UpdateSnapshotStatusParams{
			Uid:    pgUID,
			Status: statusJSON,
		})
		if updateErr != nil {
			return updateErr
		}
		result = sqlcSnapshotToDomain(row)

		data, _ := proto.Marshal(domainSnapshotToProto(result))
		_, evtErr := r.outboxWriter.WriteEvent(ctx, tx, outbox.Event{
			EventType:    "MODIFIED",
			ResourceKind: "Snapshot",
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

func (r *SnapshotRepo) SoftDelete(ctx context.Context, uid string) error {
	pgUID, err := strToUUID(uid)
	if err != nil {
		return err
	}
	return r.transactor.InTx(ctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if err := q.SoftDeleteSnapshot(ctx, pgUID); err != nil {
			return err
		}
		_, evtErr := r.outboxWriter.WriteEvent(ctx, tx, outbox.Event{
			EventType:    "DELETED",
			ResourceKind: "Snapshot",
			ResourceUID:  uid,
			Data:         nil,
		})
		return evtErr
	})
}

func (r *SnapshotRepo) HardDelete(ctx context.Context, uid string) error {
	pgUID, err := strToUUID(uid)
	if err != nil {
		return err
	}
	txErr := r.transactor.InTx(ctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if err := q.DeleteSnapshot(ctx, pgUID); err != nil {
			return err
		}
		_, evtErr := r.outboxWriter.WriteEvent(ctx, tx, outbox.Event{
			EventType:    "DELETED",
			ResourceKind: "Snapshot",
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

func (r *SnapshotRepo) ListPendingReconcile(ctx context.Context) ([]*domain.Snapshot, error) {
	q := sqlcgen.New(r.pool)
	rows, err := q.ListSnapshotsPendingReconcile(ctx)
	if err != nil {
		return nil, err
	}
	var snaps []*domain.Snapshot
	for _, row := range rows {
		snaps = append(snaps, sqlcSnapshotToDomain(row))
	}
	return snaps, nil
}

func sqlcSnapshotToDomain(row sqlcgen.Snapshot) *domain.Snapshot {
	var sp snapshotSpec
	jsonbToAny(row.Spec, &sp)

	var st snapshotStatus
	jsonbToAny(row.Status, &st)

	snap := &domain.Snapshot{
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

		DisplayName:     sp.DisplayName,
		Description:     sp.Description,
		DiskID:          sp.DiskID,
		State:           snapshotStateFromString(st.State),
		ProgressPercent: st.ProgressPercent,
		ObservedGeneration: st.ObservedGeneration,
	}
	if st.StateLastTransitionAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, st.StateLastTransitionAt); err == nil {
			snap.StateLastTransitionAt = t
		}
	}
	return snap
}

func snapshotStateToString(s domain.SnapshotState) string {
	switch s {
	case domain.SnapshotStateCreating:
		return "CREATING"
	case domain.SnapshotStateReady:
		return "READY"
	case domain.SnapshotStateError:
		return "ERROR"
	case domain.SnapshotStateDeleting:
		return "DELETING"
	default:
		return "UNSPECIFIED"
	}
}

func snapshotStateFromString(s string) domain.SnapshotState {
	switch s {
	case "CREATING":
		return domain.SnapshotStateCreating
	case "READY":
		return domain.SnapshotStateReady
	case "ERROR":
		return domain.SnapshotStateError
	case "DELETING":
		return domain.SnapshotStateDeleting
	default:
		return domain.SnapshotStateUnspecified
	}
}

func domainSnapshotToProto(snap *domain.Snapshot) *computev1.Snapshot {
	meta := &commonv1.ResourceMeta{
		Uid:             snap.UID,
		Name:            snap.Name,
		FolderId:        snap.FolderID,
		CloudId:         snap.CloudID,
		OrganizationId:  snap.OrganizationID,
		Labels:          snap.Labels,
		Annotations:     snap.Annotations,
		ResourceVersion: fmt.Sprintf("%d", snap.ResourceVersion),
		Generation:      snap.Generation,
		Finalizers:      snap.Finalizers,
	}
	if !snap.CreationTimestamp.IsZero() {
		meta.CreationTimestamp = timestamppb.New(snap.CreationTimestamp)
	}
	if snap.DeletionTimestamp != nil {
		meta.DeletionTimestamp = timestamppb.New(*snap.DeletionTimestamp)
	}

	pbState := computev1.SnapshotStatus_STATE_UNSPECIFIED
	switch snap.State {
	case domain.SnapshotStateCreating:
		pbState = computev1.SnapshotStatus_STATE_CREATING
	case domain.SnapshotStateReady:
		pbState = computev1.SnapshotStatus_STATE_READY
	case domain.SnapshotStateError:
		pbState = computev1.SnapshotStatus_STATE_ERROR
	case domain.SnapshotStateDeleting:
		pbState = computev1.SnapshotStatus_STATE_DELETING
	}

	st := &computev1.SnapshotStatus{
		State:              pbState,
		ProgressPercent:    snap.ProgressPercent,
		ObservedGeneration: snap.ObservedGeneration,
	}
	if !snap.StateLastTransitionAt.IsZero() {
		st.StateLastTransitionAt = timestamppb.New(snap.StateLastTransitionAt)
	}

	return &computev1.Snapshot{
		Metadata: meta,
		Spec: &computev1.SnapshotSpec{
			DisplayName: snap.DisplayName,
			Description: snap.Description,
			DiskId:      snap.DiskID,
		},
		Status: st,
	}
}

func buildListQuerySnapshots(br selector.BuildResult, pageClause string) string {
	var sb strings.Builder
	sb.WriteString(
		"SELECT uid, folder_id, cloud_id, organization_id, name, labels, annotations, creation_timestamp, resource_version, generation, deletion_timestamp, finalizers, spec, status FROM snapshots WHERE deletion_timestamp IS NULL",
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
