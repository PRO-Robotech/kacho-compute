package reconciler

import (
	"context"
	"log/slog"
	"time"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	"github.com/PRO-Robotech/kacho-compute/internal/config"
	"github.com/PRO-Robotech/kacho-compute/internal/repo"
)

const (
	// pollInterval — интервал опроса БД для поиска ресурсов в переходных состояниях.
	pollInterval = 1 * time.Second
	// reconcileBatchSize — количество ресурсов за одну итерацию.
	reconcileBatchSize = 10
)

// Dispatcher — главный цикл reconciler-а. Запускает InstanceHandler, DiskHandler, SnapshotHandler.
type Dispatcher struct {
	instRepo *repo.InstanceRepo
	diskRepo *repo.DiskRepo
	snapRepo *repo.SnapshotRepo
	opsRepo  operations.Repo
	sim      config.SimConfig
	logger   *slog.Logger

	instHandler *InstanceHandler
	diskHandler *DiskHandler
	snapHandler *SnapshotHandler
}

// NewDispatcher создаёт Dispatcher.
func NewDispatcher(
	instRepo *repo.InstanceRepo,
	diskRepo *repo.DiskRepo,
	snapRepo *repo.SnapshotRepo,
	opsRepo operations.Repo,
	sim config.SimConfig,
	logger *slog.Logger,
) *Dispatcher {
	return &Dispatcher{
		instRepo: instRepo,
		diskRepo: diskRepo,
		snapRepo: snapRepo,
		opsRepo:  opsRepo,
		sim:      sim,
		logger:   logger,
		instHandler: NewInstanceHandler(instRepo, diskRepo, opsRepo, sim, logger),
		diskHandler: NewDiskHandler(diskRepo, opsRepo, sim, logger),
		snapHandler: NewSnapshotHandler(snapRepo, opsRepo, sim, logger),
	}
}

// Run запускает reconcile-цикл. Блокирует до отмены ctx.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	d.logger.Info("reconciler started")
	for {
		select {
		case <-ctx.Done():
			d.logger.Info("reconciler stopped")
			return
		case <-ticker.C:
			d.reconcileOnce(ctx)
		}
	}
}

func (d *Dispatcher) reconcileOnce(ctx context.Context) {
	// Инстансы.
	insts, err := d.instRepo.ListPendingReconcile(ctx, reconcileBatchSize)
	if err != nil {
		d.logger.Error("reconcile: list pending instances", "err", err)
	} else {
		for _, inst := range insts {
			inst := inst // capture
			go d.instHandler.Reconcile(ctx, inst)
		}
	}

	// Диски.
	disks, err := d.diskRepo.ListPendingReconcile(ctx, reconcileBatchSize)
	if err != nil {
		d.logger.Error("reconcile: list pending disks", "err", err)
	} else {
		for _, disk := range disks {
			disk := disk
			go d.diskHandler.Reconcile(ctx, disk)
		}
	}

	// Снимки.
	snaps, err := d.snapRepo.ListPendingReconcile(ctx, reconcileBatchSize)
	if err != nil {
		d.logger.Error("reconcile: list pending snapshots", "err", err)
	} else {
		for _, snap := range snaps {
			snap := snap
			go d.snapHandler.Reconcile(ctx, snap)
		}
	}
}
