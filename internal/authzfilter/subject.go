package authzfilter

import (
	"context"
	"strings"

	"google.golang.org/grpc/metadata"
)

// metadata keys (lowercase per gRPC convention) — written by api-gateway
// Phase 3 authz middleware after token validation. Compute reads them on
// each List* RPC to scope FGA ListObjects calls.
const (
	mdKeySubject     = "x-kacho-subject"      // canonical FGA subject: "user:usr_..." / "service_account:sa_..."
	mdKeySubjectType = "x-kacho-subject-type" // legacy: "user" / "service_account"
	mdKeySubjectID   = "x-kacho-subject-id"   // legacy: bare id
	mdKeyActor       = "x-kacho-actor"        // for audit-trail (sub claim)
	mdKeyAdmin       = "x-kacho-admin"        // cluster-admin marker (bypass FGA)
)

// SubjectFromCtx — извлекает FGA subject из gRPC metadata.
//
// Поддерживает два формата:
//  1. Каноничный: `x-kacho-subject: user:usr_alice` (предпочитаемый, api-gateway Phase 3+).
//  2. Legacy: `x-kacho-subject-type: user` + `x-kacho-subject-id: usr_alice` (Phase 0-2).
//
// admin=true возвращается если `x-kacho-admin: true` — caller имеет
// cluster-wide read и handler должен bypass FGA filter (BypassAll).
// Anonymous caller (нет ни одного header) → subject="", admin=false —
// handler решает что делать (production-mode → PermissionDenied, dev → bypass).
func SubjectFromCtx(ctx context.Context) (subject string, admin bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", false
	}

	if v := md.Get(mdKeyAdmin); len(v) > 0 && strings.EqualFold(v[0], "true") {
		admin = true
	}

	if v := md.Get(mdKeySubject); len(v) > 0 && v[0] != "" {
		return v[0], admin
	}

	// Legacy fallback.
	sType := first(md.Get(mdKeySubjectType))
	sID := first(md.Get(mdKeySubjectID))
	if sType != "" && sID != "" {
		return sType + ":" + sID, admin
	}

	// Final fallback: actor header (best-effort, treat as user).
	if actor := first(md.Get(mdKeyActor)); actor != "" {
		// Only build subject if actor looks like an opaque id (no "@" — that
		// would be a service account / human display name we shouldn't treat
		// as an FGA subject).
		if !strings.ContainsAny(actor, "@:/ ") {
			return "user:" + actor, admin
		}
	}

	return "", admin
}

func first(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}
