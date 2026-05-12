// Package handler — internal_hypervisor_handler.go: реестр гипервизоров
// (InternalHypervisorService). Регистрируется ТОЛЬКО на internal listener (:9091),
// проброшен через api-gateway internal mux. На external TLS endpoint не доступен —
// workspace CLAUDE.md §запрет 6 / §«Инфра-чувствительные данные».
package handler

import (
	"context"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/protoconv"
	svc "github.com/PRO-Robotech/kacho-compute/internal/service"
)

// InternalHypervisorHandler реализует computev1.InternalHypervisorServiceServer.
type InternalHypervisorHandler struct {
	computev1.UnimplementedInternalHypervisorServiceServer
	svc *svc.HypervisorService
}

// NewInternalHypervisorHandler создаёт InternalHypervisorHandler.
func NewInternalHypervisorHandler(s *svc.HypervisorService) *InternalHypervisorHandler {
	return &InternalHypervisorHandler{svc: s}
}

func capFromProto(c *computev1.Hypervisor_Capacity) domain.HypervisorCapacity {
	if c == nil {
		return domain.HypervisorCapacity{}
	}
	return domain.HypervisorCapacity{VCPUs: c.GetVcpus(), MemoryBytes: c.GetMemoryBytes(), Instances: c.GetInstances()}
}

// RegisterHypervisor регистрирует хост и присваивает node_index.
func (h *InternalHypervisorHandler) RegisterHypervisor(ctx context.Context, req *computev1.RegisterHypervisorRequest) (*computev1.Hypervisor, error) {
	hv, err := h.svc.Register(ctx, req.GetId(), req.GetZoneId(), req.GetFqdn(), capFromProto(req.GetCapacity()))
	if err != nil {
		return nil, internalMapErr("register hypervisor", err)
	}
	return protoconv.Hypervisor(hv), nil
}

// GetHypervisor возвращает хост по id.
func (h *InternalHypervisorHandler) GetHypervisor(ctx context.Context, req *computev1.GetHypervisorRequest) (*computev1.Hypervisor, error) {
	hv, err := h.svc.Get(ctx, req.GetHypervisorId())
	if err != nil {
		return nil, internalMapErr("get hypervisor", err)
	}
	return protoconv.Hypervisor(hv), nil
}

// ListHypervisors возвращает хосты (опц. фильтр по зоне/состоянию).
func (h *InternalHypervisorHandler) ListHypervisors(ctx context.Context, req *computev1.ListHypervisorsRequest) (*computev1.ListHypervisorsResponse, error) {
	hvs, next, err := h.svc.List(ctx, req.GetZoneId(), domain.HypervisorState(req.GetState()), svc.Pagination{PageSize: req.GetPageSize(), PageToken: req.GetPageToken()})
	if err != nil {
		return nil, internalMapErr("list hypervisors", err)
	}
	resp := &computev1.ListHypervisorsResponse{NextPageToken: next}
	for _, hv := range hvs {
		resp.Hypervisors = append(resp.Hypervisors, protoconv.Hypervisor(hv))
	}
	return resp, nil
}

// UpdateHypervisorState меняет state/capacity/heartbeat.
func (h *InternalHypervisorHandler) UpdateHypervisorState(ctx context.Context, req *computev1.UpdateHypervisorStateRequest) (*computev1.Hypervisor, error) {
	var capPtr *domain.HypervisorCapacity
	if req.GetCapacity() != nil {
		c := capFromProto(req.GetCapacity())
		capPtr = &c
	}
	hv, err := h.svc.UpdateState(ctx, req.GetHypervisorId(), domain.HypervisorState(req.GetState()), capPtr)
	if err != nil {
		return nil, internalMapErr("update hypervisor state", err)
	}
	return protoconv.Hypervisor(hv), nil
}

// DeregisterHypervisor снимает хост с регистрации.
func (h *InternalHypervisorHandler) DeregisterHypervisor(ctx context.Context, req *computev1.DeregisterHypervisorRequest) (*computev1.DeregisterHypervisorResponse, error) {
	if err := h.svc.Deregister(ctx, req.GetHypervisorId()); err != nil {
		return nil, internalMapErr("deregister hypervisor", err)
	}
	return &computev1.DeregisterHypervisorResponse{}, nil
}
