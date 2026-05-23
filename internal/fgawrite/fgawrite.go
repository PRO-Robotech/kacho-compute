// Package fgawrite — write-side OpenFGA integration for kacho-compute (KAC-133).
//
// kacho-compute has a read-side FGA path (per-RPC Check interceptor, Phase 4
// List filter) but never published the per-resource hierarchy tuple that a
// created resource needs for the FGA `<rel> from project` cascade to resolve:
//
//	compute_disk:<id>      #project @project:<project_id>
//	compute_image:<id>     #project @project:<project_id>
//	compute_snapshot:<id>  #project @project:<project_id>
//	compute_instance:<id>  #project @project:<project_id>
//
// Without the parent-pointer tuple a per-resource Get/Update/Delete Check has
// no path to the project where the principal's role binding lives → fail-closed
// DENY (authz.no_path). This package provides the write-side — mirrors
// kacho-vpc/internal/apps/kacho/fgawrite (KAC-127 issue #22).
//
// The writer is invoked from each resource's Operation worker AFTER the
// resource row is committed. It is best-effort + non-fatal: the row is already
// durable, a tuple-write failure is logged for the operator.
package fgawrite

import (
	"context"
	"fmt"
	"log/slog"
)

// HierarchyTupleWriter — port-interface a compute Create use-case needs to
// publish the resource→project hierarchy tuple. Implemented by
// internal/clients.OpenFGAWriteClient (composition root wires it; nil when
// OpenFGA tuple-write is not configured).
type HierarchyTupleWriter interface {
	// WriteHierarchyTuple writes `<objectType>:<objectID>#project@project:<projectID>`.
	// Idempotent — re-writing an existing tuple is a no-op success.
	WriteHierarchyTuple(ctx context.Context, objectType, objectID, projectID string) error
}

// Emit publishes the resource→project hierarchy tuple, best-effort. A nil
// writer is a no-op (OpenFGA tuple-write not configured — dev / degraded mode).
// Failures are logged, never returned — the resource row is already committed
// and an Operation must not fail because of a downstream FGA hiccup.
//
// objectType is the compute_* FGA type ("compute_disk", "compute_image", ...).
func Emit(ctx context.Context, w HierarchyTupleWriter, logger *slog.Logger, objectType, objectID, projectID string) {
	if w == nil {
		return
	}
	if objectID == "" || projectID == "" {
		if logger != nil {
			logger.Warn("compute fga hierarchy-tuple skipped: empty id (KAC-133)",
				"object_type", objectType, "object_id", objectID, "project_id", projectID)
		}
		return
	}
	if err := w.WriteHierarchyTuple(ctx, objectType, objectID, projectID); err != nil {
		if logger != nil {
			logger.Warn("compute fga hierarchy-tuple write failed (KAC-133)",
				"err", err, "object", fmt.Sprintf("%s:%s", objectType, objectID),
				"project", projectID)
		}
		return
	}
	if logger != nil {
		logger.Info("compute fga hierarchy-tuple written (KAC-133)",
			"object", fmt.Sprintf("%s:%s", objectType, objectID), "project", projectID)
	}
}
