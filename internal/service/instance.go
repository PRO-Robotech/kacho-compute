package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
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
	"github.com/PRO-Robotech/kacho-compute/internal/fgawrite"
	"github.com/PRO-Robotech/kacho-compute/internal/protoconv"
)

// validCoreFractions — допустимые значения core_fraction (verbatim YC).
// Полная per-platform валидация (cores/memory/gpus per standard-v1/v2/v3, gpu-*)
// — TODO platforms.go; пока — basic sanity check (см. CLAUDE.md §5).
var validCoreFractions = map[int64]struct{}{0: {}, 5: {}, 20: {}, 50: {}, 100: {}}

// NICSpec — спека сетевого интерфейса для Create / AttachNetworkInterface.
//
// NicID — опционально: id существующего kacho-vpc NetworkInterface-ресурса,
// который нужно приаттачить к создаваемой ВМ вместо создания нового NIC. Ровно
// одно из {SubnetID, NicID} должно быть задано (как в proto).
type NICSpec struct {
	SubnetID         string
	NicID            string
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
	// zones — existence-check zone_id. Авторитетный источник — kacho-vpc
	// InternalZoneService (compute зон не владеет); при SKIP_PEER_VALIDATION —
	// fallback на локальную таблицу `zones`. Wiring — cmd/compute/main.go.
	zones         ZoneRegistry
	projectClient ProjectClient
	vpcClient     VPCClient
	opsRepo       operations.Repo
	// skipIPAM — true при KACHO_COMPUTE_SKIP_PEER_VALIDATION: cross-service
	// VPC IPAM-аллокация отключена, NIC-ам выдаются синтетические IP (synth*).
	skipIPAM bool
	// fgaWriter — write-side OpenFGA: publishes compute_instance hierarchy tuple
	// after Create. nil = FGA write disabled (dev/no-config). KAC-133.
	fgaWriter fgawrite.HierarchyTupleWriter
	logger    *slog.Logger
}

// NewInstanceService создаёт InstanceService. skipIPAM=true (зеркалит
// KACHO_COMPUTE_SKIP_PEER_VALIDATION) → NIC-ам выдаются синтетические IP вместо
// реальных, выделенных через kacho-vpc IPAM (для unit/newman/load без VPC).
func NewInstanceService(repo InstanceRepo, diskRepo DiskRepo, imageRepo ImageRepo, snapshotRepo SnapshotRepo, zones ZoneRegistry, projectClient ProjectClient, vpcClient VPCClient, opsRepo operations.Repo, skipIPAM bool, fgaWriter fgawrite.HierarchyTupleWriter, logger *slog.Logger) *InstanceService {
	return &InstanceService{
		repo: repo, diskRepo: diskRepo, imageRepo: imageRepo, snapshotRepo: snapshotRepo,
		zones: zones, projectClient: projectClient, vpcClient: vpcClient, opsRepo: opsRepo,
		skipIPAM: skipIPAM, fgaWriter: fgaWriter, logger: logger,
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
	if len(req.NICs) == 0 {
		return nil, invalidArg("network_interface_specs", "at least one network_interface_spec is required")
	}
	for i, nic := range req.NICs {
		if (nic.SubnetID == "") == (nic.NicID == "") {
			return nil, invalidArg(fmt.Sprintf("network_interface_specs[%d]", i), "exactly one of subnet_id or nic_id is required")
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

	// NIC cross-service validation + materialization: real IPv4 allocation via
	// kacho-vpc IPAM (ephemeral Address) + creation of (or attach to) the backing
	// kacho-vpc NetworkInterface resource (epic KAC-2). On failure after creating
	// ephemeral Address / NIC resources — release them best-effort.
	nics, createdAddrIDs, createdNICIDs, err := s.materializeNICs(ctx, instanceID, req)
	if err != nil {
		s.releaseNICs(ctx, createdNICIDs)
		s.releaseAddresses(ctx, createdAddrIDs)
		return nil, err
	}

	// Boot disk + secondary disks: resolve existing OR materialize inline.
	var inlineDisks []*domain.Disk
	bootAD, bootInline, err := s.resolveDiskSource(ctx, req.ProjectID, req.ZoneID, req.BootDisk, true)
	if err != nil {
		s.releaseNICs(ctx, createdNICIDs)
		s.releaseAddresses(ctx, createdAddrIDs)
		return nil, err
	}
	if bootInline != nil {
		inlineDisks = append(inlineDisks, bootInline)
	}
	attached := []domain.AttachedDisk{bootAD}
	for _, sd := range req.SecondaryDisks {
		ad, inline, err := s.resolveDiskSource(ctx, req.ProjectID, req.ZoneID, sd, false)
		if err != nil {
			s.releaseNICs(ctx, createdNICIDs)
			s.releaseAddresses(ctx, createdAddrIDs)
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
		NetworkInterfaces:     nics,
		AttachedDisks:         attached,
	}
	created, err := s.repo.Insert(ctx, in, inlineDisks)
	if err != nil {
		s.releaseNICs(ctx, createdNICIDs)
		s.releaseAddresses(ctx, createdAddrIDs)
		return nil, mapRepoErr(err)
	}
	// Referrer-tracking (YC-like): mark every VPC Address this instance's NICs
	// use with a compute_instance reference. Ephemeral addresses compute itself
	// created (internal <vmid>-nicN + ephemeral external <vmid>-natN) get
	// MarkAddressEphemeralInUse → reserved=false, used=true + referrer atomically;
	// reserved user addresses (one-to-one NAT by address_id) keep their reserved
	// flag and just get a referrer via SetAddressReference. Best-effort — the IPs
	// are already allocated; a missing reference must not fail the instance create.
	s.setNICAddressReferences(ctx, created)
	// KAC-133: publish FGA hierarchy tuple so per-resource Check (Get/Update/Delete)
	// can cascade from compute_instance:<id>#project to project:<project_id>.
	fgawrite.Emit(ctx, s.fgaWriter, s.logger, "compute_instance", created.ID, created.ProjectID)
	return anypb.New(protoconv.Instance(created))
}

// setNICAddressReferences best-effort помечает каждый VPC Address-ресурс, который
// используют NIC-и инстанса:
//   - эфемерный internal (PrimaryV4AddressID — compute создал его сам) и
//     эфемерный external NAT (PrimaryV4Nat/PrimaryV6Nat.AddressID при Ephemeral=true)
//     → MarkAddressEphemeralInUse (reserved=false, used=true + referrer);
//   - reserved external NAT (Ephemeral=false, передан клиентом по address_id)
//     → SetAddressReference (referrer, reserved не трогаем).
//
// Ошибка → warning в лог, не валит вызывающую операцию.
func (s *InstanceService) setNICAddressReferences(ctx context.Context, in *domain.Instance) {
	for i := range in.NetworkInterfaces {
		nic := &in.NetworkInterfaces[i]
		// PrimaryV4AddressID is always an ephemeral internal Address compute created.
		s.markEphemeralAddressInUse(ctx, nic.PrimaryV4AddressID, in.ID, in.Name)
		if nat := nic.PrimaryV4Nat; nat != nil {
			s.markNatAddress(ctx, nat, in.ID, in.Name)
		}
		if nat := nic.PrimaryV6Nat; nat != nil {
			s.markNatAddress(ctx, nat, in.ID, in.Name)
		}
	}
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

// materializeNICs валидирует cross-service refs каждого NIC-spec'а и
// материализует domain.NetworkInterface:
//   - spec.NicID задан → аттач существующего kacho-vpc NetworkInterface-ресурса
//     (проверяем существование + что он не приаттачен к другому инстансу),
//     denorm-поля (subnet_id, sg ids) копируются из NIC-ресурса;
//   - иначе (spec.SubnetID задан) → создаём эфемерный internal Address из CIDR
//     подсети и kacho-vpc NetworkInterface-ресурс, ссылающийся на него; внешний
//     (one-to-one NAT) IP — эфемерный external Address либо reserved Address.
//
// В режиме skipIPAM (SKIP_PEER_VALIDATION) — синтетические IP, без обращения к
// kacho-vpc и без создания NIC-ресурса (NICID берётся как есть из spec, если задан).
//
// Возвращает (nics, createdAddrIDs, createdNICIDs, error): два последних — id
// эфемерных Address- и NIC-ресурсов, созданных compute'ом (для best-effort
// rollback выше по стеку).
func (s *InstanceService) materializeNICs(ctx context.Context, instanceID string, req CreateInstanceReq) ([]domain.NetworkInterface, []string, []string, error) {
	nics := make([]domain.NetworkInterface, 0, len(req.NICs))
	var createdAddrIDs, createdNICIDs []string
	for i, spec := range req.NICs {
		idx := spec.Index
		if idx == "" {
			idx = fmt.Sprintf("%d", i)
		}

		// --- attach an existing kacho-vpc NIC by id ---
		if spec.NicID != "" {
			nic, err := s.attachExistingNIC(ctx, instanceID, idx, spec.NicID)
			if err != nil {
				return nil, createdAddrIDs, createdNICIDs, err
			}
			nics = append(nics, nic)
			continue
		}

		// --- create a fresh NIC (with an internal Address) in the given subnet ---
		subnet, found, err := s.vpcClient.GetSubnet(ctx, spec.SubnetID)
		if err != nil {
			return nil, createdAddrIDs, createdNICIDs, status.Errorf(codes.Unavailable, "subnet check: %v", err)
		}
		if !found {
			return nil, createdAddrIDs, createdNICIDs, status.Errorf(codes.NotFound, "Subnet %s not found", spec.SubnetID)
		}
		if subnet.ZoneID != "" && subnet.ZoneID != req.ZoneID {
			return nil, createdAddrIDs, createdNICIDs, status.Errorf(codes.InvalidArgument, "Subnet %s is in zone %s, instance zone is %s", spec.SubnetID, subnet.ZoneID, req.ZoneID)
		}
		for _, sg := range spec.SecurityGroupIDs {
			ok, err := s.vpcClient.SecurityGroupExists(ctx, sg)
			if err != nil {
				return nil, createdAddrIDs, createdNICIDs, status.Errorf(codes.Unavailable, "security group check: %v", err)
			}
			if !ok {
				return nil, createdAddrIDs, createdNICIDs, status.Errorf(codes.NotFound, "Security group %s not found", sg)
			}
		}

		nic := domain.NetworkInterface{
			Index:            idx,
			SubnetID:         spec.SubnetID,
			SecurityGroupIDs: spec.SecurityGroupIDs,
		}

		// Internal IPv4.
		switch {
		case spec.PrimaryV4Address != "":
			// Manual address — validate it's within the subnet CIDR (a manual
			// primary v4 address inside the subnet is used as-is). No ephemeral
			// Address resource is created for it (so the kacho-vpc NIC below gets
			// no v4_address_ids ref — MVP).
			if err := validateIPInSubnet("primary_v4_address_spec.address", spec.PrimaryV4Address, subnet.V4CidrBlocks); err != nil {
				return nil, createdAddrIDs, createdNICIDs, err
			}
			nic.PrimaryV4Address = spec.PrimaryV4Address
		case s.skipIPAM:
			nic.PrimaryV4Address = synthInternalIP(i)
		default:
			addr, err := s.vpcClient.CreateInternalAddress(ctx, req.ProjectID, nicAddressName(instanceID, idx), spec.SubnetID)
			if err != nil {
				return nil, createdAddrIDs, createdNICIDs, status.Errorf(codes.Internal, "allocate internal ip for nic %s: %v", idx, err)
			}
			createdAddrIDs = append(createdAddrIDs, addr.AddressID)
			nic.PrimaryV4Address = addr.IP
			nic.PrimaryV4AddressID = addr.AddressID
		}

		// Create the backing kacho-vpc NetworkInterface resource (skipped in
		// skipIPAM mode — synthetic NIC, no vpc resource).
		if !s.skipIPAM {
			var v4Addrs []string
			if nic.PrimaryV4AddressID != "" {
				v4Addrs = []string{nic.PrimaryV4AddressID}
			}
			nicID, err := s.vpcClient.CreateNetworkInterface(ctx, CreateNICReq{
				ProjectID:        req.ProjectID,
				Name:             nicResourceName(instanceID, idx),
				SubnetID:         spec.SubnetID,
				SecurityGroupIDs: spec.SecurityGroupIDs,
				V4AddressIDs:     v4Addrs,
				InstanceID:       instanceID,
				Index:            idx,
			})
			if err != nil {
				return nil, createdAddrIDs, createdNICIDs, status.Errorf(codes.Internal, "create network interface for nic %s: %v", idx, err)
			}
			createdNICIDs = append(createdNICIDs, nicID)
			nic.NICID = nicID
		}

		// External (one-to-one NAT) IPv4.
		if spec.OneToOneNat != nil {
			nat, addrID, err := s.resolveNatAddress(ctx, req.ProjectID, req.ZoneID, nicNatAddressName(instanceID, idx), spec.OneToOneNat, i)
			if err != nil {
				return nil, createdAddrIDs, createdNICIDs, err
			}
			if addrID != "" && nat.Ephemeral {
				createdAddrIDs = append(createdAddrIDs, addrID)
			}
			nic.PrimaryV4Nat = nat
		}
		nics = append(nics, nic)
	}
	return nics, createdAddrIDs, createdNICIDs, nil
}

// attachExistingNIC проверяет существование kacho-vpc NIC по id, что он не
// приаттачен к другому инстансу, аттачит его к instanceID@idx и собирает
// domain.NetworkInterface с denorm-полями из NIC-ресурса. В skipIPAM-режиме —
// без обращения к VPC (NICID берётся как есть).
//
// Software fast-path-check (info.InstanceID != "" && != instanceID) — для
// human-friendly error до похода в Attach RPC. Финальная race-safe защита —
// на vpc-стороне (миграция 0016 partial UNIQUE + conditional UPDATE CAS,
// KAC-52). Если concurrent Attach к одному и тому же existing NIC прошёл
// software-guard, vpc вернёт FailedPrecondition — здесь мы её **пробрасываем
// как есть** (не оборачиваем в codes.Internal), чтобы клиент получил
// нормальный 412 с осмысленным сообщением, а не "internal error".
func (s *InstanceService) attachExistingNIC(ctx context.Context, instanceID, idx, nicID string) (domain.NetworkInterface, error) {
	if s.skipIPAM {
		return domain.NetworkInterface{Index: idx, NICID: nicID, PrimaryV4Address: synthInternalIP(0)}, nil
	}
	info, found, err := s.vpcClient.GetNetworkInterface(ctx, nicID)
	if err != nil {
		return domain.NetworkInterface{}, status.Errorf(codes.Unavailable, "network interface check: %v", err)
	}
	if !found {
		return domain.NetworkInterface{}, status.Errorf(codes.NotFound, "Network interface %s not found", nicID)
	}
	if info.InstanceID != "" && info.InstanceID != instanceID {
		return domain.NetworkInterface{}, status.Errorf(codes.FailedPrecondition, "Network interface %s is already attached to instance %s", nicID, info.InstanceID)
	}
	if err := s.vpcClient.AttachNetworkInterface(ctx, nicID, instanceID, idx); err != nil {
		// Map gRPC code from kacho-vpc:
		//   FailedPrecondition — NIC race / уже attached (DB-side CAS отбил);
		//   NotFound — NIC исчез между Get и Attach;
		//   InvalidArgument — bad request (нереально с проверенным id, но прокидываем);
		//   Unavailable — vpc недоступен, retryable;
		//   остальное — Internal.
		switch status.Code(err) {
		case codes.FailedPrecondition, codes.NotFound, codes.InvalidArgument, codes.Unavailable:
			return domain.NetworkInterface{}, err
		default:
			return domain.NetworkInterface{}, status.Errorf(codes.Internal, "attach network interface %s: %v", nicID, err)
		}
	}
	return domain.NetworkInterface{
		Index:            idx,
		NICID:            nicID,
		SubnetID:         info.SubnetID,
		SecurityGroupIDs: info.SecurityGroupIDs,
	}, nil
}

// releaseNICs best-effort удаляет kacho-vpc NetworkInterface-ресурсы, созданные
// compute'ом для этого инстанса (на rollback Create или при teardown Delete).
// VPC недоступен / NIC уже удалён — лишь предупреждение в лог.
func (s *InstanceService) releaseNICs(ctx context.Context, nicIDs []string) {
	for _, id := range nicIDs {
		if id == "" {
			continue
		}
		if err := s.vpcClient.DeleteNetworkInterface(ctx, id); err != nil {
			slog.WarnContext(ctx, "failed to release kacho-vpc network interface (best-effort)", "nic_id", id, "err", err)
		}
	}
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

// Move инициирует перенос ВМ в другой folder.
func (s *InstanceService) Move(ctx context.Context, id, destProjectID string) (*operations.Operation, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	if destProjectID == "" {
		return nil, invalidArg("destination_project_id", "destination_project_id is required")
	}
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Move instance %s", id),
		&computev1.MoveInstanceMetadata{InstanceId: id, DestinationProjectId: destProjectID})
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}
	operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		if err := s.checkFolder(ctx, destProjectID); err != nil {
			return nil, err
		}
		updated, err := s.repo.SetProjectID(ctx, id, destProjectID)
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
		// Detach + delete the backing kacho-vpc NetworkInterface resources owned
		// by this instance's NICs (epic KAC-2), then release any ephemeral VPC
		// Address resources. All best-effort: VPC unavailable / already-gone → log
		// warning, don't fail the delete — mirrors the legacy NAT-release path.
		// The referrer rows of ephemeral addresses go away via FK CASCADE on
		// delete; for any reserved one-to-one-NAT address we explicitly clear the
		// referrer so it shows used=false again.
		for _, nicID := range nicResourceIDs(in) {
			if err := s.vpcClient.DetachNetworkInterface(ctx, nicID); err != nil {
				slog.WarnContext(ctx, "failed to detach kacho-vpc network interface (best-effort)", "nic_id", nicID, "err", err)
			}
		}
		s.releaseNICs(ctx, nicResourceIDs(in))
		s.releaseAddresses(ctx, ephemeralAddressIDs(in))
		for _, addrID := range reservedNatAddressIDs(in) {
			s.clearAddressReference(ctx, addrID)
		}
		return anypb.New(&emptypb.Empty{})
	})
	return &op, nil
}

// nicResourceIDs возвращает id kacho-vpc NetworkInterface-ресурсов, на которые
// ссылаются NIC-и инстанса (пусто для legacy / skip-peer NIC-ей).
func nicResourceIDs(in *domain.Instance) []string {
	var out []string
	for i := range in.NetworkInterfaces {
		if in.NetworkInterfaces[i].NICID != "" {
			out = append(out, in.NetworkInterfaces[i].NICID)
		}
	}
	return out
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

func synthInternalIP(i int) string { return fmt.Sprintf("10.0.0.%d", 10+i) }
func synthExternalIP(i int) string { return fmt.Sprintf("203.0.113.%d", 10+i) }

// nicAddressName / nicNatAddressName — имена эфемерных VPC Address-ресурсов,
// создаваемых для NIC'а. Уникальны в пределах folder (instanceID уникален) и
// соответствуют regex имени Address (`|[a-zA-Z]([-_a-zA-Z0-9]{0,61}[a-zA-Z0-9])?`,
// ≤63 символов): instanceID начинается с буквы `e` (prefix `epd`), длина 20.
func nicAddressName(instanceID, idx string) string    { return instanceID + "-nic" + idx }
func nicNatAddressName(instanceID, idx string) string { return instanceID + "-nat" + idx }

// nicResourceName — имя kacho-vpc NetworkInterface-ресурса, создаваемого для
// NIC'а инстанса (уникально в пределах folder; ≤63 символов, начинается с буквы).
func nicResourceName(instanceID, idx string) string { return instanceID + "-ni" + idx }

// validateIPInSubnet проверяет, что manual IPv4 (`primary_v4_address_spec.address`)
// принадлежит одному из v4-CIDR-блоков подсети. Если CIDR-блоки неизвестны (напр.
// NoopVPCClient в SKIP_PEER_VALIDATION) — проверка пропускается.
func validateIPInSubnet(field, ip string, v4CidrBlocks []string) error {
	if len(v4CidrBlocks) == 0 {
		return nil
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return invalidArg(field, "address is not a valid IP")
	}
	for _, raw := range v4CidrBlocks {
		cidr, perr := netip.ParsePrefix(strings.TrimSpace(raw))
		if perr != nil {
			continue
		}
		if cidr.Contains(addr) {
			return nil
		}
	}
	return invalidArg(field, fmt.Sprintf("address %s is not within subnet cidr %s", ip, v4CidrBlocks[0]))
}
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
