// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package authzfilter

import (
	"context"
	"testing"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// SubjectFromPrincipal — извлекает FGA-subject из operations.Principal,
// который кладёт в ctx principal-extract-интерсептор (читает api-gateway
// headers x-kacho-principal-type/id). Это ЕДИНЫЙ источник identity и для
// per-RPC Check, и для list-filter (parity с kacho-vpc pbconv.SubjectFromContext).

func TestSubjectFromPrincipal_User(t *testing.T) {
	ctx := operations.WithPrincipal(context.Background(), operations.Principal{
		Type: "user", ID: "usr_alice", DisplayName: "alice@kacho.local",
	})
	if got := SubjectFromPrincipal(ctx); got != "user:usr_alice" {
		t.Fatalf("user: want user:usr_alice, got %q", got)
	}
}

func TestSubjectFromPrincipal_ServiceAccount(t *testing.T) {
	ctx := operations.WithPrincipal(context.Background(), operations.Principal{
		Type: "service_account", ID: "sva_kube",
	})
	if got := SubjectFromPrincipal(ctx); got != "service_account:sva_kube" {
		t.Fatalf("sa: want service_account:sva_kube, got %q", got)
	}
}

// System principal (control-plane bootstrap / no-auth dev) → пустой subject.
// Handler трактует это как fail-closed (пустой List, не bypass-all).
func TestSubjectFromPrincipal_System(t *testing.T) {
	ctx := operations.WithPrincipal(context.Background(), operations.SystemPrincipal())
	if got := SubjectFromPrincipal(ctx); got != "" {
		t.Fatalf("system: want empty subject, got %q", got)
	}
}

// Пустой ctx (нет principal-extract) → SystemPrincipal → пустой subject.
func TestSubjectFromPrincipal_EmptyCtx(t *testing.T) {
	if got := SubjectFromPrincipal(context.Background()); got != "" {
		t.Fatalf("empty ctx: want empty subject, got %q", got)
	}
}

// Anonymous principal (gateway проброс без identity: type=user, id=anonymous)
// либо неполный principal (только type) → пустой subject (fail-closed).
func TestSubjectFromPrincipal_Incomplete(t *testing.T) {
	cases := []operations.Principal{
		{Type: "user", ID: ""},
		{Type: "", ID: "usr_x"},
		{Type: "system", ID: "anonymous"},
	}
	for _, p := range cases {
		ctx := operations.WithPrincipal(context.Background(), p)
		if got := SubjectFromPrincipal(ctx); got != "" {
			t.Fatalf("incomplete principal %+v: want empty subject, got %q", p, got)
		}
	}
}
