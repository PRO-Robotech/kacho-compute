// Package service — Hypervisor use-cases (internal-only реестр гипервизоров).
package service

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/ids"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
)

// HypervisorService — управление реестром гипервизоров. Синхронные RPC (не
// Operation): это инфра-реестр железа, не tenant-facing ресурс. INTERNAL-ONLY —
// см. workspace CLAUDE.md §«Инфра-чувствительные данные».
type HypervisorService struct {
	repo HypervisorRepo
}

// NewHypervisorService создаёт HypervisorService.
func NewHypervisorService(repo HypervisorRepo) *HypervisorService { return &HypervisorService{repo: repo} }

// Register регистрирует новый хост и присваивает ему node_index (next-free из
// sequence/free-list). Идемпотентно по явному id: повторный вызов с уже
// существующим id возвращает существующую запись без переаллокации node_index.
func (s *HypervisorService) Register(ctx context.Context, id, zoneID, fqdn string, capacity domain.HypervisorCapacity) (*domain.Hypervisor, error) {
	zoneID = strings.TrimSpace(zoneID)
	if zoneID == "" {
		return nil, status.Error(codes.InvalidArgument, "zone_id required")
	}
	id = strings.TrimSpace(id)
	if id != "" {
		if existing, err := s.repo.Get(ctx, id); err == nil {
			return existing, nil // идемпотентно
		}
	} else {
		id = ids.NewID("hyp")
	}
	h := &domain.Hypervisor{
		ID:       id,
		ZoneID:   zoneID,
		FQDN:     fqdn,
		State:    domain.HypervisorStateReady,
		Capacity: capacity,
	}
	created, err := s.repo.Insert(ctx, h)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return created, nil
}

// Get возвращает хост по id.
func (s *HypervisorService) Get(ctx context.Context, id string) (*domain.Hypervisor, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "hypervisor_id required")
	}
	h, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return h, nil
}

// List возвращает хосты (опц. фильтр по зоне/состоянию).
func (s *HypervisorService) List(ctx context.Context, zoneID string, state domain.HypervisorState, p Pagination) ([]*domain.Hypervisor, string, error) {
	out, next, err := s.repo.List(ctx, zoneID, state, p)
	if err != nil {
		return nil, "", mapRepoErr(err)
	}
	return out, next, nil
}

// UpdateState меняет состояние и/или ёмкость хоста; updated_at всегда
// обновляется (heartbeat — implicit). state==Unspecified → state не меняется;
// capacity==nil → ёмкость не меняется.
func (s *HypervisorService) UpdateState(ctx context.Context, id string, state domain.HypervisorState, capacity *domain.HypervisorCapacity) (*domain.Hypervisor, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "hypervisor_id required")
	}
	cur, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	if state != domain.HypervisorStateUnspecified {
		cur.State = state
	}
	if capacity != nil {
		cur.Capacity = *capacity
	}
	updated, err := s.repo.Update(ctx, cur)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return updated, nil
}

// Deregister снимает хост с регистрации, возвращает node_index во free-list.
func (s *HypervisorService) Deregister(ctx context.Context, id string) error {
	if id == "" {
		return status.Error(codes.InvalidArgument, "hypervisor_id required")
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return mapRepoErr(err)
	}
	return nil
}
