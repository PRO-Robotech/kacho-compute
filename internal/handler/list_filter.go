// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// list_filter.go — FGA-filtered List helpers.
//
// Each public List handler (Disk / Image / Snapshot / Instance) calls
// resolveListFilter() to compute the allow-list of resource ids the calling
// subject can read, then passes that allow-list into svc.List via the
// AllowedIDs field of the appropriate filter struct.
//
// resolveListFilter encapsulates the per-RPC contract:
//   - filter == nil (FGA disabled config-gate / dev) → bypass (no FGA call)
//   - subject == "" (system principal / no identity) → FAIL-CLOSED (empty list).
//     This is NOT a bypass: a missing identity must never widen visibility to the
//     whole project. Cluster-admin / owner получают весь список через IAM
//     ListObjects (owner→viewer FGA-каскад), не через handler-side bypass.
//   - normal caller → iam.ListObjects → allowedIDs / empty
//   - iam unreachable and fail-closed → Unavailable (returned to caller)
//   - iam unreachable and fail-open → bypass + audit warn (filter handles)
//
// Caller-identity (subject) берётся из request Principal — ЕДИНЫЙ источник и для
// per-RPC Check, и для list-filter. Прежний источник из `x-kacho-subject*`
// gRPC-метадаты упразднён: api-gateway такие заголовки не шлёт, что давало
// subject="" → bypass → утечку всего списка мимо list-authz (over-show leak).
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

// resolveListFilter — извлекает subject из request Principal, вызывает FGA filter,
// возвращает (bypass, allowedIDs, error).
//
// Decision rules:
//   - filter nil → bypass (FGA disabled in dev / breakglass).
//   - subject == "" (system principal / no identity) → fail-closed: empty
//     allow-list (handler возвращает []), НЕ bypass-all.
//   - normal flow → call filter; respect bypass / empty / allowedIDs.
func resolveListFilter(ctx context.Context, f authzfilter.Filter, resourceType, action string) (listFilterDecision, error) {
	if f == nil {
		return listFilterDecision{bypass: true}, nil
	}
	subject := authzfilter.SubjectFromPrincipal(ctx)
	if subject == "" {
		// No caller-identity (system principal / anonymous): fail-closed. A
		// missing subject must never bypass the filter — that is exactly the
		// over-show leak. Return an empty allow-list so the handler responds
		// with an empty page (existence of other resources stays unknowable).
		return listFilterDecision{allowedIDs: []string{}}, nil
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
