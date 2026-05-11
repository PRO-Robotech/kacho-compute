package handler

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"

	"github.com/PRO-Robotech/kacho-compute/internal/protoconv"
	svc "github.com/PRO-Robotech/kacho-compute/internal/service"
)

// InstanceHandler реализует computev1.InstanceServiceServer (тонкий transport-слой).
//
// Unimplemented RPC (наследуются из UnimplementedInstanceServiceServer →
// codes.Unimplemented): AttachFilesystem/DetachFilesystem (blocked:kacho-filesystem),
// AttachNetworkInterface/DetachNetworkInterface, UpdateNetworkInterface, Relocate
// (blocked: cross-zone disk move), ListAccessBindings/SetAccessBindings/UpdateAccessBindings
// (AAA-скелет). См. docs/architecture/07-known-divergences.md.
type InstanceHandler struct {
	computev1.UnimplementedInstanceServiceServer
	svc *svc.InstanceService
}

// NewInstanceHandler создаёт InstanceHandler.
func NewInstanceHandler(s *svc.InstanceService) *InstanceHandler { return &InstanceHandler{svc: s} }

// Get возвращает Instance по id.
func (h *InstanceHandler) Get(ctx context.Context, req *computev1.GetInstanceRequest) (*computev1.Instance, error) {
	if req.InstanceId == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	in, err := h.svc.Get(ctx, req.InstanceId)
	if err != nil {
		return nil, err
	}
	if err := AssertFolderOwnership(ctx, in.FolderID); err != nil {
		return nil, err
	}
	return protoconv.Instance(in), nil
}

// List возвращает список ВМ в folder.
func (h *InstanceHandler) List(ctx context.Context, req *computev1.ListInstancesRequest) (*computev1.ListInstancesResponse, error) {
	if err := AssertFolderOwnership(ctx, req.FolderId); err != nil {
		return nil, err
	}
	ins, nextToken, err := h.svc.List(ctx, svc.InstanceFilter{FolderID: req.FolderId, Filter: req.Filter},
		svc.Pagination{PageToken: req.PageToken, PageSize: req.PageSize})
	if err != nil {
		return nil, err
	}
	resp := &computev1.ListInstancesResponse{NextPageToken: nextToken}
	for _, in := range ins {
		resp.Instances = append(resp.Instances, protoconv.Instance(in))
	}
	return resp, nil
}

// Create инициирует создание Instance.
func (h *InstanceHandler) Create(ctx context.Context, req *computev1.CreateInstanceRequest) (*operationpb.Operation, error) {
	if err := AssertFolderOwnership(ctx, req.FolderId); err != nil {
		return nil, err
	}
	cr := svc.CreateInstanceReq{
		FolderID:         req.FolderId,
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
	for _, nic := range req.NetworkInterfaceSpecs {
		cr.NICs = append(cr.NICs, nicSpecFromProto(nic))
	}
	op, err := h.svc.Create(ctx, cr)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
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
	if err := AssertFolderOwnership(ctx, in.FolderID); err != nil {
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
	if err := AssertFolderOwnership(ctx, in.FolderID); err != nil {
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
	if err := AssertFolderOwnership(ctx, in.FolderID); err != nil {
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
	if err := AssertFolderOwnership(ctx, in.FolderID); err != nil {
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
	if err := AssertFolderOwnership(ctx, in.FolderID); err != nil {
		return nil, err
	}
	op, err := h.svc.DetachDisk(ctx, req.InstanceId, req.GetDiskId(), req.GetDeviceName())
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// AddOneToOneNat инициирует включение NAT на NIC.
func (h *InstanceHandler) AddOneToOneNat(ctx context.Context, req *computev1.AddInstanceOneToOneNatRequest) (*operationpb.Operation, error) {
	if req.InstanceId == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	in, err := h.svc.Get(ctx, req.InstanceId)
	if err != nil {
		return nil, err
	}
	if err := AssertFolderOwnership(ctx, in.FolderID); err != nil {
		return nil, err
	}
	var natSpec *svc.NatSpec
	if req.OneToOneNatSpec != nil {
		natSpec = &svc.NatSpec{Address: req.OneToOneNatSpec.Address, IPVersion: int32(req.OneToOneNatSpec.IpVersion)}
	}
	op, err := h.svc.AddOneToOneNat(ctx, req.InstanceId, req.NetworkInterfaceIndex, natSpec)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// RemoveOneToOneNat инициирует выключение NAT на NIC.
func (h *InstanceHandler) RemoveOneToOneNat(ctx context.Context, req *computev1.RemoveInstanceOneToOneNatRequest) (*operationpb.Operation, error) {
	if req.InstanceId == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	in, err := h.svc.Get(ctx, req.InstanceId)
	if err != nil {
		return nil, err
	}
	if err := AssertFolderOwnership(ctx, in.FolderID); err != nil {
		return nil, err
	}
	op, err := h.svc.RemoveOneToOneNat(ctx, req.InstanceId, req.NetworkInterfaceIndex)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// Move инициирует перенос ВМ в другой folder.
func (h *InstanceHandler) Move(ctx context.Context, req *computev1.MoveInstanceRequest) (*operationpb.Operation, error) {
	if req.InstanceId == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	in, err := h.svc.Get(ctx, req.InstanceId)
	if err != nil {
		return nil, err
	}
	if err := AssertFolderOwnership(ctx, in.FolderID); err != nil {
		return nil, err
	}
	if err := AssertFolderOwnership(ctx, req.DestinationFolderId); err != nil {
		return nil, err
	}
	op, err := h.svc.Move(ctx, req.InstanceId, req.DestinationFolderId)
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
	if err := AssertFolderOwnership(ctx, in.FolderID); err != nil {
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
	if err := AssertFolderOwnership(ctx, in.FolderID); err != nil {
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
	if err := AssertFolderOwnership(ctx, in.FolderID); err != nil {
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
	if err := AssertFolderOwnership(ctx, in.FolderID); err != nil {
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
		out.NewDiskSizeGiB = ds.GetSize()
		out.NewDiskTypeID = ds.GetTypeId()
		out.NewSourceImage = ds.GetImageId()
		out.NewSourceSnap = ds.GetSnapshotId()
	}
	return out
}

func nicSpecFromProto(n *computev1.NetworkInterfaceSpec) svc.NICSpec {
	out := svc.NICSpec{
		SubnetID:         n.GetSubnetId(),
		Index:            n.GetIndex(),
		SecurityGroupIDs: n.GetSecurityGroupIds(),
	}
	if v4 := n.GetPrimaryV4AddressSpec(); v4 != nil {
		out.PrimaryV4Address = v4.GetAddress()
		if nat := v4.GetOneToOneNatSpec(); nat != nil {
			out.OneToOneNat = &svc.NatSpec{Address: nat.GetAddress(), IPVersion: int32(nat.GetIpVersion())}
		}
	}
	return out
}
