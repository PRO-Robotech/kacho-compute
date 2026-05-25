package service

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
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

// imageFamilyRe — verbatim YC Image.family pattern: `|[a-z][-a-z0-9]{1,61}[a-z0-9]`
// (пустая строка ИЛИ lowercase-letter + 1..61×[-a-z0-9] + letter/digit; нет underscore).
var imageFamilyRe = regexp.MustCompile(`^([a-z][-a-z0-9]{1,61}[a-z0-9])?$`)

// validateImageFamily проверяет family-поле Image (sync).
func validateImageFamily(family string) error {
	if !imageFamilyRe.MatchString(family) {
		return invalidArg("family", `family must match ^([a-z][-a-z0-9]{1,61}[a-z0-9])?$ (lowercase letters, digits, hyphens; 3..63 chars; empty allowed)`)
	}
	return nil
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// CreateImageReq — запрос на создание образа. source = ровно один из
// {ImageID, DiskID, SnapshotID, URI}.
type CreateImageReq struct {
	ProjectID          string
	Name               string
	Description        string
	Labels             map[string]string
	Family             string
	MinDiskSize        int64
	ProductIDs         []string
	ImageID            string
	DiskID             string
	SnapshotID         string
	URI                string
	Os                 *computev1.Os
	Pooled             bool
	HardwareGeneration *computev1.HardwareGeneration
}

// UpdateImageReq — запрос на обновление образа.
type UpdateImageReq struct {
	ImageID     string
	Name        string
	Description string
	Labels      map[string]string
	MinDiskSize int64
	UpdateMask  []string
}

// ImageService — бизнес-логика управления образами.
type ImageService struct {
	repo          ImageRepo
	diskRepo      DiskRepo
	snapshotRepo  SnapshotRepo
	projectClient ProjectClient
	opsRepo       operations.Repo

	// fgaWriter / logger — KAC-188 follow-up: after the Image row is committed,
	// publish `compute_image:<id>#project@project:<project_id>` so a per-resource
	// Check resolves. nil → no-op.
	fgaWriter fgawrite.HierarchyTupleWriter
	logger    *slog.Logger
}

// NewImageService создаёт ImageService.
func NewImageService(repo ImageRepo, diskRepo DiskRepo, snapshotRepo SnapshotRepo, projectClient ProjectClient, opsRepo operations.Repo) *ImageService {
	return &ImageService{repo: repo, diskRepo: diskRepo, snapshotRepo: snapshotRepo, projectClient: projectClient, opsRepo: opsRepo}
}

// WithFGAWriter wires the OpenFGA hierarchy-tuple writer (KAC-188 follow-up).
// Without it a created Image has no `compute_image:<id>#project@project` tuple
// and every per-resource Check is FGA `no path`.
func (s *ImageService) WithFGAWriter(w fgawrite.HierarchyTupleWriter, logger *slog.Logger) *ImageService {
	s.fgaWriter = w
	s.logger = logger
	return s
}

// Get возвращает Image по ID.
func (s *ImageService) Get(ctx context.Context, id string) (*domain.Image, error) {
	i, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return i, nil
}

// GetLatestByFamily возвращает самый новый Image в family внутри folder.
func (s *ImageService) GetLatestByFamily(ctx context.Context, folderID, family string) (*domain.Image, error) {
	if folderID == "" {
		return nil, status.Error(codes.InvalidArgument, "project_id required")
	}
	i, err := s.repo.GetLatestByFamily(ctx, folderID, family)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return i, nil
}

// List возвращает список образов. project_id обязателен.
func (s *ImageService) List(ctx context.Context, f ImageFilter, p Pagination) ([]*domain.Image, string, error) {
	if f.ProjectID == "" {
		return nil, "", status.Error(codes.InvalidArgument, "project_id required")
	}
	return s.repo.List(ctx, f, p)
}

// Create инициирует создание Image.
func (s *ImageService) Create(ctx context.Context, req CreateImageReq) (*operations.Operation, error) {
	if req.ProjectID == "" {
		return nil, status.Error(codes.InvalidArgument, "project_id required")
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
	if err := validateImageFamily(req.Family); err != nil {
		return nil, err
	}
	srcCount := 0
	for _, v := range []string{req.ImageID, req.DiskID, req.SnapshotID, req.URI} {
		if v != "" {
			srcCount++
		}
	}
	if srcCount != 1 {
		return nil, invalidArg("source", "exactly one of image_id, disk_id, snapshot_id or uri must be set")
	}

	imageID := ids.NewID(ids.PrefixImage)
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Create image %s", req.Name),
		&computev1.CreateImageMetadata{ImageId: imageID})
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}
	operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		return s.doCreate(ctx, imageID, req)
	})
	return &op, nil
}

func (s *ImageService) doCreate(ctx context.Context, imageID string, req CreateImageReq) (*anypb.Any, error) {
	if err := s.checkFolder(ctx, req.ProjectID); err != nil {
		return nil, err
	}
	// minDiskSize / storageSize наследуются от источника (verbatim YC: образ,
	// созданный из disk/snapshot/image, требует диск ≥ размера источника).
	minDiskSize := req.MinDiskSize
	storageSize := int64(0)
	switch {
	case req.ImageID != "":
		src, err := s.repo.Get(ctx, req.ImageID)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "Image %s not found", req.ImageID)
		}
		minDiskSize = maxInt64(minDiskSize, src.MinDiskSize)
		storageSize = src.StorageSize
	case req.SnapshotID != "":
		src, err := s.snapshotRepo.Get(ctx, req.SnapshotID)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "Snapshot %s not found", req.SnapshotID)
		}
		minDiskSize = maxInt64(minDiskSize, src.DiskSize)
		storageSize = src.DiskSize
	case req.DiskID != "":
		d, err := s.diskRepo.Get(ctx, req.DiskID)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "Disk %s not found", req.DiskID)
		}
		if d.Status != domain.DiskStatusReady {
			return nil, status.Errorf(codes.FailedPrecondition, "Disk %s is not READY", req.DiskID)
		}
		minDiskSize = maxInt64(minDiskSize, d.Size)
		storageSize = d.Size
	case req.URI != "":
		// control-plane: download via signed URL — мгновенно (status READY).
		// min_disk_size остаётся как задан в запросе (или 0 — раскроется при создании диска из образа).
	}

	i := &domain.Image{
		ID:                 imageID,
		ProjectID:          req.ProjectID,
		CreatedAt:          time.Now().UTC(),
		Name:               req.Name,
		Description:        req.Description,
		Labels:             req.Labels,
		Family:             req.Family,
		MinDiskSize:        minDiskSize,
		StorageSize:        storageSize,
		ProductIDs:         req.ProductIDs,
		Status:             domain.ImageStatusReady,           // control-plane only
		OsType:             domain.OsType(computev1.Os_LINUX), // verbatim YC default
		Pooled:             req.Pooled,
		HardwareGeneration: req.HardwareGeneration,
		SourceImageID:      req.ImageID,
		SourceSnapshotID:   req.SnapshotID,
		SourceDiskID:       req.DiskID,
		SourceURI:          req.URI,
	}
	if req.Os != nil {
		if req.Os.GetType() != computev1.Os_TYPE_UNSPECIFIED {
			i.OsType = domain.OsType(req.Os.GetType())
		}
		if req.Os.GetNvidia() != nil {
			i.OsNvidiaDriver = req.Os.GetNvidia().GetDriver()
		}
	}
	created, err := s.repo.Insert(ctx, i)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	// KAC-188 follow-up: publish the compute_image→project hierarchy tuple so a
	// per-resource Check resolves. Best-effort + non-fatal (row committed).
	fgawrite.Emit(ctx, s.fgaWriter, s.logger, "compute_image", created.ID, created.ProjectID)
	return anypb.New(protoconv.Image(created))
}

// Update обновляет Image.
func (s *ImageService) Update(ctx context.Context, req UpdateImageReq) (*operations.Operation, error) {
	if req.ImageID == "" {
		return nil, status.Error(codes.InvalidArgument, "image_id required")
	}
	if err := validateImageUpdate(req); err != nil {
		return nil, err
	}
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Update image %s", req.ImageID),
		&computev1.UpdateImageMetadata{ImageId: req.ImageID})
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}
	operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		i, err := s.repo.Get(ctx, req.ImageID)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		updates := req.UpdateMask
		if len(updates) == 0 {
			updates = []string{"name", "description", "labels", "min_disk_size"}
		}
		for _, f := range updates {
			switch f {
			case "name":
				i.Name = req.Name
			case "description":
				i.Description = req.Description
			case "labels":
				i.Labels = req.Labels
			case "min_disk_size":
				if len(req.UpdateMask) == 0 && req.MinDiskSize == 0 {
					continue
				}
				i.MinDiskSize = req.MinDiskSize
			}
		}
		updated, err := s.repo.Update(ctx, i)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		return anypb.New(protoconv.Image(updated))
	})
	return &op, nil
}

func validateImageUpdate(req UpdateImageReq) error {
	known := map[string]struct{}{"name": {}, "description": {}, "labels": {}, "min_disk_size": {}}
	if err := corevalidate.UpdateMask("update_mask", req.UpdateMask, known); err != nil {
		return err
	}
	for _, f := range req.UpdateMask {
		switch f {
		case "family", "os", "product_ids", "pooled":
			return invalidArg(f, f+" is immutable after Image.Create")
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

// Delete удаляет Image.
func (s *ImageService) Delete(ctx context.Context, id string) (*operations.Operation, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "image_id required")
	}
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Delete image %s", id),
		&computev1.DeleteImageMetadata{ImageId: id})
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}
	operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		if err := s.repo.Delete(ctx, id); err != nil {
			return nil, mapRepoErr(err)
		}
		return anypb.New(&emptypb.Empty{})
	})
	return &op, nil
}

// ListOperations возвращает операции для Image.
func (s *ImageService) ListOperations(ctx context.Context, id string, p Pagination) ([]operations.Operation, string, error) {
	if _, err := s.repo.Get(ctx, id); err != nil {
		return nil, "", mapRepoErr(err)
	}
	return s.opsRepo.List(ctx, operations.ListFilter{ResourceID: id, PageSize: p.PageSize, PageToken: p.PageToken})
}

func (s *ImageService) checkFolder(ctx context.Context, folderID string) error {
	exists, err := s.projectClient.Exists(ctx, folderID)
	if err != nil {
		return status.Errorf(codes.Unavailable, "folder check: %v", err)
	}
	if !exists {
		return status.Errorf(codes.NotFound, "Folder with id %s not found", folderID)
	}
	return nil
}
