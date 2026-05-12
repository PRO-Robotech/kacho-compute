package service

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corevalidate "github.com/PRO-Robotech/kacho-corelib/validate"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
)

// ---- DiskTypeService (read-only public + admin CRUD через Internal* handler) ----

// DiskTypeService — read/CRUD доступ к справочнику типов дисков.
type DiskTypeService struct {
	repo DiskTypeRepo
}

// NewDiskTypeService создаёт DiskTypeService.
func NewDiskTypeService(repo DiskTypeRepo) *DiskTypeService { return &DiskTypeService{repo: repo} }

// Get возвращает DiskType по id.
func (s *DiskTypeService) Get(ctx context.Context, id string) (*domain.DiskType, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "disk_type_id required")
	}
	t, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return t, nil
}

// List возвращает все типы дисков.
func (s *DiskTypeService) List(ctx context.Context, p Pagination) ([]*domain.DiskType, string, error) {
	return s.repo.List(ctx, p)
}

// Create создаёт тип диска (admin-only).
func (s *DiskTypeService) Create(ctx context.Context, id, description string, zoneIDs []string) (*domain.DiskType, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "id required")
	}
	t, err := s.repo.Insert(ctx, &domain.DiskType{ID: id, Description: description, ZoneIDs: zoneIDs})
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return t, nil
}

// Update обновляет тип диска (admin-only).
func (s *DiskTypeService) Update(ctx context.Context, id, description string, zoneIDs []string) (*domain.DiskType, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "disk_type_id required")
	}
	t, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	t.Description = description
	t.ZoneIDs = zoneIDs
	updated, err := s.repo.Update(ctx, t)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return updated, nil
}

// Delete удаляет тип диска (admin-only).
func (s *DiskTypeService) Delete(ctx context.Context, id string) error {
	if id == "" {
		return status.Error(codes.InvalidArgument, "disk_type_id required")
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return mapRepoErr(err)
	}
	return nil
}

// ---- ZoneService (read-only public + admin CRUD через Internal* handler) ----

// ZoneService — доступ к справочнику зон. kacho-compute — owner Geography
// (Region/Zone), поэтому Get/List/Create/Update/Delete работают напрямую с
// локальной таблицей `zones` (никакого proxy в kacho-vpc). Другие сервисы
// валидируют zone_id вызовом ZoneService.Get. См. workspace CLAUDE.md
// §«Кросс-доменные ссылки на ресурсы».
type ZoneService struct {
	repo ZoneRepo
}

// NewZoneService создаёт ZoneService поверх локальной таблицы `zones`.
func NewZoneService(repo ZoneRepo) *ZoneService { return &ZoneService{repo: repo} }

// Get возвращает Zone по id.
func (s *ZoneService) Get(ctx context.Context, id string) (*domain.Zone, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "zone_id required")
	}
	z, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return z, nil
}

// List возвращает зоны.
func (s *ZoneService) List(ctx context.Context, p Pagination) ([]*domain.Zone, string, error) {
	if _, err := corevalidate.PageSize("page_size", p.PageSize); err != nil {
		return nil, "", err
	}
	return s.repo.List(ctx, p)
}

// Create создаёт зону (admin-only).
func (s *ZoneService) Create(ctx context.Context, id, regionID, name string, st domain.ZoneStatus) (*domain.Zone, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "id required")
	}
	z, err := s.repo.Insert(ctx, &domain.Zone{ID: id, RegionID: regionID, Name: name, Status: st})
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return z, nil
}

// Update обновляет зону (admin-only).
func (s *ZoneService) Update(ctx context.Context, id, regionID, name string, st domain.ZoneStatus) (*domain.Zone, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "zone_id required")
	}
	z, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	if regionID != "" {
		z.RegionID = regionID
	}
	if name != "" {
		z.Name = name
	}
	z.Status = st
	updated, err := s.repo.Update(ctx, z)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return updated, nil
}

// Delete удаляет зону (admin-only).
func (s *ZoneService) Delete(ctx context.Context, id string) error {
	if id == "" {
		return status.Error(codes.InvalidArgument, "zone_id required")
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return mapRepoErr(err)
	}
	return nil
}

// ---- RegionService (read-only public + admin CRUD через Internal* handler) ----

// RegionService — доступ к справочнику регионов. kacho-compute — owner Geography.
type RegionService struct {
	repo RegionRepo
}

// NewRegionService создаёт RegionService поверх локальной таблицы `regions`.
func NewRegionService(repo RegionRepo) *RegionService { return &RegionService{repo: repo} }

// Get возвращает Region по id.
func (s *RegionService) Get(ctx context.Context, id string) (*domain.Region, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "region_id required")
	}
	r, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return r, nil
}

// List возвращает регионы.
func (s *RegionService) List(ctx context.Context, p Pagination) ([]*domain.Region, string, error) {
	if _, err := corevalidate.PageSize("page_size", p.PageSize); err != nil {
		return nil, "", err
	}
	return s.repo.List(ctx, p)
}

// Create создаёт регион (admin-only).
func (s *RegionService) Create(ctx context.Context, id, name string) (*domain.Region, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "id required")
	}
	r, err := s.repo.Insert(ctx, &domain.Region{ID: id, Name: name})
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return r, nil
}

// Update обновляет регион (admin-only).
func (s *RegionService) Update(ctx context.Context, id, name string) (*domain.Region, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "region_id required")
	}
	r, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	if name != "" {
		r.Name = name
	}
	updated, err := s.repo.Update(ctx, r)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return updated, nil
}

// Delete удаляет регион (admin-only). Блокируется, если у региона есть зоны
// (FailedPrecondition; на уровне БД защищено FK RESTRICT zones→regions).
func (s *RegionService) Delete(ctx context.Context, id string) error {
	if id == "" {
		return status.Error(codes.InvalidArgument, "region_id required")
	}
	if n, err := s.repo.CountZones(ctx, id); err == nil && n > 0 {
		return status.Errorf(codes.FailedPrecondition, "region %s has %d zone(s); delete the zones first", id, n)
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return mapRepoErr(err)
	}
	return nil
}
