// Package handler — internal_catalog_handler.go: admin-CRUD над справочником
// DiskType (kacho-only RPC). Регистрируется ТОЛЬКО на internal listener (:9091),
// проброшен через api-gateway internal mux. На external TLS endpoint не доступен —
// workspace CLAUDE.md §запрет 6.
package handler

import (
	"context"

	computev1 "github.com/PRO-Robotech/kacho-compute/proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/protoconv"
	svc "github.com/PRO-Robotech/kacho-compute/internal/service"
)

// InternalDiskTypeHandler реализует computev1.InternalDiskTypeServiceServer.
type InternalDiskTypeHandler struct {
	computev1.UnimplementedInternalDiskTypeServiceServer
	svc *svc.DiskTypeService
}

// NewInternalDiskTypeHandler создаёт InternalDiskTypeHandler.
func NewInternalDiskTypeHandler(s *svc.DiskTypeService) *InternalDiskTypeHandler {
	return &InternalDiskTypeHandler{svc: s}
}

// Create создаёт тип диска.
func (h *InternalDiskTypeHandler) Create(ctx context.Context, req *computev1.CreateDiskTypeRequest) (*computev1.DiskType, error) {
	t, err := h.svc.Create(ctx, req.Id, req.Description, req.ZoneIds)
	if err != nil {
		return nil, internalMapErr("create disk type", err)
	}
	return protoconv.DiskType(t), nil
}

// Update обновляет тип диска.
func (h *InternalDiskTypeHandler) Update(ctx context.Context, req *computev1.UpdateDiskTypeRequest) (*computev1.DiskType, error) {
	t, err := h.svc.Update(ctx, req.DiskTypeId, req.Description, req.ZoneIds)
	if err != nil {
		return nil, internalMapErr("update disk type", err)
	}
	return protoconv.DiskType(t), nil
}

// Delete удаляет тип диска.
func (h *InternalDiskTypeHandler) Delete(ctx context.Context, req *computev1.DeleteDiskTypeRequest) (*computev1.DeleteDiskTypeResponse, error) {
	if err := h.svc.Delete(ctx, req.DiskTypeId); err != nil {
		return nil, internalMapErr("delete disk type", err)
	}
	return &computev1.DeleteDiskTypeResponse{}, nil
}
