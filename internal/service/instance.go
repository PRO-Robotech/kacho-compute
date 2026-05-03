package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
)

// ErrNotFound возвращается, когда ресурс не найден.
var ErrNotFound = errors.New("not found")

// ErrConflict возвращается при нарушении optimistic concurrency.
var ErrConflict = errors.New("resource version conflict")

// ErrInvalidArg возвращается при некорректных входных данных.
var ErrInvalidArg = errors.New("invalid argument")

// InstanceService — бизнес-логика управления VM.
type InstanceService struct {
	repo         InstanceRepo
	diskRepo     DiskRepo
	folderClient FolderClient
	subnetClient SubnetClient
	opsRepo      operations.Repo
}

// NewInstanceService создаёт InstanceService.
func NewInstanceService(
	repo InstanceRepo,
	diskRepo DiskRepo,
	folderClient FolderClient,
	subnetClient SubnetClient,
	opsRepo operations.Repo,
) *InstanceService {
	return &InstanceService{
		repo:         repo,
		diskRepo:     diskRepo,
		folderClient: folderClient,
		subnetClient: subnetClient,
		opsRepo:      opsRepo,
	}
}

// Get возвращает Instance по ID.
func (s *InstanceService) Get(ctx context.Context, id string) (*domain.Instance, error) {
	inst, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return inst, nil
}

// List возвращает список Instance в folder с пагинацией.
func (s *InstanceService) List(ctx context.Context, f InstanceFilter, page Pagination) ([]*domain.Instance, string, error) {
	return s.repo.List(ctx, f, page)
}

// Create создаёт Operation + инициирует создание Instance.
func (s *InstanceService) Create(ctx context.Context, req CreateInstanceReq) (*operations.Operation, error) {
	if req.FolderID == "" {
		return nil, status.Error(codes.InvalidArgument, "folder_id required")
	}
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name required")
	}

	instID := ids.NewUID()
	op, err := operations.New(
		fmt.Sprintf("Create instance %s", req.Name),
		&computev1.CreateInstanceMetadata{InstanceId: instID},
	)
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}

	operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		return s.doCreate(ctx, instID, req)
	})

	return &op, nil
}

func (s *InstanceService) doCreate(ctx context.Context, instID string, req CreateInstanceReq) (*anypb.Any, error) {
	// Валидация folder.
	exists, err := s.folderClient.Exists(ctx, req.FolderID)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "folder check: %v", err)
	}
	if !exists {
		return nil, status.Errorf(codes.NotFound, "folder %s not found", req.FolderID)
	}

	// Валидация subnet-ов.
	for _, ni := range req.NetworkInterfaces {
		if ni.SubnetID != "" {
			ok, err := s.subnetClient.Exists(ctx, ni.SubnetID)
			if err != nil {
				return nil, status.Errorf(codes.Unavailable, "subnet check: %v", err)
			}
			if !ok {
				return nil, status.Errorf(codes.NotFound, "subnet %s not found", ni.SubnetID)
			}
		}
	}

	// Валидация boot disk.
	if req.BootDisk.DiskID != "" {
		disk, err := s.diskRepo.Get(ctx, req.BootDisk.DiskID)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "boot disk %s not found", req.BootDisk.DiskID)
		}
		if disk.Status != domain.DiskStatusReady {
			return nil, status.Errorf(codes.FailedPrecondition, "boot disk not in READY state")
		}
	}

	now := time.Now().UTC()
	inst := &domain.Instance{
		ID:                    instID,
		FolderID:              req.FolderID,
		CreatedAt:             now,
		Name:                  req.Name,
		Description:           req.Description,
		Labels:                req.Labels,
		ZoneID:                req.ZoneID,
		PlatformID:            req.PlatformID,
		Resources:             req.Resources,
		Status:                domain.InstanceStatusProvisioning,
		FQDN:                  req.FQDN,
		Metadata:              req.Metadata,
		BootDisk:              req.BootDisk,
		SecondaryDisks:        req.SecondaryDisks,
		NetworkInterfaces:     req.NetworkInterfaces,
		ServiceAccountID:      req.ServiceAccountID,
		SchedulingPolicy:      req.SchedulingPolicy,
		DesiredPowerState:     domain.PowerStateRunning,
		Generation:            1,
		ResourceVersion:       ids.NewUID(),
		ObservedGeneration:    0,
		StatusLastTransitionAt: now,
	}

	created, err := s.repo.Insert(ctx, inst)
	if err != nil {
		return nil, err
	}

	// Reconciler подхватит PROVISIONING → RUNNING через poll.
	return anypb.New(instanceToProto(created))
}

// Update обновляет Instance (spec-change).
func (s *InstanceService) Update(ctx context.Context, req UpdateInstanceReq) (*operations.Operation, error) {
	inst, err := s.repo.Get(ctx, req.InstanceID)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	if req.ResourceVersion != "" && inst.ResourceVersion != req.ResourceVersion {
		return nil, status.Error(codes.Aborted, "resource version conflict")
	}

	op, err := operations.New(
		fmt.Sprintf("Update instance %s", req.InstanceID),
		&computev1.UpdateInstanceMetadata{InstanceId: req.InstanceID},
	)
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}

	operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		return s.doUpdate(ctx, req)
	})

	return &op, nil
}

func (s *InstanceService) doUpdate(ctx context.Context, req UpdateInstanceReq) (*anypb.Any, error) {
	inst, err := s.repo.Get(ctx, req.InstanceID)
	if err != nil {
		return nil, mapRepoErr(err)
	}

	// Применяем изменения.
	if req.Name != "" {
		inst.Name = req.Name
	}
	if req.Description != "" {
		inst.Description = req.Description
	}
	if req.Labels != nil {
		inst.Labels = req.Labels
	}
	if req.Resources != nil {
		inst.Resources = *req.Resources
	}
	if req.Metadata != nil {
		inst.Metadata = req.Metadata
	}
	if req.ServiceAccountID != "" {
		inst.ServiceAccountID = req.ServiceAccountID
	}

	inst.Generation++
	inst.ResourceVersion = ids.NewUID()
	inst.Status = domain.InstanceStatusProvisioning
	inst.StatusLastTransitionAt = time.Now().UTC()

	updated, err := s.repo.Update(ctx, inst)
	if err != nil {
		return nil, err
	}
	return anypb.New(instanceToProto(updated))
}

// Delete помечает Instance на удаление.
func (s *InstanceService) Delete(ctx context.Context, instanceID string) (*operations.Operation, error) {
	inst, err := s.repo.Get(ctx, instanceID)
	if err != nil {
		return nil, mapRepoErr(err)
	}

	op, err := operations.New(
		fmt.Sprintf("Delete instance %s", instanceID),
		&computev1.DeleteInstanceMetadata{InstanceId: instanceID},
	)
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	inst.Status = domain.InstanceStatusDeleting
	inst.DeletedAt = &now
	inst.StatusLastTransitionAt = now

	if _, err := s.repo.Update(ctx, inst); err != nil {
		return nil, err
	}

	// Reconciler завершит удаление.
	return &op, nil
}

// Start запускает остановленный Instance.
func (s *InstanceService) Start(ctx context.Context, instanceID string) (*operations.Operation, error) {
	inst, err := s.repo.Get(ctx, instanceID)
	if err != nil {
		return nil, mapRepoErr(err)
	}

	op, err := operations.New(
		fmt.Sprintf("Start instance %s", instanceID),
		&computev1.StartInstanceMetadata{InstanceId: instanceID},
	)
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	inst.Status = domain.InstanceStatusStarting
	inst.DesiredPowerState = domain.PowerStateRunning
	inst.StatusLastTransitionAt = now

	if _, err := s.repo.Update(ctx, inst); err != nil {
		return nil, err
	}
	return &op, nil
}

// Stop останавливает работающий Instance.
func (s *InstanceService) Stop(ctx context.Context, instanceID string) (*operations.Operation, error) {
	inst, err := s.repo.Get(ctx, instanceID)
	if err != nil {
		return nil, mapRepoErr(err)
	}

	op, err := operations.New(
		fmt.Sprintf("Stop instance %s", instanceID),
		&computev1.StopInstanceMetadata{InstanceId: instanceID},
	)
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	inst.Status = domain.InstanceStatusStopping
	inst.DesiredPowerState = domain.PowerStateStopped
	inst.StatusLastTransitionAt = now

	if _, err := s.repo.Update(ctx, inst); err != nil {
		return nil, err
	}
	return &op, nil
}

// Restart перезапускает Instance.
func (s *InstanceService) Restart(ctx context.Context, instanceID string) (*operations.Operation, error) {
	inst, err := s.repo.Get(ctx, instanceID)
	if err != nil {
		return nil, mapRepoErr(err)
	}

	op, err := operations.New(
		fmt.Sprintf("Restart instance %s", instanceID),
		&computev1.RestartInstanceMetadata{InstanceId: instanceID},
	)
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	inst.Status = domain.InstanceStatusStopping
	inst.DesiredPowerState = domain.PowerStateRunning // после restart — running
	inst.StatusLastTransitionAt = now

	if _, err := s.repo.Update(ctx, inst); err != nil {
		return nil, err
	}
	return &op, nil
}

// ---- request types ----

// CreateInstanceReq — параметры создания инстанса.
type CreateInstanceReq struct {
	FolderID          string
	Name              string
	Description       string
	Labels            map[string]string
	ZoneID            string
	PlatformID        string
	Resources         domain.Resources
	FQDN              string
	Metadata          map[string]string
	BootDisk          domain.BootDisk
	SecondaryDisks    []domain.AttachedDisk
	NetworkInterfaces []domain.NetworkInterface
	ServiceAccountID  string
	SchedulingPolicy  domain.SchedulingPolicy
}

// UpdateInstanceReq — параметры обновления инстанса.
type UpdateInstanceReq struct {
	InstanceID       string
	ResourceVersion  string
	Name             string
	Description      string
	Labels           map[string]string
	Resources        *domain.Resources
	Metadata         map[string]string
	ServiceAccountID string
}

// ---- helpers ----

func mapRepoErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotFound) {
		return status.Error(codes.NotFound, err.Error())
	}
	return err
}
