package handler

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"

	"github.com/PRO-Robotech/kacho-compute/internal/protoconv"
	svc "github.com/PRO-Robotech/kacho-compute/internal/service"
)

// SnapshotHandler реализует computev1.SnapshotServiceServer (тонкий transport-слой).
// access-bindings RPC наследуются из UnimplementedSnapshotServiceServer (Unimplemented).
type SnapshotHandler struct {
	computev1.UnimplementedSnapshotServiceServer
	svc *svc.SnapshotService
}

// NewSnapshotHandler создаёт SnapshotHandler.
func NewSnapshotHandler(s *svc.SnapshotService) *SnapshotHandler { return &SnapshotHandler{svc: s} }

// Get возвращает Snapshot по id.
func (h *SnapshotHandler) Get(ctx context.Context, req *computev1.GetSnapshotRequest) (*computev1.Snapshot, error) {
	if req.SnapshotId == "" {
		return nil, status.Error(codes.InvalidArgument, "snapshot_id required")
	}
	snap, err := h.svc.Get(ctx, req.SnapshotId)
	if err != nil {
		return nil, err
	}
	if err := AssertFolderOwnership(ctx, snap.FolderID); err != nil {
		return nil, err
	}
	return protoconv.Snapshot(snap), nil
}

// List возвращает список снапшотов в folder.
func (h *SnapshotHandler) List(ctx context.Context, req *computev1.ListSnapshotsRequest) (*computev1.ListSnapshotsResponse, error) {
	if err := AssertFolderOwnership(ctx, req.FolderId); err != nil {
		return nil, err
	}
	snaps, nextToken, err := h.svc.List(ctx, svc.SnapshotFilter{FolderID: req.FolderId, Filter: req.Filter},
		svc.Pagination{PageToken: req.PageToken, PageSize: req.PageSize})
	if err != nil {
		return nil, err
	}
	resp := &computev1.ListSnapshotsResponse{NextPageToken: nextToken}
	for _, s := range snaps {
		resp.Snapshots = append(resp.Snapshots, protoconv.Snapshot(s))
	}
	return resp, nil
}

// Create инициирует создание Snapshot.
func (h *SnapshotHandler) Create(ctx context.Context, req *computev1.CreateSnapshotRequest) (*operationpb.Operation, error) {
	if err := AssertFolderOwnership(ctx, req.FolderId); err != nil {
		return nil, err
	}
	op, err := h.svc.Create(ctx, svc.CreateSnapshotReq{
		FolderID:           req.FolderId,
		DiskID:             req.DiskId,
		Name:               req.Name,
		Description:        req.Description,
		Labels:             req.Labels,
		HardwareGeneration: req.HardwareGeneration,
	})
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// Update инициирует обновление Snapshot.
func (h *SnapshotHandler) Update(ctx context.Context, req *computev1.UpdateSnapshotRequest) (*operationpb.Operation, error) {
	if req.SnapshotId == "" {
		return nil, status.Error(codes.InvalidArgument, "snapshot_id required")
	}
	snap, err := h.svc.Get(ctx, req.SnapshotId)
	if err != nil {
		return nil, err
	}
	if err := AssertFolderOwnership(ctx, snap.FolderID); err != nil {
		return nil, err
	}
	var mask []string
	if req.UpdateMask != nil {
		mask = req.UpdateMask.Paths
	}
	op, err := h.svc.Update(ctx, svc.UpdateSnapshotReq{
		SnapshotID:  req.SnapshotId,
		Name:        req.Name,
		Description: req.Description,
		Labels:      req.Labels,
		UpdateMask:  mask,
	})
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// Delete инициирует удаление Snapshot.
func (h *SnapshotHandler) Delete(ctx context.Context, req *computev1.DeleteSnapshotRequest) (*operationpb.Operation, error) {
	if req.SnapshotId == "" {
		return nil, status.Error(codes.InvalidArgument, "snapshot_id required")
	}
	snap, err := h.svc.Get(ctx, req.SnapshotId)
	if err != nil {
		return nil, err
	}
	if err := AssertFolderOwnership(ctx, snap.FolderID); err != nil {
		return nil, err
	}
	op, err := h.svc.Delete(ctx, req.SnapshotId)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// ListOperations возвращает операции для Snapshot.
func (h *SnapshotHandler) ListOperations(ctx context.Context, req *computev1.ListSnapshotOperationsRequest) (*computev1.ListSnapshotOperationsResponse, error) {
	if req.SnapshotId == "" {
		return nil, status.Error(codes.InvalidArgument, "snapshot_id required")
	}
	snap, err := h.svc.Get(ctx, req.SnapshotId)
	if err != nil {
		return nil, err
	}
	if err := AssertFolderOwnership(ctx, snap.FolderID); err != nil {
		return nil, err
	}
	ops, nextToken, err := h.svc.ListOperations(ctx, req.SnapshotId, svc.Pagination{PageToken: req.PageToken, PageSize: req.PageSize})
	if err != nil {
		return nil, err
	}
	resp := &computev1.ListSnapshotOperationsResponse{NextPageToken: nextToken}
	for i := range ops {
		resp.Operations = append(resp.Operations, operationToProto(&ops[i]))
	}
	return resp, nil
}
