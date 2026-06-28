// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// fga_reconcile_adapter.go — per-service adapters for the corelib
// outbox/reconciler backstop.
//
// The reconciler orchestrates re-drive / derive-from-state backfill / inverse-
// orphan GC; the DOMAIN knowledge (which tables hold tenant resources, their
// project_id, whether one still exists) is per-service and injected through
// reconciler.ResourceEnumerator + reconciler.TupleRegistry. This file implements
// those ports over the compute resource tables (project-hierarchy only — every
// compute resource carries project_id; compute has no owner-self-grant).
//
// Clean Architecture note: this adapter imports pgx (an adapter concern) only;
// it reaches FGA exclusively through kacho-iam via the register-outbox (no direct
// FGA write/read here — structural gate).
package clients

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PRO-Robotech/kacho-corelib/outbox/reconciler"
)

// computeResourceTables maps each outbox resource_kind to its compute table. The
// kind labels match what repo.Insert/Delete write to compute_fga_register_outbox
// ("Instance"/"Disk"/"Image"/"Snapshot"). All rows carry (id, project_id).
var computeResourceTables = []struct {
	kind  string
	table string
}{
	{"Instance", "instances"},
	{"Disk", "disks"},
	{"Image", "images"},
	{"Snapshot", "snapshots"},
}

// FGAReconcileAdapter implements reconciler.ResourceEnumerator + TupleRegistry over
// the compute resource tables + the register-outbox.
type FGAReconcileAdapter struct {
	pool *pgxpool.Pool
	// table — full register-outbox table name (public.compute_fga_register_outbox).
	table string
}

// NewFGAReconcileAdapter constructs the per-service reconciler adapter. table is
// the full register-outbox table name the drainer/reconciler share.
func NewFGAReconcileAdapter(pool *pgxpool.Pool, table string) *FGAReconcileAdapter {
	return &FGAReconcileAdapter{pool: pool, table: table}
}

func computeKindToTable(kind string) string {
	for _, rt := range computeResourceTables {
		if rt.kind == kind {
			return rt.table
		}
	}
	return ""
}

// ListResources enumerates every live compute resource as (kind, id, project_id).
// DiskType / Region / Zone are Internal admin catalogs (no project_id / no
// owner-tuple) and are out-by-design — not listed.
func (a *FGAReconcileAdapter) ListResources(ctx context.Context) ([]reconciler.ResourceRow, error) {
	var out []reconciler.ResourceRow
	for _, rt := range computeResourceTables {
		rows, err := a.pool.Query(ctx,
			fmt.Sprintf(`SELECT id, project_id FROM %s`, rt.table)) //nolint:gosec // trusted literal
		if err != nil {
			return nil, fmt.Errorf("compute reconcile enumerate %s: %w", rt.table, err)
		}
		for rows.Next() {
			var id, projectID string
			if err := rows.Scan(&id, &projectID); err != nil {
				rows.Close()
				return nil, fmt.Errorf("compute reconcile scan %s: %w", rt.table, err)
			}
			out = append(out, reconciler.ResourceRow{Kind: rt.kind, ID: id, ProjectID: projectID})
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("compute reconcile rows %s: %w", rt.table, err)
		}
	}
	return out, nil
}

// ResourceExists reports whether (kind,id) still exists — used by inverse-orphan
// GC to confirm a registered tuple's resource is gone before unregistering.
func (a *FGAReconcileAdapter) ResourceExists(ctx context.Context, kind, id string) (bool, error) {
	table := computeKindToTable(kind)
	if table == "" {
		return false, nil
	}
	var exists bool
	if err := a.pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $1)`, table), //nolint:gosec // trusted literal
		id,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("compute reconcile exists %s/%s: %w", kind, id, err)
	}
	return exists, nil
}

// ListRegistered derives the orphan-GC candidate set from the register-outbox:
// every (resource_kind, resource_id) whose LATEST intent is a delivered
// fga.register (sent_at NOT NULL). The reconciler then confirms absence +
// anti-race before any unregister. No direct FGA read.
func (a *FGAReconcileAdapter) ListRegistered(ctx context.Context) ([]reconciler.RegisteredTuple, error) {
	rows, err := a.pool.Query(ctx, fmt.Sprintf(`
		SELECT DISTINCT ON (resource_id) resource_kind, resource_id, event_type
		  FROM %s
		 WHERE resource_id <> '' AND sent_at IS NOT NULL
		 ORDER BY resource_id, id DESC`, a.table)) //nolint:gosec // trusted literal
	if err != nil {
		return nil, fmt.Errorf("compute reconcile list-registered: %w", err)
	}
	defer rows.Close()
	var out []reconciler.RegisteredTuple
	for rows.Next() {
		var kind, id, eventType string
		if err := rows.Scan(&kind, &id, &eventType); err != nil {
			return nil, fmt.Errorf("compute reconcile list-registered scan: %w", err)
		}
		if eventType != "fga.register" {
			continue
		}
		out = append(out, reconciler.RegisteredTuple{Kind: kind, ID: id})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("compute reconcile list-registered rows: %w", err)
	}
	return out, nil
}

var (
	_ reconciler.ResourceEnumerator = (*FGAReconcileAdapter)(nil)
	_ reconciler.TupleRegistry      = (*FGAReconcileAdapter)(nil)
)
