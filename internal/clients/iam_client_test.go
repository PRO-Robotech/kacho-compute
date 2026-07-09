// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package clients

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	iamv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/iam/v1"
	operationv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"
)

// fakeProjectServiceClient — in-memory iamv1.ProjectServiceClient для unit-теста
// ProjectClient. getFn полностью контролирует ответ Get (found / not-found /
// down / blocking); остальные методы не используются ProjectClient.Exists и
// возвращают Unimplemented.
type fakeProjectServiceClient struct {
	getFn func(ctx context.Context, in *iamv1.GetProjectRequest) (*iamv1.Project, error)
}

func (f *fakeProjectServiceClient) Get(ctx context.Context, in *iamv1.GetProjectRequest, _ ...grpc.CallOption) (*iamv1.Project, error) {
	return f.getFn(ctx, in)
}

func (f *fakeProjectServiceClient) List(_ context.Context, _ *iamv1.ListProjectsRequest, _ ...grpc.CallOption) (*iamv1.ListProjectsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used in test")
}

func (f *fakeProjectServiceClient) Create(_ context.Context, _ *iamv1.CreateProjectRequest, _ ...grpc.CallOption) (*operationv1.Operation, error) {
	return nil, status.Error(codes.Unimplemented, "not used in test")
}

func (f *fakeProjectServiceClient) Update(_ context.Context, _ *iamv1.UpdateProjectRequest, _ ...grpc.CallOption) (*operationv1.Operation, error) {
	return nil, status.Error(codes.Unimplemented, "not used in test")
}

func (f *fakeProjectServiceClient) Delete(_ context.Context, _ *iamv1.DeleteProjectRequest, _ ...grpc.CallOption) (*operationv1.Operation, error) {
	return nil, status.Error(codes.Unimplemented, "not used in test")
}

func (f *fakeProjectServiceClient) ListOperations(_ context.Context, _ *iamv1.ListProjectOperationsRequest, _ ...grpc.CallOption) (*iamv1.ListProjectOperationsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used in test")
}

// TestProjectClient_Exists_Found — iam возвращает Project → Exists true, nil.
func TestProjectClient_Exists_Found(t *testing.T) {
	fake := &fakeProjectServiceClient{getFn: func(_ context.Context, in *iamv1.GetProjectRequest) (*iamv1.Project, error) {
		require.Equal(t, "prj_x", in.GetProjectId())
		return &iamv1.Project{Id: "prj_x"}, nil
	}}
	c := NewProjectClientWith(fake)

	exists, err := c.Exists(context.Background(), "prj_x")
	require.NoError(t, err)
	require.True(t, exists)
}

// TestProjectClient_Exists_NotFound — iam NOT_FOUND → (false, nil), не ошибка
// (checkProject маппит exists=false в NotFound "Folder ... not found").
func TestProjectClient_Exists_NotFound(t *testing.T) {
	fake := &fakeProjectServiceClient{getFn: func(_ context.Context, _ *iamv1.GetProjectRequest) (*iamv1.Project, error) {
		return nil, status.Error(codes.NotFound, "Project no-such-project not found")
	}}
	c := NewProjectClientWith(fake)

	exists, err := c.Exists(context.Background(), "no-such-project")
	require.NoError(t, err)
	require.False(t, exists)
}

// TestProjectClient_Exists_Unavailable — iam недоступен (transport-ошибка) →
// Exists пробрасывает ошибку (НЕ трактует как not-found, НЕ silent-success);
// checkProject мапит это в Unavailable "folder check: ..." (fail-closed).
//
// retry.OnUnavailable имеет 30s-бюджет; ctx отменяется до вызова, чтобы тест
// был быстрым и детерминированным (retry-цикл прерывается немедленно).
func TestProjectClient_Exists_Unavailable(t *testing.T) {
	fake := &fakeProjectServiceClient{getFn: func(_ context.Context, _ *iamv1.GetProjectRequest) (*iamv1.Project, error) {
		return nil, status.Error(codes.Unavailable, "connection refused")
	}}
	c := NewProjectClientWith(fake)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.Exists(ctx, "prj_x")
	require.Error(t, err, "iam-down must propagate an error, never silent success")
}

// TestProjectClient_Exists_PositiveCacheBounded — audit-r3 leak regression: the
// positive-cache must stay bounded by maxEntries. Before the fix, every distinct
// projectID that passed Exists() wrote c.exists[projectID] and was never removed
// (TTL was consulted only on read, never triggering a delete) → a long-running
// control-plane process with project churn accumulated entries forever (unbounded
// memory growth on the Create/Move hot-path). The bounded putExists evicts an
// arbitrary entry at the cap, mirroring authzfilter.FGAFilter.putCache.
func TestProjectClient_Exists_PositiveCacheBounded(t *testing.T) {
	fake := &fakeProjectServiceClient{getFn: func(_ context.Context, in *iamv1.GetProjectRequest) (*iamv1.Project, error) {
		return &iamv1.Project{Id: in.GetProjectId()}, nil
	}}
	c := NewProjectClientWith(fake)
	c.maxEntries = 8 // shrink the bound for a fast, deterministic test

	const distinct = 100
	for i := 0; i < distinct; i++ {
		exists, err := c.Exists(context.Background(), fmt.Sprintf("prj_%03d", i))
		require.NoError(t, err)
		require.True(t, exists)
	}

	c.mu.RLock()
	size := len(c.exists)
	c.mu.RUnlock()
	require.LessOrEqual(t, size, c.maxEntries,
		"positive cache must stay bounded by maxEntries; unbounded growth on distinct-projectID churn leaks memory")
}

// TestProjectClient_Exists_BlockingPeer_TimesOut — audit-r6 P1 regression: an
// app-slow iam peer (alive, never responds — NOT codes.Unavailable) must be
// bounded by ProjectClient's own per-call timeout, not hang for the life of the
// caller's (here: undeadlined) ctx. Before the fix, c.cli.Get carried the raw
// ctx with no deadline of its own — every Create/Move hot-path call would be
// exposed to an indefinite hang on an app-slow kacho-iam.
func TestProjectClient_Exists_BlockingPeer_TimesOut(t *testing.T) {
	unblock := make(chan struct{})
	defer close(unblock)
	fake := &fakeProjectServiceClient{getFn: func(ctx context.Context, _ *iamv1.GetProjectRequest) (*iamv1.Project, error) {
		select {
		case <-ctx.Done():
			return nil, status.Error(codes.DeadlineExceeded, ctx.Err().Error())
		case <-unblock:
			t.Error("peer must never observe unblock — Exists should return via its own per-call timeout first")
			return nil, status.Error(codes.Unavailable, "unreachable")
		}
	}}
	c := NewProjectClientWith(fake)
	c.timeout = 20 * time.Millisecond // shrink for a fast, deterministic test

	start := time.Now()
	_, err := c.Exists(context.Background(), "prj_x") // caller ctx has NO deadline
	elapsed := time.Since(start)

	require.Error(t, err, "blocking peer must yield an error, never a silent success")
	require.Less(t, elapsed, 2*time.Second,
		"Exists must return around its own configured per-call timeout, not hang on the caller's undeadlined ctx")
}
