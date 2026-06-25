// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package authzfilter

import (
	"context"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// SubjectFromPrincipal — извлекает FGA-subject ("user:usr_x" / "service_account:sva_x")
// из Principal запроса. Principal кладёт в ctx principal-extract-интерсептор,
// читающий заголовки api-gateway `x-kacho-principal-type` / `x-kacho-principal-id`.
//
// Это ЕДИНЫЙ источник caller-identity и для per-RPC Check, и для list-filter —
// тот же путь, что в kacho-vpc (pbconv.SubjectFromContext). system-principal
// (control-plane bootstrap / no-auth) либо неполный principal → "" (handler
// трактует пустой subject как fail-closed: пустой List, НЕ bypass-all).
//
// Прежний источник из gRPC-метадаты `x-kacho-subject*` упразднён: api-gateway
// такие заголовки не шлёт, что приводило к subject="" → bypass → утечке всего
// списка мимо list-authz (over-show leak).
func SubjectFromPrincipal(ctx context.Context) string {
	p := operations.PrincipalFromContext(ctx)
	if p.Type == "" || p.ID == "" || p.Type == "system" {
		return ""
	}
	return p.Type + ":" + p.ID
}
