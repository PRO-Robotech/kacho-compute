package handler

import (
	"context"

	computev1 "github.com/PRO-Robotech/kacho-compute/proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/protoconv"
	svc "github.com/PRO-Robotech/kacho-compute/internal/service"
)

// DiskTypeHandler реализует computev1.DiskTypeServiceServer (read-only public).
type DiskTypeHandler struct {
	computev1.UnimplementedDiskTypeServiceServer
	svc *svc.DiskTypeService
}

// NewDiskTypeHandler создаёт DiskTypeHandler.
func NewDiskTypeHandler(s *svc.DiskTypeService) *DiskTypeHandler { return &DiskTypeHandler{svc: s} }

// Get возвращает DiskType по id.
func (h *DiskTypeHandler) Get(ctx context.Context, req *computev1.GetDiskTypeRequest) (*computev1.DiskType, error) {
	t, err := h.svc.Get(ctx, req.DiskTypeId)
	if err != nil {
		return nil, err
	}
	return protoconv.DiskType(t), nil
}

// List возвращает все типы дисков.
func (h *DiskTypeHandler) List(ctx context.Context, req *computev1.ListDiskTypesRequest) (*computev1.ListDiskTypesResponse, error) {
	types, nextToken, err := h.svc.List(ctx, svc.Pagination{PageToken: req.PageToken, PageSize: req.PageSize})
	if err != nil {
		return nil, err
	}
	resp := &computev1.ListDiskTypesResponse{NextPageToken: nextToken}
	for _, t := range types {
		resp.DiskTypes = append(resp.DiskTypes, protoconv.DiskType(t))
	}
	return resp, nil
}
