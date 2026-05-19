package authzfilter

import (
	"context"
	"testing"

	"google.golang.org/grpc/metadata"
)

func ctxWithMD(pairs ...string) context.Context {
	md := metadata.Pairs(pairs...)
	return metadata.NewIncomingContext(context.Background(), md)
}

func TestSubjectFromCtx_CanonicalSubject(t *testing.T) {
	ctx := ctxWithMD(mdKeySubject, "user:usr_alice")
	got, admin := SubjectFromCtx(ctx)
	if got != "user:usr_alice" {
		t.Fatalf("subject: want user:usr_alice, got %q", got)
	}
	if admin {
		t.Fatalf("admin: want false, got true")
	}
}

func TestSubjectFromCtx_LegacyTwoHeaders(t *testing.T) {
	ctx := ctxWithMD(mdKeySubjectType, "service_account", mdKeySubjectID, "sa_xyz")
	got, _ := SubjectFromCtx(ctx)
	if got != "service_account:sa_xyz" {
		t.Fatalf("legacy: want service_account:sa_xyz, got %q", got)
	}
}

func TestSubjectFromCtx_AdminTrue(t *testing.T) {
	ctx := ctxWithMD(mdKeySubject, "user:usr_root", mdKeyAdmin, "true")
	got, admin := SubjectFromCtx(ctx)
	if got != "user:usr_root" {
		t.Fatalf("subject: got %q", got)
	}
	if !admin {
		t.Fatalf("admin: want true")
	}
}

func TestSubjectFromCtx_Anonymous(t *testing.T) {
	got, admin := SubjectFromCtx(context.Background())
	if got != "" {
		t.Fatalf("anonymous: subject should be empty, got %q", got)
	}
	if admin {
		t.Fatalf("anonymous: admin should be false")
	}
}

func TestSubjectFromCtx_ActorFallback(t *testing.T) {
	ctx := ctxWithMD(mdKeyActor, "usr_bob")
	got, _ := SubjectFromCtx(ctx)
	if got != "user:usr_bob" {
		t.Fatalf("actor fallback: want user:usr_bob, got %q", got)
	}
}

// Actor that looks like an email is NOT treated as an FGA subject.
func TestSubjectFromCtx_ActorWithAtNotUsed(t *testing.T) {
	ctx := ctxWithMD(mdKeyActor, "admin@kacho.local")
	got, _ := SubjectFromCtx(ctx)
	if got != "" {
		t.Fatalf("actor with @: should be empty (not a clean id), got %q", got)
	}
}

// Canonical subject takes precedence over legacy / actor.
func TestSubjectFromCtx_Precedence(t *testing.T) {
	ctx := ctxWithMD(
		mdKeySubject, "user:usr_canonical",
		mdKeySubjectType, "user", mdKeySubjectID, "usr_legacy",
		mdKeyActor, "usr_actor",
	)
	got, _ := SubjectFromCtx(ctx)
	if got != "user:usr_canonical" {
		t.Fatalf("precedence: want canonical, got %q", got)
	}
}
