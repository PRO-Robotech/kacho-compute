package service

import (
	"context"
	"reflect"

	coreerrors "github.com/PRO-Robotech/kacho-corelib/errors"
	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-compute/internal/domain"
)

// SnapshotService реализует use-cases для Snapshot.
type SnapshotService struct {
	repo     SnapshotRepo
	diskRepo DiskRepo
}

// NewSnapshotService создаёт SnapshotService.
func NewSnapshotService(repo SnapshotRepo, diskRepo DiskRepo) *SnapshotService {
	return &SnapshotService{repo: repo, diskRepo: diskRepo}
}

// Upsert создаёт или обновляет снапшот.
func (s *SnapshotService) Upsert(ctx context.Context, snap *domain.Snapshot) (*domain.Snapshot, error) {
	if snap.Name == "" {
		return nil, coreerrors.InvalidArgument().AddFieldViolation("name", "name is required").Err()
	}
	if snap.FolderID == "" {
		return nil, coreerrors.InvalidArgument().AddFieldViolation("folder_id", "folder_id is required").Err()
	}
	if snap.DiskID == "" {
		return nil, coreerrors.InvalidArgument().AddFieldViolation("spec.disk_id", "disk_id is required").Err()
	}

	// Валидация Disk — должен существовать и быть READY
	disk, err := s.diskRepo.GetByUID(ctx, snap.DiskID)
	if err != nil {
		return nil, err
	}
	if disk == nil {
		return nil, coreerrors.NotFound("Disk", snap.DiskID).Err()
	}
	if disk.State != domain.DiskStateReady {
		return nil, coreerrors.FailedPrecondition("Disk is not in READY state, cannot create Snapshot").Err()
	}

	existing, err := s.repo.GetByFolderAndName(ctx, snap.FolderID, snap.Name)
	if err != nil {
		return nil, err
	}

	if existing == nil {
		snap.UID = ids.NewUID()
		snap.State = domain.SnapshotStateCreating
		snap.ProgressPercent = 0
		return s.repo.Insert(ctx, snap)
	}

	// no-op detection
	if !snapshotDiff(existing, snap) {
		return existing, nil
	}

	existing.Labels = snap.Labels
	existing.Annotations = snap.Annotations
	existing.DisplayName = snap.DisplayName
	existing.Description = snap.Description
	return s.repo.Update(ctx, existing)
}

// GetByUID возвращает снапшот по UID.
func (s *SnapshotService) GetByUID(ctx context.Context, uid string) (*domain.Snapshot, error) {
	if uid == "" {
		return nil, coreerrors.InvalidArgument().AddFieldViolation("uid", "uid is required").Err()
	}
	return s.repo.GetByUID(ctx, uid)
}

// List возвращает список снапшотов.
func (s *SnapshotService) List(ctx context.Context, selectors []Selector, page Pagination) ([]*domain.Snapshot, string, int64, error) {
	return s.repo.List(ctx, selectors, page)
}

// SnapshotResourceVersion возвращает текущий resource version.
func (s *SnapshotService) SnapshotResourceVersion(ctx context.Context) (int64, error) {
	return s.repo.SnapshotResourceVersion(ctx)
}

// Delete удаляет снапшот.
func (s *SnapshotService) Delete(ctx context.Context, uid string) error {
	if uid == "" {
		return coreerrors.InvalidArgument().AddFieldViolation("uid", "uid is required").Err()
	}
	existing, err := s.repo.GetByUID(ctx, uid)
	if err != nil {
		return err
	}
	if existing == nil {
		return coreerrors.NotFound("Snapshot", uid).Err()
	}
	return s.repo.HardDelete(ctx, uid)
}

// UpdateStatus обновляет статус снапшота (только через Internal).
func (s *SnapshotService) UpdateStatus(ctx context.Context, snap *domain.Snapshot) (*domain.Snapshot, error) {
	return s.repo.UpdateStatus(ctx, snap)
}

func snapshotDiff(existing, incoming *domain.Snapshot) bool {
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
