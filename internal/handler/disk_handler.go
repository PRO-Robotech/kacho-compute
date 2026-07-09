// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package handler

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"

	"github.com/PRO-Robotech/kacho-compute/internal/authzfilter"
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
	svc        *svc.DiskService
	listFilter authzfilter.Filter
}

// NewDiskHandler создаёт DiskHandler. listFilter может быть nil — тогда
// FGA-фильтрация на List отключена (dev/breakglass).
func NewDiskHandler(s *svc.DiskService, listFilter authzfilter.Filter) *DiskHandler {
	return &DiskHandler{svc: s, listFilter: listFilter}
}

// Get возвращает Disk по id.
func (h *DiskHandler) Get(ctx context.Context, req *computev1.GetDiskRequest) (*computev1.Disk, error) {
	if req.DiskId == "" {
		return nil, status.Error(codes.InvalidArgument, "disk_id required")
	}
	d, err := h.svc.Get(ctx, req.DiskId)
	if err != nil {
		return nil, err
	}
	if err := AssertProjectOwnership(ctx, d.ProjectID); err != nil {
		return nil, err
	}
	return protoconv.Disk(d), nil
}

// List возвращает список дисков в folder.
//
// Вызов фильтруется через iam.AuthorizeService.ListObjects
// (caller subject → allowed disk_ids). admin / dev-bypass → no filtering.
func (h *DiskHandler) List(ctx context.Context, req *computev1.ListDisksRequest) (*computev1.ListDisksResponse, error) {
	if err := AssertProjectOwnership(ctx, req.ProjectId); err != nil {
		return nil, err
	}
	dec, err := resolveListFilter(ctx, h.listFilter, authzfilter.ResourceTypeDisk, authzfilter.ActionDiskRead)
	if err != nil {
		return nil, err
	}
	filter := svc.DiskFilter{ProjectID: req.ProjectId, Filter: req.Filter}
	if !dec.IsBypass() {
		if len(dec.IDs()) == 0 {
			// Empty grant → return empty list (NOT error).
			return &computev1.ListDisksResponse{}, nil
		}
		filter.AllowedIDs = dec.IDs()
	}
	disks, nextToken, err := h.svc.List(ctx, filter,
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
	if err := AssertProjectOwnership(ctx, req.ProjectId); err != nil {
		return nil, err
	}
	op, err := h.svc.Create(ctx, svc.CreateDiskReq{
		ProjectID:           req.ProjectId,
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
	if err := AssertProjectOwnership(ctx, d.ProjectID); err != nil {
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
	if err := AssertProjectOwnership(ctx, d.ProjectID); err != nil {
		return nil, err
	}
	op, err := h.svc.Delete(ctx, req.DiskId)
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
	if err := AssertProjectOwnership(ctx, d.ProjectID); err != nil {
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
	if err := AssertProjectOwnership(ctx, d.ProjectID); err != nil {
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
