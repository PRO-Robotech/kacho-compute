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

// DiskHandler реализует computev1.DiskServiceServer.
type DiskHandler struct {
	computev1.UnimplementedDiskServiceServer
	svc *svc.DiskService
}

// NewDiskHandler создаёт DiskHandler.
func NewDiskHandler(svc *svc.DiskService) *DiskHandler {
	return &DiskHandler{svc: svc}
}

func (h *DiskHandler) Get(ctx context.Context, req *computev1.GetDiskRequest) (*computev1.Disk, error) {
	if req.DiskId == "" {
		return nil, status.Error(codes.InvalidArgument, "disk_id required")
	}
	d, err := h.svc.Get(ctx, req.DiskId)
	if err != nil {
		return nil, err
	}
	return diskToProto(d), nil
}

func (h *DiskHandler) List(ctx context.Context, req *computev1.ListDisksRequest) (*computev1.ListDisksResponse, error) {
	disks, nextToken, err := h.svc.List(ctx, svc.DiskFilter{
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

	resp := &computev1.ListDisksResponse{NextPageToken: nextToken}
	for _, d := range disks {
		resp.Disks = append(resp.Disks, diskToProto(d))
	}
	return resp, nil
}

func (h *DiskHandler) Create(ctx context.Context, req *computev1.CreateDiskRequest) (*operationv1.Operation, error) {
	op, err := h.svc.Create(ctx, svc.CreateDiskReq{
		FolderID:    req.FolderId,
		Name:        req.Name,
		Description: req.Description,
		Labels:      req.Labels,
		DiskTypeID:  req.DiskTypeId,
		ZoneID:      req.ZoneId,
		Size:        req.Size,
		ImageID:     req.ImageId,
	})
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

func (h *DiskHandler) Update(ctx context.Context, req *computev1.UpdateDiskRequest) (*operationv1.Operation, error) {
	if req.DiskId == "" {
		return nil, status.Error(codes.InvalidArgument, "disk_id required")
	}
	op, err := h.svc.Update(ctx, svc.UpdateDiskReq{
		DiskID:          req.DiskId,
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

func (h *DiskHandler) Delete(ctx context.Context, req *computev1.DeleteDiskRequest) (*operationv1.Operation, error) {
	if req.DiskId == "" {
		return nil, status.Error(codes.InvalidArgument, "disk_id required")
	}
	op, err := h.svc.Delete(ctx, req.DiskId)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// ---- domain → proto ----

func diskToProto(d *domain.Disk) *computev1.Disk {
	return &computev1.Disk{
		Id:                   d.ID,
		FolderId:             d.FolderID,
		Name:                 d.Name,
		Description:          d.Description,
		CreatedAt:            timestamppb.New(d.CreatedAt),
		Labels:               d.Labels,
		DiskTypeId:           d.DiskTypeID,
		ZoneId:               d.ZoneID,
		Size:                 d.Size,
		ImageId:              d.ImageID,
		Status:               computev1.DiskStatus(d.Status),
		AttachedToInstanceId: d.AttachedToInstanceID,
		Generation:           d.Generation,
		ResourceVersion:      d.ResourceVersion,
		ObservedGeneration:   d.ObservedGeneration,
		StatusLastTransitionAt: timestamppb.New(d.StatusLastTransitionAt),
	}
}
