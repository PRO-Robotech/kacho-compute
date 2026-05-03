package service

import (
	"context"
	"reflect"
	"time"

	coreerrors "github.com/PRO-Robotech/kacho-corelib/errors"
	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-compute/internal/domain"
)

// InstanceService реализует use-cases для Instance.
type InstanceService struct {
	repo         InstanceRepo
	diskRepo     DiskRepo
	folderClient FolderClient
	subnetClient SubnetClient
}

// NewInstanceService создаёт InstanceService.
func NewInstanceService(
	repo InstanceRepo,
	diskRepo DiskRepo,
	folderClient FolderClient,
	subnetClient SubnetClient,
) *InstanceService {
	return &InstanceService{
		repo:         repo,
		diskRepo:     diskRepo,
		folderClient: folderClient,
		subnetClient: subnetClient,
	}
}

// Upsert создаёт или обновляет инстанс.
func (s *InstanceService) Upsert(ctx context.Context, inst *domain.Instance) (*domain.Instance, error) {
	if inst.Name == "" {
		return nil, coreerrors.InvalidArgument().AddFieldViolation("name", "name is required").Err()
	}
	if inst.FolderID == "" {
		return nil, coreerrors.InvalidArgument().AddFieldViolation("folder_id", "folder_id is required").Err()
	}

	// Валидация Folder
	exists, err := s.folderClient.Exists(ctx, inst.FolderID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, coreerrors.NotFound("Folder", inst.FolderID).Err()
	}

	// Валидация bootDisk — должен существовать и быть READY (nit-2)
	if inst.BootDisk != nil && inst.BootDisk.DiskID != "" {
		disk, err := s.diskRepo.GetByUID(ctx, inst.BootDisk.DiskID)
		if err != nil {
			return nil, err
		}
		if disk == nil {
			return nil, coreerrors.NotFound("Disk", inst.BootDisk.DiskID).Err()
		}
		if disk.State != domain.DiskStateReady {
			return nil, coreerrors.FailedPrecondition("boot disk is not in READY state").Err()
		}
	}

	// Валидация NetworkInterfaces — SubnetClient
	for i, ni := range inst.NetworkInterfaces {
		if ni.SubnetID != "" {
			subnetExists, err := s.subnetClient.Exists(ctx, ni.SubnetID)
			if err != nil {
				return nil, err
			}
			if !subnetExists {
				_ = i
			return nil, coreerrors.NotFound("Subnet", ni.SubnetID).Err()
			}
		}
	}

	existing, err := s.repo.GetByFolderAndName(ctx, inst.FolderID, inst.Name)
	if err != nil {
		return nil, err
	}

	if existing == nil {
		inst.UID = ids.NewUID()
		inst.State = domain.InstanceStateProvisioning
		// Добавляем finalizer для detach дисков при удалении
		if inst.BootDisk != nil && inst.BootDisk.DiskID != "" {
			inst.Finalizers = appendIfMissing(inst.Finalizers, "compute.kacho.io/disk-detach")
		}
		return s.repo.Insert(ctx, inst)
	}

	// no-op detection
	if !instanceDiff(existing, inst) {
		return existing, nil
	}

	existing.Labels = inst.Labels
	existing.Annotations = inst.Annotations
	existing.DisplayName = inst.DisplayName
	existing.Description = inst.Description
	existing.DesiredPowerState = inst.DesiredPowerState
	existing.Metadata = inst.Metadata
	existing.Resources = inst.Resources
	existing.NetworkInterfaces = inst.NetworkInterfaces
	existing.SchedulingPolicy = inst.SchedulingPolicy
	return s.repo.Update(ctx, existing)
}

// GetByUID возвращает инстанс по UID.
func (s *InstanceService) GetByUID(ctx context.Context, uid string) (*domain.Instance, error) {
	if uid == "" {
		return nil, coreerrors.InvalidArgument().AddFieldViolation("uid", "uid is required").Err()
	}
	return s.repo.GetByUID(ctx, uid)
}

// List возвращает список инстансов.
func (s *InstanceService) List(ctx context.Context, selectors []Selector, page Pagination) ([]*domain.Instance, string, int64, error) {
	return s.repo.List(ctx, selectors, page)
}

// SnapshotResourceVersion возвращает текущий resource version.
func (s *InstanceService) SnapshotResourceVersion(ctx context.Context) (int64, error) {
	return s.repo.SnapshotResourceVersion(ctx)
}

// Delete удаляет инстанс (soft delete — устанавливает deletionTimestamp).
func (s *InstanceService) Delete(ctx context.Context, uid string) error {
	if uid == "" {
		return coreerrors.InvalidArgument().AddFieldViolation("uid", "uid is required").Err()
	}
	existing, err := s.repo.GetByUID(ctx, uid)
	if err != nil {
		return err
	}
	if existing == nil {
		return coreerrors.NotFound("Instance", uid).Err()
	}
	return s.repo.SoftDelete(ctx, uid)
}

// Restart планирует перезапуск инстанса.
func (s *InstanceService) Restart(ctx context.Context, uid string) (*domain.Instance, error) {
	if uid == "" {
		return nil, coreerrors.InvalidArgument().AddFieldViolation("uid", "uid is required").Err()
	}
	existing, err := s.repo.GetByUID(ctx, uid)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, coreerrors.NotFound("Instance", uid).Err()
	}
	if existing.State != domain.InstanceStateRunning {
		return nil, coreerrors.FailedPrecondition("Instance must be in RUNNING state to restart").Err()
	}
	// Устанавливаем restartedAt = now() — reconciler подхватит и выполнит цикл stop+start
	now := time.Now().UTC()
	existing.RestartedAt = &now
	return s.repo.SetRestart(ctx, uid)
}

// UpdateStatus обновляет статус инстанса (только через Internal).
func (s *InstanceService) UpdateStatus(ctx context.Context, inst *domain.Instance) (*domain.Instance, error) {
	return s.repo.UpdateStatus(ctx, inst)
}

// UpdateMetadata обновляет метаданные инстанса (finalizers, restartedAt).
func (s *InstanceService) UpdateMetadata(ctx context.Context, uid string, finalizers []string, updateFinalizers bool, restartedAt *string) (*domain.Instance, error) {
	return s.repo.UpdateMetadata(ctx, uid, finalizers, updateFinalizers, restartedAt)
}

// HasDependents проверяет наличие зависимых ресурсов у Instance.
func (s *InstanceService) HasDependents(_ context.Context, _ string) (bool, []string, error) {
	// В 0.4 нет cross-service зависимостей у Instance (LB в 0.5)
	return false, nil, nil
}

func instanceDiff(existing, incoming *domain.Instance) bool {
	if existing.DisplayName != incoming.DisplayName {
		return true
	}
	if existing.Description != incoming.Description {
		return true
	}
	if existing.DesiredPowerState != incoming.DesiredPowerState {
		return true
	}
	if !reflect.DeepEqual(normalizeMap(existing.Labels), normalizeMap(incoming.Labels)) {
		return true
	}
	if !reflect.DeepEqual(normalizeMap(existing.Annotations), normalizeMap(incoming.Annotations)) {
		return true
	}
	if !reflect.DeepEqual(normalizeMap(existing.Metadata), normalizeMap(incoming.Metadata)) {
		return true
	}
	return false
}

func appendIfMissing(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}
