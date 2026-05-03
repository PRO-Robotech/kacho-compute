package handler

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	operationv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	svc "github.com/PRO-Robotech/kacho-compute/internal/service"
)

// InstanceHandler реализует computev1.InstanceServiceServer.
type InstanceHandler struct {
	computev1.UnimplementedInstanceServiceServer
	svc *svc.InstanceService
}

// NewInstanceHandler создаёт InstanceHandler.
func NewInstanceHandler(svc *svc.InstanceService) *InstanceHandler {
	return &InstanceHandler{svc: svc}
}

func (h *InstanceHandler) Get(ctx context.Context, req *computev1.GetInstanceRequest) (*computev1.Instance, error) {
	if req.InstanceId == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	inst, err := h.svc.Get(ctx, req.InstanceId)
	if err != nil {
		return nil, err
	}
	return instanceToProto(inst), nil
}

func (h *InstanceHandler) List(ctx context.Context, req *computev1.ListInstancesRequest) (*computev1.ListInstancesResponse, error) {
	insts, nextToken, err := h.svc.List(ctx, svc.InstanceFilter{
		FolderID: req.FolderId,
		Filter:   req.Filter,
		OrderBy:  req.OrderBy,
	}, svc.Pagination{
		PageToken: req.PageToken,
		PageSize:  req.PageSize,
	})
	if err != nil {
		return nil, err
	}

	resp := &computev1.ListInstancesResponse{NextPageToken: nextToken}
	for _, inst := range insts {
		resp.Instances = append(resp.Instances, instanceToProto(inst))
	}
	return resp, nil
}

func (h *InstanceHandler) Create(ctx context.Context, req *computev1.CreateInstanceRequest) (*operationv1.Operation, error) {
	createReq := svc.CreateInstanceReq{
		FolderID:    req.FolderId,
		Name:        req.Name,
		Description: req.Description,
		Labels:      req.Labels,
		ZoneID:      req.ZoneId,
		PlatformID:  req.PlatformId,
		FQDN:        req.Fqdn,
		Metadata:    req.Metadata,
		ServiceAccountID: req.ServiceAccountId,
	}
	if req.Resources != nil {
		createReq.Resources = domain.Resources{
			Cores:        req.Resources.Cores,
			Memory:       req.Resources.Memory,
			CoreFraction: req.Resources.CoreFraction,
			GPUs:         req.Resources.Gpus,
		}
	}
	if req.BootDisk != nil {
		createReq.BootDisk = domain.BootDisk{
			DiskID:     req.BootDisk.DiskId,
			DeviceName: req.BootDisk.DeviceName,
			AutoDelete: req.BootDisk.AutoDelete,
		}
	}
	for _, sd := range req.SecondaryDisks {
		createReq.SecondaryDisks = append(createReq.SecondaryDisks, domain.AttachedDisk{
			DiskID:     sd.DiskId,
			DeviceName: sd.DeviceName,
			AutoDelete: sd.AutoDelete,
		})
	}
	for _, ni := range req.NetworkInterfaces {
		createReq.NetworkInterfaces = append(createReq.NetworkInterfaces, domain.NetworkInterface{
			SubnetID:         ni.SubnetId,
			PrimaryV4Address: ni.PrimaryV4Address,
			SecurityGroupIDs: ni.SecurityGroupIds,
		})
	}
	if req.SchedulingPolicy != nil {
		createReq.SchedulingPolicy = domain.SchedulingPolicy{Preemptible: req.SchedulingPolicy.Preemptible}
	}

	op, err := h.svc.Create(ctx, createReq)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

func (h *InstanceHandler) Update(ctx context.Context, req *computev1.UpdateInstanceRequest) (*operationv1.Operation, error) {
	if req.InstanceId == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	updReq := svc.UpdateInstanceReq{
		InstanceID:      req.InstanceId,
		ResourceVersion: req.ResourceVersion,
		Name:            req.Name,
		Description:     req.Description,
		Labels:          req.Labels,
		Metadata:        req.Metadata,
		ServiceAccountID: req.ServiceAccountId,
	}
	if req.Resources != nil {
		r := domain.Resources{
			Cores:        req.Resources.Cores,
			Memory:       req.Resources.Memory,
			CoreFraction: req.Resources.CoreFraction,
			GPUs:         req.Resources.Gpus,
		}
		updReq.Resources = &r
	}

	op, err := h.svc.Update(ctx, updReq)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

func (h *InstanceHandler) Delete(ctx context.Context, req *computev1.DeleteInstanceRequest) (*operationv1.Operation, error) {
	if req.InstanceId == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	op, err := h.svc.Delete(ctx, req.InstanceId)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

func (h *InstanceHandler) Start(ctx context.Context, req *computev1.StartInstanceRequest) (*operationv1.Operation, error) {
	if req.InstanceId == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	op, err := h.svc.Start(ctx, req.InstanceId)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

func (h *InstanceHandler) Stop(ctx context.Context, req *computev1.StopInstanceRequest) (*operationv1.Operation, error) {
	if req.InstanceId == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	op, err := h.svc.Stop(ctx, req.InstanceId)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

func (h *InstanceHandler) Restart(ctx context.Context, req *computev1.RestartInstanceRequest) (*operationv1.Operation, error) {
	if req.InstanceId == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	op, err := h.svc.Restart(ctx, req.InstanceId)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// ---- domain → proto mapping ----

func instanceToProto(inst *domain.Instance) *computev1.Instance {
	p := &computev1.Instance{
		Id:          inst.ID,
		FolderId:    inst.FolderID,
		CreatedAt:   timestamppb.New(inst.CreatedAt),
		Name:        inst.Name,
		Description: inst.Description,
		Labels:      inst.Labels,
		ZoneId:      inst.ZoneID,
		PlatformId:  inst.PlatformID,
		Resources: &computev1.Resources{
			Cores:        inst.Resources.Cores,
			Memory:       inst.Resources.Memory,
			CoreFraction: inst.Resources.CoreFraction,
			Gpus:         inst.Resources.GPUs,
		},
		Status: computev1.Status(inst.Status),
		Fqdn:   inst.FQDN,
		Metadata: inst.Metadata,
		BootDisk: &computev1.BootDisk{
			DiskId:     inst.BootDisk.DiskID,
			DeviceName: inst.BootDisk.DeviceName,
			AutoDelete: inst.BootDisk.AutoDelete,
		},
		ServiceAccountId:   inst.ServiceAccountID,
		DesiredPowerState:  computev1.PowerState(inst.DesiredPowerState),
		Generation:         inst.Generation,
		ResourceVersion:    inst.ResourceVersion,
		ObservedGeneration: inst.ObservedGeneration,
		StatusLastTransitionAt: timestamppb.New(inst.StatusLastTransitionAt),
		Ips: &computev1.Ips{
			Internal: inst.IPs.Internal,
			External: inst.IPs.External,
		},
		SchedulingPolicy: &computev1.SchedulingPolicy{
			Preemptible: inst.SchedulingPolicy.Preemptible,
		},
	}
	for _, d := range inst.SecondaryDisks {
		p.SecondaryDisks = append(p.SecondaryDisks, &computev1.AttachedDisk{
			DiskId:     d.DiskID,
			DeviceName: d.DeviceName,
			AutoDelete: d.AutoDelete,
		})
	}
	for _, ni := range inst.NetworkInterfaces {
		p.NetworkInterfaces = append(p.NetworkInterfaces, &computev1.NetworkInterface{
			SubnetId:         ni.SubnetID,
			PrimaryV4Address: ni.PrimaryV4Address,
			SecurityGroupIds: ni.SecurityGroupIDs,
		})
	}
	if inst.LastRestartCompletedAt != nil {
		p.LastRestartCompletedAt = timestamppb.New(*inst.LastRestartCompletedAt)
	}
	return p
}
