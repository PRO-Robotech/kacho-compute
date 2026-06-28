// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package check_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	computev1 "github.com/PRO-Robotech/kacho-compute/proto/gen/go/kacho/cloud/compute/v1"
	"github.com/PRO-Robotech/kacho-corelib/authz"
	"github.com/PRO-Robotech/kacho-corelib/operations"

	"github.com/PRO-Robotech/kacho-compute/internal/check"
)

func principalCtx(typ, id string) context.Context {
	return operations.WithPrincipal(context.Background(), operations.Principal{
		Type:        typ,
		ID:          id,
		DisplayName: "test",
	})
}

// newTestInterceptor — фабрика interceptor'а с подменным CheckClient'ом.
func newTestInterceptor(t *testing.T, fn func(ctx context.Context, subject, relation, object string) (bool, error)) (*authz.Interceptor, *int) {
	t.Helper()
	calls := 0
	wrapped := authz.CheckClientFunc(func(ctx context.Context, subject, relation, object string) (bool, error) {
		calls++
		return fn(ctx, subject, relation, object)
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		ServiceName: "kacho-compute-test",
		Map:         check.PermissionMap(),
		Client:      wrapped,
	})
	return intr, &calls
}

func TestInterceptor_Unary_Allow_InstanceCreate(t *testing.T) {
	intr, calls := newTestInterceptor(t, func(_ context.Context, subject, relation, object string) (bool, error) {
		require.Equal(t, "user:usr_alice", subject)
		require.Equal(t, "editor", relation)
		require.Equal(t, "project:prj_demo", object)
		return true, nil
	})
	uIntr := intr.Unary()

	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return "ok", nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.compute.v1.InstanceService/Create"}
	ctx := principalCtx("user", "usr_alice")
	req := &computev1.CreateInstanceRequest{ProjectId: "prj_demo", Name: "vm1"}

	resp, err := uIntr(ctx, req, info, handler)
	require.NoError(t, err)
	require.Equal(t, "ok", resp)
	require.True(t, called)
	require.Equal(t, 1, *calls)
}

func TestInterceptor_Unary_Deny_InstanceStop(t *testing.T) {
	intr, calls := newTestInterceptor(t, func(_ context.Context, subject, relation, object string) (bool, error) {
		require.Equal(t, "user:usr_bob", subject)
		require.Equal(t, "v_update", relation)
		require.Equal(t, "compute_instance:epd_xxx", object)
		return false, nil
	})
	uIntr := intr.Unary()

	handlerCalled := false
	handler := func(ctx context.Context, req any) (any, error) {
		handlerCalled = true
		return "should not be returned", nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.compute.v1.InstanceService/Stop"}
	ctx := principalCtx("user", "usr_bob")
	req := &computev1.StopInstanceRequest{InstanceId: "epd_xxx"}

	_, err := uIntr(ctx, req, info, handler)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.PermissionDenied, st.Code())
	require.False(t, handlerCalled)
	require.Equal(t, 1, *calls)
}

func TestInterceptor_Unary_Unavailable_FailClosed(t *testing.T) {
	intr, _ := newTestInterceptor(t, func(_ context.Context, _, _, _ string) (bool, error) {
		return false, errors.New("iam unavailable: connection refused")
	})
	uIntr := intr.Unary()

	handler := func(ctx context.Context, req any) (any, error) {
		t.Fatal("handler must not be called on Unavailable")
		return nil, nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.compute.v1.InstanceService/Create"}
	ctx := principalCtx("user", "usr_alice")
	req := &computev1.CreateInstanceRequest{ProjectId: "prj_demo"}

	_, err := uIntr(ctx, req, info, handler)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.PermissionDenied, st.Code())
}

func TestInterceptor_Unary_DiskTypeList_ClusterCatalog(t *testing.T) {
	// Catalog object — "cluster:cluster_kacho_root": FGA model имеет `type cluster`
	// с user:* viewer cascade.
	intr, _ := newTestInterceptor(t, func(_ context.Context, subject, relation, object string) (bool, error) {
		require.Equal(t, "user:usr_alice", subject)
		require.Equal(t, "viewer", relation)
		require.Equal(t, "cluster:cluster_kacho_root", object, "DiskType/Zone/Region — viewer on cluster:cluster_kacho_root")
		return true, nil
	})
	uIntr := intr.Unary()
	called := false
	handler := func(ctx context.Context, req any) (any, error) { called = true; return "ok", nil }
	info := &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.compute.v1.DiskTypeService/List"}
	ctx := principalCtx("user", "usr_alice")

	_, err := uIntr(ctx, &computev1.ListDiskTypesRequest{}, info, handler)
	require.NoError(t, err)
	require.True(t, called)
}

func TestInterceptor_Unary_NoPrincipal_Denied(t *testing.T) {
	intr, calls := newTestInterceptor(t, func(_ context.Context, _, _, _ string) (bool, error) {
		t.Fatal("Check must not be called when principal is empty")
		return false, nil
	})
	uIntr := intr.Unary()

	handler := func(ctx context.Context, req any) (any, error) {
		t.Fatal("handler must not be called")
		return nil, nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.compute.v1.InstanceService/Get"}
	ctx := operations.WithPrincipal(context.Background(), operations.Principal{Type: "user", ID: ""})
	req := &computev1.GetInstanceRequest{InstanceId: "epd_x"}

	_, err := uIntr(ctx, req, info, handler)
	require.Error(t, err)
	st, _ := status.FromError(err)
	require.Equal(t, codes.PermissionDenied, st.Code())
	require.Equal(t, 0, *calls)
}

func TestInterceptor_Unary_UnmappedRPC_Denied(t *testing.T) {
	intr, _ := newTestInterceptor(t, func(_ context.Context, _, _, _ string) (bool, error) {
		t.Fatal("Check не должен вызываться для unmapped RPC")
		return false, nil
	})
	uIntr := intr.Unary()
	handler := func(ctx context.Context, req any) (any, error) {
		t.Fatal("handler не должен вызываться для unmapped RPC")
		return nil, nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.compute.v1.InstanceService/SomeNewMethodWithoutMapping"}
	ctx := principalCtx("user", "usr_alice")
	_, err := uIntr(ctx, struct{}{}, info, handler)
	require.Error(t, err)
	st, _ := status.FromError(err)
	require.Equal(t, codes.PermissionDenied, st.Code())
}

func TestInterceptor_Unary_UnmappedInternalRPC_FailClosed(t *testing.T) {
	// Не-замапленный RPC fail-closed даже при "Internal" в имени: interceptor
	// больше не делает name-based исключений (молчаливый пропуск по имени
	// "Internal*" был fail-open вектором на internal-периметре). Exempt-RPC
	// обязан явно стоять в PermissionMap (Public либо relation-gated).
	// InternalWatchService/Watch в Map отсутствует, поэтому запрос отклоняется
	// с PermissionDenied (rpc not mapped): ни handler, ни Check не вызываются.
	intr, calls := newTestInterceptor(t, func(_ context.Context, _, _, _ string) (bool, error) {
		t.Fatal("Check не должен вызываться для unmapped Internal* RPC")
		return false, nil
	})
	uIntr := intr.Unary()
	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return "ok", nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.compute.v1.InternalWatchService/Watch"}
	ctx := principalCtx("user", "usr_alice")

	_, err := uIntr(ctx, struct{}{}, info, handler)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.PermissionDenied, st.Code())
	require.False(t, called)
	require.Equal(t, 0, *calls)
}

func TestInterceptor_Unary_CacheHit(t *testing.T) {
	intr, calls := newTestInterceptor(t, func(_ context.Context, _, _, _ string) (bool, error) {
		return true, nil
	})
	uIntr := intr.Unary()
	handler := func(ctx context.Context, req any) (any, error) { return "ok", nil }
	info := &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.compute.v1.InstanceService/Get"}
	ctx := principalCtx("user", "usr_alice")
	req := &computev1.GetInstanceRequest{InstanceId: "epd_x"}

	_, err := uIntr(ctx, req, info, handler)
	require.NoError(t, err)
	require.Equal(t, 1, *calls)
	_, err = uIntr(ctx, req, info, handler)
	require.NoError(t, err)
	require.Equal(t, 1, *calls, "повторный Check на ту же тройку — cache hit")
}

func TestInterceptor_Unary_Breakglass_AllowsAll(t *testing.T) {
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		ServiceName: "kacho-compute-test",
		Map:         check.PermissionMap(),
		Breakglass:  true,
	})
	uIntr := intr.Unary()
	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return "ok", nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.compute.v1.InstanceService/Delete"}
	ctx := principalCtx("user", "usr_bob")
	req := &computev1.DeleteInstanceRequest{InstanceId: "epd_x"}

	resp, err := uIntr(ctx, req, info, handler)
	require.NoError(t, err)
	require.Equal(t, "ok", resp)
	require.True(t, called)
}

func TestPermissionMap_CoverageSnapshot(t *testing.T) {
	m := check.PermissionMap()
	// 7 services × ~5-10 methods each + Operation × 2 + Catalog × 6 ≈ 40+
	if len(m) < 35 {
		t.Errorf("PermissionMap слишком мала (%d entries): подозрение на drift регистраций", len(m))
	}
}

func TestFactory_NoIAMConn_NoBreakglass_Error(t *testing.T) {
	_, err := check.NewInterceptor(check.Options{
		ServiceName: "kacho-compute-test",
		IAMConn:     nil,
		Breakglass:  false,
	})
	require.ErrorIs(t, err, check.ErrIAMConnNotConfigured)
}

func TestFactory_Breakglass_NoIAMConn_OK(t *testing.T) {
	intr, err := check.NewInterceptor(check.Options{
		ServiceName: "kacho-compute-test",
		IAMConn:     nil,
		Breakglass:  true,
	})
	require.NoError(t, err)
	require.NotNil(t, intr)
}
