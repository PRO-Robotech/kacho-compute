// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	"github.com/PRO-Robotech/kacho-corelib/outbox/bootgate"

	"github.com/PRO-Robotech/kacho-compute/internal/observability/health"
)

// okPinger — readiness DB-pinger, всегда здоров.
type okPinger struct{}

func (okPinger) Ping(context.Context) error { return nil }

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestBuildReadinessCheckers_DrainerFlipsWithBootGate(t *testing.T) {
	// lro-worker checker reads the package-level operations.Ready(); start the
	// default dispatcher so the only variable under test is the drainer/bootGate.
	operations.Start()

	gate := bootgate.New(bootgate.Config{RequireIAM: true, Service: "kacho-compute"})
	checkers := buildReadinessCheckers(okPinger{}, gate, nil)
	agg := health.New(checkers)

	// register-drainer not connected → not ready.
	if ready, down := agg.Evaluate(context.Background()); ready {
		t.Fatalf("expected not-ready before drainer connect; down=%v", down)
	}
	// Connect → ready.
	gate.SetConnected(true)
	if ready, down := agg.Evaluate(context.Background()); !ready {
		t.Fatalf("expected ready after drainer connect; down=%v", down)
	}
}

func TestBuildReadinessCheckers_IAMAuthzCheckerPresence(t *testing.T) {
	gate := bootgate.New(bootgate.Config{})

	none := buildReadinessCheckers(okPinger{}, gate, nil)
	if hasChecker(none, "iam-authz") {
		t.Fatal("iam-authz checker must be absent when authzConn is nil")
	}

	conn, err := grpc.NewClient("passthrough:///unused", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer conn.Close()
	with := buildReadinessCheckers(okPinger{}, gate, conn)
	if !hasChecker(with, "iam-authz") {
		t.Fatal("iam-authz checker must be present when authzConn is set")
	}
}

func TestAuthzConnHealth_ShutdownIsDown(t *testing.T) {
	conn, err := grpc.NewClient("passthrough:///unused", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	if err := authzConnHealth(conn); err != nil {
		t.Fatalf("fresh conn should be up, got %v", err)
	}
	conn.Close()
	if err := authzConnHealth(conn); !errors.Is(err, errIAMConnShutdown) {
		t.Fatalf("closed conn should be down (errIAMConnShutdown), got %v", err)
	}
}

func TestSuperviseBackground_UnexpectedExitTriggersShutdown(t *testing.T) {
	var fired atomic.Bool
	ctx := context.Background() // live ctx: a return is UNEXPECTED
	err := superviseBackground(ctx, "drainer",
		func(context.Context) error { return errors.New("loop crashed") },
		func() { fired.Store(true) },
		quietLogger())
	if err == nil {
		t.Fatal("unexpected exit must return a non-nil error")
	}
	if !fired.Load() {
		t.Fatal("unexpected exit must invoke onUnexpectedExit (readiness flip)")
	}
}

func TestSuperviseBackground_NormalShutdownNoTrigger(t *testing.T) {
	var fired atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // ctx already cancelled: a return is the NORMAL shutdown path
	err := superviseBackground(ctx, "reconciler",
		func(c context.Context) error { <-c.Done(); return nil },
		func() { fired.Store(true) },
		quietLogger())
	if err != nil {
		t.Fatalf("normal shutdown must return nil, got %v", err)
	}
	if fired.Load() {
		t.Fatal("normal shutdown must NOT invoke onUnexpectedExit")
	}
}

func hasChecker(checkers []health.Checker, name string) bool {
	for _, c := range checkers {
		if c.Name == name {
			return true
		}
	}
	return false
}
