package reconciler

import (
	"context"
	"time"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// DiskHandler обрабатывает переходы состояний Disk.
type DiskHandler struct {
	diskRepo   service.DiskRepo
	diskSvc    *service.DiskService
	simCfg     SimConfig
}

// NewDiskHandler создаёт DiskHandler.
func NewDiskHandler(diskRepo service.DiskRepo, diskSvc *service.DiskService, simCfg SimConfig) *DiskHandler {
	return &DiskHandler{
		diskRepo: diskRepo,
		diskSvc:  diskSvc,
		simCfg:   simCfg,
	}
}

// Process обрабатывает один Disk.
func (h *DiskHandler) Process(ctx context.Context, disk *domain.Disk) {
	switch disk.State {
	case domain.DiskStateCreating:
		h.processCreating(ctx, disk)
	}
}

func (h *DiskHandler) processCreating(ctx context.Context, disk *domain.Disk) {
	// Симулируем задержку создания
	delay := randBetween(h.simCfg.DiskCreateMinMs, h.simCfg.DiskCreateMaxMs)
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Duration(delay) * time.Millisecond):
	}

	// Переходим в READY
	disk.State = domain.DiskStateReady
	disk.StateLastTransitionAt = time.Now().UTC()
	_, _ = h.diskRepo.UpdateStatus(ctx, disk)
}
