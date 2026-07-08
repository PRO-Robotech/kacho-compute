// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package service

import (
	"context"
	"errors"
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

// validCoreFractions — допустимые значения core_fraction (конвенция Kachō).
// Полная per-platform валидация (cores/memory/gpus по платформам standard-v1/v2/v3,
// gpu-*) живет в platforms.go; здесь — базовая проверка допустимых core_fraction.
var validCoreFractions = map[int64]struct{}{0: {}, 5: {}, 20: {}, 50: {}, 100: {}}

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

// InstanceService — бизнес-логика управления ВМ + state-машина.
type InstanceService struct {
	repo         InstanceRepo
	diskRepo     DiskRepo
	imageRepo    ImageRepo
	snapshotRepo SnapshotRepo
	// zones — existence-check zone_id. Авторитетный источник — kacho-geo
	// (geo.v1.ZoneService.Get; Geography принадлежит kacho-geo); при
	// SKIP_PEER_VALIDATION — no-op. Wiring — cmd/compute/main.go.
	zones         ZoneRegistry
	projectClient ProjectClient
	opsRepo       operations.Repo
}

// NewInstanceService создаёт InstanceService.
func NewInstanceService(repo InstanceRepo, diskRepo DiskRepo, imageRepo ImageRepo, snapshotRepo SnapshotRepo, zones ZoneRegistry, projectClient ProjectClient, opsRepo operations.Repo) *InstanceService {
	return &InstanceService{
		repo: repo, diskRepo: diskRepo, imageRepo: imageRepo, snapshotRepo: snapshotRepo,
		zones: zones, projectClient: projectClient, opsRepo: opsRepo,
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

// ValidateCreateInstanceReq — синхронная pre-flight валидация Create-запроса
// (формат/диапазоны полей): required-поля, name/description/labels,
// resources_spec (cores/memory/core_fraction), boot + secondary disk specs.
// Чистая (без DB/peer-вызовов) — тот же контракт, что энфорсит RPC ДО постановки
// async-операции. Выделена, чтобы её мог прогонять fuzz (internal/fuzz) на
// hostile-входах без поднятого сервиса. Возвращает InvalidArgument-status при
// нарушении, nil при валидном req.
func ValidateCreateInstanceReq(req CreateInstanceReq) error {
	if req.ProjectID == "" {
		return status.Error(codes.InvalidArgument, "project_id required")
	}
	if req.ZoneID == "" {
		return status.Error(codes.InvalidArgument, "zone_id required")
	}
	if req.PlatformID == "" {
		return status.Error(codes.InvalidArgument, "platform_id required")
	}
	if err := corevalidate.NameCompute("name", req.Name); err != nil {
		return err
	}
	if err := corevalidate.Description("description", req.Description); err != nil {
		return err
	}
	if err := corevalidate.Labels("labels", req.Labels); err != nil {
		return err
	}
	if err := validateResources(req.Cores, req.Memory, req.CoreFraction); err != nil {
		return err
	}
	if err := validateDiskSourceSpec("boot_disk_spec", req.BootDisk); err != nil {
		return err
	}
	for i, sd := range req.SecondaryDisks {
		if err := validateDiskSourceSpec(fmt.Sprintf("secondary_disk_specs[%d]", i), sd); err != nil {
			return err
		}
	}
	return nil
}

// Create инициирует создание Instance.
func (s *InstanceService) Create(ctx context.Context, req CreateInstanceReq) (*operations.Operation, error) {
	if err := ValidateCreateInstanceReq(req); err != nil {
		return nil, err
	}

	instanceID := ids.NewID(ids.PrefixInstance)
	return runOp(ctx, s.opsRepo, fmt.Sprintf("Create instance %s", req.Name),
		&computev1.CreateInstanceMetadata{InstanceId: instanceID},
		func(ctx context.Context) (*anypb.Any, error) {
			return s.doCreate(ctx, instanceID, req)
		})
}

func (s *InstanceService) doCreate(ctx context.Context, instanceID string, req CreateInstanceReq) (*anypb.Any, error) {
	if err := checkProject(ctx, s.projectClient, req.ProjectID); err != nil {
		return nil, err
	}
	if err := s.zones.GetZone(ctx, req.ZoneID); err != nil {
		return nil, mapZoneRefErr(err, req.ZoneID)
	}

	// Instance is created WITHOUT any network interface. NIC binding has
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
	// the compute_instance→project owner-tuple (and any inline boot/secondary
	// disk tuples) is registered transactionally — repo.Insert writes the FGA
	// register-intent in the SAME writer-tx as the rows (compute_fga_register_outbox)
	// and the register-drainer applies it via kacho-iam InternalIAMService
	// .RegisterResource. No direct FGA write, no best-effort post-commit dual-write
	// (closes the lost-tuple bug N5: the owner-tuple is now durable + retried, so the
	// per-resource Check "no path" DENY window is finite, not permanent).
	return anypb.New(protoconv.Instance(created))
}

// resolveDiskSource резолвит DiskSourceSpec в AttachedDisk + (опционально) новый
// диск для inline-вставки. Для существующего диска проверяет READY + zone + not-attached.
func (s *InstanceService) resolveDiskSource(ctx context.Context, folderID, zoneID string, spec DiskSourceSpec, isBoot bool) (domain.AttachedDisk, *domain.Disk, error) {
	if spec.DiskID != "" {
		d, err := s.diskRepo.Get(ctx, spec.DiskID)
		if err != nil {
			return domain.AttachedDisk{}, nil, mapRefErr(err, "Disk", spec.DiskID)
		}
		// Cross-project BOLA guard: repo.Get resolves across ALL projects; a
		// caller must not attach a disk owned by another project to their own
		// instance (cross-project takeover + auto_delete destruction). Reject
		// with NotFound (no existence oracle) BEFORE any state leak.
		if d.ProjectID != folderID {
			return domain.AttachedDisk{}, nil, crossProjectNotFound("Disk", spec.DiskID)
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
		img, err := s.imageRepo.Get(ctx, spec.NewSourceImage)
		if err != nil {
			return domain.AttachedDisk{}, nil, mapRefErr(err, "Image", spec.NewSourceImage)
		}
		// Cross-project BOLA guard: an inline boot/secondary disk must not be
		// seeded from a source image owned by another project (data exfiltration
		// into the caller's project). Reject with NotFound (no existence oracle).
		if img.ProjectID != folderID {
			return domain.AttachedDisk{}, nil, crossProjectNotFound("Image", spec.NewSourceImage)
		}
	}
	if spec.NewSourceSnap != "" {
		snap, err := s.snapshotRepo.Get(ctx, spec.NewSourceSnap)
		if err != nil {
			return domain.AttachedDisk{}, nil, mapRefErr(err, "Snapshot", spec.NewSourceSnap)
		}
		if snap.ProjectID != folderID {
			return domain.AttachedDisk{}, nil, crossProjectNotFound("Snapshot", spec.NewSourceSnap)
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
	return runOp(ctx, s.opsRepo, fmt.Sprintf("Update instance %s", req.InstanceID),
		&computev1.UpdateInstanceMetadata{InstanceId: req.InstanceID},
		func(ctx context.Context) (*anypb.Any, error) {
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
			// changed — фактически изменённые mask-поля. Передаётся в repo.Update, чтобы
			// UPDATE писал ТОЛЬКО эти колонки (column-scoped), а не весь набор из
			// устаревшего Get-снимка — иначе конкурентный Update по другому полю
			// затирается (lost update). Поля, пропущенные из-за условий (silent-ignore),
			// в changed НЕ добавляются.
			changed := make([]string, 0, len(updates))
			for _, f := range updates {
				switch f {
				case "name":
					in.Name = req.Name
					changed = append(changed, "name")
				case "description":
					in.Description = req.Description
					changed = append(changed, "description")
				case "labels":
					in.Labels = req.Labels
					labelsInMask = true
					changed = append(changed, "labels")
				case "service_account_id":
					in.ServiceAccountID = req.ServiceAccountID
					changed = append(changed, "service_account_id")
				case "placement_policy":
					in.PlacementPolicy = req.PlacementPolicy
					changed = append(changed, "placement_policy")
				case "network_settings":
					if req.NetworkSettingsType != "" {
						in.NetworkSettingsType = req.NetworkSettingsType
						changed = append(changed, "network_settings")
					}
				case "resources_spec":
					if !full {
						touchesCompute = true
						if err := validateResources(req.Cores, req.Memory, req.CoreFraction); err != nil {
							return nil, err
						}
						in.Cores, in.Memory, in.CoreFraction, in.GPUs = req.Cores, req.Memory, defaultCoreFraction(req.CoreFraction), req.GPUs
						changed = append(changed, "resources_spec")
					}
				case "platform_id":
					if !full && req.PlatformID != "" {
						touchesCompute = true
						in.PlatformID = req.PlatformID
						changed = append(changed, "platform_id")
					}
				}
			}
			if touchesCompute && in.Status != domain.InstanceStatusStopped {
				return nil, status.Error(codes.FailedPrecondition, "Instance must be stopped")
			}
			updated, err := s.repo.Update(ctx, in, labelsInMask, changed)
			if err != nil {
				return nil, mapRepoErr(err)
			}
			return anypb.New(protoconv.Instance(updated))
		})
}

func validateInstanceUpdate(req UpdateInstanceReq) error {
	known := map[string]struct{}{
		"name": {}, "description": {}, "labels": {}, "service_account_id": {},
		"placement_policy": {}, "network_settings": {}, "scheduling_policy": {},
		"resources_spec": {}, "platform_id": {}, "metadata_options": {},
	}
	// Immutable-check ПЕРЕД UpdateMask: known-set не содержит immutable-полей,
	// поэтому UpdateMask вернул бы generic "unknown field" вместо конвенционного
	// "<field> is immutable after Instance.Create" (api-conventions: update_mask).
	for _, f := range req.UpdateMask {
		switch f {
		case "zone_id", "boot_disk", "metadata":
			return invalidArg(f, f+" is immutable after Instance.Create (use AttachDisk/UpdateMetadata/Relocate)")
		}
	}
	if err := corevalidate.UpdateMask("update_mask", req.UpdateMask, known); err != nil {
		return err
	}
	for _, f := range req.UpdateMask {
		switch f {
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
	return runOp(ctx, s.opsRepo, fmt.Sprintf("Update instance %s metadata", instanceID),
		&computev1.UpdateInstanceMetadataMetadata{InstanceId: instanceID},
		func(ctx context.Context) (*anypb.Any, error) {
			// Атомарный delete+upsert merge на DB-уровне (project-rule 10). Прежний
			// Get→merge-in-Go→SetMetadata(full-overwrite) был read-modify-write вне TX и
			// терял дельту конкурентного UpdateMetadata (second-writer-wins).
			updated, err := s.repo.MergeMetadata(ctx, instanceID, del, upsert)
			if err != nil {
				return nil, mapRepoErr(err)
			}
			return anypb.New(protoconv.Instance(updated))
		})
}

// Start/Stop/Restart — state-машина. DB-уровневый CAS (within-service-инвариант
// на DB-уровне): `Get → check → SetStatus` заменено на atomic
// `SetStatusCAS(expected, next)` в одной транзакции — concurrent Stop+Restart на
// RUNNING ВМ не может привести к second-writer-wins / lost-state.
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

// Restart перезапускает RUNNING ВМ. Single atomic CAS RUNNING→RUNNING (без
// durably-committed промежуточного RESTARTING).
//
// Почему НЕ two-step (RUNNING→RESTARTING→RUNNING): промежуточный RESTARTING
// коммитился отдельной транзакцией; прерывание воркера между двумя коммитами
// (crash / panic / срабатывание per-op deadline) оставляло ВМ навсегда в
// RESTARTING без пути восстановления — Start требует STOPPED, Stop/Restart
// требуют RUNNING, AttachDisk требует RUNNING|STOPPED → любой последующий CAS
// падал FailedPrecondition (bricked instance). Orphan-resolver трактует
// RestartInstanceMetadata как kindUpdate и, видя ВМ присутствующей, помечал
// операцию Done(current) не переигрывая замыкание — заклиненный RESTARTING
// маскировался под успех.
//
// В control-plane реального гипервизора нет — restart мгновенный, промежуточный
// durable state не нужен. Одиночный `SetStatusCAS(RUNNING→RUNNING)` идемпотентен:
// row-level lock сериализует конкурентные Restart'ы, каждый видит RUNNING и
// подтверждает RUNNING (no bricking, no intermediate). RESTARTING enum в proto
// сохранён — контракт не тронут.
func (s *InstanceService) Restart(ctx context.Context, id string) (*operations.Operation, error) {
	// RUNNING→RUNNING идёт через общий lifecycle-путь (тот же single-atomic CAS +
	// mapLifecycleErr + async-обвязка), а не переинлайнит её — единый диспетчер, без
	// drift относительно Start/Stop.
	return s.lifecycle(ctx, id, "Restart", domain.InstanceStatusRunning, domain.InstanceStatusRunning,
		"Instance is not running", &computev1.RestartInstanceMetadata{InstanceId: id})
}

func (s *InstanceService) lifecycle(ctx context.Context, id, action string, from, to domain.InstanceStatus, precondMsg string, meta protoreflectMessage) (*operations.Operation, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	return runOp(ctx, s.opsRepo, fmt.Sprintf("%s instance %s", action, id), meta,
		func(ctx context.Context) (*anypb.Any, error) {
			updated, err := s.repo.SetStatusCAS(ctx, id, from, to)
			if err != nil {
				return nil, mapLifecycleErr(err, precondMsg)
			}
			return anypb.New(protoconv.Instance(updated))
		})
}

// mapLifecycleErr маппит ошибку SetStatusCAS в gRPC-status: ErrFailedPrecondition
// от CAS-промаха («status != expected») транслируется в FailedPrecondition с
// precondMsg ("Instance is not running"/"... not stopped"); все остальные
// (ErrNotFound, ErrInternal, ...) — стандартный mapRepoErr.
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
	return runOp(ctx, s.opsRepo, fmt.Sprintf("Attach disk to instance %s", id),
		&computev1.AttachInstanceDiskMetadata{InstanceId: id, DiskId: spec.DiskID},
		func(ctx context.Context) (*anypb.Any, error) {
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
				return nil, mapRefErr(err, "Disk", spec.DiskID)
			}
			// Cross-project BOLA guard: repo.Get resolves across ALL projects; a
			// caller must not attach a disk owned by another project to their own
			// instance (cross-project takeover + auto_delete destruction). Reject
			// with NotFound (no existence oracle) BEFORE the status/zone leak. The
			// insertAttachedDiskTx CTE re-enforces this at the DB level.
			if d.ProjectID != in.ProjectID {
				return nil, crossProjectNotFound("Disk", spec.DiskID)
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
}

// DetachDisk отвязывает диск (по disk_id или device_name; не boot).
func (s *InstanceService) DetachDisk(ctx context.Context, id, diskID, deviceName string) (*operations.Operation, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	if diskID == "" && deviceName == "" {
		return nil, invalidArg("disk", "one of disk_id or device_name is required")
	}
	return runOp(ctx, s.opsRepo, fmt.Sprintf("Detach disk from instance %s", id),
		&computev1.DetachInstanceDiskMetadata{InstanceId: id, DiskId: diskID},
		func(ctx context.Context) (*anypb.Any, error) {
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
}

// SimulateMaintenanceEvent — no-op (control-plane: возвращает done-операцию с самой ВМ).
func (s *InstanceService) SimulateMaintenanceEvent(ctx context.Context, id string) (*operations.Operation, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	return runOp(ctx, s.opsRepo, fmt.Sprintf("Simulate maintenance event for instance %s", id),
		&computev1.SimulateInstanceMaintenanceEventMetadata{InstanceId: id},
		func(ctx context.Context) (*anypb.Any, error) {
			in, err := s.repo.Get(ctx, id)
			if err != nil {
				return nil, mapRepoErr(err)
			}
			return anypb.New(protoconv.Instance(in))
		})
}

// Delete инициирует удаление ВМ (auto-delete диски удаляются, остальные detach'атся CASCADE).
func (s *InstanceService) Delete(ctx context.Context, id string) (*operations.Operation, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	return runOp(ctx, s.opsRepo, fmt.Sprintf("Delete instance %s", id),
		&computev1.DeleteInstanceMetadata{InstanceId: id},
		func(ctx context.Context) (*anypb.Any, error) {
			// auto-delete множество вычисляет repo.Delete ВНУТРИ своей TX из текущих
			// attached_disks (project-rule 10) — здесь его больше НЕ снимаем out-of-tx,
			// иначе конкурентный AttachDisk(auto_delete) оставил бы orphan-диск.
			if err := s.repo.Delete(ctx, id); err != nil {
				return nil, mapRepoErr(err)
			}
			return anypb.New(&emptypb.Empty{})
		})
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

func fqdn(id, hostname string) string {
	if hostname != "" {
		return hostname + ".ru-central1.internal"
	}
	return id + ".auto.internal"
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
