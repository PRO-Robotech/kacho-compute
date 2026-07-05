// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package service

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	corevalidate "github.com/PRO-Robotech/kacho-corelib/validate"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/protoconv"
)

// CreateSnapshotReq — запрос на создание снапшота (disk_id обязателен).
type CreateSnapshotReq struct {
	ProjectID          string
	DiskID             string
	Name               string
	Description        string
	Labels             map[string]string
	HardwareGeneration *computev1.HardwareGeneration
}

// UpdateSnapshotReq — запрос на обновление снапшота.
type UpdateSnapshotReq struct {
	SnapshotID  string
	Name        string
	Description string
	Labels      map[string]string
	UpdateMask  []string
}

// SnapshotService — бизнес-логика управления снапшотами.
type SnapshotService struct {
	repo          SnapshotRepo
	diskRepo      DiskRepo
	projectClient ProjectClient
	opsRepo       operations.Repo
}

// NewSnapshotService создаёт SnapshotService.
func NewSnapshotService(repo SnapshotRepo, diskRepo DiskRepo, projectClient ProjectClient, opsRepo operations.Repo) *SnapshotService {
	return &SnapshotService{repo: repo, diskRepo: diskRepo, projectClient: projectClient, opsRepo: opsRepo}
}

// Get возвращает Snapshot по ID.
func (s *SnapshotService) Get(ctx context.Context, id string) (*domain.Snapshot, error) {
	snap, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return snap, nil
}

// List возвращает список снапшотов. project_id обязателен.
func (s *SnapshotService) List(ctx context.Context, f SnapshotFilter, p Pagination) ([]*domain.Snapshot, string, error) {
	if f.ProjectID == "" {
		return nil, "", status.Error(codes.InvalidArgument, "project_id required")
	}
	return s.repo.List(ctx, f, p)
}

// Create инициирует создание Snapshot из Disk.
func (s *SnapshotService) Create(ctx context.Context, req CreateSnapshotReq) (*operations.Operation, error) {
	if req.ProjectID == "" {
		return nil, status.Error(codes.InvalidArgument, "project_id required")
	}
	if req.DiskID == "" {
		return nil, status.Error(codes.InvalidArgument, "disk_id required")
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
	snapID := ids.NewID(ids.PrefixSnapshot)
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Create snapshot %s", req.Name),
		&computev1.CreateSnapshotMetadata{SnapshotId: snapID, DiskId: req.DiskID})
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
	exists, err := s.projectClient.Exists(ctx, req.ProjectID)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "folder check: upstream project service unavailable")
	}
	if !exists {
		return nil, status.Errorf(codes.NotFound, "Folder with id %s not found", req.ProjectID)
	}
	d, err := s.diskRepo.Get(ctx, req.DiskID)
	if err != nil {
		return nil, mapRefErr(err, "Disk", req.DiskID)
	}
	if d.Status != domain.DiskStatusReady {
		return nil, status.Errorf(codes.FailedPrecondition, "Disk %s is not READY", req.DiskID)
	}
	snap := &domain.Snapshot{
		ID:                 snapID,
		ProjectID:          req.ProjectID,
		CreatedAt:          time.Now().UTC(),
		Name:               req.Name,
		Description:        req.Description,
		Labels:             req.Labels,
		StorageSize:        d.Size,
		DiskSize:           d.Size,
		Status:             domain.SnapshotStatusReady, // control-plane only
		SourceDiskID:       req.DiskID,
		HardwareGeneration: req.HardwareGeneration,
	}
	created, err := s.repo.Insert(ctx, snap)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	// the compute_snapshot→project owner-tuple is registered transactionally
	// via the FGA register-intent written in repo.Insert's writer-tx and applied by
	// the register-drainer through kacho-iam (no direct FGA, no dual-write).
	return anypb.New(protoconv.Snapshot(created))
}

// Update обновляет Snapshot.
func (s *SnapshotService) Update(ctx context.Context, req UpdateSnapshotReq) (*operations.Operation, error) {
	if req.SnapshotID == "" {
		return nil, status.Error(codes.InvalidArgument, "snapshot_id required")
	}
	if err := validateSnapshotUpdate(req); err != nil {
		return nil, err
	}
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Update snapshot %s", req.SnapshotID),
		&computev1.UpdateSnapshotMetadata{SnapshotId: req.SnapshotID})
	if err != nil {
		return nil, err
	}
	if err := s.opsRepo.Create(ctx, op); err != nil {
		return nil, err
	}
	operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		snap, err := s.repo.Get(ctx, req.SnapshotID)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		updates := req.UpdateMask
		if len(updates) == 0 {
			updates = []string{"name", "description", "labels"}
		}
		// labelsInMask (parity с InstanceService.Update): triggers an FGA
		// register-intent refresh (mirror.upsert) so label-scoped grants revoke on
		// label-remove/change. Empty mask = full-PATCH includes labels.
		labelsInMask := false
		// changed — фактически изменённые колонки (column-scoped UPDATE, no lost update).
		changed := make([]string, 0, len(updates))
		for _, f := range updates {
			switch f {
			case "name":
				snap.Name = req.Name
				changed = append(changed, "name")
			case "description":
				snap.Description = req.Description
				changed = append(changed, "description")
			case "labels":
				snap.Labels = req.Labels
				labelsInMask = true
				changed = append(changed, "labels")
			}
		}
		updated, err := s.repo.Update(ctx, snap, labelsInMask, changed)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		return anypb.New(protoconv.Snapshot(updated))
	})
	return &op, nil
}

func validateSnapshotUpdate(req UpdateSnapshotReq) error {
	known := map[string]struct{}{"name": {}, "description": {}, "labels": {}}
	if err := corevalidate.UpdateMask("update_mask", req.UpdateMask, known); err != nil {
		return err
	}
	for _, f := range req.UpdateMask {
		switch f {
		case "source_disk_id", "disk_size", "storage_size":
			return invalidArg(f, f+" is immutable after Snapshot.Create")
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

// Delete удаляет Snapshot.
func (s *SnapshotService) Delete(ctx context.Context, id string) (*operations.Operation, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "snapshot_id required")
	}
	op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Delete snapshot %s", id),
		&computev1.DeleteSnapshotMetadata{SnapshotId: id})
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

// ListOperations возвращает операции для Snapshot.
func (s *SnapshotService) ListOperations(ctx context.Context, id string, p Pagination) ([]operations.Operation, string, error) {
	if _, err := s.repo.Get(ctx, id); err != nil {
		return nil, "", mapRepoErr(err)
	}
	return s.opsRepo.List(ctx, operations.ListFilter{ResourceID: id, PageSize: p.PageSize, PageToken: p.PageToken})
}
