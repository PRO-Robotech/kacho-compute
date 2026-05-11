package service

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"

	"google.golang.org/protobuf/proto"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	corevalidate "github.com/PRO-Robotech/kacho-corelib/validate"
	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/protoconv"
)

// validCoreFractions — допустимые значения core_fraction (verbatim YC).
// Полная per-platform валидация (cores/memory/gpus per standard-v1/v2/v3, gpu-*)
// — TODO platforms.go; пока — basic sanity check (см. CLAUDE.md §5).
var validCoreFractions = map[int64]struct{}{0: {}, 5: {}, 20: {}, 50: {}, 100: {}}

// NICSpec — спека сетевого интерфейса для Create / AttachNetworkInterface.
type NICSpec struct {
	SubnetID         string
	Index            string
	PrimaryV4Address string   // manual internal IP ("" = auto)
	OneToOneNat      *NatSpec // nil = без NAT
	SecurityGroupIDs []string
}

// NatSpec — спека one-to-one NAT.
type NatSpec struct {
	Address   string // manual external IP ("" = auto-allocate синтетический)
	IPVersion int32
	AddressID string // VPC address ref (если задан — проверяется существование)
}

// DiskSourceSpec — оборачивает либо ссылку на существующий диск, либо параметры
// inline-создания нового диска (ровно одно поле непусто).
type DiskSourceSpec struct {
	DiskID         string
	NewDiskSizeGiB int64
	NewDiskTypeID  string
	NewSourceImage string
	NewSourceSnap  string
	DeviceName     string
	AutoDelete     bool
	Mode           int32 // computev1.AttachedDiskSpec_Mode
}

// CreateInstanceReq — запрос на создание ВМ.
type CreateInstanceReq struct {
	FolderID            string
	Name                string
	Description         string
	Labels              map[string]string
	ZoneID              string
	PlatformID          string
	Cores               int64
	Memory              int64
	CoreFraction        int64
	GPUs                int64
	Metadata            map[string]string
	MetadataOptions     *computev1.MetadataOptions
	BootDisk            DiskSourceSpec
	SecondaryDisks      []DiskSourceSpec
	NICs                []NICSpec
	Hostname            string
	Preemptible         bool
	ServiceAccountID    string
	NetworkSettingsType string
	PlacementPolicy     *computev1.PlacementPolicy
	HardwareGeneration  *computev1.HardwareGeneration
	Application         *computev1.Application
}

// UpdateInstanceReq — запрос на обновление ВМ.
type UpdateInstanceReq struct {
	InstanceID          string
	Name                string
	Description         string
	Labels              map[string]string
	ServiceAccountID    string
	Cores               int64
	Memory              int64
	CoreFraction        int64
	GPUs                int64
	PlatformID          string
	PlacementPolicy     *computev1.PlacementPolicy
	NetworkSettingsType string
	UpdateMask          []string
}

// InstanceService — бизнес-логика управления ВМ + state-машина (CLAUDE.md §8).
type InstanceService struct {
	repo         InstanceRepo
	diskRepo     DiskRepo
	imageRepo    ImageRepo
	snapshotRepo SnapshotRepo
	zoneRepo     ZoneRepo
	folderClient FolderClient
	vpcClient    VPCClient
	opsRepo      operations.Repo
}

// NewInstanceService создаёт InstanceService.
func NewInstanceService(repo InstanceRepo, diskRepo DiskRepo, imageRepo ImageRepo, snapshotRepo SnapshotRepo, zoneRepo ZoneRepo, folderClient FolderClient, vpcClient VPCClient, opsRepo operations.Repo) *InstanceService {
	return &InstanceService{
		repo: repo, diskRepo: diskRepo, imageRepo: imageRepo, snapshotRepo: snapshotRepo,
		zoneRepo: zoneRepo, folderClient: folderClient, vpcClient: vpcClient, opsRepo: opsRepo,
	}
}

// Get возвращает Instance по ID.
func (s *InstanceService) Get(ctx context.Context, id string) (*domain.Instance, error) {
	in, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return in, nil
}

// List возвращает список ВМ. folder_id обязателен.
func (s *InstanceService) List(ctx context.Context, f InstanceFilter, p Pagination) ([]*domain.Instance, string, error) {
	if f.FolderID == "" {
		return nil, "", status.Error(codes.InvalidArgument, "folder_id required")
	}
	return s.repo.List(ctx, f, p)
}

// Create инициирует создание Instance.
func (s *InstanceService) Create(ctx context.Context, req CreateInstanceReq) (*operations.Operation, error) {
	if req.FolderID == "" {
		return nil, status.Error(codes.InvalidArgument, "folder_id required")
	}
	if req.ZoneID == "" {
		return nil, status.Error(codes.InvalidArgument, "zone_id required")
	}
	if req.PlatformID == "" {
		return nil, status.Error(codes.InvalidArgument, "platform_id required")
	}
	if err := corevalidate.NameCompute("name", req.Name); err != nil {
		return nil, err
	}
	if err := corevalidate.Description("description", req.Description); err != nil {
		return nil, err
	}
	if err := corevalidate.Labels("labels", req.Labels); err != nil {
		return nil, err
	}
	if err := validateResources(req.Cores, req.Memory, req.CoreFraction); err != nil {
		return nil, err
	}
	if err := validateDiskSourceSpec("boot_disk_spec", req.BootDisk); err != nil {
		return nil, err
	}
	for i, sd := range req.SecondaryDisks {
		if err := validateDiskSourceSpec(fmt.Sprintf("secondary_disk_specs[%d]", i), sd); err != nil {
			return nil, err
		}
	}
	if len(req.NICs) == 0 {
		return nil, invalidArg("network_interface_specs", "at least one network_interface_spec is required")
	}
	for i, nic := range req.NICs {
		if nic.SubnetID == "" {
			return nil, invalidArg(fmt.Sprintf("network_interface_specs[%d].subnet_id", i), "subnet_id is required")
		}
	}

	instanceID := ids.NewID(ids.PrefixInstance)
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Create instance %s", req.Name),
		&computev1.CreateInstanceMetadata{InstanceId: instanceID})
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}
	operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		return s.doCreate(ctx, instanceID, req)
	})
	return &op, nil
}

func (s *InstanceService) doCreate(ctx context.Context, instanceID string, req CreateInstanceReq) (*anypb.Any, error) {
	if err := s.checkFolder(ctx, req.FolderID); err != nil {
		return nil, err
	}
	if _, err := s.zoneRepo.Get(ctx, req.ZoneID); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "Zone %s not found", req.ZoneID)
	}

	// NIC cross-service validation + materialization.
	nics := make([]domain.NetworkInterface, 0, len(req.NICs))
	for i, spec := range req.NICs {
		zone, found, err := s.vpcClient.GetSubnet(ctx, spec.SubnetID)
		if err != nil {
			return nil, status.Errorf(codes.Unavailable, "subnet check: %v", err)
		}
		if !found {
			return nil, status.Errorf(codes.NotFound, "Subnet %s not found", spec.SubnetID)
		}
		if zone != "" && zone != req.ZoneID {
			return nil, status.Errorf(codes.InvalidArgument, "Subnet %s is in zone %s, instance zone is %s", spec.SubnetID, zone, req.ZoneID)
		}
		for _, sg := range spec.SecurityGroupIDs {
			ok, err := s.vpcClient.SecurityGroupExists(ctx, sg)
			if err != nil {
				return nil, status.Errorf(codes.Unavailable, "security group check: %v", err)
			}
			if !ok {
				return nil, status.Errorf(codes.NotFound, "Security group %s not found", sg)
			}
		}
		if spec.OneToOneNat != nil && spec.OneToOneNat.AddressID != "" {
			ok, err := s.vpcClient.AddressExists(ctx, spec.OneToOneNat.AddressID)
			if err != nil {
				return nil, status.Errorf(codes.Unavailable, "address check: %v", err)
			}
			if !ok {
				return nil, status.Errorf(codes.NotFound, "Address %s not found", spec.OneToOneNat.AddressID)
			}
		}
		idx := spec.Index
		if idx == "" {
			idx = fmt.Sprintf("%d", i)
		}
		nic := domain.NetworkInterface{
			Index:            idx,
			SubnetID:         spec.SubnetID,
			PrimaryV4Address: spec.PrimaryV4Address,
			SecurityGroupIDs: spec.SecurityGroupIDs,
		}
		if nic.PrimaryV4Address == "" {
			nic.PrimaryV4Address = synthInternalIP(i)
		}
		if spec.OneToOneNat != nil {
			nic.PrimaryV4Nat = &domain.OneToOneNat{Address: synthExternalIP(i), IPVersion: spec.OneToOneNat.IPVersion}
			if spec.OneToOneNat.Address != "" {
				nic.PrimaryV4Nat.Address = spec.OneToOneNat.Address
			}
		}
		nics = append(nics, nic)
	}

	// Boot disk + secondary disks: resolve existing OR materialize inline.
	var inlineDisks []*domain.Disk
	bootAD, bootInline, err := s.resolveDiskSource(ctx, req.FolderID, req.ZoneID, req.BootDisk, true)
	if err != nil {
		return nil, err
	}
	if bootInline != nil {
		inlineDisks = append(inlineDisks, bootInline)
	}
	attached := []domain.AttachedDisk{bootAD}
	for _, sd := range req.SecondaryDisks {
		ad, inline, err := s.resolveDiskSource(ctx, req.FolderID, req.ZoneID, sd, false)
		if err != nil {
			return nil, err
		}
		if inline != nil {
			inlineDisks = append(inlineDisks, inline)
		}
		attached = append(attached, ad)
	}

	in := &domain.Instance{
		ID:                    instanceID,
		FolderID:              req.FolderID,
		CreatedAt:             time.Now().UTC(),
		Name:                  req.Name,
		Description:           req.Description,
		Labels:                req.Labels,
		ZoneID:                req.ZoneID,
		PlatformID:            req.PlatformID,
		Cores:                 req.Cores,
		Memory:                req.Memory,
		CoreFraction:          defaultCoreFraction(req.CoreFraction),
		GPUs:                  req.GPUs,
		Status:                domain.InstanceStatusRunning, // control-plane: PROVISIONING→RUNNING instantly
		Metadata:              req.Metadata,
		MetadataOptions:       req.MetadataOptions,
		ServiceAccountID:      req.ServiceAccountID,
		Hostname:              req.Hostname,
		FQDN:                  fqdn(instanceID, req.Hostname),
		NetworkSettingsType:   orDefault(req.NetworkSettingsType, "STANDARD"),
		SchedulingPreemptible: req.Preemptible,
		PlacementPolicy:       req.PlacementPolicy,
		HardwareGeneration:    req.HardwareGeneration,
		Application:           req.Application,
		NetworkInterfaces:     nics,
		AttachedDisks:         attached,
	}
	created, err := s.repo.Insert(ctx, in, inlineDisks)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return anypb.New(protoconv.Instance(created))
}

// resolveDiskSource резолвит DiskSourceSpec в AttachedDisk + (опционально) новый
// диск для inline-вставки. Для существующего диска проверяет READY + zone + not-attached.
func (s *InstanceService) resolveDiskSource(ctx context.Context, folderID, zoneID string, spec DiskSourceSpec, isBoot bool) (domain.AttachedDisk, *domain.Disk, error) {
	if spec.DiskID != "" {
		d, err := s.diskRepo.Get(ctx, spec.DiskID)
		if err != nil {
			return domain.AttachedDisk{}, nil, status.Errorf(codes.NotFound, "Disk %s not found", spec.DiskID)
		}
		if d.Status != domain.DiskStatusReady {
			return domain.AttachedDisk{}, nil, status.Errorf(codes.FailedPrecondition, "Disk %s is not READY", spec.DiskID)
		}
		if d.ZoneID != zoneID {
			return domain.AttachedDisk{}, nil, status.Errorf(codes.InvalidArgument, "Disk %s is in zone %s, instance zone is %s", spec.DiskID, d.ZoneID, zoneID)
		}
		attached, err := s.diskRepo.IsAttached(ctx, spec.DiskID)
		if err != nil {
			return domain.AttachedDisk{}, nil, mapRepoErr(err)
		}
		if attached {
			return domain.AttachedDisk{}, nil, status.Errorf(codes.FailedPrecondition, "Disk %s is already attached", spec.DiskID)
		}
		return domain.AttachedDisk{
			DiskID: spec.DiskID, IsBoot: isBoot, Mode: domain.AttachedDiskMode(spec.Mode),
			DeviceName: spec.DeviceName, AutoDelete: spec.AutoDelete, AttachedAt: time.Now().UTC(),
		}, nil, nil
	}
	// inline disk_spec → create a new READY disk in the same TX.
	newDiskID := ids.NewID(ids.PrefixDisk)
	size := spec.NewDiskSizeGiB
	if size == 0 {
		size = diskSizeMin
	}
	if spec.NewSourceImage != "" {
		if _, err := s.imageRepo.Get(ctx, spec.NewSourceImage); err != nil {
			return domain.AttachedDisk{}, nil, status.Errorf(codes.NotFound, "Image %s not found", spec.NewSourceImage)
		}
	}
	if spec.NewSourceSnap != "" {
		if _, err := s.snapshotRepo.Get(ctx, spec.NewSourceSnap); err != nil {
			return domain.AttachedDisk{}, nil, status.Errorf(codes.NotFound, "Snapshot %s not found", spec.NewSourceSnap)
		}
	}
	d := &domain.Disk{
		ID:               newDiskID,
		FolderID:         folderID,
		CreatedAt:        time.Now().UTC(),
		TypeID:           orDefault(spec.NewDiskTypeID, defaultDiskType),
		ZoneID:           zoneID,
		Size:             size,
		BlockSize:        defaultBlockSize,
		Status:           domain.DiskStatusReady,
		SourceImageID:    spec.NewSourceImage,
		SourceSnapshotID: spec.NewSourceSnap,
	}
	return domain.AttachedDisk{
		DiskID: newDiskID, IsBoot: isBoot, Mode: domain.AttachedDiskMode(spec.Mode),
		DeviceName: spec.DeviceName, AutoDelete: true, AttachedAt: time.Now().UTC(),
	}, d, nil
}

// Update обновляет ВМ. mask ⊇ {resources_spec/platform_id} требует STOPPED.
func (s *InstanceService) Update(ctx context.Context, req UpdateInstanceReq) (*operations.Operation, error) {
	if req.InstanceID == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	if err := validateInstanceUpdate(req); err != nil {
		return nil, err
	}
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Update instance %s", req.InstanceID),
		&computev1.UpdateInstanceMetadata{InstanceId: req.InstanceID})
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}
	operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		in, err := s.repo.Get(ctx, req.InstanceID)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		updates := req.UpdateMask
		full := len(updates) == 0
		if full {
			updates = []string{"name", "description", "labels", "service_account_id", "placement_policy", "network_settings"}
		}
		touchesCompute := false
		for _, f := range updates {
			switch f {
			case "name":
				in.Name = req.Name
			case "description":
				in.Description = req.Description
			case "labels":
				in.Labels = req.Labels
			case "service_account_id":
				in.ServiceAccountID = req.ServiceAccountID
			case "placement_policy":
				in.PlacementPolicy = req.PlacementPolicy
			case "network_settings":
				if req.NetworkSettingsType != "" {
					in.NetworkSettingsType = req.NetworkSettingsType
				}
			case "resources_spec":
				if !full {
					touchesCompute = true
					if err := validateResources(req.Cores, req.Memory, req.CoreFraction); err != nil {
						return nil, err
					}
					in.Cores, in.Memory, in.CoreFraction, in.GPUs = req.Cores, req.Memory, defaultCoreFraction(req.CoreFraction), req.GPUs
				}
			case "platform_id":
				if !full && req.PlatformID != "" {
					touchesCompute = true
					in.PlatformID = req.PlatformID
				}
			}
		}
		if touchesCompute && in.Status != domain.InstanceStatusStopped {
			return nil, status.Error(codes.FailedPrecondition, "Instance must be stopped")
		}
		updated, err := s.repo.Update(ctx, in)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		return anypb.New(protoconv.Instance(updated))
	})
	return &op, nil
}

func validateInstanceUpdate(req UpdateInstanceReq) error {
	known := map[string]struct{}{
		"name": {}, "description": {}, "labels": {}, "service_account_id": {},
		"placement_policy": {}, "network_settings": {}, "scheduling_policy": {},
		"resources_spec": {}, "platform_id": {}, "metadata_options": {},
	}
	if err := corevalidate.UpdateMask("update_mask", req.UpdateMask, known); err != nil {
		return err
	}
	for _, f := range req.UpdateMask {
		switch f {
		case "zone_id", "boot_disk", "metadata":
			return invalidArg(f, f+" is immutable after Instance.Create (use AttachDisk/UpdateMetadata/Relocate)")
		case "name":
			if err := corevalidate.NameCompute("name", req.Name); err != nil {
				return err
			}
		case "description":
			if err := corevalidate.Description("description", req.Description); err != nil {
				return err
			}
		case "labels":
			if err := corevalidate.Labels("labels", req.Labels); err != nil {
				return err
			}
		}
	}
	return nil
}

// UpdateMetadata обновляет map metadata (delete + upsert).
func (s *InstanceService) UpdateMetadata(ctx context.Context, instanceID string, del []string, upsert map[string]string) (*operations.Operation, error) {
	if instanceID == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Update instance %s metadata", instanceID),
		&computev1.UpdateInstanceMetadataMetadata{InstanceId: instanceID})
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}
	operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		in, err := s.repo.Get(ctx, instanceID)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		md := map[string]string{}
		for k, v := range in.Metadata {
			md[k] = v
		}
		for _, k := range del {
			delete(md, k)
		}
		for k, v := range upsert {
			md[k] = v
		}
		updated, err := s.repo.SetMetadata(ctx, instanceID, md)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		return anypb.New(protoconv.Instance(updated))
	})
	return &op, nil
}

// Start/Stop/Restart — state-машина (см. CLAUDE.md §8).
func (s *InstanceService) Start(ctx context.Context, id string) (*operations.Operation, error) {
	return s.lifecycle(ctx, id, "Start", domain.InstanceStatusStopped, domain.InstanceStatusRunning,
		"Instance is not stopped", &computev1.StartInstanceMetadata{InstanceId: id})
}

// Stop переводит ВМ RUNNING→STOPPED.
func (s *InstanceService) Stop(ctx context.Context, id string) (*operations.Operation, error) {
	return s.lifecycle(ctx, id, "Stop", domain.InstanceStatusRunning, domain.InstanceStatusStopped,
		"Instance is not running", &computev1.StopInstanceMetadata{InstanceId: id})
}

// Restart перезапускает RUNNING ВМ (status остаётся RUNNING).
func (s *InstanceService) Restart(ctx context.Context, id string) (*operations.Operation, error) {
	return s.lifecycle(ctx, id, "Restart", domain.InstanceStatusRunning, domain.InstanceStatusRunning,
		"Instance is not running", &computev1.RestartInstanceMetadata{InstanceId: id})
}

func (s *InstanceService) lifecycle(ctx context.Context, id, action string, from, to domain.InstanceStatus, precondMsg string, meta protoreflectMessage) (*operations.Operation, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("%s instance %s", action, id), meta)
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}
	operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		in, err := s.repo.Get(ctx, id)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		if in.Status != from {
			return nil, status.Error(codes.FailedPrecondition, precondMsg)
		}
		updated, err := s.repo.SetStatus(ctx, id, to)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		return anypb.New(protoconv.Instance(updated))
	})
	return &op, nil
}

// AttachDisk подключает READY-диск к ВМ (status ∈ {RUNNING, STOPPED}).
func (s *InstanceService) AttachDisk(ctx context.Context, id string, spec DiskSourceSpec) (*operations.Operation, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	if spec.DiskID == "" {
		return nil, invalidArg("attached_disk_spec.disk_id", "disk_id is required (inline disk_spec not supported on AttachDisk)")
	}
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Attach disk to instance %s", id),
		&computev1.AttachInstanceDiskMetadata{InstanceId: id, DiskId: spec.DiskID})
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}
	operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		in, err := s.repo.Get(ctx, id)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		if in.Status != domain.InstanceStatusRunning && in.Status != domain.InstanceStatusStopped {
			return nil, status.Error(codes.FailedPrecondition, "Instance is not running or stopped")
		}
		for _, ad := range in.AttachedDisks {
			if ad.DiskID == spec.DiskID {
				return nil, status.Errorf(codes.FailedPrecondition, "Disk %s is already attached", spec.DiskID)
			}
			if spec.DeviceName != "" && ad.DeviceName == spec.DeviceName {
				return nil, status.Errorf(codes.FailedPrecondition, "device_name %s is already in use", spec.DeviceName)
			}
		}
		d, err := s.diskRepo.Get(ctx, spec.DiskID)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "Disk %s not found", spec.DiskID)
		}
		if d.Status != domain.DiskStatusReady {
			return nil, status.Errorf(codes.FailedPrecondition, "Disk %s is not READY", spec.DiskID)
		}
		if d.ZoneID != in.ZoneID {
			return nil, status.Errorf(codes.InvalidArgument, "Disk %s is in zone %s, instance zone is %s", spec.DiskID, d.ZoneID, in.ZoneID)
		}
		attached, err := s.diskRepo.IsAttached(ctx, spec.DiskID)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		if attached {
			return nil, status.Errorf(codes.FailedPrecondition, "Disk %s is already attached", spec.DiskID)
		}
		updated, err := s.repo.AttachDisk(ctx, id, domain.AttachedDisk{
			DiskID: spec.DiskID, IsBoot: false, Mode: domain.AttachedDiskMode(spec.Mode),
			DeviceName: spec.DeviceName, AutoDelete: spec.AutoDelete, AttachedAt: time.Now().UTC(),
		})
		if err != nil {
			return nil, mapRepoErr(err)
		}
		return anypb.New(protoconv.Instance(updated))
	})
	return &op, nil
}

// DetachDisk отвязывает диск (по disk_id или device_name; не boot).
func (s *InstanceService) DetachDisk(ctx context.Context, id, diskID, deviceName string) (*operations.Operation, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	if diskID == "" && deviceName == "" {
		return nil, invalidArg("disk", "one of disk_id or device_name is required")
	}
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Detach disk from instance %s", id),
		&computev1.DetachInstanceDiskMetadata{InstanceId: id, DiskId: diskID})
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}
	operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		in, err := s.repo.Get(ctx, id)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		var target *domain.AttachedDisk
		for i := range in.AttachedDisks {
			ad := &in.AttachedDisks[i]
			if (diskID != "" && ad.DiskID == diskID) || (deviceName != "" && ad.DeviceName == deviceName) {
				target = ad
				break
			}
		}
		if target == nil {
			return nil, status.Error(codes.FailedPrecondition, "Disk is not attached to the instance")
		}
		if target.IsBoot {
			return nil, status.Error(codes.FailedPrecondition, "Cannot detach boot disk")
		}
		updated, err := s.repo.DetachDisk(ctx, id, target.DiskID)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		return anypb.New(protoconv.Instance(updated))
	})
	return &op, nil
}

// AddOneToOneNat включает NAT на NIC (status ∈ {RUNNING, STOPPED}).
func (s *InstanceService) AddOneToOneNat(ctx context.Context, id, nicIndex string, spec *NatSpec) (*operations.Operation, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Add one-to-one NAT to instance %s", id),
		&computev1.AddInstanceOneToOneNatMetadata{InstanceId: id})
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}
	operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		in, err := s.repo.Get(ctx, id)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		if in.Status != domain.InstanceStatusRunning && in.Status != domain.InstanceStatusStopped {
			return nil, status.Error(codes.FailedPrecondition, "Instance is not running or stopped")
		}
		nic := findNIC(in, nicIndex)
		if nic == nil {
			return nil, status.Errorf(codes.InvalidArgument, "network interface %q not found", nicIndex)
		}
		if nic.PrimaryV4Nat != nil {
			return nil, status.Error(codes.FailedPrecondition, "One-to-one NAT is already enabled on the network interface")
		}
		copyNIC := *nic
		copyNIC.PrimaryV4Nat = &domain.OneToOneNat{Address: synthExternalIP(0)}
		if spec != nil && spec.Address != "" {
			copyNIC.PrimaryV4Nat.Address = spec.Address
		}
		updated, err := s.repo.ReplaceNIC(ctx, id, copyNIC)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		return anypb.New(protoconv.Instance(updated))
	})
	return &op, nil
}

// RemoveOneToOneNat выключает NAT на NIC.
func (s *InstanceService) RemoveOneToOneNat(ctx context.Context, id, nicIndex string) (*operations.Operation, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Remove one-to-one NAT from instance %s", id),
		&computev1.RemoveInstanceOneToOneNatMetadata{InstanceId: id})
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}
	operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		in, err := s.repo.Get(ctx, id)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		nic := findNIC(in, nicIndex)
		if nic == nil {
			return nil, status.Errorf(codes.InvalidArgument, "network interface %q not found", nicIndex)
		}
		if nic.PrimaryV4Nat == nil {
			return nil, status.Error(codes.FailedPrecondition, "One-to-one NAT is not enabled on the network interface")
		}
		copyNIC := *nic
		copyNIC.PrimaryV4Nat = nil
		updated, err := s.repo.ReplaceNIC(ctx, id, copyNIC)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		return anypb.New(protoconv.Instance(updated))
	})
	return &op, nil
}

// Move инициирует перенос ВМ в другой folder.
func (s *InstanceService) Move(ctx context.Context, id, destFolderID string) (*operations.Operation, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	if destFolderID == "" {
		return nil, invalidArg("destination_folder_id", "destination_folder_id is required")
	}
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Move instance %s", id),
		&computev1.MoveInstanceMetadata{InstanceId: id, DestinationFolderId: destFolderID})
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}
	operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		if err := s.checkFolder(ctx, destFolderID); err != nil {
			return nil, err
		}
		updated, err := s.repo.SetFolderID(ctx, id, destFolderID)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		return anypb.New(protoconv.Instance(updated))
	})
	return &op, nil
}

// SimulateMaintenanceEvent — no-op (control-plane: возвращает done-операцию с самой ВМ).
func (s *InstanceService) SimulateMaintenanceEvent(ctx context.Context, id string) (*operations.Operation, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Simulate maintenance event for instance %s", id),
		&computev1.SimulateInstanceMaintenanceEventMetadata{InstanceId: id})
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}
	operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		in, err := s.repo.Get(ctx, id)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		return anypb.New(protoconv.Instance(in))
	})
	return &op, nil
}

// Delete инициирует удаление ВМ (auto-delete диски удаляются, остальные detach'атся CASCADE).
func (s *InstanceService) Delete(ctx context.Context, id string) (*operations.Operation, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Delete instance %s", id),
		&computev1.DeleteInstanceMetadata{InstanceId: id})
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}
	operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		in, err := s.repo.Get(ctx, id)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		var autoDelete []string
		for _, ad := range in.AttachedDisks {
			if ad.AutoDelete {
				autoDelete = append(autoDelete, ad.DiskID)
			}
		}
		if err := s.repo.Delete(ctx, id, autoDelete); err != nil {
			return nil, mapRepoErr(err)
		}
		return anypb.New(&emptypb.Empty{})
	})
	return &op, nil
}

// GetSerialPortOutput — sync RPC: синтетический текст (control-plane).
func (s *InstanceService) GetSerialPortOutput(ctx context.Context, id string) (string, error) {
	in, err := s.repo.Get(ctx, id)
	if err != nil {
		return "", mapRepoErr(err)
	}
	return fmt.Sprintf("[control-plane] serial port output for instance %s (status=%s) is not available — no real hypervisor.\n", in.ID, instanceStatusName(in.Status)), nil
}

// ListOperations возвращает операции для конкретной ВМ.
func (s *InstanceService) ListOperations(ctx context.Context, id string, p Pagination) ([]operations.Operation, string, error) {
	if _, err := s.repo.Get(ctx, id); err != nil {
		return nil, "", mapRepoErr(err)
	}
	return s.opsRepo.List(ctx, operations.ListFilter{ResourceID: id, PageSize: p.PageSize, PageToken: p.PageToken})
}

func (s *InstanceService) checkFolder(ctx context.Context, folderID string) error {
	exists, err := s.folderClient.Exists(ctx, folderID)
	if err != nil {
		return status.Errorf(codes.Unavailable, "folder check: %v", err)
	}
	if !exists {
		return status.Errorf(codes.NotFound, "Folder with id %s not found", folderID)
	}
	return nil
}

// ---- helpers ----

// protoreflectMessage — alias для proto.Message (operations.New принимает его).
type protoreflectMessage = proto.Message

func validateResources(cores, memory, coreFraction int64) error {
	if cores <= 0 {
		return invalidArg("resources_spec.cores", "cores must be > 0")
	}
	if memory <= 0 {
		return invalidArg("resources_spec.memory", "memory must be > 0 bytes")
	}
	if coreFraction != 0 {
		if _, ok := validCoreFractions[coreFraction]; !ok {
			return invalidArg("resources_spec.core_fraction", "core_fraction must be one of 0, 5, 20, 50, 100")
		}
	}
	return nil
}

func validateDiskSourceSpec(field string, spec DiskSourceSpec) error {
	hasRef := spec.DiskID != ""
	hasInline := spec.NewDiskSizeGiB != 0 || spec.NewDiskTypeID != "" || spec.NewSourceImage != "" || spec.NewSourceSnap != ""
	if hasRef == hasInline {
		return invalidArg(field, "exactly one of disk_id or disk_spec must be set")
	}
	return nil
}

func defaultCoreFraction(cf int64) int64 {
	if cf == 0 {
		return 100
	}
	return cf
}

func findNIC(in *domain.Instance, index string) *domain.NetworkInterface {
	if index == "" && len(in.NetworkInterfaces) == 1 {
		return &in.NetworkInterfaces[0]
	}
	for i := range in.NetworkInterfaces {
		if in.NetworkInterfaces[i].Index == index {
			return &in.NetworkInterfaces[i]
		}
	}
	return nil
}

func fqdn(id, hostname string) string {
	if hostname != "" {
		return hostname + ".ru-central1.internal"
	}
	return id + ".auto.internal"
}

func synthInternalIP(i int) string { return fmt.Sprintf("10.0.0.%d", 10+i) }
func synthExternalIP(i int) string { return fmt.Sprintf("203.0.113.%d", 10+i) }
func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func instanceStatusName(s domain.InstanceStatus) string {
	if v, ok := computev1.Instance_Status_name[int32(s)]; ok {
		return v
	}
	return "STATUS_UNSPECIFIED"
}
