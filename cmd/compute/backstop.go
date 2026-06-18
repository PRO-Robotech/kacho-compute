package main

// backstop.go — sub-phase 1.4 S3: the corelib outbox backstop wiring for
// kacho-compute (D-6 reconciler + D-7 metrics + D-8 fail-closed boot-gate). It
// adds observability + safety on TOP of the existing SEC-D register-drainer
// WITHOUT changing co-commit atomicity (no migration; D-9).
//
//   - reconciler: periodic RedrivePoisoned re-drives poisoned/exhausted intents
//     (with their original decoder-correct payload) back to claimable.
//   - metrics: a Collector scans backlog/oldest/poisoned; the drainer's
//     WithPoisonObserver bumps outbox_poisoned_total.
//   - boot-gate: KACHO_COMPUTE_REQUIRE_IAM refuses mutating Create + NotReady
//     until the IAM-connected register-drainer is up.

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PRO-Robotech/kacho-corelib/outbox/metrics"
	"github.com/PRO-Robotech/kacho-corelib/outbox/reconciler"

	"github.com/PRO-Robotech/kacho-compute/internal/clients"
)

const (
	// computeFGAOutboxTable / Channel — the register-outbox the drainer,
	// reconciler and metrics-collector all share.
	computeFGAOutboxTable   = "public.compute_fga_register_outbox"
	computeFGAOutboxChannel = "compute_fga_register_outbox"
)

// startBackstop wires the reconciler RedrivePoisoned pass + metrics Collector over
// the compute register-outbox and runs them in the background until ctx is
// cancelled. Both are best-effort observability/repair — a transient error is
// logged, never fatal.
func startBackstop(ctx context.Context, pool *pgxpool.Pool, rec metrics.Recorder, logger *slog.Logger) error {
	ad := clients.NewFGAReconcileAdapter(pool, computeFGAOutboxTable)
	rc, err := reconciler.New(pool, reconciler.Config{
		Table:       computeFGAOutboxTable,
		Channel:     computeFGAOutboxChannel,
		GraceWindow: time.Minute, // anti-race deferral (D-6c)
	}, reconciler.Adapters{Enumerator: ad, Registry: ad},
		logger.With(slog.String("component", "fga-register-reconciler")))
	if err != nil {
		return err
	}

	go runReconciler(ctx, rc, logger)

	col := metrics.NewCollector(pool, rec, metrics.CollectorConfig{Table: computeFGAOutboxTable})
	go col.Run(ctx, func(err error) {
		logger.Warn("outbox metrics scan failed", "err", err)
	})

	logger.Info("FGA register backstop started (reconciler + metrics)", "table", computeFGAOutboxTable)
	return nil
}

// runReconciler runs the reconciler RedrivePoisoned pass on a periodic ticker
// (1.4-30): poisoned/exhausted register-intents are reset to claimable so the
// drainer re-delivers them with their ORIGINAL, decoder-correct tuple payload.
//
// BackfillFromState / GCOrphans are deliberately NOT run for kacho-compute: they
// re-emit corelib-fixed payloads ({"project_id":…} / {}) the compute tuple-set
// decoder ({tuples:[…]}) cannot decode — running them would poison good state. And
// because every compute Create co-commits its register-intent in the resource
// writer-tx (SEC-D atomicity, D-9 untouched), there are no legacy never-enqueued
// rows to backfill. The enumerator/registry adapter is still wired (reconciler.New
// requires it) so the backstop is ready if the corelib re-emit contract grows a
// per-service payload hook.
func runReconciler(ctx context.Context, rc *reconciler.Reconciler, logger *slog.Logger) {
	const interval = 5 * time.Minute
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if n, err := rc.RedrivePoisoned(ctx); err != nil {
				logger.Warn("reconciler redrive-poisoned failed", "err", err)
			} else if n > 0 {
				logger.Info("reconciler re-drove poisoned intents", "count", n)
			}
		}
	}
}
