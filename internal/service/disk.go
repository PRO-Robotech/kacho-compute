package service

import (
	"context"
	"reflect"

	coreerrors "github.com/PRO-Robotech/kacho-corelib/errors"
	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-compute/internal/domain"
)

// DiskService реализует use-cases для Disk.
type DiskService struct {
	repo         DiskRepo
	imageRepo    ImageRepo
	folderClient FolderClient
}

// NewDiskService создаёт DiskService.
func NewDiskService(repo DiskRepo, imageRepo ImageRepo, folderClient FolderClient) *DiskService {
	return &DiskService{repo: repo, imageRepo: imageRepo, folderClient: folderClient}
}

// Upsert создаёт или обновляет диск.
func (s *DiskService) Upsert(ctx context.Context, disk *domain.Disk) (*domain.Disk, error) {
	if disk.Name == "" {
		return nil, coreerrors.InvalidArgument().AddFieldViolation("name", "name is required").Err()
	}
	if disk.FolderID == "" {
		return nil, coreerrors.InvalidArgument().AddFieldViolation("folder_id", "folder_id is required").Err()
	}
	if disk.DiskTypeID == "" {
		return nil, coreerrors.InvalidArgument().AddFieldViolation("spec.disk_type_id", "disk_type_id is required").Err()
	}
	if disk.ZoneID == "" {
		return nil, coreerrors.InvalidArgument().AddFieldViolation("spec.zone_id", "zone_id is required").Err()
	}
	if disk.Size == "" {
		return nil, coreerrors.InvalidArgument().AddFieldViolation("spec.size", "size is required").Err()
	}

	// Валидация Folder
	exists, err := s.folderClient.Exists(ctx, disk.FolderID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, coreerrors.NotFound("Folder", disk.FolderID).Err()
	}

	// Валидация Image (если задан)
	if disk.ImageID != "" {
		img, err := s.imageRepo.GetByUID(ctx, disk.ImageID)
		if err != nil {
			return nil, err
		}
		if img == nil {
			return nil, coreerrors.NotFound("Image", disk.ImageID).Err()
		}
	}

	existing, err := s.repo.GetByFolderAndName(ctx, disk.FolderID, disk.Name)
	if err != nil {
		return nil, err
	}

	if existing == nil {
		disk.UID = ids.NewUID()
		disk.State = domain.DiskStateCreating
		return s.repo.Insert(ctx, disk)
	}

	// no-op detection
	if !diskDiff(existing, disk) {
		return existing, nil
	}

	existing.Labels = disk.Labels
	existing.Annotations = disk.Annotations
	existing.DisplayName = disk.DisplayName
	existing.Description = disk.Description
	return s.repo.Update(ctx, existing)
}

// GetByUID возвращает диск по UID.
func (s *DiskService) GetByUID(ctx context.Context, uid string) (*domain.Disk, error) {
	if uid == "" {
		return nil, coreerrors.InvalidArgument().AddFieldViolation("uid", "uid is required").Err()
	}
	return s.repo.GetByUID(ctx, uid)
}

// List возвращает список дисков.
func (s *DiskService) List(ctx context.Context, selectors []Selector, page Pagination) ([]*domain.Disk, string, int64, error) {
	return s.repo.List(ctx, selectors, page)
}

// SnapshotResourceVersion возвращает текущий resource version.
func (s *DiskService) SnapshotResourceVersion(ctx context.Context) (int64, error) {
	return s.repo.SnapshotResourceVersion(ctx)
}

// Delete удаляет диск.
func (s *DiskService) Delete(ctx context.Context, uid string) error {
	if uid == "" {
		return coreerrors.InvalidArgument().AddFieldViolation("uid", "uid is required").Err()
	}
	existing, err := s.repo.GetByUID(ctx, uid)
	if err != nil {
		return err
	}
	if existing == nil {
		return coreerrors.NotFound("Disk", uid).Err()
	}
	// Проверяем наличие снапшотов
	hasSnaps, err := s.repo.HasSnapshots(ctx, uid)
	if err != nil {
		return err
	}
	if hasSnaps {
		return coreerrors.FailedPrecondition("Disk has dependent Snapshots").Err()
	}
	return s.repo.HardDelete(ctx, uid)
}

// HasSnapshots проверяет наличие зависимых снапшотов.
func (s *DiskService) HasSnapshots(ctx context.Context, uid string) (bool, error) {
	return s.repo.HasSnapshots(ctx, uid)
}

// UpdateStatus обновляет статус диска (только через Internal).
func (s *DiskService) UpdateStatus(ctx context.Context, disk *domain.Disk) (*domain.Disk, error) {
	return s.repo.UpdateStatus(ctx, disk)
}

func diskDiff(existing, incoming *domain.Disk) bool {
	if existing.DisplayName != incoming.DisplayName {
		return true
	}
	if existing.Description != incoming.Description {
		return true
	}
	if !reflect.DeepEqual(normalizeMap(existing.Labels), normalizeMap(incoming.Labels)) {
		return true
	}
	if !reflect.DeepEqual(normalizeMap(existing.Annotations), normalizeMap(incoming.Annotations)) {
		return true
	}
	return false
}
