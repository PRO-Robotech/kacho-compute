package handler

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"
	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/protoconv"
	svc "github.com/PRO-Robotech/kacho-compute/internal/service"
)

// DiskHandler реализует computev1.DiskServiceServer (тонкий transport-слой).
//
// Unimplemented RPC (ListSnapshotSchedules — blocked:kacho-snapshot-schedule;
// ListAccessBindings/SetAccessBindings/UpdateAccessBindings — AAA-скелет) не
// переопределены и наследуются из UnimplementedDiskServiceServer (возвращают
// codes.Unimplemented). См. docs/architecture/07-known-divergences.md.
type DiskHandler struct {
	computev1.UnimplementedDiskServiceServer
	svc *svc.DiskService
}

// NewDiskHandler создаёт DiskHandler.
func NewDiskHandler(s *svc.DiskService) *DiskHandler { return &DiskHandler{svc: s} }

// Get возвращает Disk по id.
func (h *DiskHandler) Get(ctx context.Context, req *computev1.GetDiskRequest) (*computev1.Disk, error) {
	if req.DiskId == "" {
		return nil, status.Error(codes.InvalidArgument, "disk_id required")
	}
	d, err := h.svc.Get(ctx, req.DiskId)
	if err != nil {
		return nil, err
	}
	if err := AssertFolderOwnership(ctx, d.FolderID); err != nil {
		return nil, err
	}
	return protoconv.Disk(d), nil
}

// List возвращает список дисков в folder.
func (h *DiskHandler) List(ctx context.Context, req *computev1.ListDisksRequest) (*computev1.ListDisksResponse, error) {
	if err := AssertFolderOwnership(ctx, req.FolderId); err != nil {
		return nil, err
	}
	disks, nextToken, err := h.svc.List(ctx, svc.DiskFilter{FolderID: req.FolderId, Filter: req.Filter},
		svc.Pagination{PageToken: req.PageToken, PageSize: req.PageSize})
	if err != nil {
		return nil, err
	}
	resp := &computev1.ListDisksResponse{NextPageToken: nextToken}
	for _, d := range disks {
		resp.Disks = append(resp.Disks, protoconv.Disk(d))
	}
	return resp, nil
}

// Create инициирует создание Disk.
func (h *DiskHandler) Create(ctx context.Context, req *computev1.CreateDiskRequest) (*operationpb.Operation, error) {
	if err := AssertFolderOwnership(ctx, req.FolderId); err != nil {
		return nil, err
	}
	op, err := h.svc.Create(ctx, svc.CreateDiskReq{
		FolderID:            req.FolderId,
		Name:                req.Name,
		Description:         req.Description,
		Labels:              req.Labels,
		TypeID:              req.TypeId,
		ZoneID:              req.ZoneId,
		Size:                req.Size,
		BlockSize:           req.BlockSize,
		ImageID:             req.GetImageId(),
		SnapshotID:          req.GetSnapshotId(),
		DiskPlacementPolicy: req.DiskPlacementPolicy,
		HardwareGeneration:  req.HardwareGeneration,
		KMSKeyID:            req.KmsKeyId,
	})
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// Update инициирует обновление Disk.
func (h *DiskHandler) Update(ctx context.Context, req *computev1.UpdateDiskRequest) (*operationpb.Operation, error) {
	if req.DiskId == "" {
		return nil, status.Error(codes.InvalidArgument, "disk_id required")
	}
	d, err := h.svc.Get(ctx, req.DiskId)
	if err != nil {
		return nil, err
	}
	if err := AssertFolderOwnership(ctx, d.FolderID); err != nil {
		return nil, err
	}
	var mask []string
	if req.UpdateMask != nil {
		mask = req.UpdateMask.Paths
	}
	op, err := h.svc.Update(ctx, svc.UpdateDiskReq{
		DiskID:              req.DiskId,
		Name:                req.Name,
		Description:         req.Description,
		Labels:              req.Labels,
		Size:                req.Size,
		DiskPlacementPolicy: req.DiskPlacementPolicy,
		UpdateMask:          mask,
	})
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// Delete инициирует удаление Disk.
func (h *DiskHandler) Delete(ctx context.Context, req *computev1.DeleteDiskRequest) (*operationpb.Operation, error) {
	if req.DiskId == "" {
		return nil, status.Error(codes.InvalidArgument, "disk_id required")
	}
	d, err := h.svc.Get(ctx, req.DiskId)
	if err != nil {
		return nil, err
	}
	if err := AssertFolderOwnership(ctx, d.FolderID); err != nil {
		return nil, err
	}
	op, err := h.svc.Delete(ctx, req.DiskId)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// Move инициирует перенос Disk в другой folder.
func (h *DiskHandler) Move(ctx context.Context, req *computev1.MoveDiskRequest) (*operationpb.Operation, error) {
	if req.DiskId == "" {
		return nil, status.Error(codes.InvalidArgument, "disk_id required")
	}
	d, err := h.svc.Get(ctx, req.DiskId)
	if err != nil {
		return nil, err
	}
	if err := AssertFolderOwnership(ctx, d.FolderID); err != nil {
		return nil, err
	}
	if err := AssertFolderOwnership(ctx, req.DestinationFolderId); err != nil {
		return nil, err
	}
	op, err := h.svc.Move(ctx, req.DiskId, req.DestinationFolderId)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// Relocate инициирует перенос Disk в другую зону.
func (h *DiskHandler) Relocate(ctx context.Context, req *computev1.RelocateDiskRequest) (*operationpb.Operation, error) {
	if req.DiskId == "" {
		return nil, status.Error(codes.InvalidArgument, "disk_id required")
	}
	d, err := h.svc.Get(ctx, req.DiskId)
	if err != nil {
		return nil, err
	}
	if err := AssertFolderOwnership(ctx, d.FolderID); err != nil {
		return nil, err
	}
	op, err := h.svc.Relocate(ctx, req.DiskId, req.DestinationZoneId)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// ListOperations возвращает операции для Disk.
func (h *DiskHandler) ListOperations(ctx context.Context, req *computev1.ListDiskOperationsRequest) (*computev1.ListDiskOperationsResponse, error) {
	if req.DiskId == "" {
		return nil, status.Error(codes.InvalidArgument, "disk_id required")
	}
	d, err := h.svc.Get(ctx, req.DiskId)
	if err != nil {
		return nil, err
	}
	if err := AssertFolderOwnership(ctx, d.FolderID); err != nil {
		return nil, err
	}
	ops, nextToken, err := h.svc.ListOperations(ctx, req.DiskId, svc.Pagination{PageToken: req.PageToken, PageSize: req.PageSize})
	if err != nil {
		return nil, err
	}
	resp := &computev1.ListDiskOperationsResponse{NextPageToken: nextToken}
	for i := range ops {
		resp.Operations = append(resp.Operations, operationToProto(&ops[i]))
	}
	return resp, nil
}
