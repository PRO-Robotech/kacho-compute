package service

import (
	"context"
	"errors"

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

// ZoneService — read-доступ к справочнику зон (Get/List). На данный момент
// авторитетный источник зон — kacho-vpc InternalZoneService (compute зон не
// владеет): Get/List проксируются туда через `source` (ZoneSource). Локальная
// таблица `zones` (`repo`) — fallback, используется только при
// KACHO_COMPUTE_SKIP_PEER_VALIDATION=true (unit/newman без поднятого kacho-vpc).
// Admin-CRUD (`Create/Update/Delete` ниже, через InternalZoneService-handler)
// всегда работает с локальной таблицей `zones` — т.е. с fallback'ом.
type ZoneService struct {
	repo     ZoneRepo
	source   ZoneSource
	skipPeer bool
}

// NewZoneService создаёт ZoneService. repo — локальная таблица `zones`
// (fallback); source — kacho-vpc InternalZoneService (VPCClient); skipPeer ==
// cfg.SkipPeerValidation: true → Get/List читают из `repo`, иначе → из `source`.
func NewZoneService(repo ZoneRepo, source ZoneSource, skipPeer bool) *ZoneService {
	return &ZoneService{repo: repo, source: source, skipPeer: skipPeer}
}

// Get возвращает Zone по id (из kacho-vpc, либо из локальной таблицы при skipPeer).
func (s *ZoneService) Get(ctx context.Context, id string) (*domain.Zone, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "zone_id required")
	}
	if s.skipPeer {
		z, err := s.repo.Get(ctx, id)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		return z, nil
	}
	info, err := s.source.GetZone(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "Zone %s not found", id)
		}
		return nil, mapRepoErr(err)
	}
	return zoneFromInfo(info), nil
}

// List возвращает зоны (из kacho-vpc, либо из локальной таблицы при skipPeer).
func (s *ZoneService) List(ctx context.Context, p Pagination) ([]*domain.Zone, string, error) {
	pageSize, err := corevalidate.PageSize("page_size", p.PageSize)
	if err != nil {
		return nil, "", err
	}
	if s.skipPeer {
		return s.repo.List(ctx, p)
	}
	infos, next, err := s.source.ListZones(ctx, pageSize, p.PageToken)
	if err != nil {
		return nil, "", mapRepoErr(err)
	}
	out := make([]*domain.Zone, 0, len(infos))
	for _, info := range infos {
		out = append(out, zoneFromInfo(info))
	}
	return out, next, nil
}

// zoneFromInfo строит domain.Zone из ZoneInfo. kacho-vpc не трекает zone-status
// → compute всегда репортит ZoneStatusUp (== computev1.Zone_UP).
func zoneFromInfo(info ZoneInfo) *domain.Zone {
	return &domain.Zone{ID: info.ID, RegionID: info.RegionID, Status: domain.ZoneStatusUp}
}

// Create создаёт зону (admin-only).
func (s *ZoneService) Create(ctx context.Context, id, regionID string, st domain.ZoneStatus) (*domain.Zone, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "id required")
	}
	z, err := s.repo.Insert(ctx, &domain.Zone{ID: id, RegionID: regionID, Status: st})
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return z, nil
}

// Update обновляет зону (admin-only).
func (s *ZoneService) Update(ctx context.Context, id, regionID string, st domain.ZoneStatus) (*domain.Zone, error) {
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
