package service

import (
	"context"
	"fmt"
	"time"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
)

// DiskService — бизнес-логика управления блочными дисками.
type DiskService struct {
	repo         DiskRepo
	imageRepo    ImageRepo
	folderClient FolderClient
	opsRepo      operations.Repo
}

// NewDiskService создаёт DiskService.
func NewDiskService(
	repo DiskRepo,
	imageRepo ImageRepo,
	folderClient FolderClient,
	opsRepo operations.Repo,
) *DiskService {
	return &DiskService{
		repo:         repo,
		imageRepo:    imageRepo,
		folderClient: folderClient,
		opsRepo:      opsRepo,
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

// List возвращает список Disk.
func (s *DiskService) List(ctx context.Context, f DiskFilter, page Pagination) ([]*domain.Disk, string, error) {
	return s.repo.List(ctx, f, page)
}

// Create создаёт Operation + инициирует создание Disk.
func (s *DiskService) Create(ctx context.Context, req CreateDiskReq) (*operations.Operation, error) {
	if req.FolderID == "" {
		return nil, status.Error(codes.InvalidArgument, "folder_id required")
	}
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name required")
	}

	diskID := ids.NewUID()
	op, err := operations.New(
		fmt.Sprintf("Create disk %s", req.Name),
		&computev1.CreateDiskMetadata{DiskId: diskID},
	)
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
	exists, err := s.folderClient.Exists(ctx, req.FolderID)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "folder check: %v", err)
	}
	if !exists {
		return nil, status.Errorf(codes.NotFound, "folder %s not found", req.FolderID)
	}

	if req.ImageID != "" {
		if _, err := s.imageRepo.Get(ctx, req.ImageID); err != nil {
			return nil, status.Errorf(codes.NotFound, "image %s not found", req.ImageID)
		}
	}

	now := time.Now().UTC()
	d := &domain.Disk{
		ID:              diskID,
		FolderID:        req.FolderID,
		Name:            req.Name,
		Description:     req.Description,
		CreatedAt:       now,
		Labels:          req.Labels,
		DiskTypeID:      req.DiskTypeID,
		ZoneID:          req.ZoneID,
		Size:            req.Size,
		ImageID:         req.ImageID,
		Status:          domain.DiskStatusCreating,
		Generation:      1,
		ResourceVersion: ids.NewUID(),
		StatusLastTransitionAt: now,
	}

	created, err := s.repo.Insert(ctx, d)
	if err != nil {
		return nil, err
	}
	// Reconciler подхватит CREATING → READY.
	return anypb.New(diskToProto(created))
}

// Update обновляет метаданные Disk.
func (s *DiskService) Update(ctx context.Context, req UpdateDiskReq) (*operations.Operation, error) {
	d, err := s.repo.Get(ctx, req.DiskID)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	if req.ResourceVersion != "" && d.ResourceVersion != req.ResourceVersion {
		return nil, status.Error(codes.Aborted, "resource version conflict")
	}

	op, err := operations.New(
		fmt.Sprintf("Update disk %s", req.DiskID),
		&computev1.UpdateDiskMetadata{DiskId: req.DiskID},
	)
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
	if req.Name != "" {
		d.Name = req.Name
	}
	if req.Description != "" {
		d.Description = req.Description
	}
	if req.Labels != nil {
		d.Labels = req.Labels
	}
	d.Generation++
	d.ResourceVersion = ids.NewUID()
	d.StatusLastTransitionAt = time.Now().UTC()

	updated, err := s.repo.Update(ctx, d)
	if err != nil {
		return nil, err
	}
	return anypb.New(diskToProto(updated))
}

// Delete помечает Disk на удаление.
func (s *DiskService) Delete(ctx context.Context, diskID string) (*operations.Operation, error) {
	d, err := s.repo.Get(ctx, diskID)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	if d.AttachedToInstanceID != "" {
		return nil, status.Errorf(codes.FailedPrecondition, "disk is attached to instance %s", d.AttachedToInstanceID)
	}

	op, err := operations.New(
		fmt.Sprintf("Delete disk %s", diskID),
		&computev1.DeleteDiskMetadata{DiskId: diskID},
	)
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	d.Status = domain.DiskStatusDeleting
	d.DeletedAt = &now
	if _, err := s.repo.Update(ctx, d); err != nil {
		return nil, err
	}
	return &op, nil
}

// ---- request types ----

// CreateDiskReq — параметры создания диска.
type CreateDiskReq struct {
	FolderID   string
	Name       string
	Description string
	Labels     map[string]string
	DiskTypeID string
	ZoneID     string
	Size       string
	ImageID    string
}

// UpdateDiskReq — параметры обновления диска.
type UpdateDiskReq struct {
	DiskID          string
	ResourceVersion string
	Name            string
	Description     string
	Labels          map[string]string
}
