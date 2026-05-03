package handler

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	operationv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	svc "github.com/PRO-Robotech/kacho-compute/internal/service"
)

// SnapshotHandler реализует computev1.SnapshotServiceServer.
type SnapshotHandler struct {
	computev1.UnimplementedSnapshotServiceServer
	svc *svc.SnapshotService
}

// NewSnapshotHandler создаёт SnapshotHandler.
func NewSnapshotHandler(svc *svc.SnapshotService) *SnapshotHandler {
	return &SnapshotHandler{svc: svc}
}

func (h *SnapshotHandler) Get(ctx context.Context, req *computev1.GetSnapshotRequest) (*computev1.Snapshot, error) {
	if req.SnapshotId == "" {
		return nil, status.Error(codes.InvalidArgument, "snapshot_id required")
	}
	s, err := h.svc.Get(ctx, req.SnapshotId)
	if err != nil {
		return nil, err
	}
	return snapshotToProto(s), nil
}

func (h *SnapshotHandler) List(ctx context.Context, req *computev1.ListSnapshotsRequest) (*computev1.ListSnapshotsResponse, error) {
	snaps, nextToken, err := h.svc.List(ctx, svc.SnapshotFilter{
		FolderID: req.FolderId,
		Filter:   req.Filter,
		OrderBy:  req.OrderBy,
	}, svc.Pagination{
		PageToken: req.PageToken,
		PageSize:  req.PageSize,
	})
	if err != nil {
		return nil, err
	}

	resp := &computev1.ListSnapshotsResponse{NextPageToken: nextToken}
	for _, s := range snaps {
		resp.Snapshots = append(resp.Snapshots, snapshotToProto(s))
	}
	return resp, nil
}

func (h *SnapshotHandler) Create(ctx context.Context, req *computev1.CreateSnapshotRequest) (*operationv1.Operation, error) {
	op, err := h.svc.Create(ctx, svc.CreateSnapshotReq{
		FolderID:    req.FolderId,
		DiskID:      req.DiskId,
		Name:        req.Name,
		Description: req.Description,
		Labels:      req.Labels,
	})
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

func (h *SnapshotHandler) Update(ctx context.Context, req *computev1.UpdateSnapshotRequest) (*operationv1.Operation, error) {
	if req.SnapshotId == "" {
		return nil, status.Error(codes.InvalidArgument, "snapshot_id required")
	}
	op, err := h.svc.Update(ctx, svc.UpdateSnapshotReq{
		SnapshotID:      req.SnapshotId,
		ResourceVersion: req.ResourceVersion,
		Name:            req.Name,
		Description:     req.Description,
		Labels:          req.Labels,
	})
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

func (h *SnapshotHandler) Delete(ctx context.Context, req *computev1.DeleteSnapshotRequest) (*operationv1.Operation, error) {
	if req.SnapshotId == "" {
		return nil, status.Error(codes.InvalidArgument, "snapshot_id required")
	}
	op, err := h.svc.Delete(ctx, req.SnapshotId)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// ---- domain → proto ----

func snapshotToProto(s *domain.Snapshot) *computev1.Snapshot {
	return &computev1.Snapshot{
		Id:                 s.ID,
		FolderId:           s.FolderID,
		Name:               s.Name,
		Description:        s.Description,
		CreatedAt:          timestamppb.New(s.CreatedAt),
		Labels:             s.Labels,
		DiskId:             s.DiskID,
		Size:               s.Size,
		Status:             computev1.SnapshotStatus(s.Status),
		ProgressPercent:    s.ProgressPercent,
		Generation:         s.Generation,
		ResourceVersion:    s.ResourceVersion,
		ObservedGeneration: s.ObservedGeneration,
	}
}
