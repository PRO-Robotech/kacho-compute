package handler

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// ComputeInternalHandler реализует pb.ComputeInternalServiceServer.
// НЕ регистрируется в api-gateway (запрет #7).
type ComputeInternalHandler struct {
	pb.UnimplementedComputeInternalServiceServer
	instanceSvc  *service.InstanceService
	diskSvc      *service.DiskService
	snapshotSvc  *service.SnapshotService
	instanceRepo service.InstanceRepo
	diskRepo     service.DiskRepo
	snapshotRepo service.SnapshotRepo
}

// NewComputeInternalHandler создаёт ComputeInternalHandler.
func NewComputeInternalHandler(
	instanceSvc *service.InstanceService,
	diskSvc *service.DiskService,
	snapshotSvc *service.SnapshotService,
	instanceRepo service.InstanceRepo,
	diskRepo service.DiskRepo,
	snapshotRepo service.SnapshotRepo,
) *ComputeInternalHandler {
	return &ComputeInternalHandler{
		instanceSvc:  instanceSvc,
		diskSvc:      diskSvc,
		snapshotSvc:  snapshotSvc,
		instanceRepo: instanceRepo,
		diskRepo:     diskRepo,
		snapshotRepo: snapshotRepo,
	}
}

// InstanceExists проверяет существование Instance (для loadbalancer).
func (h *ComputeInternalHandler) InstanceExists(ctx context.Context, req *pb.InstanceExistsRequest) (*pb.ExistsResponse, error) {
	inst, err := h.instanceRepo.GetByUID(ctx, req.GetUid())
	if err != nil {
		return &pb.ExistsResponse{Exists: false}, nil //nolint:nilerr
	}
	if inst == nil || inst.DeletionTimestamp != nil {
		return &pb.ExistsResponse{Exists: false}, nil
	}
	return &pb.ExistsResponse{Exists: true}, nil
}

// InstanceHasDependents проверяет наличие зависимых ресурсов у Instance.
func (h *ComputeInternalHandler) InstanceHasDependents(ctx context.Context, req *pb.InstanceHasDependentsRequest) (*pb.HasDependentsResponse, error) {
	has, kinds, err := h.instanceSvc.HasDependents(ctx, req.GetUid())
	if err != nil {
		return nil, err
	}
	return &pb.HasDependentsResponse{HasDependents: has, Kinds: kinds}, nil
}

// DiskExists проверяет существование Disk.
func (h *ComputeInternalHandler) DiskExists(ctx context.Context, req *pb.DiskExistsRequest) (*pb.ExistsResponse, error) {
	disk, err := h.diskRepo.GetByUID(ctx, req.GetUid())
	if err != nil {
		return &pb.ExistsResponse{Exists: false}, nil //nolint:nilerr
	}
	if disk == nil || disk.DeletionTimestamp != nil {
		return &pb.ExistsResponse{Exists: false}, nil
	}
	return &pb.ExistsResponse{Exists: true}, nil
}

// DiskHasDependents проверяет наличие зависимых Snapshot у Disk.
func (h *ComputeInternalHandler) DiskHasDependents(ctx context.Context, req *pb.DiskHasDependentsRequest) (*pb.HasDependentsResponse, error) {
	has, err := h.diskSvc.HasSnapshots(ctx, req.GetUid())
	if err != nil {
		return nil, err
	}
	var kinds []string
	if has {
		kinds = []string{"Snapshot"}
	}
	return &pb.HasDependentsResponse{HasDependents: has, Kinds: kinds}, nil
}

// SnapshotExists проверяет существование Snapshot.
func (h *ComputeInternalHandler) SnapshotExists(ctx context.Context, req *pb.SnapshotExistsRequest) (*pb.ExistsResponse, error) {
	snap, err := h.snapshotRepo.GetByUID(ctx, req.GetUid())
	if err != nil {
		return &pb.ExistsResponse{Exists: false}, nil //nolint:nilerr
	}
	if snap == nil || snap.DeletionTimestamp != nil {
		return &pb.ExistsResponse{Exists: false}, nil
	}
	return &pb.ExistsResponse{Exists: true}, nil
}

// UpdateInstanceStatus обновляет status Instance (только reconciler).
func (h *ComputeInternalHandler) UpdateInstanceStatus(ctx context.Context, req *pb.UpdateInstanceStatusRequest) (*pb.UpdateInstanceStatusResponse, error) {
	uid := req.GetUid()
	if uid == "" {
		return nil, status.Error(codes.InvalidArgument, "uid is required")
	}

	existing, err := h.instanceRepo.GetByUID(ctx, uid)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, status.Errorf(codes.NotFound, "Instance %s not found", uid)
	}

	pbStatus := req.GetStatus()
	if pbStatus != nil {
		existing.State = domain.InstanceState(pbStatus.GetState())
		if pbStatus.GetStateLastTransitionAt() != nil {
			existing.StateLastTransitionAt = pbStatus.GetStateLastTransitionAt().AsTime()
		}
		if pbStatus.GetIps() != nil {
			existing.IPs = &domain.IPs{
				Internal: pbStatus.GetIps().GetInternal(),
				External: pbStatus.GetIps().GetExternal(),
			}
		}
		if pbStatus.GetLastRestartCompletedAt() != nil {
			t := pbStatus.GetLastRestartCompletedAt().AsTime()
			existing.LastRestartCompletedAt = &t
		}
	}

	_, err = h.instanceRepo.UpdateStatus(ctx, existing)
	if err != nil {
		return nil, err
	}
	return &pb.UpdateInstanceStatusResponse{}, nil
}

// UpdateDiskStatus обновляет status Disk (только reconciler).
func (h *ComputeInternalHandler) UpdateDiskStatus(ctx context.Context, req *pb.UpdateDiskStatusRequest) (*pb.UpdateDiskStatusResponse, error) {
	uid := req.GetUid()
	if uid == "" {
		return nil, status.Error(codes.InvalidArgument, "uid is required")
	}

	existing, err := h.diskRepo.GetByUID(ctx, uid)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, status.Errorf(codes.NotFound, "Disk %s not found", uid)
	}

	pbStatus := req.GetStatus()
	if pbStatus != nil {
		existing.State = domain.DiskState(pbStatus.GetState())
		if pbStatus.GetStateLastTransitionAt() != nil {
			existing.StateLastTransitionAt = pbStatus.GetStateLastTransitionAt().AsTime()
		}
		existing.AttachedToInstanceID = pbStatus.GetAttachedToInstanceId()
		existing.DeviceName = pbStatus.GetDeviceName()
	}

	_, err = h.diskRepo.UpdateStatus(ctx, existing)
	if err != nil {
		return nil, err
	}
	return &pb.UpdateDiskStatusResponse{}, nil
}

// UpdateSnapshotStatus обновляет status Snapshot (только reconciler).
func (h *ComputeInternalHandler) UpdateSnapshotStatus(ctx context.Context, req *pb.UpdateSnapshotStatusRequest) (*pb.UpdateSnapshotStatusResponse, error) {
	uid := req.GetUid()
	if uid == "" {
		return nil, status.Error(codes.InvalidArgument, "uid is required")
	}

	existing, err := h.snapshotRepo.GetByUID(ctx, uid)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, status.Errorf(codes.NotFound, "Snapshot %s not found", uid)
	}

	pbStatus := req.GetStatus()
	if pbStatus != nil {
		existing.State = domain.SnapshotState(pbStatus.GetState())
		if pbStatus.GetStateLastTransitionAt() != nil {
			existing.StateLastTransitionAt = pbStatus.GetStateLastTransitionAt().AsTime()
		}
		existing.ProgressPercent = pbStatus.GetProgressPercent()
	}

	_, err = h.snapshotRepo.UpdateStatus(ctx, existing)
	if err != nil {
		return nil, err
	}
	return &pb.UpdateSnapshotStatusResponse{}, nil
}

// UpdateInstanceMetadata обновляет metadata Instance (finalizers, restartedAt).
func (h *ComputeInternalHandler) UpdateInstanceMetadata(ctx context.Context, req *pb.UpdateInstanceMetadataRequest) (*pb.UpdateInstanceMetadataResponse, error) {
	uid := req.GetUid()
	if uid == "" {
		return nil, status.Error(codes.InvalidArgument, "uid is required")
	}

	var restartedAt *string
	if ra := req.GetRestartedAt(); ra != "" {
		restartedAt = &ra
	}

	_, err := h.instanceSvc.UpdateMetadata(ctx, uid, req.GetFinalizers(), req.GetUpdateFinalizers(), restartedAt)
	if err != nil {
		return nil, err
	}
	return &pb.UpdateInstanceMetadataResponse{}, nil
}

// RemoveTargetFinalizer удаляет finalizer loadbalancer после deregister.
func (h *ComputeInternalHandler) RemoveTargetFinalizer(ctx context.Context, req *pb.RemoveTargetFinalizerRequest) (*pb.RemoveTargetFinalizerResponse, error) {
	instanceID := req.GetInstanceId()
	if instanceID == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id is required")
	}

	existing, err := h.instanceRepo.GetByUID(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		// Уже удалён — идемпотентно
		return &pb.RemoveTargetFinalizerResponse{}, nil
	}

	newFinalizers := removeTargetFinalizer(existing.Finalizers)
	_, err = h.instanceSvc.UpdateMetadata(ctx, instanceID, newFinalizers, true, nil)
	if err != nil {
		return nil, err
	}
	return &pb.RemoveTargetFinalizerResponse{}, nil
}

func removeTargetFinalizer(finalizers []string) []string {
	var result []string
	for _, f := range finalizers {
		if f != "loadbalancer.kacho.io/target-deregister" {
			result = append(result, f)
		}
	}
	return result
}

var _ = time.Now
