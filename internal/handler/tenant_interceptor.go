// Package handler — tenant_interceptor.go: gRPC unary/stream interceptor
// который извлекает caller-folder identity из metadata и кладёт в context.
//
// Это **scaffolding** под AuthZ: сейчас метаданные читаются как plaintext (нет
// AuthN, нет токенов). Когда будет IAM — вместо metadata будут claims из
// validated JWT/IAM-token, но downstream API (TenantFromCtx, AssertFolderOwnership)
// не изменится. Зеркалит kacho-vpc/internal/handler/tenant_interceptor.go.
package handler

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type tenantCtxKey struct{}

// TenantCtx — caller identity. Сейчас populated из gRPC metadata
// (`x-kacho-folder-id`, `x-kacho-actor`); future — из validated IAM token.
type TenantCtx struct {
	// ProjectIDs — folders которые caller'у разрешено читать/писать.
	// Empty = full access (admin / cluster-scoped) — backward-compat без AuthN.
	ProjectIDs map[string]struct{}
	// Actor — для audit log (admin@kacho, или sub-claim из JWT).
	Actor string
	// Admin — true если caller имеет cluster-wide read/write.
	Admin bool
}

// HasFolderAccess — может ли caller трогать ресурс из folder'а.
func (t TenantCtx) HasFolderAccess(folderID string) bool {
	if t.Admin || len(t.ProjectIDs) == 0 {
		return true
	}
	_, ok := t.ProjectIDs[folderID]
	return ok
}

// IsAnonymous — true если caller не предъявил identity, влияющую на AuthZ
// (ни Admin-claim, ни ProjectIDs). Actor — orthogonal audit-trail.
func (t TenantCtx) IsAnonymous() bool {
	return !t.Admin && len(t.ProjectIDs) == 0
}

// TenantFromCtx извлекает TenantCtx из context. Если interceptor не сработал —
// empty TenantCtx (ProjectIDs=nil → backward-compat "full access").
func TenantFromCtx(ctx context.Context) TenantCtx {
	if v := ctx.Value(tenantCtxKey{}); v != nil {
		if t, ok := v.(TenantCtx); ok {
			return t
		}
	}
	return TenantCtx{}
}

// ErrCrossTenant — sentinel для cross-folder access denied.
var ErrCrossTenant = errors.New("permission denied")

// AssertFolderOwnership — handler-side AuthZ check. PermissionDenied если caller
// не имеет доступа к folder'у. Вызывается в Get/Update/Delete/List после repo.Get.
func AssertFolderOwnership(ctx context.Context, folderID string) error {
	t := TenantFromCtx(ctx)
	if t.HasFolderAccess(folderID) {
		return nil
	}
	return status.Error(codes.PermissionDenied, "Permission denied")
}

// TenantUnaryInterceptor — gRPC unary interceptor. Извлекает caller-folder
// identity из metadata и кладёт в ctx как TenantCtx.
//
// requireAdmin=true (internal :9091) — отвергает caller'а без admin-flag.
// productionMode=true — fail-closed гейт: anonymous caller → PermissionDenied сразу.
func TenantUnaryInterceptor(requireAdmin, productionMode bool) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		t := tenantFromMetadata(ctx)
		if productionMode && t.IsAnonymous() {
			return nil, status.Error(codes.PermissionDenied,
				"AuthN required (production mode): set x-kacho-* identity headers via gateway")
		}
		if requireAdmin {
			if err := assertAdminAccess(t, info.FullMethod); err != nil {
				return nil, err
			}
		}
		ctx = context.WithValue(ctx, tenantCtxKey{}, t)
		return handler(ctx, req)
	}
}

// TenantStreamInterceptor — то же для server-stream RPC (для Watch).
func TenantStreamInterceptor(requireAdmin, productionMode bool) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		t := tenantFromMetadata(ss.Context())
		if productionMode && t.IsAnonymous() {
			return status.Error(codes.PermissionDenied,
				"AuthN required (production mode): set x-kacho-* identity headers via gateway")
		}
		if requireAdmin {
			if err := assertAdminAccess(t, info.FullMethod); err != nil {
				return err
			}
		}
		ctx := context.WithValue(ss.Context(), tenantCtxKey{}, t)
		return handler(srv, &wrappedStream{ServerStream: ss, ctx: ctx})
	}
}

// assertAdminAccess — internal :9091 listener gate. Отвергает не-admin caller'а.
// Anonymous (нет AuthN) → пропускается в dev-mode (backward-compat).
func assertAdminAccess(t TenantCtx, fullMethod string) error {
	if t.IsAnonymous() {
		return nil
	}
	if !t.Admin {
		if !strings.HasPrefix(fullMethod, "/kacho.cloud.compute.v1.Internal") {
			return status.Error(codes.NotFound, "not found")
		}
		return status.Error(codes.PermissionDenied, "Permission denied")
	}
	return nil
}

type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }

// tenantFromMetadata — internal helper, извлекает TenantCtx из gRPC md.
func tenantFromMetadata(ctx context.Context) TenantCtx {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return TenantCtx{}
	}
	t := TenantCtx{}
	if v := md.Get("x-kacho-actor"); len(v) > 0 {
		t.Actor = v[0]
	}
	if v := md.Get("x-kacho-admin"); len(v) > 0 && v[0] == "true" {
		t.Admin = true
	}
	if folders := md.Get("x-kacho-folder-id"); len(folders) > 0 {
		t.ProjectIDs = make(map[string]struct{}, len(folders))
		for _, f := range folders {
			if f != "" {
				t.ProjectIDs[f] = struct{}{}
			}
		}
	}
	return t
}
