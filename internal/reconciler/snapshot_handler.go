package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	"github.com/PRO-Robotech/kacho-compute/internal/config"
	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/repo"
	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SnapshotHandler — reconciler для Snapshot.
type SnapshotHandler struct {
	snapRepo *repo.SnapshotRepo
	opsRepo  operations.Repo
	sim      config.SimConfig
	logger   *slog.Logger
}

// NewSnapshotHandler создаёт SnapshotHandler.
func NewSnapshotHandler(
	snapRepo *repo.SnapshotRepo,
	opsRepo operations.Repo,
	sim config.SimConfig,
	logger *slog.Logger,
) *SnapshotHandler {
	return &SnapshotHandler{
		snapRepo: snapRepo,
		opsRepo:  opsRepo,
		sim:      sim,
		logger:   logger,
	}
}

// Reconcile обрабатывает одну итерацию reconcile для Snapshot.
func (h *SnapshotHandler) Reconcile(ctx context.Context, s *domain.Snapshot) {
	switch s.Status {
	case domain.SnapshotStatusCreating:
		h.handleCreating(ctx, s)
	case domain.SnapshotStatusDeleting:
		h.handleDeleting(ctx, s)
	}
}

// snapshotProgressStep — шаг прогресса (0→25→50→75→100).
const snapshotProgressStep = 25

func (h *SnapshotHandler) handleCreating(ctx context.Context, s *domain.Snapshot) {
	// Симулируем прогресс 0→25→50→75→100 с равными интервалами.
	min, max := h.sim.DiskCreateDuration()
	totalDelay := randDuration(min, max)
	stepDelay := totalDelay / 4

	h.logger.Info("snapshot creating", "id", s.ID, "total_delay_ms", totalDelay.Milliseconds())

	for step := 1; step <= 4; step++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(stepDelay):
		}

		s.ProgressPercent = int32(step * snapshotProgressStep)
		s.ResourceVersion = ids.NewUID()
		if _, err := h.snapRepo.Update(ctx, s); err != nil {
			h.logger.Error("snapshot progress update failed", "id", s.ID, "step", step, "err", err)
			return
		}
	}

	// Переводим в READY.
	s.Status = domain.SnapshotStatusReady
	s.ProgressPercent = 100
	s.ObservedGeneration = s.Generation
	s.ResourceVersion = ids.NewUID()

	updated, err := h.snapRepo.Update(ctx, s)
	if err != nil {
		h.logger.Error("snapshot creating: final update failed", "id", s.ID, "err", err)
		return
	}

	if err := h.markOperationDone(ctx, s.ID, updated); err != nil {
		h.logger.Error("snapshot creating: markDone failed", "id", s.ID, "err", err)
	}
}

func (h *SnapshotHandler) handleDeleting(ctx context.Context, s *domain.Snapshot) {
	if err := h.snapRepo.HardDelete(ctx, s.ID); err != nil {
		h.logger.Error("snapshot deleting: hard delete failed", "id", s.ID, "err", err)
		return
	}
	emptySnap := &computev1.Snapshot{Id: s.ID}
	resp, _ := anypb.New(emptySnap)
	if err := h.markOperationDoneWithResp(ctx, s.ID, resp); err != nil {
		h.logger.Error("snapshot deleting: markDone failed", "id", s.ID, "err", err)
	}
}

func (h *SnapshotHandler) markOperationDone(ctx context.Context, snapID string, s *domain.Snapshot) error {
	resp, err := anypb.New(domainSnapshotToProto(s))
	if err != nil {
		return err
	}
	return h.markOperationDoneWithResp(ctx, snapID, resp)
}

func (h *SnapshotHandler) markOperationDoneWithResp(ctx context.Context, snapID string, resp *anypb.Any) error {
	ops, _, err := h.opsRepo.List(ctx, operations.ListFilter{
		ResourceID: snapID,
		PageSize:   10,
	})
	if err != nil {
		return fmt.Errorf("list ops: %w", err)
	}
	for _, op := range ops {
		if op.Done {
			continue
		}
		if err := h.opsRepo.MarkDone(ctx, op.ID, resp); err != nil {
			return fmt.Errorf("mark done op %s: %w", op.ID, err)
		}
		break
	}
	return nil
}

func domainSnapshotToProto(s *domain.Snapshot) *computev1.Snapshot {
	return &computev1.Snapshot{
		Id:                 s.ID,
		FolderId:           s.FolderID,
		Name:               s.Name,
		Description:        s.Description,
		CreatedAt:          timestamppb.New(s.CreatedAt),
		Labels:             s.Labels,
		DiskId:             s.DiskID,
		Size:               s.Size,
		Status:             computev1.SnapshotStatus(s.Status),
		ProgressPercent:    s.ProgressPercent,
		Generation:         s.Generation,
		ResourceVersion:    s.ResourceVersion,
		ObservedGeneration: s.ObservedGeneration,
	}
}
