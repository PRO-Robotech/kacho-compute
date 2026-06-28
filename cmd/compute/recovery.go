// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package main

// Durable LRO recovery wiring: доменный resolver compute + corelib-reconciler.
//
// При крахе процесса live-worker'ы умирают, их in-flight операции остаются
// done=false навсегда (worker добирает только операции, диспетчеризованные в ЭТОМ
// процессе). Reconciler при старте (RecoverAll — ДО приёма трафика) и
// периодическим sweep'ом (Run — backstop под супервизором) разрешает осиротевшие
// операции в терминал, сверяясь с committed-реальностью ресурса через доменный
// resolver. Покрывает backlog-overflow, исчерпание terminal-write retry, shutdown
// и crash mid-op.

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PRO-Robotech/kacho-corelib/operations"

	"github.com/PRO-Robotech/kacho-compute/internal/operationresolver"
)

const (
	// reconcileOrphanGrace — orphan-кандидат должен быть старше этого окна, чтобы
	// reconciler не разрешил преждевременно ещё-живого worker'а. Должен превышать
	// максимальную ожидаемую длительность операции (worker per-op timeout — 4m).
	reconcileOrphanGrace = 5 * time.Minute
	// reconcileInterval — каденция периодического backstop-sweep'а.
	reconcileInterval = 30 * time.Second
	// reconcileBatchSize — размер пачки claim'а за один sweep.
	reconcileBatchSize = 100
	// lroOperationsSchema — schema-квалификатор таблицы operations compute
	// (DSN без search_path → public; совпадает с operations.NewRepo(pool, "public")).
	lroOperationsSchema = "public"
)

// startLRORecovery конструирует доменный resolver + corelib-reconciler поверх
// schema public, прогоняет startup-recovery (RecoverAll, ДО Serve) и возвращает
// reconciler — его периодический Run(ctx) вешается на супервизор в runServe.
// Ошибка startup-recovery — не фатальна (best-effort backstop; периодический Run
// добьёт позже): boot не валится из-за transient DB-сбоя reconciler'а.
func startLRORecovery(ctx context.Context, pool *pgxpool.Pool, readers operationresolver.Readers, rec operations.Recorder, logger *slog.Logger) *operations.Reconciler {
	resolver := operationresolver.New(readers, operationresolver.WithLogger(logger))
	reconciler := operations.NewReconciler(pool, resolver, operations.ReconcilerConfig{
		Schema:      lroOperationsSchema,
		OrphanGrace: reconcileOrphanGrace,
		BatchSize:   reconcileBatchSize,
		Interval:    reconcileInterval,
	},
		operations.WithReconcilerRecorder(rec),
		operations.WithReconcilerLogger(logger.With(slog.String("component", "lro-reconciler"))),
	)

	if err := reconciler.RecoverAll(ctx); err != nil {
		logger.Error("LRO startup-recovery failed; periodic sweep will retry", "err", err)
	} else {
		logger.Info("LRO startup-recovery complete (orphaned operations resolved)")
	}
	return reconciler
}
