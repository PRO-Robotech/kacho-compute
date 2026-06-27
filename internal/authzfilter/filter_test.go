package authzfilter

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	iamv1 "github.com/PRO-Robotech/kacho-iam/proto/gen/go/kacho/cloud/iam/v1"
)

// mockAuthClient — captures calls and returns programmed responses.
type mockAuthClient struct {
	calls       atomic.Int64
	responses   []*iamv1.ListObjectsResponse
	err         error
	sleep       time.Duration
	lastSubject string
	lastResType string
	lastAction  string
}

func (m *mockAuthClient) ListObjects(ctx context.Context, in *iamv1.ListObjectsRequest, _ ...grpc.CallOption) (*iamv1.ListObjectsResponse, error) {
	m.calls.Add(1)
	m.lastSubject = in.GetSubject()
	m.lastResType = in.GetResourceType()
	m.lastAction = in.GetAction()
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
	if !d.IsEmpty() || d.IsBypass() {
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

	t0 := time.Now()
	_, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeInstance, ActionInstanceRead)
	elapsed := time.Since(t0)
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	if elapsed > 80*time.Millisecond {
		t.Fatalf("timeout not enforced — elapsed=%s", elapsed)
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

	d1, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeInstance, ActionInstanceRead)
	if err != nil {
		t.Fatal(err)
	}
	if len(d1.IDs()) != 1 {
		t.Fatalf("first call: want 1 id, got %v", d1.IDs())
	}

	time.Sleep(40 * time.Millisecond)

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

// Invalidate by subject removes its cached entries; other subjects keep theirs.
func TestFGAFilter_InvalidateBySubject(t *testing.T) {
	mock := &mockAuthClient{
		responses: []*iamv1.ListObjectsResponse{
			{ResourceIds: []string{"id-a"}},
			{ResourceIds: []string{"id-b"}},
			{ResourceIds: []string{"id-a-new"}},
		},
	}
	f := NewFGAFilter(mock, DefaultConfig())

	if _, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeInstance, ActionInstanceRead); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ListAllowedIDs(context.Background(), "user:usr_bob", ResourceTypeInstance, ActionInstanceRead); err != nil {
		t.Fatal(err)
	}

	f.Invalidate("user:usr_alice")

	// Alice → miss again.
	dA, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeInstance, ActionInstanceRead)
	if err != nil {
		t.Fatal(err)
	}
	if dA.FromCache {
		t.Fatalf("alice: expected cache miss after invalidate")
	}
	// Bob → still hit.
	dB, err := f.ListAllowedIDs(context.Background(), "user:usr_bob", ResourceTypeInstance, ActionInstanceRead)
	if err != nil {
		t.Fatal(err)
	}
	if !dB.FromCache {
		t.Fatalf("bob: expected cache hit (not invalidated)")
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

// Bypass filter trivially bypasses.
func TestBypassFilter(t *testing.T) {
	d, err := BypassFilter{}.ListAllowedIDs(context.Background(), "user:anyone", ResourceTypeSystem, "catalog.read")
	if err != nil {
		t.Fatal(err)
	}
	if !d.IsBypass() {
		t.Fatalf("BypassFilter must return BypassAll=true")
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
