package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	ProjectID           string
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
	// zones — existence-check zone_id. Авторитетный источник — kacho-geo
	// (geo.v1.ZoneService.Get; Geography принадлежит kacho-geo, Stage S7); при
	// SKIP_PEER_VALIDATION — no-op. Wiring — cmd/compute/main.go.
	zones         ZoneRegistry
	projectClient ProjectClient
	vpcClient     VPCClient
	opsRepo       operations.Repo
	// skipIPAM — true при KACHO_COMPUTE_SKIP_PEER_VALIDATION: cross-service
	// VPC IPAM-аллокация отключена, NIC-ам выдаются синтетические IP (synth*).
	skipIPAM bool
}

// NewInstanceService создаёт InstanceService. skipIPAM=true (зеркалит
// KACHO_COMPUTE_SKIP_PEER_VALIDATION) → NIC-ам выдаются синтетические IP вместо
// реальных, выделенных через kacho-vpc IPAM (для unit/newman/load без VPC).
func NewInstanceService(repo InstanceRepo, diskRepo DiskRepo, imageRepo ImageRepo, snapshotRepo SnapshotRepo, zones ZoneRegistry, projectClient ProjectClient, vpcClient VPCClient, opsRepo operations.Repo, skipIPAM bool) *InstanceService {
	return &InstanceService{
		repo: repo, diskRepo: diskRepo, imageRepo: imageRepo, snapshotRepo: snapshotRepo,
		zones: zones, projectClient: projectClient, vpcClient: vpcClient, opsRepo: opsRepo,
		skipIPAM: skipIPAM,
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

// List возвращает список ВМ. project_id обязателен.
func (s *InstanceService) List(ctx context.Context, f InstanceFilter, p Pagination) ([]*domain.Instance, string, error) {
	if f.ProjectID == "" {
		return nil, "", status.Error(codes.InvalidArgument, "project_id required")
	}
	return s.repo.List(ctx, f, p)
}

// Create инициирует создание Instance.
func (s *InstanceService) Create(ctx context.Context, req CreateInstanceReq) (*operations.Operation, error) {
	if req.ProjectID == "" {
		return nil, status.Error(codes.InvalidArgument, "project_id required")
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
	if err := s.checkFolder(ctx, req.ProjectID); err != nil {
		return nil, err
	}
	if _, err := s.zones.GetZone(ctx, req.ZoneID); err != nil {
		return nil, mapZoneRefErr(err, req.ZoneID)
	}

	// KAC-266: Instance is created WITHOUT any network interface. NIC binding has
	// been removed from the Instance lifecycle entirely (no auto-NIC) — no
	// kacho-vpc Address/NetworkInterface resources are created or attached at
	// Create time. NICs can be managed independently through kacho-vpc.

	// Boot disk + secondary disks: resolve existing OR materialize inline.
	var inlineDisks []*domain.Disk
	bootAD, bootInline, err := s.resolveDiskSource(ctx, req.ProjectID, req.ZoneID, req.BootDisk, true)
	if err != nil {
		return nil, err
	}
	if bootInline != nil {
		inlineDisks = append(inlineDisks, bootInline)
	}
	attached := []domain.AttachedDisk{bootAD}
	for _, sd := range req.SecondaryDisks {
		ad, inline, err := s.resolveDiskSource(ctx, req.ProjectID, req.ZoneID, sd, false)
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
		ProjectID:             req.ProjectID,
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
		AttachedDisks:         attached,
	}
	created, err := s.repo.Insert(ctx, in, inlineDisks)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	// SEC-D: the compute_instance→project owner-tuple (and any inline boot/secondary
	// disk tuples) is registered transactionally — repo.Insert writes the FGA
	// register-intent in the SAME writer-tx as the rows (compute_fga_register_outbox)
	// and the register-drainer applies it via kacho-iam InternalIAMService
	// .RegisterResource. No direct FGA write, no best-effort post-commit dual-write
	// (closes the lost-tuple bug N5: the owner-tuple is now durable + retried, so the
	// per-resource Check "no path" DENY window is finite, not permanent).
	return anypb.New(protoconv.Instance(created))
}

// markNatAddress помечает one-to-one-NAT Address: эфемерный (compute создал) →
// MarkAddressEphemeralInUse; reserved (по address_id) → SetAddressReference.
func (s *InstanceService) markNatAddress(ctx context.Context, nat *domain.OneToOneNat, instanceID, instanceName string) {
	if nat.AddressID == "" {
		return
	}
	if nat.Ephemeral {
		s.markEphemeralAddressInUse(ctx, nat.AddressID, instanceID, instanceName)
		return
	}
	s.setAddressReference(ctx, nat.AddressID, instanceID, instanceName)
}

// markEphemeralAddressInUse — best-effort MarkAddressEphemeralInUse на один
// Address (no-op если addressID пуст): reserved=false, used=true + referrer.
// Ошибка → warning, не fatal.
func (s *InstanceService) markEphemeralAddressInUse(ctx context.Context, addressID, instanceID, instanceName string) {
	if addressID == "" {
		return
	}
	if err := s.vpcClient.MarkAddressEphemeralInUse(ctx, addressID, "compute_instance", instanceID, instanceName); err != nil {
		slog.WarnContext(ctx, "failed to mark vpc address ephemeral-in-use (best-effort)",
			"address_id", addressID, "instance_id", instanceID, "err", err)
	}
}

// setAddressReference — best-effort SetAddressReference на один Address (no-op
// если addressID пуст; reserved-флаг адреса не меняется). Ошибка → warning, не fatal.
func (s *InstanceService) setAddressReference(ctx context.Context, addressID, instanceID, instanceName string) {
	if addressID == "" {
		return
	}
	if err := s.vpcClient.SetAddressReference(ctx, addressID, "compute_instance", instanceID, instanceName); err != nil {
		slog.WarnContext(ctx, "failed to set vpc address referrer (best-effort)",
			"address_id", addressID, "instance_id", instanceID, "err", err)
	}
}

// clearAddressReference — best-effort ClearAddressReference на один Address
// (no-op если addressID пуст). Ошибка → warning, не fatal.
func (s *InstanceService) clearAddressReference(ctx context.Context, addressID string) {
	if addressID == "" {
		return
	}
	if err := s.vpcClient.ClearAddressReference(ctx, addressID); err != nil {
		slog.WarnContext(ctx, "failed to clear vpc address referrer (best-effort)",
			"address_id", addressID, "err", err)
	}
}

// reservedNatAddressIDs возвращает id reserved (Ephemeral=false) external
// NAT-адресов, на которые ссылаются NIC-и инстанса. Эфемерные не включаются —
// для них referrer уходит через FK CASCADE при DeleteAddress.
func reservedNatAddressIDs(in *domain.Instance) []string {
	var out []string
	for i := range in.NetworkInterfaces {
		nic := &in.NetworkInterfaces[i]
		if nic.PrimaryV4Nat != nil && !nic.PrimaryV4Nat.Ephemeral && nic.PrimaryV4Nat.AddressID != "" {
			out = append(out, nic.PrimaryV4Nat.AddressID)
		}
		if nic.PrimaryV6Nat != nil && !nic.PrimaryV6Nat.Ephemeral && nic.PrimaryV6Nat.AddressID != "" {
			out = append(out, nic.PrimaryV6Nat.AddressID)
		}
	}
	return out
}

// resolveNatAddress материализует one-to-one NAT-конфигурацию NIC'а:
//   - spec.AddressID задан → reserved Address: читаем его external IP (ephemeral=false);
//   - skipIPAM → синтетический external IP (ephemeral=false);
//   - spec.Address задан вручную → используем как есть (ephemeral=false);
//   - иначе → создаём эфемерный external Address в folder/zone (kacho-vpc inline
//     выделяет публичный IP из AddressPool) (ephemeral=true).
//
// Возвращает (*OneToOneNat, addressID, error). addressID непустой только для
// случаев reserved/эфемерный (нужен для teardown эфемерных).
func (s *InstanceService) resolveNatAddress(ctx context.Context, folderID, zoneID, addrName string, spec *NatSpec, idx int) (*domain.OneToOneNat, string, error) {
	nat := &domain.OneToOneNat{IPVersion: spec.IPVersion}
	switch {
	case spec.AddressID != "":
		addr, found, err := s.vpcClient.GetExternalAddress(ctx, spec.AddressID)
		if err != nil {
			return nil, "", status.Errorf(codes.Unavailable, "address check: %v", err)
		}
		if !found {
			return nil, "", status.Errorf(codes.NotFound, "Address %s not found", spec.AddressID)
		}
		nat.AddressID = spec.AddressID
		nat.Address = addr.IP
		if nat.Address == "" && s.skipIPAM {
			nat.Address = synthExternalIP(idx)
		}
		return nat, spec.AddressID, nil
	case s.skipIPAM:
		nat.Address = synthExternalIP(idx)
		if spec.Address != "" {
			nat.Address = spec.Address
		}
		return nat, "", nil
	case spec.Address != "":
		nat.Address = spec.Address
		return nat, "", nil
	default:
		addr, err := s.vpcClient.CreateExternalAddress(ctx, folderID, addrName, zoneID)
		if err != nil {
			return nil, "", status.Errorf(codes.Internal, "allocate external ip for nic %d: %v", idx, err)
		}
		nat.Address = addr.IP
		nat.AddressID = addr.AddressID
		nat.Ephemeral = true
		return nat, addr.AddressID, nil
	}
}

// releaseAddresses best-effort удаляет эфемерные Address-ресурсы (на rollback
// Create или при teardown Delete). VPC недоступен / Address уже удалён — лишь
// предупреждение в лог; не валит вызывающую операцию.
func (s *InstanceService) releaseAddresses(ctx context.Context, addressIDs []string) {
	for _, id := range addressIDs {
		if id == "" {
			continue
		}
		if err := s.vpcClient.DeleteAddress(ctx, id); err != nil {
			slog.WarnContext(ctx, "failed to release ephemeral vpc address (best-effort)", "address_id", id, "err", err)
		}
	}
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
		ProjectID:        folderID,
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
		// labelsInMask (epic RSAB β, D-β6): triggers an FGA register-intent refresh
		// so the IAM resource_mirror tracks dev→prod label dynamics. A full-object
		// PATCH (empty mask) applies labels too, so `updates` already includes it.
		labelsInMask := false
		for _, f := range updates {
			switch f {
			case "name":
				in.Name = req.Name
			case "description":
				in.Description = req.Description
			case "labels":
				in.Labels = req.Labels
				labelsInMask = true
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
		updated, err := s.repo.Update(ctx, in, labelsInMask)
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

// Start/Stop/Restart — state-машина (см. CLAUDE.md §8). DB-уровневый CAS
// (workspace CLAUDE.md §«Within-service refs — DB-уровень обязателен»):
// `Get → check → SetStatus` заменено на atomic `SetStatusCAS(expected, next)`
// в одной транзакции — concurrent Stop+Restart на RUNNING ВМ не может
// привести к second-writer-wins / lost-state (KAC-91, gap G2 audit KAC-85,
// parity c kacho-vpc KAC-52 NIC-attach race).
func (s *InstanceService) Start(ctx context.Context, id string) (*operations.Operation, error) {
	return s.lifecycle(ctx, id, "Start", domain.InstanceStatusStopped, domain.InstanceStatusRunning,
		"Instance is not stopped", &computev1.StartInstanceMetadata{InstanceId: id})
}

// Stop переводит ВМ RUNNING→STOPPED. CAS-условие на DB-уровне: только из
// RUNNING. Второй concurrent Stop увидит status=STOPPED и получит
// FailedPrecondition.
func (s *InstanceService) Stop(ctx context.Context, id string) (*operations.Operation, error) {
	return s.lifecycle(ctx, id, "Stop", domain.InstanceStatusRunning, domain.InstanceStatusStopped,
		"Instance is not running", &computev1.StopInstanceMetadata{InstanceId: id})
}

// Restart перезапускает RUNNING ВМ. Two-step CAS: RUNNING→RESTARTING (gate
// для concurrent Restart — ровно один winner), затем RESTARTING→RUNNING
// (мы owner state RESTARTING, второй CAS не race-able). Конечный state — RUNNING.
func (s *InstanceService) Restart(ctx context.Context, id string) (*operations.Operation, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Restart instance %s", id),
		&computev1.RestartInstanceMetadata{InstanceId: id})
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}
	operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		// Step 1: RUNNING → RESTARTING (gate). Concurrent Restarts: только
		// один writer пройдёт CAS, остальные получат FailedPrecondition.
		if _, err := s.repo.SetStatusCAS(ctx, id, domain.InstanceStatusRunning, domain.InstanceStatusRestarting); err != nil {
			return nil, mapLifecycleErr(err, "Instance is not running")
		}
		// Step 2: RESTARTING → RUNNING (control-plane: реального гипервизора
		// нет, restart мгновенный). Мы owner state RESTARTING — race не
		// возможен; если кто-то параллельно перевёл нас (admin/etc.) и
		// status уже не RESTARTING — это аномалия, лучше FailedPrecondition.
		updated, err := s.repo.SetStatusCAS(ctx, id, domain.InstanceStatusRestarting, domain.InstanceStatusRunning)
		if err != nil {
			return nil, mapLifecycleErr(err, "Instance is not running")
		}
		return anypb.New(protoconv.Instance(updated))
	})
	return &op, nil
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
		updated, err := s.repo.SetStatusCAS(ctx, id, from, to)
		if err != nil {
			return nil, mapLifecycleErr(err, precondMsg)
		}
		return anypb.New(protoconv.Instance(updated))
	})
	return &op, nil
}

// mapLifecycleErr маппит ошибку SetStatusCAS в gRPC-status: ErrFailedPrecondition
// от CAS-промаха («status != expected») транслируется в FailedPrecondition с
// verbatim YC-style precondMsg ("Instance is not running"/"... not stopped");
// все остальные (ErrNotFound, ErrInternal, ...) — стандартный mapRepoErr.
func mapLifecycleErr(err error, precondMsg string) error {
	if errors.Is(err, ErrFailedPrecondition) {
		return status.Error(codes.FailedPrecondition, precondMsg)
	}
	return mapRepoErr(err)
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
		natSpec := spec
		if natSpec == nil {
			natSpec = &NatSpec{}
		}
		nat, addrID, err := s.resolveNatAddress(ctx, in.ProjectID, in.ZoneID, nicNatAddressName(id, nic.Index), natSpec, 0)
		if err != nil {
			return nil, err
		}
		copyNIC := *nic
		copyNIC.PrimaryV4Nat = nat
		updated, err := s.repo.ReplaceNIC(ctx, id, copyNIC)
		if err != nil {
			if nat.Ephemeral && addrID != "" {
				s.releaseAddresses(ctx, []string{addrID})
			}
			return nil, mapRepoErr(err)
		}
		// Referrer-tracking: a newly-created ephemeral external Address →
		// MarkAddressEphemeralInUse (reserved=false, used=true + referrer); a
		// user-provided reserved address → SetAddressReference (referrer only).
		// Best-effort.
		s.markNatAddress(ctx, nat, in.ID, in.Name)
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
		releaseID := ""  // ephemeral address to delete (referrer goes via FK CASCADE)
		clearRefID := "" // reserved address whose referrer we explicitly clear
		if nic.PrimaryV4Nat.Ephemeral {
			releaseID = nic.PrimaryV4Nat.AddressID
		} else {
			clearRefID = nic.PrimaryV4Nat.AddressID
		}
		copyNIC := *nic
		copyNIC.PrimaryV4Nat = nil
		updated, err := s.repo.ReplaceNIC(ctx, id, copyNIC)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		// Free the ephemeral external Address (best-effort) now that the NIC no
		// longer references it; or clear the referrer on a reserved address.
		if releaseID != "" {
			s.releaseAddresses(ctx, []string{releaseID})
		}
		s.clearAddressReference(ctx, clearRefID)
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
		// Release any ephemeral VPC Address resources still referenced by the
		// instance's NICs (one-to-one NAT addresses created via AddOneToOneNat).
		// All best-effort: VPC unavailable / already-gone → log warning, don't
		// fail the delete. The referrer rows of ephemeral addresses go away via
		// FK CASCADE on delete; for any reserved one-to-one-NAT address we
		// explicitly clear the referrer so it shows used=false again.
		s.releaseAddresses(ctx, ephemeralAddressIDs(in))
		for _, addrID := range reservedNatAddressIDs(in) {
			s.clearAddressReference(ctx, addrID)
		}
		return anypb.New(&emptypb.Empty{})
	})
	return &op, nil
}

// ephemeralAddressIDs возвращает id всех Address-ресурсов, которые compute
// создал для NIC-ей этого инстанса (internal — непустой PrimaryV4AddressID;
// external — PrimaryV4Nat.AddressID при Ephemeral=true). Reserved-адреса
// (переданные клиентом по address_id) не включаются.
func ephemeralAddressIDs(in *domain.Instance) []string {
	var out []string
	for i := range in.NetworkInterfaces {
		nic := &in.NetworkInterfaces[i]
		if nic.PrimaryV4AddressID != "" {
			out = append(out, nic.PrimaryV4AddressID)
		}
		if nic.PrimaryV4Nat != nil && nic.PrimaryV4Nat.Ephemeral && nic.PrimaryV4Nat.AddressID != "" {
			out = append(out, nic.PrimaryV4Nat.AddressID)
		}
	}
	return out
}

// GetSerialPortOutput — sync RPC: синтетический текст (control-plane).
func (s *InstanceService) GetSerialPortOutput(ctx context.Context, id string) (string, error) {
	in, err := s.repo.Get(ctx, id)
	if err != nil {
		return "", mapRepoErr(err)
	}
	return fmt.Sprintf("[control-plane] serial port output for instance %s (status=%s) is not available (control-plane only).\n", in.ID, instanceStatusName(in.Status)), nil
}

// ListOperations возвращает операции для конкретной ВМ.
func (s *InstanceService) ListOperations(ctx context.Context, id string, p Pagination) ([]operations.Operation, string, error) {
	if _, err := s.repo.Get(ctx, id); err != nil {
		return nil, "", mapRepoErr(err)
	}
	return s.opsRepo.List(ctx, operations.ListFilter{ResourceID: id, PageSize: p.PageSize, PageToken: p.PageToken})
}

func (s *InstanceService) checkFolder(ctx context.Context, folderID string) error {
	exists, err := s.projectClient.Exists(ctx, folderID)
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

func synthExternalIP(i int) string { return fmt.Sprintf("203.0.113.%d", 10+i) }

// nicNatAddressName — имя эфемерного VPC Address-ресурса, создаваемого для
// one-to-one NAT NIC'а (AddOneToOneNat). Уникально в пределах folder
// (instanceID уникален) и соответствует regex имени Address
// (`|[a-zA-Z]([-_a-zA-Z0-9]{0,61}[a-zA-Z0-9])?`, ≤63 символов): instanceID
// начинается с буквы `e` (prefix `epd`), длина 20.
func nicNatAddressName(instanceID, idx string) string { return instanceID + "-nat" + idx }

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
