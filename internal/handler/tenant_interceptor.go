// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package handler — tenant_interceptor.go: gRPC unary/stream interceptor
// который извлекает caller-project identity из metadata и кладёт в context.
//
// Это **scaffolding** под AuthZ: сейчас метаданные читаются как plaintext (нет
// AuthN, нет токенов). Когда будет IAM — вместо metadata будут claims из
// validated JWT/IAM-token, но downstream API (TenantFromCtx, AssertProjectOwnership)
// не изменится. Зеркалит kacho-vpc/internal/handler/tenant_interceptor.go.
package handler

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// principalForwarded сообщает, несёт ли ctx реальный forwarded end-user principal.
// api-gateway форвардит identity как `x-kacho-principal-*` (без legacy
// `x-kacho-project-id`); UnaryTrustedPrincipalExtract кладёт его в стандартный
// operations-carrier ТОЛЬКО для доверенного forwarder'а (untrusted → scrub до
// SystemPrincipal). Поэтому запрос с реальным principal тут = trusted principal и
// НЕ anonymous, даже если tenant-headers пусты. Project-scoping энфорсится ниже —
// per-object authz-интерсептором (FGA Check) + listFilter, а не этим guard'ом.
// Зеркалит kacho-iam `authzguard.IsAnonymous` и kacho-vpc `principalForwarded`.
func principalForwarded(ctx context.Context) bool {
	p := operations.PrincipalFromContext(ctx)
	if p.ID == "" || p.Type == "" {
		return false
	}
	if p.Type == "anonymous" || p.ID == "anonymous" {
		return false
	}
	if p.Type == "system" && p.ID == "bootstrap" {
		return false
	}
	return true
}

type tenantCtxKey struct{}

// TenantCtx — caller identity. Сейчас populated из gRPC metadata
// (`x-kacho-project-id`, `x-kacho-actor`); future — из validated IAM token.
type TenantCtx struct {
	// ProjectIDs — projects которые caller'у разрешено читать/писать.
	// Empty = full access (admin / cluster-scoped) — backward-compat без AuthN.
	ProjectIDs map[string]struct{}
	// Actor — для audit log (admin@kacho, или sub-claim из JWT).
	Actor string
	// Admin — true если caller имеет cluster-wide read/write.
	Admin bool
}

// HasProjectAccess — может ли caller трогать ресурс из project'а.
func (t TenantCtx) HasProjectAccess(projectID string) bool {
	if t.Admin || len(t.ProjectIDs) == 0 {
		return true
	}
	_, ok := t.ProjectIDs[projectID]
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

// AssertProjectOwnership — handler-side AuthZ check. PermissionDenied если caller
// не имеет доступа к project'у. Вызывается в Get/Update/Delete/List после repo.Get.
func AssertProjectOwnership(ctx context.Context, projectID string) error {
	t := TenantFromCtx(ctx)
	if t.HasProjectAccess(projectID) {
		return nil
	}
	return status.Error(codes.PermissionDenied, "Permission denied")
}

// TenantUnaryInterceptor — gRPC unary interceptor. Извлекает caller-project
// identity из metadata и кладёт в ctx как TenantCtx.
//
// requireAdmin=true (internal :9091) — отвергает caller'а без admin-flag.
// productionMode=true — fail-closed гейт: anonymous caller → PermissionDenied сразу.
func TenantUnaryInterceptor(requireAdmin, productionMode bool) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		_, trusted := grpcsrv.TrustedPrincipalFromContext(ctx)
		t := tenantFromMetadata(ctx, trusted)
		if productionMode && t.IsAnonymous() && !principalForwarded(ctx) {
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
		_, trusted := grpcsrv.TrustedPrincipalFromContext(ss.Context())
		t := tenantFromMetadata(ss.Context(), trusted)
		if productionMode && t.IsAnonymous() && !principalForwarded(ss.Context()) {
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
//
// trusted — решение trust-aware principal-extract'а (grpcsrv.TrustedPrincipalFromContext):
// на mTLS-листенере метадата доверяется ⟺ peer предъявил verified client-cert
// trusted forwarder'а (api-gateway); insecure-листенер = back-compat trusted.
// authz-влияющие заголовки (x-kacho-admin → Admin, x-kacho-project-id → ProjectIDs)
// читаются ТОЛЬКО от trusted peer'а — иначе peer, дотянувшийся до листенера напрямую
// (TLS без verified cert), мог бы подделать `x-kacho-admin: true` и пройти admin-gate
// / project-ownership, т.к. эти заголовки не связаны с verified peer-identity
// (в отличие от principal, который trust-gated). x-kacho-actor — audit-only, не
// влияет на authz → читается всегда.
func tenantFromMetadata(ctx context.Context, trusted bool) TenantCtx {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return TenantCtx{}
	}
	t := TenantCtx{}
	if v := md.Get("x-kacho-actor"); len(v) > 0 {
		t.Actor = v[0]
	}
	if !trusted {
		// Untrusted peer: игнорируем forgeable authz-заголовки. TenantCtx остаётся
		// anonymous для authz (Admin=false, ProjectIDs=nil) — production-mode gate
		// отобьёт его как anonymous, а authoritative per-RPC FGA Check (на trust-gated
		// principal) остаётся основным гейтом.
		return t
	}
	if v := md.Get("x-kacho-admin"); len(v) > 0 && v[0] == "true" {
		t.Admin = true
	}
	if projects := md.Get("x-kacho-project-id"); len(projects) > 0 {
		t.ProjectIDs = make(map[string]struct{}, len(projects))
		for _, p := range projects {
			if p != "" {
				t.ProjectIDs[p] = struct{}{}
			}
		}
	}
	return t
}
