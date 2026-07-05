// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// backstop_integration_test.go — kacho-compute backstop: reconciler + metrics +
// fail-closed boot-gate over the register-outbox, WITHOUT changing co-commit
// atomicity (no migration).
//
//   - reconciler re-drives a poisoned row back to claimable → delivered
//   - fail-closed boot-gate: require-iam + no drainer → Create refused
//   - long-outage no-poison: IAM down > MaxAttempts (transient) → not poisoned →
//     delivered exactly once on recovery + metrics surface backlog/poisoned while
//     pending
//
// testcontainers Postgres 16; real corelib reconciler/drainer/metrics + fake IAM.
// Reuses the harness in register_drainer_integration_test.go (setupDrainerDB,
// fakeIAMRegister, newDrainer, insertIntent, countSent). Skipped under -short.
package clients_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/outbox/bootgate"
	"github.com/PRO-Robotech/kacho-corelib/outbox/metrics"
	"github.com/PRO-Robotech/kacho-corelib/outbox/reconciler"
	iamv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/iam/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/clients"
	"github.com/PRO-Robotech/kacho-compute/internal/fgaboot"
	"github.com/PRO-Robotech/kacho-compute/internal/fgaintent"
)

const computeOutboxTbl = "public.compute_fga_register_outbox"

// Test_1_4_30_ReconcilerRedrivesPoisoned — 1.4-30: a poisoned register-intent
// (attempt_count >= MaxAttempts, sent_at NULL) is re-driven to claimable by the
// reconciler → the drainer then delivers it (sent_at NOT NULL) with its ORIGINAL
// decoder-correct tuple payload. Atomicity untouched (no resource-writer change).
func Test_1_4_30_ReconcilerRedrivesPoisoned(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := setupDrainerDB(t)

	// A poisoned intent with a valid tuple payload (the cause is now fixed).
	insertIntent(ctx, t, pool, fgaintent.EventRegister, "Instance", "epd-redrive",
		fgaintent.Tuple{SubjectID: "project:proj-x", Relation: "project", Object: "compute_instance:epd-redrive"})
	_, err := pool.Exec(ctx,
		`UPDATE compute_fga_register_outbox SET attempt_count = 10, last_error = 'was permanent'
		   WHERE resource_id = 'epd-redrive'`)
	require.NoError(t, err)

	ad := clients.NewFGAReconcileAdapter(pool, computeOutboxTbl)
	rc, err := reconciler.New(pool, reconciler.Config{
		Table:       computeOutboxTbl,
		Channel:     "compute_fga_register_outbox",
		MaxAttempts: 10,
	}, reconciler.Adapters{Enumerator: ad, Registry: ad}, nil)
	require.NoError(t, err)

	n, err := rc.RedrivePoisoned(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "exactly one poisoned row re-driven")

	var attempt int
	var lastErr *string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT attempt_count, last_error FROM compute_fga_register_outbox WHERE resource_id='epd-redrive'`).
		Scan(&attempt, &lastErr))
	assert.Less(t, attempt, 10, "attempt_count reset below MaxAttempts (claimable)")
	assert.Nil(t, lastErr, "last_error cleared")

	// The drainer now delivers the re-driven intent (IAM healthy).
	fake := &fakeIAMRegister{}
	applier := clients.NewIAMRegisterApplierWithClient(fake)
	d := newDrainer(t, pool, applier)
	go func() { _ = d.Run(ctx) }()
	require.Eventually(t, func() bool {
		return fake.registeredCount() == 1
	}, 5*time.Second, 50*time.Millisecond, "re-driven intent delivered exactly once")
}

// Test_1_4_31_FailClosedBootGate_RefusesCreate — 1.4-31: require-iam armed +
// register-drainer not connected → guardCreateUnary refuses a mutating Create
// (UNAVAILABLE), the resource is not created; read RPCs pass; Internal-admin
// Creates (DiskType/Region/Zone) are not gated; connect → Create allowed.
func Test_1_4_31_FailClosedBootGate_RefusesCreate(t *testing.T) {
	gate := bootgate.New(bootgate.Config{RequireIAM: true, Service: "kacho-compute"})
	assert.False(t, gate.Ready(), "require-iam + not connected → NotReady")
	guard := fgaboot.GuardCreateUnary(gate)

	createInvoked := false
	createHandler := func(_ context.Context, _ any) (any, error) { createInvoked = true; return "ok", nil }
	_, err := guard(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.compute.v1.InstanceService/Create"}, createHandler)
	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err), "Create refused fail-closed (UNAVAILABLE)")
	assert.False(t, createInvoked, "resource not created — handler never reached")

	getInvoked := false
	_, err = guard(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.compute.v1.InstanceService/Get"},
		func(_ context.Context, _ any) (any, error) { getInvoked = true; return "inst", nil })
	require.NoError(t, err)
	assert.True(t, getInvoked, "read RPC works on a not-yet-ready instance")

	adminInvoked := false
	_, err = guard(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.compute.v1.InternalZoneService/Create"},
		func(_ context.Context, _ any) (any, error) { adminInvoked = true; return "zone", nil })
	require.NoError(t, err)
	assert.True(t, adminInvoked, "Internal-admin Create not gated (no owner-tuple)")

	gate.SetConnected(true)
	assert.True(t, gate.Ready(), "connected → Ready")
	createInvoked = false
	_, err = guard(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.compute.v1.InstanceService/Create"}, createHandler)
	require.NoError(t, err)
	assert.True(t, createInvoked, "Create allowed once IAM-register path connected")
}

// Test_1_4_31_RequireIAMOff_NoOp — contrast: require-iam=false (dev) → no-op gate.
func Test_1_4_31_RequireIAMOff_NoOp(t *testing.T) {
	gate := bootgate.New(bootgate.Config{RequireIAM: false, Service: "kacho-compute"})
	assert.True(t, gate.Ready(), "require-iam off → always Ready (dev)")
	guard := fgaboot.GuardCreateUnary(gate)
	invoked := false
	_, err := guard(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.compute.v1.InstanceService/Create"},
		func(_ context.Context, _ any) (any, error) { invoked = true; return "ok", nil })
	require.NoError(t, err)
	assert.True(t, invoked, "Create allowed in dev back-compat mode")
}

// controllableIAM — a fake IAM register client whose outage is flipped by the
// test (down → Unavailable on every call). Used for the deterministic long-outage
// no-poison scenario.
type controllableIAM struct {
	down     atomic.Bool
	attempts atomic.Int32
	applied  atomic.Int32
}

func (c *controllableIAM) RegisterResource(_ context.Context, _ *iamv1.RegisterResourceRequest, _ ...grpc.CallOption) (*iamv1.RegisterResourceResponse, error) {
	if c.down.Load() {
		c.attempts.Add(1)
		return nil, status.Error(codes.Unavailable, "iam down")
	}
	c.applied.Add(1)
	return &iamv1.RegisterResourceResponse{}, nil
}

func (c *controllableIAM) UnregisterResource(_ context.Context, _ *iamv1.UnregisterResourceRequest, _ ...grpc.CallOption) (*iamv1.UnregisterResourceResponse, error) {
	return &iamv1.UnregisterResourceResponse{}, nil
}

// Test_1_4_32_LongOutageNoPoison_ThenMetricsSurface — 1.4-32 + 1.4-23: IAM
// Unavailable for MORE than MaxAttempts consecutive transient attempts (D-5) → the
// intent is NOT poisoned (stays pending) → delivered exactly once on recovery; the
// metrics Collector surfaces backlog/oldest while pending, poisoned stays 0.
func Test_1_4_32_LongOutageNoPoison_ThenMetricsSurface(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := setupDrainerDB(t)

	const maxAttempts = 5
	iam := &controllableIAM{}
	iam.down.Store(true)
	applier := clients.NewIAMRegisterApplierWithClient(iam)
	d := newDrainer(t, pool, applier) // MaxAttempts=5 in the harness
	go func() { _ = d.Run(ctx) }()

	insertIntent(ctx, t, pool, fgaintent.EventRegister, "Instance", "epd-long",
		fgaintent.Tuple{SubjectID: "project:proj-x", Relation: "project", Object: "compute_instance:epd-long"})

	rec := metrics.NewMemRecorder()
	col := metrics.NewCollector(pool, rec, metrics.CollectorConfig{Table: computeOutboxTbl, MaxAttempts: maxAttempts})

	// While IAM is down: > maxAttempts transient attempts yet the intent is NOT
	// poisoned (still pending) — and metrics surface backlog + oldest age (D-5/D-7).
	require.Eventually(t, func() bool {
		_ = col.Scan(ctx)
		return iam.attempts.Load() > maxAttempts &&
			rec.BacklogDepth(computeOutboxTbl) >= 1 && rec.OldestPendingAgeSeconds(computeOutboxTbl) > 0
	}, 10*time.Second, 100*time.Millisecond, "> maxAttempts transient attempts, still pending, backlog surfaced")

	var sentNull bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT sent_at IS NULL FROM compute_fga_register_outbox WHERE resource_id='epd-long'`).Scan(&sentNull))
	assert.True(t, sentNull, "intent durable (pending) through a transient outage longer than MaxAttempts")

	// IAM recovers → delivered exactly once.
	iam.down.Store(false)
	require.Eventually(t, func() bool {
		return iam.applied.Load() == 1
	}, 10*time.Second, 100*time.Millisecond, "tuple delivered exactly once after long transient outage (no poison)")

	sent, _ := countSent(ctx, t, pool)
	assert.Equal(t, 1, sent, "intent ultimately delivered (not lost)")

	require.NoError(t, col.Scan(ctx))
	assert.Equal(t, float64(0), rec.PoisonedCount(computeOutboxTbl),
		"a transient (Unavailable) outage must NOT poison — outbox_poisoned stays 0")
}
