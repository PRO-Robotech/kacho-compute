// Package handler — internal_catalog_handler.go: admin-CRUD над справочниками
// DiskType / Zone (kacho-only RPC, нет в verbatim YC). Регистрируется ТОЛЬКО на
// internal listener (:9091), проброшен через api-gateway internal mux. На
// external TLS endpoint не доступен — workspace CLAUDE.md §запрет 6.
package handler

import (
	"context"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
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

// InternalZoneHandler реализует computev1.InternalZoneServiceServer.
type InternalZoneHandler struct {
	computev1.UnimplementedInternalZoneServiceServer
	svc *svc.ZoneService
}

// NewInternalZoneHandler создаёт InternalZoneHandler.
func NewInternalZoneHandler(s *svc.ZoneService) *InternalZoneHandler {
	return &InternalZoneHandler{svc: s}
}

// Create создаёт зону.
func (h *InternalZoneHandler) Create(ctx context.Context, req *computev1.CreateZoneRequest) (*computev1.Zone, error) {
	z, err := h.svc.Create(ctx, req.Id, req.RegionId, domain.ZoneStatus(req.Status))
	if err != nil {
		return nil, internalMapErr("create zone", err)
	}
	return protoconv.Zone(z), nil
}

// Update обновляет зону.
func (h *InternalZoneHandler) Update(ctx context.Context, req *computev1.UpdateZoneRequest) (*computev1.Zone, error) {
	z, err := h.svc.Update(ctx, req.ZoneId, req.RegionId, domain.ZoneStatus(req.Status))
	if err != nil {
		return nil, internalMapErr("update zone", err)
	}
	return protoconv.Zone(z), nil
}

// Delete удаляет зону.
func (h *InternalZoneHandler) Delete(ctx context.Context, req *computev1.DeleteZoneRequest) (*computev1.DeleteZoneResponse, error) {
	if err := h.svc.Delete(ctx, req.ZoneId); err != nil {
		return nil, internalMapErr("delete zone", err)
	}
	return &computev1.DeleteZoneResponse{}, nil
}
