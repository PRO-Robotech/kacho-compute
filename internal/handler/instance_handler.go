// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package handler

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"

	"github.com/PRO-Robotech/kacho-compute/internal/authzfilter"
	"github.com/PRO-Robotech/kacho-compute/internal/protoconv"
	svc "github.com/PRO-Robotech/kacho-compute/internal/service"
)

// InstanceHandler реализует computev1.InstanceServiceServer (тонкий transport-слой).
//
// Unimplemented RPC (наследуются из UnimplementedInstanceServiceServer →
// codes.Unimplemented): AttachFilesystem/DetachFilesystem (blocked:kacho-filesystem),
// AttachNetworkInterface/DetachNetworkInterface/UpdateNetworkInterface/AddOneToOneNat/
// RemoveOneToOneNat (NIC binding removed from the Instance lifecycle — no auto-NIC:
// Instance создаётся без network interface, поэтому включать/выключать NAT не над чем),
// Relocate (blocked: cross-zone disk move), ListAccessBindings/SetAccessBindings/
// UpdateAccessBindings (AAA-скелет). См. docs/architecture/07-known-divergences.md.
type InstanceHandler struct {
	computev1.UnimplementedInstanceServiceServer
	svc        *svc.InstanceService
	listFilter authzfilter.Filter
}

// NewInstanceHandler создаёт InstanceHandler. listFilter может быть nil — тогда
// FGA-фильтрация на List отключена (dev/breakglass).
func NewInstanceHandler(s *svc.InstanceService, listFilter authzfilter.Filter) *InstanceHandler {
	return &InstanceHandler{svc: s, listFilter: listFilter}
}

// Get возвращает Instance по id.
func (h *InstanceHandler) Get(ctx context.Context, req *computev1.GetInstanceRequest) (*computev1.Instance, error) {
	if req.InstanceId == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	in, err := h.svc.Get(ctx, req.InstanceId)
	if err != nil {
		return nil, err
	}
	if err := AssertProjectOwnership(ctx, in.ProjectID); err != nil {
		return nil, err
	}
	p := protoconv.Instance(in)
	// GetInstanceRequest.view — metadata возвращается только при view=FULL.
	if req.View != computev1.InstanceView_FULL {
		p.Metadata = nil
	}
	return p, nil
}

// List возвращает список ВМ в folder.
//
// Вызов фильтруется через iam.AuthorizeService.ListObjects
// (caller subject → allowed instance_ids). admin / dev-bypass → no filtering.
// Empty grant → empty list (NOT 403 — конвенция Kachō для list-empty).
func (h *InstanceHandler) List(ctx context.Context, req *computev1.ListInstancesRequest) (*computev1.ListInstancesResponse, error) {
	if err := AssertProjectOwnership(ctx, req.ProjectId); err != nil {
		return nil, err
	}
	dec, err := resolveListFilter(ctx, h.listFilter, authzfilter.ResourceTypeInstance, authzfilter.ActionInstanceRead)
	if err != nil {
		return nil, err
	}
	filter := svc.InstanceFilter{ProjectID: req.ProjectId, Filter: req.Filter}
	if !dec.IsBypass() {
		if len(dec.IDs()) == 0 {
			return &computev1.ListInstancesResponse{}, nil
		}
		filter.AllowedIDs = dec.IDs()
	}
	ins, nextToken, err := h.svc.List(ctx, filter,
		svc.Pagination{PageToken: req.PageToken, PageSize: req.PageSize})
	if err != nil {
		return nil, err
	}
	resp := &computev1.ListInstancesResponse{NextPageToken: nextToken}
	for _, in := range ins {
		p := protoconv.Instance(in)
		// metadata всегда опускается в List response (в ListInstancesRequest
		// нет view-параметра — это документировано в instance.proto комментарии к Instance.metadata).
		p.Metadata = nil
		resp.Instances = append(resp.Instances, p)
	}
	return resp, nil
}

// Create инициирует создание Instance.
func (h *InstanceHandler) Create(ctx context.Context, req *computev1.CreateInstanceRequest) (*operationpb.Operation, error) {
	if err := AssertProjectOwnership(ctx, req.ProjectId); err != nil {
		return nil, err
	}
	op, err := h.svc.Create(ctx, CreateReqFromProto(req))
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// CreateReqFromProto — чистая proto→use-case конвертация CreateInstanceRequest в
// svc.CreateInstanceReq (без auth/transport). Тот же маппинг, что выполняет RPC
// Create; выделен, чтобы fuzz (internal/fuzz) прогонял ровно этот путь на
// hostile-входах. network_interface_specs игнорируются — Instance создаётся без
// auto-NIC (NIC-binding вынесен из lifecycle Instance).
func CreateReqFromProto(req *computev1.CreateInstanceRequest) svc.CreateInstanceReq {
	cr := svc.CreateInstanceReq{
		ProjectID:        req.ProjectId,
		Name:             req.Name,
		Description:      req.Description,
		Labels:           req.Labels,
		ZoneID:           req.ZoneId,
		PlatformID:       req.PlatformId,
		Metadata:         req.Metadata,
		MetadataOptions:  req.MetadataOptions,
		BootDisk:         diskSourceFromSpec(req.BootDiskSpec),
		Hostname:         req.Hostname,
		ServiceAccountID: req.ServiceAccountId,
		PlacementPolicy:  req.PlacementPolicy,
		Application:      req.Application,
	}
	if rs := req.ResourcesSpec; rs != nil {
		cr.Cores, cr.Memory, cr.CoreFraction, cr.GPUs = rs.Cores, rs.Memory, rs.CoreFraction, rs.Gpus
	}
	if sp := req.SchedulingPolicy; sp != nil {
		cr.Preemptible = sp.Preemptible
	}
	if ns := req.NetworkSettings; ns != nil {
		cr.NetworkSettingsType = ns.Type.String()
	}
	for _, sd := range req.SecondaryDiskSpecs {
		cr.SecondaryDisks = append(cr.SecondaryDisks, diskSourceFromSpec(sd))
	}
	return cr
}

// Update инициирует обновление Instance.
func (h *InstanceHandler) Update(ctx context.Context, req *computev1.UpdateInstanceRequest) (*operationpb.Operation, error) {
	if req.InstanceId == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	in, err := h.svc.Get(ctx, req.InstanceId)
	if err != nil {
		return nil, err
	}
	if err := AssertProjectOwnership(ctx, in.ProjectID); err != nil {
		return nil, err
	}
	var mask []string
	if req.UpdateMask != nil {
		mask = req.UpdateMask.Paths
	}
	ur := svc.UpdateInstanceReq{
		InstanceID:       req.InstanceId,
		Name:             req.Name,
		Description:      req.Description,
		Labels:           req.Labels,
		ServiceAccountID: req.ServiceAccountId,
		PlatformID:       req.PlatformId,
		PlacementPolicy:  req.PlacementPolicy,
		UpdateMask:       mask,
	}
	if rs := req.ResourcesSpec; rs != nil {
		ur.Cores, ur.Memory, ur.CoreFraction, ur.GPUs = rs.Cores, rs.Memory, rs.CoreFraction, rs.Gpus
	}
	if ns := req.NetworkSettings; ns != nil {
		ur.NetworkSettingsType = ns.Type.String()
	}
	op, err := h.svc.Update(ctx, ur)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// UpdateMetadata инициирует обновление metadata ВМ.
func (h *InstanceHandler) UpdateMetadata(ctx context.Context, req *computev1.UpdateInstanceMetadataRequest) (*operationpb.Operation, error) {
	if req.InstanceId == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	in, err := h.svc.Get(ctx, req.InstanceId)
	if err != nil {
		return nil, err
	}
	if err := AssertProjectOwnership(ctx, in.ProjectID); err != nil {
		return nil, err
	}
	op, err := h.svc.UpdateMetadata(ctx, req.InstanceId, req.Delete, req.Upsert)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// Start инициирует запуск ВМ.
func (h *InstanceHandler) Start(ctx context.Context, req *computev1.StartInstanceRequest) (*operationpb.Operation, error) {
	return h.lifecycle(ctx, req.InstanceId, h.svc.Start)
}

// Stop инициирует остановку ВМ.
func (h *InstanceHandler) Stop(ctx context.Context, req *computev1.StopInstanceRequest) (*operationpb.Operation, error) {
	return h.lifecycle(ctx, req.InstanceId, h.svc.Stop)
}

// Restart инициирует перезапуск ВМ.
func (h *InstanceHandler) Restart(ctx context.Context, req *computev1.RestartInstanceRequest) (*operationpb.Operation, error) {
	return h.lifecycle(ctx, req.InstanceId, h.svc.Restart)
}

func (h *InstanceHandler) lifecycle(ctx context.Context, id string, fn func(context.Context, string) (*operations.Operation, error)) (*operationpb.Operation, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	in, err := h.svc.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := AssertProjectOwnership(ctx, in.ProjectID); err != nil {
		return nil, err
	}
	op, err := fn(ctx, id)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// AttachDisk инициирует подключение диска к ВМ.
func (h *InstanceHandler) AttachDisk(ctx context.Context, req *computev1.AttachInstanceDiskRequest) (*operationpb.Operation, error) {
	if req.InstanceId == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	in, err := h.svc.Get(ctx, req.InstanceId)
	if err != nil {
		return nil, err
	}
	if err := AssertProjectOwnership(ctx, in.ProjectID); err != nil {
		return nil, err
	}
	op, err := h.svc.AttachDisk(ctx, req.InstanceId, diskSourceFromSpec(req.AttachedDiskSpec))
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// DetachDisk инициирует отвязку диска от ВМ.
func (h *InstanceHandler) DetachDisk(ctx context.Context, req *computev1.DetachInstanceDiskRequest) (*operationpb.Operation, error) {
	if req.InstanceId == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	in, err := h.svc.Get(ctx, req.InstanceId)
	if err != nil {
		return nil, err
	}
	if err := AssertProjectOwnership(ctx, in.ProjectID); err != nil {
		return nil, err
	}
	op, err := h.svc.DetachDisk(ctx, req.InstanceId, req.GetDiskId(), req.GetDeviceName())
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// SimulateMaintenanceEvent — no-op (control-plane).
func (h *InstanceHandler) SimulateMaintenanceEvent(ctx context.Context, req *computev1.SimulateInstanceMaintenanceEventRequest) (*operationpb.Operation, error) {
	if req.InstanceId == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	in, err := h.svc.Get(ctx, req.InstanceId)
	if err != nil {
		return nil, err
	}
	if err := AssertProjectOwnership(ctx, in.ProjectID); err != nil {
		return nil, err
	}
	op, err := h.svc.SimulateMaintenanceEvent(ctx, req.InstanceId)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// Delete инициирует удаление ВМ.
func (h *InstanceHandler) Delete(ctx context.Context, req *computev1.DeleteInstanceRequest) (*operationpb.Operation, error) {
	if req.InstanceId == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	in, err := h.svc.Get(ctx, req.InstanceId)
	if err != nil {
		return nil, err
	}
	if err := AssertProjectOwnership(ctx, in.ProjectID); err != nil {
		return nil, err
	}
	op, err := h.svc.Delete(ctx, req.InstanceId)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// GetSerialPortOutput — sync RPC: синтетический текст.
func (h *InstanceHandler) GetSerialPortOutput(ctx context.Context, req *computev1.GetInstanceSerialPortOutputRequest) (*computev1.GetInstanceSerialPortOutputResponse, error) {
	if req.InstanceId == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	in, err := h.svc.Get(ctx, req.InstanceId)
	if err != nil {
		return nil, err
	}
	if err := AssertProjectOwnership(ctx, in.ProjectID); err != nil {
		return nil, err
	}
	contents, err := h.svc.GetSerialPortOutput(ctx, req.InstanceId)
	if err != nil {
		return nil, err
	}
	return &computev1.GetInstanceSerialPortOutputResponse{Contents: contents}, nil
}

// ListOperations возвращает операции для ВМ.
func (h *InstanceHandler) ListOperations(ctx context.Context, req *computev1.ListInstanceOperationsRequest) (*computev1.ListInstanceOperationsResponse, error) {
	if req.InstanceId == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	in, err := h.svc.Get(ctx, req.InstanceId)
	if err != nil {
		return nil, err
	}
	if err := AssertProjectOwnership(ctx, in.ProjectID); err != nil {
		return nil, err
	}
	ops, nextToken, err := h.svc.ListOperations(ctx, req.InstanceId, svc.Pagination{PageToken: req.PageToken, PageSize: req.PageSize})
	if err != nil {
		return nil, err
	}
	resp := &computev1.ListInstanceOperationsResponse{NextPageToken: nextToken}
	for i := range ops {
		resp.Operations = append(resp.Operations, operationToProto(&ops[i]))
	}
	return resp, nil
}

// ---- conversion helpers ----

func diskSourceFromSpec(s *computev1.AttachedDiskSpec) svc.DiskSourceSpec {
	if s == nil {
		return svc.DiskSourceSpec{}
	}
	out := svc.DiskSourceSpec{
		DeviceName: s.GetDeviceName(),
		AutoDelete: s.GetAutoDelete(),
		Mode:       int32(s.GetMode()),
	}
	if s.GetDiskId() != "" {
		out.DiskID = s.GetDiskId()
		return out
	}
	if ds := s.GetDiskSpec(); ds != nil {
		out.NewDiskSizeBytes = ds.GetSize()
		out.NewDiskTypeID = ds.GetTypeId()
		out.NewSourceImage = ds.GetImageId()
		out.NewSourceSnap = ds.GetSnapshotId()
	}
	return out
}
