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

// SnapshotService — бизнес-логика управления снимками дисков.
type SnapshotService struct {
	repo     SnapshotRepo
	diskRepo DiskRepo
	opsRepo  operations.Repo
}

// NewSnapshotService создаёт SnapshotService.
func NewSnapshotService(
	repo SnapshotRepo,
	diskRepo DiskRepo,
	opsRepo operations.Repo,
) *SnapshotService {
	return &SnapshotService{
		repo:     repo,
		diskRepo: diskRepo,
		opsRepo:  opsRepo,
	}
}

// Get возвращает Snapshot по ID.
func (s *SnapshotService) Get(ctx context.Context, id string) (*domain.Snapshot, error) {
	snap, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return snap, nil
}

// List возвращает список Snapshot.
func (s *SnapshotService) List(ctx context.Context, f SnapshotFilter, page Pagination) ([]*domain.Snapshot, string, error) {
	return s.repo.List(ctx, f, page)
}

// Create создаёт Operation + инициирует создание Snapshot.
func (s *SnapshotService) Create(ctx context.Context, req CreateSnapshotReq) (*operations.Operation, error) {
	if req.FolderID == "" {
		return nil, status.Error(codes.InvalidArgument, "folder_id required")
	}
	if req.DiskID == "" {
		return nil, status.Error(codes.InvalidArgument, "disk_id required")
	}

	snapID := ids.NewUID()
	op, err := operations.New(
		fmt.Sprintf("Create snapshot %s", req.Name),
		&computev1.CreateSnapshotMetadata{SnapshotId: snapID},
	)
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}

	operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		return s.doCreate(ctx, snapID, req)
	})

	return &op, nil
}

func (s *SnapshotService) doCreate(ctx context.Context, snapID string, req CreateSnapshotReq) (*anypb.Any, error) {
	disk, err := s.diskRepo.Get(ctx, req.DiskID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "disk %s not found", req.DiskID)
	}
	if disk.Status != domain.DiskStatusReady {
		return nil, status.Errorf(codes.FailedPrecondition, "disk must be READY to create snapshot")
	}

	now := time.Now().UTC()
	snap := &domain.Snapshot{
		ID:              snapID,
		FolderID:        req.FolderID,
		Name:            req.Name,
		Description:     req.Description,
		CreatedAt:       now,
		Labels:          req.Labels,
		DiskID:          req.DiskID,
		Size:            disk.Size2Bytes(),
		Status:          domain.SnapshotStatusCreating,
		ProgressPercent: 0,
		Generation:      1,
		ResourceVersion: ids.NewUID(),
	}

	created, err := s.repo.Insert(ctx, snap)
	if err != nil {
		return nil, err
	}
	// Reconciler симулирует прогресс 0→25→50→75→100 → READY.
	return anypb.New(snapshotToProto(created))
}

// Update обновляет метаданные Snapshot.
func (s *SnapshotService) Update(ctx context.Context, req UpdateSnapshotReq) (*operations.Operation, error) {
	snap, err := s.repo.Get(ctx, req.SnapshotID)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	if req.ResourceVersion != "" && snap.ResourceVersion != req.ResourceVersion {
		return nil, status.Error(codes.Aborted, "resource version conflict")
	}

	op, err := operations.New(
		fmt.Sprintf("Update snapshot %s", req.SnapshotID),
		&computev1.UpdateSnapshotMetadata{SnapshotId: req.SnapshotID},
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

func (s *SnapshotService) doUpdate(ctx context.Context, req UpdateSnapshotReq) (*anypb.Any, error) {
	snap, err := s.repo.Get(ctx, req.SnapshotID)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	if req.Name != "" {
		snap.Name = req.Name
	}
	if req.Description != "" {
		snap.Description = req.Description
	}
	if req.Labels != nil {
		snap.Labels = req.Labels
	}
	snap.Generation++
	snap.ResourceVersion = ids.NewUID()

	updated, err := s.repo.Update(ctx, snap)
	if err != nil {
		return nil, err
	}
	return anypb.New(snapshotToProto(updated))
}

// Delete помечает Snapshot на удаление.
func (s *SnapshotService) Delete(ctx context.Context, snapshotID string) (*operations.Operation, error) {
	snap, err := s.repo.Get(ctx, snapshotID)
	if err != nil {
		return nil, mapRepoErr(err)
	}

	op, err := operations.New(
		fmt.Sprintf("Delete snapshot %s", snapshotID),
		&computev1.DeleteSnapshotMetadata{SnapshotId: snapshotID},
	)
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	snap.Status = domain.SnapshotStatusDeleting
	snap.DeletedAt = &now
	if _, err := s.repo.Update(ctx, snap); err != nil {
		return nil, err
	}
	return &op, nil
}

// ---- request types ----

// CreateSnapshotReq — параметры создания снимка.
type CreateSnapshotReq struct {
	FolderID    string
	DiskID      string
	Name        string
	Description string
	Labels      map[string]string
}

// UpdateSnapshotReq — параметры обновления снимка.
type UpdateSnapshotReq struct {
	SnapshotID      string
	ResourceVersion string
	Name            string
	Description     string
	Labels          map[string]string
}
