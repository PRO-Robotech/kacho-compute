// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
	"github.com/PRO-Robotech/kacho-compute/internal/protoconv"
)

// insResource / volResource — human-labels для malformed-id ошибок
// (`corevalidate.ResourceID`: `invalid <label> id '<X>'`, api-conventions).
const (
	insResource = "instance"
	volResource = "volume"
	// volumeIDPrefix — 3-char id-prefix storage-Volume ("vol"). Проверяется локально:
	// compute-пинованный kacho-corelib не знает "vol" в resourceIDPrefixes (Volume —
	// ресурс kacho-storage), поэтому corevalidate.ResourceID ложно отбил бы валидный
	// vol-id — используем собственный validateVolumeID (см. ниже).
	volumeIDPrefix = "vol"
)

// validateVolumeID — malformed-id гейт storage-Volume (api-conventions: sync
// InvalidArgument "invalid volume id '<X>'" первым стейтментом RPC). Пустой id —
// nil (presence гейтится отдельно; DetachDisk использует device_name-arm).
func validateVolumeID(id string) error {
	if id == "" {
		return nil
	}
	if !strings.HasPrefix(id, volumeIDPrefix) {
		return status.Errorf(codes.InvalidArgument, "invalid %s id '%s'", volResource, id)
	}
	return nil
}

// validCoreFractions — допустимые значения core_fraction (конвенция Kachō).
var validCoreFractions = map[int64]struct{}{0: {}, 5: {}, 20: {}, 50: {}, 100: {}}

// CreateInstanceReq — запрос на создание ВМ.
//
// Storage-split cutover: Instance.Create больше НЕ создаёт inline-диски и НЕ
// подключает тома (acceptance sec.0.3 — inline-attach out-of-scope). Инстанс создаётся
// без привязок; boot/secondary тома подключаются явными `AttachDisk` на уже
// существующих storage-Volume (vol-id).
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

// AttachDiskReq — параметры подключения существующего storage-Volume к инстансу.
type AttachDiskReq struct {
	VolumeID   string
	DeviceName string
	Mode       int32 // computev1.AttachedDiskSpec_Mode
	IsBoot     bool
	AutoDelete bool
}

// InstanceService — бизнес-логика управления ВМ + state-машина. Компьют держит
// НОЛЬ local attach-state: том↔Instance-привязка живёт в kacho-storage
// (storageClient → InternalVolumeService), NIC↔Instance — в kacho-vpc (nicClient).
type InstanceService struct {
	repo InstanceRepo
	// zones — existence-check zone_id (авторитет — kacho-geo).
	zones         ZoneRegistry
	projectClient ProjectClient
	// nicClient — compute→kacho-vpc InternalNetworkInterfaceService. Может быть nil.
	nicClient NicClient
	// storageClient — compute→kacho-storage InternalVolumeService (volume-attach
	// саги + batched mirror-read). Может быть nil (edge не сконфигурирован):
	// мутации fail-closed Unavailable, read-mirror грациозно опускается.
	storageClient StorageClient
	opsRepo       operations.Repo
}

// NewInstanceService создаёт InstanceService.
func NewInstanceService(repo InstanceRepo, zones ZoneRegistry, projectClient ProjectClient, nicClient NicClient, storageClient StorageClient, opsRepo operations.Repo) *InstanceService {
	return &InstanceService{
		repo: repo, zones: zones, projectClient: projectClient,
		nicClient: nicClient, storageClient: storageClient, opsRepo: opsRepo,
	}
}

// Get возвращает Instance по ID. NIC- и volume-зеркала подтягиваются из kacho-vpc /
// kacho-storage (source of truth) с graceful-degrade — недоступность owner'а НЕ
// роняет Get (consumer грациозно переживает недоступность owner'а).
func (s *InstanceService) Get(ctx context.Context, id string) (*domain.Instance, error) {
	in, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	s.applyNicMirror(ctx, in)
	s.applyVolumeMirror(ctx, in)
	return in, nil
}

// List возвращает список ВМ. project_id обязателен. NIC- и volume-зеркала
// резолвятся ОДНИМ batched-вызовом каждый (не N+1) с graceful-degrade.
func (s *InstanceService) List(ctx context.Context, f InstanceFilter, p Pagination) ([]*domain.Instance, string, error) {
	if f.ProjectID == "" {
		return nil, "", status.Error(codes.InvalidArgument, "project_id required")
	}
	out, next, err := s.repo.List(ctx, f, p)
	if err != nil {
		return nil, "", err
	}
	s.applyNicMirrorBatch(ctx, out)
	s.applyVolumeMirrorBatch(ctx, out)
	return out, next, nil
}

// ValidateCreateInstanceReq — синхронная pre-flight валидация Create-запроса
// (формат/диапазоны полей). Чистая (без DB/peer-вызовов). Выделена для fuzz.
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
	return nil
}

// Create инициирует создание Instance (без привязок — storage-split sec.0.3).
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
	}
	created, err := s.repo.Insert(ctx, in)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return anypb.New(protoconv.Instance(created))
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
			labelsInMask := false
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
		"placement_policy": {}, "network_settings": {},
		"resources_spec": {}, "platform_id": {},
	}
	for _, f := range req.UpdateMask {
		switch f {
		case "zone_id", "boot_disk", "boot_volume", "secondary_volumes", "network_interfaces", "metadata":
			return invalidArg(f, f+" is immutable after Instance.Create (use AttachDisk/UpdateMetadata/Relocate)")
		case "scheduling_policy", "metadata_options":
			return invalidArg(f, f+" is immutable after Instance.Create")
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
			updated, err := s.repo.MergeMetadata(ctx, instanceID, del, upsert)
			if err != nil {
				return nil, mapRepoErr(err)
			}
			return anypb.New(protoconv.Instance(updated))
		})
}

// Start/Stop/Restart — state-машина (DB-уровневый atomic CAS).
func (s *InstanceService) Start(ctx context.Context, id string) (*operations.Operation, error) {
	return s.lifecycle(ctx, id, "Start", domain.InstanceStatusStopped, domain.InstanceStatusRunning,
		"Instance is not stopped", &computev1.StartInstanceMetadata{InstanceId: id})
}

// Stop переводит ВМ RUNNING→STOPPED.
func (s *InstanceService) Stop(ctx context.Context, id string) (*operations.Operation, error) {
	return s.lifecycle(ctx, id, "Stop", domain.InstanceStatusRunning, domain.InstanceStatusStopped,
		"Instance is not running", &computev1.StopInstanceMetadata{InstanceId: id})
}

// Restart перезапускает RUNNING ВМ (single atomic CAS RUNNING→RUNNING).
func (s *InstanceService) Restart(ctx context.Context, id string) (*operations.Operation, error) {
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

// mapLifecycleErr маппит ошибку SetStatusCAS: CAS-промах → FailedPrecondition
// с precondMsg; остальное — стандартный mapRepoErr.
func mapLifecycleErr(err error, precondMsg string) error {
	if errors.Is(err, ErrFailedPrecondition) {
		return status.Error(codes.FailedPrecondition, precondMsg)
	}
	return mapRepoErr(err)
}

// AttachDisk подключает storage-Volume к ВМ (async сага → kacho-storage).
//
// Sync-фаза: malformed instance/volume-id первым стейтментом (sec.3.1). Async-worker:
// compute-local CAS-гейт (GateForAttach: state ∈ {RUNNING, STOPPED} + self-describing
// zone/project/name) → storage.Attach (fail-closed Unavailable, идемпотентный replay,
// zone/project-coherence + attach-CAS на стороне storage). Компьют attach-строку
// локально НЕ пишет (storage — владелец привязки; ацикличность).
func (s *InstanceService) AttachDisk(ctx context.Context, instanceID string, req AttachDiskReq) (*operations.Operation, error) {
	if instanceID == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	// Malformed-id первым стейтментом (sync InvalidArgument, до Operation).
	if err := corevalidate.ResourceID(insResource, ids.PrefixInstance, instanceID); err != nil {
		return nil, err
	}
	if req.VolumeID == "" {
		return nil, invalidArg("volume_id", "volume_id is required")
	}
	if err := validateVolumeID(req.VolumeID); err != nil {
		return nil, err
	}
	return runOp(ctx, s.opsRepo, fmt.Sprintf("Attach disk to instance %s", instanceID),
		&computev1.AttachInstanceDiskMetadata{InstanceId: instanceID, VolumeId: req.VolumeID},
		func(ctx context.Context) (*anypb.Any, error) {
			zoneID, projectID, name, err := s.repo.GateForAttach(ctx, instanceID)
			if err != nil {
				return nil, mapRepoErr(err)
			}
			if s.storageClient == nil {
				return nil, status.Error(codes.Unavailable, "volume service unavailable")
			}
			if _, err := s.storageClient.Attach(ctx, VolumeAttachSpec{
				VolumeID:       req.VolumeID,
				InstanceID:     instanceID,
				InstanceName:   name,
				InstanceZoneID: zoneID,
				ProjectID:      projectID,
				DeviceName:     req.DeviceName,
				IsBoot:         req.IsBoot,
				Mode:           VolumeAttachMode(req.Mode),
				AutoDelete:     req.AutoDelete,
			}); err != nil {
				return nil, err // storage-client уже нормализовал (leak-guard + contract codes)
			}
			return s.reloadWithMirror(ctx, instanceID)
		})
}

// DetachDisk отвязывает том (по volume_id ЛИБО device_name; boot нельзя).
// Идемпотентно: том не привязан → done no-op. Привязку резолвит storage
// (compute local attach-state нет) — источник истины для volume_id/is_boot.
func (s *InstanceService) DetachDisk(ctx context.Context, instanceID, volumeID, deviceName string) (*operations.Operation, error) {
	if instanceID == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	// oneof exactly_one: ровно одно из volume_id / device_name.
	if (volumeID == "") == (deviceName == "") {
		return nil, invalidArg("disk", "exactly one of volume_id or device_name is required")
	}
	if err := corevalidate.ResourceID(insResource, ids.PrefixInstance, instanceID); err != nil {
		return nil, err
	}
	if err := validateVolumeID(volumeID); err != nil {
		return nil, err
	}
	return runOp(ctx, s.opsRepo, fmt.Sprintf("Detach disk from instance %s", instanceID),
		&computev1.DetachInstanceDiskMetadata{InstanceId: instanceID, VolumeId: volumeID},
		func(ctx context.Context) (*anypb.Any, error) {
			if s.storageClient == nil {
				return nil, status.Error(codes.Unavailable, "volume service unavailable")
			}
			atts, err := s.storageClient.ListAttachments(ctx, []string{instanceID})
			if err != nil {
				return nil, err // fail-closed (Unavailable) — не роняем detach в INTERNAL
			}
			var target *VolumeAttachmentInfo
			for i := range atts {
				a := &atts[i]
				if (volumeID != "" && a.VolumeID == volumeID) || (deviceName != "" && a.DeviceName == deviceName) {
					target = a
					break
				}
			}
			if target == nil {
				// Уже отвязан — идемпотентный no-op.
				return s.reloadWithMirror(ctx, instanceID)
			}
			if target.IsBoot {
				return nil, status.Error(codes.FailedPrecondition, "boot volume cannot be detached")
			}
			if err := s.storageClient.Detach(ctx, target.VolumeID, instanceID); err != nil {
				return nil, err
			}
			return s.reloadWithMirror(ctx, instanceID)
		})
}

// SimulateMaintenanceEvent — no-op (control-plane: done-операция с самой ВМ).
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

// Delete инициирует удаление ВМ (delete-сага, M2).
//
// Порядок (crash-safe, идемпотентный): (1) гейт instance→DELETING (конкурентный
// AttachDisk-гейт видит DELETING и падает — attach-vs-delete race); (2) release всех
// NIC-привязок через kacho-vpc (fail-closed Unavailable — не оставляем dangling);
// (3) release всех volume-привязок через kacho-storage (fail-closed); (4) строка
// инстанса удаляется ПОСЛЕДНЕЙ. Списки привязок пересчитываются из storage/vpc на
// каждом прогоне (self-describing) → replay идемпотентен: уже снятая привязка
// возвращается пустым списком, повторный Detach — no-op. Crash после любого шага
// оставляет консистентное состояние (строка инстанса ещё жива → привязки резолвятся).
//
// NB: удаление auto_delete-томов (storage Volume.Delete) вынесено в отдельный
// storage-side инкремент (acceptance sec.0.3) — здесь привязки лишь СНИМАЮТСЯ
// (detach), что закрывает найденный go-review NIC/volume-leak.
func (s *InstanceService) Delete(ctx context.Context, id string) (*operations.Operation, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	return runOp(ctx, s.opsRepo, fmt.Sprintf("Delete instance %s", id),
		&computev1.DeleteInstanceMetadata{InstanceId: id},
		func(ctx context.Context) (*anypb.Any, error) {
			// (1) gate → DELETING. Уже удалён (crash-replay) → идемпотентный success.
			if _, err := s.repo.MarkDeleting(ctx, id); err != nil {
				if errors.Is(err, ErrNotFound) {
					return anypb.New(&emptypb.Empty{})
				}
				return nil, mapRepoErr(err)
			}
			// (2) release NICs (fail-closed).
			if s.nicClient != nil {
				nics, err := s.nicClient.ListByInstance(ctx, []string{id})
				if err != nil {
					return nil, mapNicErr(err)
				}
				for i := range nics {
					if err := s.nicClient.Detach(ctx, nics[i].NICID, id); err != nil {
						return nil, mapNicErr(err)
					}
				}
			}
			// (3) release volumes (fail-closed).
			if s.storageClient != nil {
				vols, err := s.storageClient.ListAttachments(ctx, []string{id})
				if err != nil {
					return nil, err
				}
				for i := range vols {
					if err := s.storageClient.Detach(ctx, vols[i].VolumeID, id); err != nil {
						return nil, err
					}
				}
			}
			// (4) delete instance row LAST.
			if err := s.repo.Delete(ctx, id); err != nil {
				if errors.Is(err, ErrNotFound) {
					return anypb.New(&emptypb.Empty{})
				}
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

// ---- mirrors (read-only проекции attach-состояния из storage/vpc) ----

// reloadWithMirror перечитывает инстанс и накладывает NIC/volume-зеркала — общий
// хвост attach/detach-саг, возвращающий свежий Instance-снимок.
func (s *InstanceService) reloadWithMirror(ctx context.Context, id string) (*anypb.Any, error) {
	in, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	s.applyNicMirror(ctx, in)
	s.applyVolumeMirror(ctx, in)
	return anypb.New(protoconv.Instance(in))
}

// applyVolumeMirror заполняет in.AttachedDisks read-only зеркалом из kacho-storage
// (source of truth). Graceful-degrade: nil-client или ошибка storage → зеркало
// опускается (Get/List не падают).
func (s *InstanceService) applyVolumeMirror(ctx context.Context, in *domain.Instance) {
	if s.storageClient == nil || in == nil {
		return
	}
	atts, err := s.storageClient.ListAttachments(ctx, []string{in.ID})
	if err != nil {
		return
	}
	in.AttachedDisks = volumeMirror(atts)
}

// applyVolumeMirrorBatch — batched (не N+1) зеркало для List: один ListAttachments
// по всем id, затем раскладка по инстансам. Graceful-degrade как applyVolumeMirror.
func (s *InstanceService) applyVolumeMirrorBatch(ctx context.Context, list []*domain.Instance) {
	if s.storageClient == nil || len(list) == 0 {
		return
	}
	instIDs := make([]string, 0, len(list))
	for _, in := range list {
		instIDs = append(instIDs, in.ID)
	}
	atts, err := s.storageClient.ListAttachments(ctx, instIDs)
	if err != nil {
		return
	}
	byInstance := make(map[string][]VolumeAttachmentInfo, len(list))
	for _, a := range atts {
		byInstance[a.InstanceID] = append(byInstance[a.InstanceID], a)
	}
	for _, in := range list {
		in.AttachedDisks = volumeMirror(byInstance[in.ID])
	}
}

// volumeMirror конвертирует storage volume-attachments в domain.AttachedDisk
// (read-only зеркало), boot первым, затем по device_name — детерминированный порядок.
func volumeMirror(atts []VolumeAttachmentInfo) []domain.AttachedDisk {
	if len(atts) == 0 {
		return nil
	}
	sorted := make([]VolumeAttachmentInfo, len(atts))
	copy(sorted, atts)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].IsBoot != sorted[j].IsBoot {
			return sorted[i].IsBoot // boot первым
		}
		return sorted[i].DeviceName < sorted[j].DeviceName
	})
	out := make([]domain.AttachedDisk, 0, len(sorted))
	for i := range sorted {
		a := &sorted[i]
		out = append(out, domain.AttachedDisk{
			DiskID:     a.VolumeID,
			IsBoot:     a.IsBoot,
			Mode:       domain.AttachedDiskMode(a.Mode),
			DeviceName: a.DeviceName,
			AutoDelete: a.AutoDelete,
		})
	}
	return out
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

func defaultCoreFraction(cf int64) int64 {
	if cf == 0 {
		return 100
	}
	return cf
}

func fqdn(id, hostname string) string {
	if hostname != "" {
		return hostname + ".kacho.internal"
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
