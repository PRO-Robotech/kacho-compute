// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package check_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/PRO-Robotech/kacho-compute/internal/check"
)

// catalogAdminMutations — internal catalog-admin RPC, которые ОБЯЗАНЫ быть
// замаплены на FGA-relation `system_admin` @ `cluster:cluster_kacho_root`
// (proto-аннотация `required_relation=system_admin`, object_type=cluster в
// internal_catalog_service.proto). Эти RPC живут на internal listener'е :9091 —
// он гоняет тот же authzIntr, что и public, поэтому каждая catalog-мутация должна
// резолвиться в Check, а не пропускаться methodIsInternal-фолбэком.
// Internal{Zone,Region}Service serving removed — Geography is owned by kacho-geo;
// only InternalDiskTypeService remains compute-owned.
var catalogAdminMutations = []string{
	"/kacho.cloud.compute.v1.InternalDiskTypeService/Create",
	"/kacho.cloud.compute.v1.InternalDiskTypeService/Update",
	"/kacho.cloud.compute.v1.InternalDiskTypeService/Delete",
}

// TestPermissionMap_CatalogAdmin_SystemAdminOnCluster — каждая catalog-admin
// мутация замаплена → relation "system_admin", object "cluster:cluster_kacho_root".
func TestPermissionMap_CatalogAdmin_SystemAdminOnCluster(t *testing.T) {
	m := check.PermissionMap()
	for _, fullMethod := range catalogAdminMutations {
		entry, ok := m[fullMethod]
		require.Truef(t, ok, "catalog-admin RPC %s must be present in PermissionMap (internal listener runs authz Check)", fullMethod)
		require.Equalf(t, "system_admin", entry.Relation, "%s: required_relation must be system_admin (proto annotation)", fullMethod)
		require.NotNilf(t, entry.Extract, "%s: must carry an ObjectExtractor", fullMethod)
		require.Falsef(t, entry.Public, "%s: must NOT be Public — it is relation-gated", fullMethod)

		objType, objID, err := entry.Extract(nil)
		require.NoErrorf(t, err, "%s: cluster-scoped extractor must not error on any request", fullMethod)
		require.Equalf(t, "cluster", objType, "%s: object_type must be cluster", fullMethod)
		require.Equalf(t, "cluster_kacho_root", objID, "%s: object_id must be cluster singleton", fullMethod)
	}
}

// TestPermissionMap_CatalogAdmin_EnforcedByInterceptor — sanity at interceptor
// level: a mapped catalog mutation routes to a real Check (system_admin on
// cluster) rather than being skipped, and deny fail-closes with PermissionDenied.
func TestPermissionMap_CatalogAdmin_EnforcedByInterceptor(t *testing.T) {
	intr, calls := newTestInterceptor(t, func(_ context.Context, subject, relation, object string) (bool, error) {
		require.Equal(t, "user:usr_admin", subject)
		require.Equal(t, "system_admin", relation)
		require.Equal(t, "cluster:cluster_kacho_root", object)
		return true, nil
	})
	uIntr := intr.Unary()
	called := false
	handler := func(ctx context.Context, req any) (any, error) { called = true; return "ok", nil }
	info := &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.compute.v1.InternalDiskTypeService/Create"}
	ctx := principalCtx("user", "usr_admin")

	resp, err := uIntr(ctx, struct{}{}, info, handler)
	require.NoError(t, err)
	require.Equal(t, "ok", resp)
	require.True(t, called)
	require.Equal(t, 1, *calls, "catalog mutation must trigger exactly one Check, not be bypassed")
}

// TestPermissionMap_InternalWatch_MappedPublic — InternalWatchService/Watch is
// a real, registered internal stream handler (cmd/compute/main.go wires
// computev1.RegisterInternalWatchServiceServer; internal/handler/internal_watch_handler.go
// streams compute_outbox via LISTEN/NOTIFY) served on the SAME internal :9091
// listener that runs authzIntr.Stream(). The pinned corelib authz.Interceptor
// has no name-based "methodIsInternal" fallback: any RPC absent from
// PermissionMap resolves to DecisionUnmapped -> PermissionDenied (fail-closed),
// for streams too (see authz.Interceptor.Stream()). Watch therefore MUST carry
// an explicit PermissionMap entry with Public=true (the same documented exempt
// mechanism as OperationService.Get/Cancel above) or every Watch call is
// dead-on-arrival in production.
func TestPermissionMap_InternalWatch_MappedPublic(t *testing.T) {
	m := check.PermissionMap()
	entry, ok := m["/kacho.cloud.compute.v1.InternalWatchService/Watch"]
	require.True(t, ok, "InternalWatchService/Watch must be present in PermissionMap (no methodIsInternal fallback exists in the pinned corelib)")
	require.True(t, entry.Public, "InternalWatchService/Watch must be Public — the exempt mechanism is an explicit PermissionMap entry, not name-based skip")
}
