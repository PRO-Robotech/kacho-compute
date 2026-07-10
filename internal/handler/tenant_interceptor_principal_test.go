// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package handler

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// mockPrincipalStream — минимальный grpc.ServerStream с настраиваемым ctx.
type mockPrincipalStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (m *mockPrincipalStream) Context() context.Context { return m.ctx }

// TestTenantUnary_PrincipalProductionPasses — regression против fe3455 production-auth
// бага: api-gateway форвардит identity как x-kacho-principal-* (UnaryTrustedPrincipalExtract
// кладёт trusted-принципал в operations-carrier), а НЕ legacy x-kacho-project-id. Запрос с
// реальным forwarded-принципалом НЕ должен считаться anonymous в production — он проходит
// tenant-guard дальше, к per-object authz-интерсептору (реальный гейт). До фикса guard
// отвергал его (учитывался только x-kacho-project-id) → аутентиф.+авториз. юзер получал 403.
func TestTenantUnary_PrincipalProductionPasses(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{})
	ctx = operations.WithPrincipal(ctx, operations.Principal{Type: "user", ID: "usr7j2yp1v24tx90tcv7"})
	interceptor := TenantUnaryInterceptor(false, true) // public listener, production
	called := false
	h := func(ctx context.Context, req any) (any, error) { called = true; return nil, nil }
	info := &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.compute.v1.InstanceService/List"}
	if _, err := interceptor(ctx, struct{}{}, info, h); err != nil {
		t.Fatalf("production-запрос с forwarded-принципалом обязан пройти tenant-guard, got: %v", err)
	}
	if !called {
		t.Fatal("downstream handler не был вызван")
	}
}

// TestTenantStream_PrincipalProductionPasses — то же для server-stream RPC.
func TestTenantStream_PrincipalProductionPasses(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{})
	ctx = operations.WithPrincipal(ctx, operations.Principal{Type: "service_account", ID: "sa9kx2"})
	interceptor := TenantStreamInterceptor(false, true)
	called := false
	h := func(srv any, ss grpc.ServerStream) error { called = true; return nil }
	info := &grpc.StreamServerInfo{FullMethod: "/kacho.cloud.compute.v1.InstanceService/List"}
	if err := interceptor(nil, &mockPrincipalStream{ctx: ctx}, info, h); err != nil {
		t.Fatalf("production stream-запрос с forwarded-принципалом обязан пройти, got: %v", err)
	}
	if !called {
		t.Fatal("downstream stream-handler не был вызван")
	}
}

// TestTenantUnary_TrulyAnonymousProductionStillRejected — negative: без principal И без
// x-kacho-project-id production по-прежнему fail-closed. Локает, что фикс не открыл дыру.
func TestTenantUnary_TrulyAnonymousProductionStillRejected(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{})
	interceptor := TenantUnaryInterceptor(false, true)
	h := func(ctx context.Context, req any) (any, error) { return nil, nil }
	info := &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.compute.v1.InstanceService/List"}
	_, err := interceptor(ctx, struct{}{}, info, h)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("настоящий anonymous в production обязан быть отвергнут, got: %v", err)
	}
}
