// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package main

// backstop.go — обвязка corelib outbox-backstop для kacho-compute (reconciler +
// metrics + fail-closed boot-gate). Добавляет наблюдаемость и страховку поверх
// register-drainer, не меняя co-commit-атомарность writer-tx (без отдельной
// миграции).
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
// the compute register-outbox. Both are best-effort observability/repair — a
// transient error is logged, never fatal. It returns their run-loops as supervised
// tasks (reconRun / colRun), wired into the runServe errgroup — not fire-and-forget.
func startBackstop(_ context.Context, pool *pgxpool.Pool, rec metrics.Recorder, logger *slog.Logger) (reconRun, colRun func(context.Context) error, err error) {
	ad := clients.NewFGAReconcileAdapter(pool, computeFGAOutboxTable)
	rc, rerr := reconciler.New(pool, reconciler.Config{
		Table:       computeFGAOutboxTable,
		Channel:     computeFGAOutboxChannel,
		GraceWindow: time.Minute, // anti-race deferral
	}, reconciler.Adapters{Enumerator: ad, Registry: ad},
		logger.With(slog.String("component", "fga-register-reconciler")))
	if rerr != nil {
		return nil, nil, rerr
	}

	col := metrics.NewCollector(pool, rec, metrics.CollectorConfig{Table: computeFGAOutboxTable})

	logger.Info("FGA register backstop started (reconciler + metrics)", "table", computeFGAOutboxTable)

	reconRun = func(ctx context.Context) error {
		runReconciler(ctx, rc, logger)
		return nil
	}
	colRun = func(ctx context.Context) error {
		col.Run(ctx, func(err error) {
			logger.Warn("outbox metrics scan failed", "err", err)
		})
		return nil
	}
	return reconRun, colRun, nil
}

// runReconciler runs the reconciler RedrivePoisoned pass on a periodic ticker:
// poisoned/exhausted register-intents are reset to claimable so the drainer
// re-delivers them with their ORIGINAL, decoder-correct tuple payload.
//
// BackfillFromState / GCOrphans are deliberately NOT run for kacho-compute: they
// re-emit corelib-fixed payloads ({"project_id":…} / {}) the compute tuple-set
// decoder ({tuples:[…]}) cannot decode — running them would poison good state. And
// because every compute Create co-commits its register-intent in the resource
// writer-tx (atomically, no separate migration), there are no legacy never-enqueued
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
