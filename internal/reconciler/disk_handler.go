package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	"github.com/PRO-Robotech/kacho-compute/internal/config"
	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/repo"
	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// DiskHandler — reconciler для Disk.
type DiskHandler struct {
	diskRepo *repo.DiskRepo
	opsRepo  operations.Repo
	sim      config.SimConfig
	logger   *slog.Logger
}

// NewDiskHandler создаёт DiskHandler.
func NewDiskHandler(
	diskRepo *repo.DiskRepo,
	opsRepo operations.Repo,
	sim config.SimConfig,
	logger *slog.Logger,
) *DiskHandler {
	return &DiskHandler{
		diskRepo: diskRepo,
		opsRepo:  opsRepo,
		sim:      sim,
		logger:   logger,
	}
}

// Reconcile обрабатывает одну итерацию reconcile для Disk.
func (h *DiskHandler) Reconcile(ctx context.Context, d *domain.Disk) {
	switch d.Status {
	case domain.DiskStatusCreating:
		h.handleCreating(ctx, d)
	case domain.DiskStatusDeleting:
		h.handleDeleting(ctx, d)
	}
}

func (h *DiskHandler) handleCreating(ctx context.Context, d *domain.Disk) {
	min, max := h.sim.DiskCreateDuration()
	delay := randDuration(min, max)

	h.logger.Info("disk creating", "id", d.ID, "delay_ms", delay.Milliseconds())
	select {
	case <-ctx.Done():
		return
	case <-time.After(delay):
	}

	now := time.Now().UTC()
	d.Status = domain.DiskStatusReady
	d.ObservedGeneration = d.Generation
	d.StatusLastTransitionAt = now

	updated, err := h.diskRepo.Update(ctx, d)
	if err != nil {
		h.logger.Error("disk creating: update failed", "id", d.ID, "err", err)
		return
	}

	if err := h.markOperationDone(ctx, d.ID, updated); err != nil {
		h.logger.Error("disk creating: markDone failed", "id", d.ID, "err", err)
	}
}

func (h *DiskHandler) handleDeleting(ctx context.Context, d *domain.Disk) {
	if err := h.diskRepo.HardDelete(ctx, d.ID); err != nil {
		h.logger.Error("disk deleting: hard delete failed", "id", d.ID, "err", err)
		return
	}

	emptyDisk := &computev1.Disk{Id: d.ID}
	resp, _ := anypb.New(emptyDisk)
	if err := h.markOperationDoneWithResp(ctx, d.ID, resp); err != nil {
		h.logger.Error("disk deleting: markDone failed", "id", d.ID, "err", err)
	}
}

func (h *DiskHandler) markOperationDone(ctx context.Context, diskID string, d *domain.Disk) error {
	resp, err := anypb.New(domainDiskToProto(d))
	if err != nil {
		return err
	}
	return h.markOperationDoneWithResp(ctx, diskID, resp)
}

func (h *DiskHandler) markOperationDoneWithResp(ctx context.Context, diskID string, resp *anypb.Any) error {
	ops, _, err := h.opsRepo.List(ctx, operations.ListFilter{
		ResourceID: diskID,
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

func domainDiskToProto(d *domain.Disk) *computev1.Disk {
	return &computev1.Disk{
		Id:                   d.ID,
		FolderId:             d.FolderID,
		Name:                 d.Name,
		Description:          d.Description,
		CreatedAt:            timestamppb.New(d.CreatedAt),
		Labels:               d.Labels,
		DiskTypeId:           d.DiskTypeID,
		ZoneId:               d.ZoneID,
		Size:                 d.Size,
		ImageId:              d.ImageID,
		Status:               computev1.DiskStatus(d.Status),
		AttachedToInstanceId: d.AttachedToInstanceID,
		Generation:           d.Generation,
		ResourceVersion:      d.ResourceVersion,
		ObservedGeneration:   d.ObservedGeneration,
	}
}
