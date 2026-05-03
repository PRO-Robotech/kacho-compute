package reconciler

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

const pollInterval = 1 * time.Second

// Dispatcher — главный цикл reconciler-а.
type Dispatcher struct {
	pool            *pgxpool.Pool
	instanceRepo    service.InstanceRepo
	diskRepo        service.DiskRepo
	snapshotRepo    service.SnapshotRepo
	instanceHandler *InstanceHandler
	diskHandler     *DiskHandler
	snapshotHandler *SnapshotHandler
	logger          *slog.Logger
}

// NewDispatcher создаёт Dispatcher.
func NewDispatcher(
	pool *pgxpool.Pool,
	instanceRepo service.InstanceRepo,
	diskRepo service.DiskRepo,
	snapshotRepo service.SnapshotRepo,
	instanceHandler *InstanceHandler,
	diskHandler *DiskHandler,
	snapshotHandler *SnapshotHandler,
	logger *slog.Logger,
) *Dispatcher {
	return &Dispatcher{
		pool:            pool,
		instanceRepo:    instanceRepo,
		diskRepo:        diskRepo,
		snapshotRepo:    snapshotRepo,
		instanceHandler: instanceHandler,
		diskHandler:     diskHandler,
		snapshotHandler: snapshotHandler,
		logger:          logger,
	}
}

// Run запускает главный цикл reconciler-а. Блокирует до отмены ctx.
func (d *Dispatcher) Run(ctx context.Context) {
	d.logger.Info("reconciler started")
	for {
		select {
		case <-ctx.Done():
			d.logger.Info("reconciler stopping")
			return
		case <-time.After(pollInterval):
			d.tick(ctx)
		}
	}
}

func (d *Dispatcher) tick(ctx context.Context) {
	d.processInstances(ctx)
	d.processDisks(ctx)
	d.processSnapshots(ctx)
}

func (d *Dispatcher) processInstances(ctx context.Context) {
	instances, err := d.instanceRepo.ListPendingReconcile(ctx)
	if err != nil {
		d.logger.Error("list pending instances", "err", err)
		return
	}

	for _, inst := range instances {
		inst := inst
		acquired, err := tryAdvisoryLock(ctx, d.pool, inst.UID)
		if err != nil {
			d.logger.Error("advisory lock error", "uid", inst.UID, "err", err)
			continue
		}
		if !acquired {
			continue
		}

		go func() {
			defer advisoryUnlock(ctx, d.pool, inst.UID)
			d.instanceHandler.Process(ctx, inst)
		}()
	}
}

func (d *Dispatcher) processDisks(ctx context.Context) {
	disks, err := d.diskRepo.ListPendingReconcile(ctx)
	if err != nil {
		d.logger.Error("list pending disks", "err", err)
		return
	}

	for _, disk := range disks {
		disk := disk
		acquired, err := tryAdvisoryLock(ctx, d.pool, disk.UID)
		if err != nil {
			d.logger.Error("advisory lock error", "uid", disk.UID, "err", err)
			continue
		}
		if !acquired {
			continue
		}

		go func() {
			defer advisoryUnlock(ctx, d.pool, disk.UID)
			d.diskHandler.Process(ctx, disk)
		}()
	}
}

func (d *Dispatcher) processSnapshots(ctx context.Context) {
	snaps, err := d.snapshotRepo.ListPendingReconcile(ctx)
	if err != nil {
		d.logger.Error("list pending snapshots", "err", err)
		return
	}

	for _, snap := range snaps {
		snap := snap
		acquired, err := tryAdvisoryLock(ctx, d.pool, snap.UID)
		if err != nil {
			d.logger.Error("advisory lock error", "uid", snap.UID, "err", err)
			continue
		}
		if !acquired {
			continue
		}

		go func() {
			defer advisoryUnlock(ctx, d.pool, snap.UID)
			d.snapshotHandler.Process(ctx, snap)
		}()
	}
}
