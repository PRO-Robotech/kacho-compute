// list_filter.go — KAC-127 Phase 4: FGA-filtered List helpers.
//
// Each public List handler (Disk / Image / Snapshot / Instance) calls
// resolveListFilter() to compute the allow-list of resource ids the calling
// subject can read, then passes that allow-list into svc.List via the
// AllowedIDs field of the appropriate filter struct.
//
// resolveListFilter encapsulates the per-RPC contract:
//   - admin / cluster-admin → bypass (no FGA call)
//   - anonymous caller in dev mode → bypass (backward compat)
//   - anonymous in production mode → handler's TenantUnaryInterceptor already
//     rejected with PermissionDenied; we never reach here
//   - normal caller → iam.ListObjects → allowedIDs
//   - iam unreachable and fail-closed → Unavailable (returned to caller)
//   - iam unreachable and fail-open → bypass + audit warn (filter handles)
//
// All FGA filter wiring is configurable via env (see internal/config/config.go
// KACHO_COMPUTE_LIST_FILTER_*). Zero hardcoded constants.
package handler

import (
	"context"

	"github.com/PRO-Robotech/kacho-compute/internal/authzfilter"
)

// listFilterDecision — convenience wrapper around authzfilter.Decision.
type listFilterDecision struct {
	bypass     bool
	allowedIDs []string
}

// resolveListFilter — извлекает subject из ctx, вызывает FGA filter,
// возвращает (bypass, allowedIDs, error).
//
// Decision rules:
//   - admin=true (x-kacho-admin: true) → bypass, no FGA call.
//   - subject == "" (anonymous in dev) → bypass; production mode handled
//     upstream by TenantUnaryInterceptor.
//   - filter nil → bypass (FGA disabled in dev).
//   - filter.Enabled=false → bypass (config gate).
//   - normal flow → call filter; respect bypass / empty / allowedIDs.
func resolveListFilter(ctx context.Context, f authzfilter.Filter, resourceType, action string) (listFilterDecision, error) {
	if f == nil {
		return listFilterDecision{bypass: true}, nil
	}
	subject, admin := authzfilter.SubjectFromCtx(ctx)
	if admin {
		return listFilterDecision{bypass: true}, nil
	}
	if subject == "" {
		// Anonymous (no identity headers). The TenantUnaryInterceptor in
		// production mode already rejects these. In dev/break-glass we
		// bypass to preserve backward-compat for unauth'd callers.
		return listFilterDecision{bypass: true}, nil
	}
	d, err := f.ListAllowedIDs(ctx, subject, resourceType, action)
	if err != nil {
		return listFilterDecision{}, err
	}
	if d.IsBypass() {
		return listFilterDecision{bypass: true}, nil
	}
	if d.IsEmpty() {
		return listFilterDecision{allowedIDs: []string{}}, nil
	}
	return listFilterDecision{allowedIDs: d.IDs()}, nil
}
