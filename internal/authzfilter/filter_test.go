// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package authzfilter

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	iamv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/iam/v1"
)

// mockAuthClient — captures calls and returns programmed responses.
type mockAuthClient struct {
	calls          atomic.Int64
	responses      []*iamv1.ListObjectsResponse
	err            error
	sleep          time.Duration
	lastSubject    string
	lastResType    string
	lastAction     string
	lastMaxResults int64
}

func (m *mockAuthClient) ListObjects(ctx context.Context, in *iamv1.ListObjectsRequest, _ ...grpc.CallOption) (*iamv1.ListObjectsResponse, error) {
	m.calls.Add(1)
	m.lastSubject = in.GetSubject()
	m.lastResType = in.GetResourceType()
	m.lastAction = in.GetAction()
	m.lastMaxResults = in.GetMaxResults()
	if m.sleep > 0 {
		select {
		case <-time.After(m.sleep):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if m.err != nil {
		return nil, m.err
	}
	if len(m.responses) == 0 {
		return &iamv1.ListObjectsResponse{}, nil
	}
	resp := m.responses[0]
	if len(m.responses) > 1 {
		m.responses = m.responses[1:]
	}
	return resp, nil
}

// P4.GWT-01: cache miss → iam call → result cached → second call hit.
func TestFGAFilter_CacheMissThenHit(t *testing.T) {
	mock := &mockAuthClient{
		responses: []*iamv1.ListObjectsResponse{
			{ResourceIds: []string{"epd-instance-2", "epd-instance-1"}},
		},
	}
	f := NewFGAFilter(mock, DefaultConfig())

	ctx := context.Background()
	d1, err := f.ListAllowedIDs(ctx, "user:usr_alice", ResourceTypeInstance, ActionInstanceRead)
	if err != nil {
		t.Fatalf("first call err: %v", err)
	}
	if d1.FromCache {
		t.Fatalf("first call: should NOT be from cache")
	}
	// Deterministic ordering
	if got := d1.IDs(); len(got) != 2 || got[0] != "epd-instance-1" || got[1] != "epd-instance-2" {
		t.Fatalf("first call: IDs not sorted: %v", got)
	}

	d2, err := f.ListAllowedIDs(ctx, "user:usr_alice", ResourceTypeInstance, ActionInstanceRead)
	if err != nil {
		t.Fatalf("second call err: %v", err)
	}
	if !d2.FromCache {
		t.Fatalf("second call: must be cache hit")
	}
	if mock.calls.Load() != 1 {
		t.Fatalf("expected exactly 1 iam call, got %d", mock.calls.Load())
	}
	if mock.lastSubject != "user:usr_alice" || mock.lastResType != ResourceTypeInstance || mock.lastAction != ActionInstanceRead {
		t.Fatalf("bad iam request: %+v", mock)
	}
}

// P4.GWT-03 / P4.GWT-33: fail-closed on iam unavailable.
func TestFGAFilter_FailClosed(t *testing.T) {
	mock := &mockAuthClient{err: status.Error(codes.Unavailable, "iam down")}
	f := NewFGAFilter(mock, DefaultConfig())

	_, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeInstance, ActionInstanceRead)
	if err == nil {
		t.Fatalf("expected error on iam unavailable, got nil")
	}
	if got := status.Code(err); got != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %s", got)
	}
}

// fail-open recovery: degraded mode.
func TestFGAFilter_FailOpen(t *testing.T) {
	mock := &mockAuthClient{err: status.Error(codes.Unavailable, "iam down")}
	cfg := DefaultConfig()
	cfg.FailOpen = true
	f := NewFGAFilter(mock, cfg)

	d, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeInstance, ActionInstanceRead)
	if err != nil {
		t.Fatalf("fail-open: must not return error, got: %v", err)
	}
	if !d.IsBypass() || !d.FailOpen {
		t.Fatalf("fail-open: expected BypassAll=true + FailOpen=true, got %+v", d)
	}
}

// Regression: fail-open degraded-mode MUST emit an audit WARN. handleErr returns
// BypassAll (every row in the project becomes visible, bypassing the per-object
// allow-list) — doc.go and the Config.FailOpen godoc both promise an audit-warn.
// Without an emitted WARN an operator who enabled fail-open gets a silently
// authz-degraded mode with zero observability. Lock the observable (a WARN is
// produced), not just the returned Decision.
func TestFGAFilter_FailOpenEmitsAuditWarn(t *testing.T) {
	mock := &mockAuthClient{err: status.Error(codes.Unavailable, "iam down")}
	cfg := DefaultConfig()
	cfg.FailOpen = true
	f := NewFGAFilter(mock, cfg)

	// White-box capture of the audit sink, mirroring the f.now injection used by
	// the TTL tests. Level threshold WARN so an accidental Info would not pass.
	var buf bytes.Buffer
	f.logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	d, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeInstance, ActionInstanceRead)
	if err != nil {
		t.Fatalf("fail-open: must not return error, got: %v", err)
	}
	if !d.IsBypass() || !d.FailOpen {
		t.Fatalf("fail-open: expected BypassAll=true + FailOpen=true, got %+v", d)
	}
	logged := buf.String()
	if logged == "" {
		t.Fatalf("fail-open: expected an audit WARN, got no log output (degraded authz mode is silent)")
	}
	if !strings.Contains(logged, "level=WARN") {
		t.Fatalf("fail-open: audit log must be WARN level, got: %q", logged)
	}
	if !strings.Contains(logged, "fail-open") {
		t.Fatalf("fail-open: audit log must identify the fail-open bypass, got: %q", logged)
	}
}

// P4.GWT-07: empty grant → empty list (NOT error).
func TestFGAFilter_EmptyGrant(t *testing.T) {
	mock := &mockAuthClient{
		responses: []*iamv1.ListObjectsResponse{{ResourceIds: []string{}}},
	}
	f := NewFGAFilter(mock, DefaultConfig())

	d, err := f.ListAllowedIDs(context.Background(), "user:usr_no_grants", ResourceTypeInstance, ActionInstanceRead)
	if err != nil {
		t.Fatalf("empty grant: should not error, got: %v", err)
	}
	if !d.Empty || d.IsBypass() {
		t.Fatalf("empty grant: expected Empty=true BypassAll=false, got %+v", d)
	}
	if len(d.IDs()) != 0 {
		t.Fatalf("empty grant: expected zero IDs, got %v", d.IDs())
	}
}

// Bypass when filter disabled (config gate).
func TestFGAFilter_DisabledIsBypass(t *testing.T) {
	mock := &mockAuthClient{}
	cfg := DefaultConfig()
	cfg.Enabled = false
	f := NewFGAFilter(mock, cfg)

	d, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeInstance, ActionInstanceRead)
	if err != nil {
		t.Fatalf("disabled: err: %v", err)
	}
	if !d.IsBypass() {
		t.Fatalf("disabled: expected BypassAll=true")
	}
	if mock.calls.Load() != 0 {
		t.Fatalf("disabled: must NOT call iam (got %d calls)", mock.calls.Load())
	}
}

// Bypass when client is nil (graceful start без iam).
func TestFGAFilter_NilClientIsBypass(t *testing.T) {
	f := NewFGAFilter(nil, DefaultConfig())

	d, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeInstance, ActionInstanceRead)
	if err != nil {
		t.Fatalf("nil client: err: %v", err)
	}
	if !d.IsBypass() {
		t.Fatalf("nil client: expected BypassAll=true")
	}
}

// Subject required when filter is enabled.
func TestFGAFilter_AnonymousFailClosed(t *testing.T) {
	mock := &mockAuthClient{}
	f := NewFGAFilter(mock, DefaultConfig())

	_, err := f.ListAllowedIDs(context.Background(), "", ResourceTypeInstance, ActionInstanceRead)
	if err == nil {
		t.Fatalf("anonymous: expected error, got nil")
	}
	if got := status.Code(err); got != codes.Unauthenticated {
		t.Fatalf("anonymous: expected Unauthenticated, got %s", got)
	}
}

// P4.GWT-29 latency: timeout enforcement.
func TestFGAFilter_TimeoutEnforced(t *testing.T) {
	mock := &mockAuthClient{sleep: 100 * time.Millisecond, err: nil, responses: []*iamv1.ListObjectsResponse{{}}}
	cfg := DefaultConfig()
	cfg.Timeout = 10 * time.Millisecond
	f := NewFGAFilter(mock, cfg)

	// The mock honours ctx.Done(): if the 10ms deadline fires before the 100ms
	// sleep completes, ListObjects returns ctx.Err() → Unavailable. If the timeout
	// were NOT enforced, the mock would sleep the full 100ms and return a success
	// response (err==nil) → the err!=nil + Unavailable assertions below would fail.
	// So enforcement is proven deterministically without a flaky wall-clock upper
	// bound on elapsed time.
	_, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeInstance, ActionInstanceRead)
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	if got := status.Code(err); got != codes.Unavailable {
		t.Fatalf("timeout: expected Unavailable, got %s", got)
	}
}

// Cache TTL expiry → second call again hits iam.
func TestFGAFilter_CacheTTLExpiry(t *testing.T) {
	mock := &mockAuthClient{
		responses: []*iamv1.ListObjectsResponse{
			{ResourceIds: []string{"id-1"}},
			{ResourceIds: []string{"id-1", "id-2"}},
		},
	}
	cfg := DefaultConfig()
	cfg.CacheTTL = 25 * time.Millisecond
	f := NewFGAFilter(mock, cfg)

	// Deterministic fake clock: TTL-expiry is driven by advancing logical time,
	// not time.Sleep + wall-clock (which is flaky under CI scheduler jitter).
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	f.now = clk.now

	d1, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeInstance, ActionInstanceRead)
	if err != nil {
		t.Fatal(err)
	}
	if len(d1.IDs()) != 1 {
		t.Fatalf("first call: want 1 id, got %v", d1.IDs())
	}

	// Advance past the 25ms TTL: entry must be treated as expired.
	clk.advance(40 * time.Millisecond)

	d2, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeInstance, ActionInstanceRead)
	if err != nil {
		t.Fatal(err)
	}
	if len(d2.IDs()) != 2 || d2.FromCache {
		t.Fatalf("post-TTL: must call iam again, got %v (fromCache=%v)", d2.IDs(), d2.FromCache)
	}
	if mock.calls.Load() != 2 {
		t.Fatalf("expected 2 iam calls after TTL expiry, got %d", mock.calls.Load())
	}
}

// Cache bound: max-entries-bounds-and-evicts.
func TestFGAFilter_CacheBounded(t *testing.T) {
	mock := &mockAuthClient{responses: []*iamv1.ListObjectsResponse{{}}}
	cfg := DefaultConfig()
	cfg.CacheMaxEntries = 3
	cfg.CacheTTL = time.Hour // ensure entries don't expire during test
	f := NewFGAFilter(mock, cfg)

	for i := 0; i < 10; i++ {
		mock.responses = []*iamv1.ListObjectsResponse{{}}
		subj := "user:usr_" + string(rune('a'+i))
		if _, err := f.ListAllowedIDs(context.Background(), subj, ResourceTypeInstance, ActionInstanceRead); err != nil {
			t.Fatal(err)
		}
	}
	if size := f.Size(); size > cfg.CacheMaxEntries {
		t.Fatalf("cache bound violated: size=%d > max=%d", size, cfg.CacheMaxEntries)
	}
}

// Result cap MUST be decoupled from cache sizing: an operator lowering
// CacheMaxEntries (memory tuning) must NOT silently truncate the tenant List
// allow-list. The FGA ListObjects MaxResults is the number of authorized ids the
// filter will ever return — capping it to the cache bound drops the tail ids,
// making an owner's own resources invisible on every page with no error surfaced.
func TestFGAFilter_ResultCapDecoupledFromCacheSize(t *testing.T) {
	mock := &mockAuthClient{
		responses: []*iamv1.ListObjectsResponse{{ResourceIds: []string{"epd-instance-1"}}},
	}
	cfg := DefaultConfig()
	cfg.CacheMaxEntries = 3 // operator tuned cache down for memory
	f := NewFGAFilter(mock, cfg)

	if _, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeInstance, ActionInstanceRead); err != nil {
		t.Fatalf("list err: %v", err)
	}
	if mock.lastMaxResults == int64(cfg.CacheMaxEntries) {
		t.Fatalf("FGA MaxResults tied to CacheMaxEntries (%d): allow-list silently truncated to cache size", mock.lastMaxResults)
	}
	if got, want := mock.lastMaxResults, int64(defaultListMaxResults); got != want {
		t.Fatalf("FGA MaxResults = %d, want dedicated result cap %d (independent of cache)", got, want)
	}
}

// Sanity: status preservation on PermissionDenied (not Unavailable wrap).
func TestFGAFilter_PreservesCodes(t *testing.T) {
	mock := &mockAuthClient{err: status.Error(codes.PermissionDenied, "no")}
	f := NewFGAFilter(mock, DefaultConfig())
	_, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeInstance, ActionInstanceRead)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := status.Code(err); got != codes.Unavailable {
		t.Fatalf("expected Unavailable wrap on upstream PD, got %s", got)
	}
}

// Generic errors → Unavailable wrap.
func TestFGAFilter_GenericErrWrapsUnavailable(t *testing.T) {
	mock := &mockAuthClient{err: errors.New("boom")}
	f := NewFGAFilter(mock, DefaultConfig())
	_, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeInstance, ActionInstanceRead)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := status.Code(err); got != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %s", got)
	}
}

// Regression: refreshing an already-present cache key while at capacity must NOT
// evict a *different* subject's live decision. The prior putCache evicted
// unconditionally on len>=max, so re-inserting an existing key dropped a random
// live entry (net map shrink → that victim re-incurs an iam.ListObjects RTT, and
// fail-closed Unavailable if iam is momentarily slow). Guard parity with the
// sibling ProjectClient.putExists (!present && len>=max). Run many independent
// trials because Go map iteration picks a random victim — under the bug at least
// one trial evicts a non-refreshed key with overwhelming probability.
func TestFGAFilter_RefreshPresentKeyDoesNotEvictLiveEntry(t *testing.T) {
	const max = 3
	keys := []string{"user:usr_a|t|r", "user:usr_b|t|r", "user:usr_c|t|r"}
	for trial := 0; trial < 64; trial++ {
		cfg := DefaultConfig()
		cfg.CacheMaxEntries = max
		cfg.CacheTTL = time.Hour // entries must not expire during the trial
		f := NewFGAFilter(nil, cfg)

		for _, k := range keys {
			f.putCache(k, Decision{})
		}
		if got := f.Size(); got != max {
			t.Fatalf("trial %d: setup size=%d, want %d", trial, got, max)
		}

		// Refresh an already-present key at capacity — must be a no-op for size.
		f.putCache(keys[0], Decision{})

		if got := f.Size(); got != max {
			t.Fatalf("trial %d: refresh of present key shrank cache: size=%d, want %d", trial, got, max)
		}
		for _, k := range keys {
			if _, ok := f.getCache(k); !ok {
				t.Fatalf("trial %d: live entry %q evicted by refresh of present key", trial, k)
			}
		}
	}
}

// fakeClock — детерминированный источник времени для TTL-тестов кеша.
// Заменяет f.now, чтобы TTL-expiry продвигался логически (advance), а не через
// time.Sleep + wall-clock (flaky под нагрузкой CI).
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
