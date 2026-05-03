package reconciler

import (
	"context"
	"time"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// SnapshotHandler обрабатывает переходы состояний Snapshot.
type SnapshotHandler struct {
	snapshotRepo service.SnapshotRepo
	simCfg       SimConfig
}

// NewSnapshotHandler создаёт SnapshotHandler.
func NewSnapshotHandler(snapshotRepo service.SnapshotRepo, simCfg SimConfig) *SnapshotHandler {
	return &SnapshotHandler{
		snapshotRepo: snapshotRepo,
		simCfg:       simCfg,
	}
}

// Process обрабатывает один Snapshot.
func (h *SnapshotHandler) Process(ctx context.Context, snap *domain.Snapshot) {
	if snap.State == domain.SnapshotStateCreating {
		h.processCreating(ctx, snap)
	}
}

func (h *SnapshotHandler) processCreating(ctx context.Context, snap *domain.Snapshot) {
	totalMs := randBetween(h.simCfg.SnapshotMinMs, h.simCfg.SnapshotMaxMs)
	stepMs := totalMs / 4

	// Прогресс: 0 → 25 → 50 → 75 → 100
	steps := []int32{25, 50, 75, 100}
	for _, progress := range steps {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(stepMs) * time.Millisecond):
		}

		snap.ProgressPercent = progress
		if progress == 100 {
			snap.State = domain.SnapshotStateReady
			snap.StateLastTransitionAt = time.Now().UTC()
		}
		_, _ = h.snapshotRepo.UpdateStatus(ctx, snap)
	}
}
