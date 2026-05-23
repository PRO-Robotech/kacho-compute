package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	corevalidate "github.com/PRO-Robotech/kacho-corelib/validate"
	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/fgawrite"
	"github.com/PRO-Robotech/kacho-compute/internal/protoconv"
)

// Disk size bounds (из proto `(value)`): Create [4 MiB .. 26 TiB], Update [4 MiB .. 4 TiB].
const (
	diskSizeMin       = 4194304
	diskSizeMaxCreate = 28587302322176
	diskSizeMaxUpdate = 4398046511104
	defaultDiskType   = "network-ssd"
	defaultBlockSize  = 4096
)

// CreateDiskReq — запрос на создание диска.
type CreateDiskReq struct {
	ProjectID           string
	Name                string
	Description         string
	Labels              map[string]string
	TypeID              string
	ZoneID              string
	Size                int64
	BlockSize           int64
	ImageID             string
	SnapshotID          string
	DiskPlacementPolicy *computev1.DiskPlacementPolicy
	HardwareGeneration  *computev1.HardwareGeneration
	KMSKeyID            string
}

// UpdateDiskReq — запрос на обновление диска.
type UpdateDiskReq struct {
	DiskID              string
	Name                string
	Description         string
	Labels              map[string]string
	Size                int64
	DiskPlacementPolicy *computev1.DiskPlacementPolicy
	UpdateMask          []string
}

// DiskService — бизнес-логика управления дисками.
type DiskService struct {
	repo         DiskRepo
	imageRepo    ImageRepo
	snapshotRepo SnapshotRepo
	diskTypeRepo DiskTypeRepo
	// zones — existence-check zone_id. Авторитетный источник — kacho-vpc
	// InternalZoneService (compute зон не владеет); при SKIP_PEER_VALIDATION —
	// fallback на локальную таблицу `zones`. Wiring — cmd/compute/main.go.
	zones         ZoneRegistry
	projectClient ProjectClient
	opsRepo       operations.Repo
	// fgaWriter — write-side OpenFGA: publishes compute_disk hierarchy tuple
	// after Create. nil = FGA write disabled (dev/no-config). KAC-133.
	fgaWriter fgawrite.HierarchyTupleWriter
	logger    *slog.Logger
}

// NewDiskService создаёт DiskService.
func NewDiskService(repo DiskRepo, imageRepo ImageRepo, snapshotRepo SnapshotRepo, diskTypeRepo DiskTypeRepo, zones ZoneRegistry, projectClient ProjectClient, opsRepo operations.Repo, fgaWriter fgawrite.HierarchyTupleWriter, logger *slog.Logger) *DiskService {
	return &DiskService{
		repo: repo, imageRepo: imageRepo, snapshotRepo: snapshotRepo,
		diskTypeRepo: diskTypeRepo, zones: zones, projectClient: projectClient, opsRepo: opsRepo,
		fgaWriter: fgaWriter, logger: logger,
	}
}

// Get возвращает Disk по ID.
func (s *DiskService) Get(ctx context.Context, id string) (*domain.Disk, error) {
	d, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return d, nil
}

// List возвращает список дисков. project_id обязателен.
func (s *DiskService) List(ctx context.Context, f DiskFilter, p Pagination) ([]*domain.Disk, string, error) {
	if f.ProjectID == "" {
		return nil, "", status.Error(codes.InvalidArgument, "project_id required")
	}
	return s.repo.List(ctx, f, p)
}

// Create инициирует создание Disk.
func (s *DiskService) Create(ctx context.Context, req CreateDiskReq) (*operations.Operation, error) {
	if req.ProjectID == "" {
		return nil, status.Error(codes.InvalidArgument, "project_id required")
	}
	if req.ZoneID == "" {
		return nil, status.Error(codes.InvalidArgument, "zone_id required")
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
	if req.Size < diskSizeMin || req.Size > diskSizeMaxCreate {
		return nil, invalidArg("size", fmt.Sprintf("size must be in range [%d, %d] bytes", diskSizeMin, diskSizeMaxCreate))
	}
	if req.ImageID != "" && req.SnapshotID != "" {
		return nil, invalidArg("source", "only one of image_id or snapshot_id may be set")
	}
	if req.KMSKeyID != "" {
		return nil, status.Error(codes.Unimplemented, "disk encryption (kms_key_id) requires kacho-kms — not yet implemented (blocked:kacho-kms)")
	}

	diskID := ids.NewID(ids.PrefixDisk)
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Create disk %s", req.Name),
		&computev1.CreateDiskMetadata{DiskId: diskID})
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}
	operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		return s.doCreate(ctx, diskID, req)
	})
	return &op, nil
}

func (s *DiskService) doCreate(ctx context.Context, diskID string, req CreateDiskReq) (*anypb.Any, error) {
	if err := s.checkFolder(ctx, req.ProjectID); err != nil {
		return nil, err
	}
	if _, err := s.zones.GetZone(ctx, req.ZoneID); err != nil {
		return nil, mapZoneRefErr(err, req.ZoneID)
	}
	typeID := req.TypeID
	if typeID == "" {
		typeID = defaultDiskType
	}
	if _, err := s.diskTypeRepo.Get(ctx, typeID); err != nil {
		return nil, status.Errorf(codes.NotFound, "Disk type %s not found", typeID)
	}
	blockSize := req.BlockSize
	if blockSize == 0 {
		blockSize = defaultBlockSize
	}

	switch {
	case req.ImageID != "":
		img, err := s.imageRepo.Get(ctx, req.ImageID)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "Image %s not found", req.ImageID)
		}
		if img.MinDiskSize > 0 && req.Size < img.MinDiskSize {
			return nil, status.Errorf(codes.InvalidArgument, "Disk size %d is less than image min_disk_size %d", req.Size, img.MinDiskSize)
		}
	case req.SnapshotID != "":
		snap, err := s.snapshotRepo.Get(ctx, req.SnapshotID)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "Snapshot %s not found", req.SnapshotID)
		}
		if snap.DiskSize > 0 && req.Size < snap.DiskSize {
			return nil, status.Errorf(codes.InvalidArgument, "Disk size %d is less than snapshot disk_size %d", req.Size, snap.DiskSize)
		}
	}

	d := &domain.Disk{
		ID:                  diskID,
		ProjectID:           req.ProjectID,
		CreatedAt:           time.Now().UTC(),
		Name:                req.Name,
		Description:         req.Description,
		Labels:              req.Labels,
		TypeID:              typeID,
		ZoneID:              req.ZoneID,
		Size:                req.Size,
		BlockSize:           blockSize,
		Status:              domain.DiskStatusReady, // control-plane only
		SourceImageID:       req.ImageID,
		SourceSnapshotID:    req.SnapshotID,
		DiskPlacementPolicy: req.DiskPlacementPolicy,
		HardwareGeneration:  req.HardwareGeneration,
	}
	created, err := s.repo.Insert(ctx, d)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	// KAC-133: publish FGA hierarchy tuple so per-resource Check (Get/Update/Delete)
	// can cascade from compute_disk:<id>#project to project:<project_id>.
	fgawrite.Emit(ctx, s.fgaWriter, s.logger, "compute_disk", created.ID, created.ProjectID)
	return anypb.New(protoconv.Disk(created))
}

// Update обновляет Disk.
func (s *DiskService) Update(ctx context.Context, req UpdateDiskReq) (*operations.Operation, error) {
	if req.DiskID == "" {
		return nil, status.Error(codes.InvalidArgument, "disk_id required")
	}
	if err := validateDiskUpdate(req); err != nil {
		return nil, err
	}
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Update disk %s", req.DiskID),
		&computev1.UpdateDiskMetadata{DiskId: req.DiskID})
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

func (s *DiskService) doUpdate(ctx context.Context, req UpdateDiskReq) (*anypb.Any, error) {
	d, err := s.repo.Get(ctx, req.DiskID)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	updates := req.UpdateMask
	if len(updates) == 0 {
		updates = []string{"name", "description", "labels", "size", "disk_placement_policy"}
	}
	for _, f := range updates {
		switch f {
		case "name":
			d.Name = req.Name
		case "description":
			d.Description = req.Description
		case "labels":
			d.Labels = req.Labels
		case "size":
			// silent-ignore size==0 при full-PATCH; explicit-mask size требует увеличения.
			if len(req.UpdateMask) == 0 && req.Size == 0 {
				continue
			}
			if req.Size < d.Size {
				return nil, status.Error(codes.InvalidArgument, "Disk size can only be increased")
			}
			if req.Size > diskSizeMaxUpdate {
				return nil, status.Errorf(codes.InvalidArgument, "size must be at most %d bytes", diskSizeMaxUpdate)
			}
			d.Size = req.Size
		case "disk_placement_policy":
			d.DiskPlacementPolicy = req.DiskPlacementPolicy
		}
	}
	updated, err := s.repo.Update(ctx, d)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return anypb.New(protoconv.Disk(updated))
}

func validateDiskUpdate(req UpdateDiskReq) error {
	known := map[string]struct{}{
		"name": {}, "description": {}, "labels": {}, "size": {}, "disk_placement_policy": {},
	}
	if err := corevalidate.UpdateMask("update_mask", req.UpdateMask, known); err != nil {
		return err
	}
	for _, f := range req.UpdateMask {
		switch f {
		case "type_id", "zone_id", "block_size", "source":
			return invalidArg(f, f+" is immutable after Disk.Create")
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

// Delete удаляет Disk (FailedPrecondition если attached).
func (s *DiskService) Delete(ctx context.Context, id string) (*operations.Operation, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "disk_id required")
	}
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Delete disk %s", id),
		&computev1.DeleteDiskMetadata{DiskId: id})
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}
	operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		attached, err := s.repo.IsAttached(ctx, id)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		if attached {
			return nil, status.Errorf(codes.FailedPrecondition, "The disk %s is being used", id)
		}
		if err := s.repo.Delete(ctx, id); err != nil {
			return nil, mapRepoErr(err)
		}
		return anypb.New(&emptypb.Empty{})
	})
	return &op, nil
}

// Move инициирует перенос Disk в другой folder.
func (s *DiskService) Move(ctx context.Context, id, destProjectID string) (*operations.Operation, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "disk_id required")
	}
	if destProjectID == "" {
		return nil, invalidArg("destination_project_id", "destination_project_id is required")
	}
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Move disk %s", id),
		&computev1.MoveDiskMetadata{DiskId: id, DestinationProjectId: destProjectID})
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
		return anypb.New(protoconv.Disk(updated))
	})
	return &op, nil
}

// Relocate инициирует перенос Disk в другую зону (precondition: disk не attached).
func (s *DiskService) Relocate(ctx context.Context, id, destZoneID string) (*operations.Operation, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "disk_id required")
	}
	if destZoneID == "" {
		return nil, invalidArg("destination_zone_id", "destination_zone_id is required")
	}
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Relocate disk %s", id),
		&computev1.RelocateDiskMetadata{DiskId: id, DestinationZoneId: destZoneID})
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}
	operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		if _, err := s.zones.GetZone(ctx, destZoneID); err != nil {
			return nil, mapZoneRefErr(err, destZoneID)
		}
		attached, err := s.repo.IsAttached(ctx, id)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		if attached {
			return nil, status.Error(codes.FailedPrecondition, "Disk is in use")
		}
		updated, err := s.repo.SetZoneID(ctx, id, destZoneID)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		return anypb.New(protoconv.Disk(updated))
	})
	return &op, nil
}

// ListOperations возвращает операции для конкретного Disk.
func (s *DiskService) ListOperations(ctx context.Context, id string, p Pagination) ([]operations.Operation, string, error) {
	if _, err := s.repo.Get(ctx, id); err != nil {
		return nil, "", mapRepoErr(err)
	}
	return s.opsRepo.List(ctx, operations.ListFilter{ResourceID: id, PageSize: p.PageSize, PageToken: p.PageToken})
}

func (s *DiskService) checkFolder(ctx context.Context, folderID string) error {
	exists, err := s.projectClient.Exists(ctx, folderID)
	if err != nil {
		return status.Errorf(codes.Unavailable, "folder check: %v", err)
	}
	if !exists {
		return status.Errorf(codes.NotFound, "Folder with id %s not found", folderID)
	}
	return nil
}
