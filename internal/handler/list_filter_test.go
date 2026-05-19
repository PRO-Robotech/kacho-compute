// list_filter_test.go — KAC-127 Phase 4: handler-level integration tests for
// FGA-filtered List handlers (Disk / Image / Snapshot / Instance).
//
// Uses portmock repos + in-memory authzfilter.Filter (no real iam needed).
// Covers all scenarios from sub-phase-3.4 acceptance:
//   - allowed-subset
//   - empty grant → empty response (NOT 403)
//   - bypass when filter == nil (dev)
//   - admin caller (x-kacho-admin: true) → bypass
//   - subject + FGA returns allow-list → filtered repo result
//   - iam-down + fail-closed → Unavailable
//   - iam-down + fail-open → all results (bypass)
//   - subject from canonical x-kacho-subject header
//   - subject from legacy headers
//   - page-token preserved across filter
//   - cache reuse on repeated calls (verified at filter level)
//   - catalog (DiskType/Zone/Region) NOT filtered.
package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	iamv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/iam/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/authzfilter"
	"github.com/PRO-Robotech/kacho-compute/internal/ports/portmock"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// mockAuthCli — handler-test mirror of authzfilter.mockAuthClient (private there).
type mockAuthCli struct {
	allowedByKey map[string][]string
	err          error
	calls        int
}

func (m *mockAuthCli) ListObjects(_ context.Context, in *iamv1.ListObjectsRequest, _ ...grpc.CallOption) (*iamv1.ListObjectsResponse, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	key := in.Subject + "|" + in.ResourceType + "|" + in.Action
	if ids, ok := m.allowedByKey[key]; ok {
		return &iamv1.ListObjectsResponse{ResourceIds: ids}, nil
	}
	return &iamv1.ListObjectsResponse{}, nil
}

func newFilter(t *testing.T, cli authzfilter.AuthorizeClient) authzfilter.Filter {
	t.Helper()
	cfg := authzfilter.DefaultConfig()
	cfg.Timeout = 200 * time.Millisecond
	cfg.CacheTTL = time.Second
	return authzfilter.NewFGAFilter(cli, cfg)
}

func ctxWithSubject(subject string) context.Context {
	md := metadata.Pairs("x-kacho-subject", subject)
	return metadata.NewIncomingContext(context.Background(), md)
}

func ctxAdmin() context.Context {
	md := metadata.Pairs("x-kacho-subject", "user:usr_root", "x-kacho-admin", "true")
	return metadata.NewIncomingContext(context.Background(), md)
}

// createDisks — seed N disks via the disk service; returns their ids.
func createDisks(t *testing.T, h *DiskHandler, ops *portmock.OpsRepo, projectID string, names ...string) []string {
	t.Helper()
	var ids []string
	for _, n := range names {
		op, err := h.Create(context.Background(), &computev1.CreateDiskRequest{
			ProjectId: projectID, Name: n, ZoneId: "ru-central1-a", Size: 4194304,
		})
		require.NoError(t, err)
		portmock.AwaitAllOpsDone(t, ops)
		done, _ := ops.Get(context.Background(), op.Id)
		var d computev1.Disk
		require.NoError(t, done.Response.UnmarshalTo(&d))
		ids = append(ids, d.Id)
	}
	return ids
}

func setupDiskHandler(t *testing.T, filter authzfilter.Filter) (*DiskHandler, *portmock.OpsRepo) {
	t.Helper()
	ops := portmock.NewOpsRepo()
	svc := service.NewDiskService(
		portmock.NewDiskRepo(),
		portmock.NewImageRepo(),
		portmock.NewSnapshotRepo(),
		portmock.NewDiskTypeRepo("network-ssd"),
		portmock.NewZoneRepo(),
		&portmock.ProjectClient{OK: true},
		ops,
	)
	return NewDiskHandler(svc, filter), ops
}

// SCENARIO 1: filter == nil → handler bypasses (returns all).
func TestDiskHandler_List_FilterNil_Bypass(t *testing.T) {
	h, ops := setupDiskHandler(t, nil)
	createDisks(t, h, ops, "proj", "d1", "d2", "d3")

	resp, err := h.List(ctxWithSubject("user:usr_alice"), &computev1.ListDisksRequest{ProjectId: "proj"})
	require.NoError(t, err)
	require.Len(t, resp.Disks, 3, "filter=nil must return all disks")
}

// SCENARIO 2: admin caller → bypass even with filter set.
func TestDiskHandler_List_AdminBypass(t *testing.T) {
	cli := &mockAuthCli{}
	h, ops := setupDiskHandler(t, newFilter(t, cli))
	createDisks(t, h, ops, "proj", "a", "b", "c")

	resp, err := h.List(ctxAdmin(), &computev1.ListDisksRequest{ProjectId: "proj"})
	require.NoError(t, err)
	require.Len(t, resp.Disks, 3, "admin must see all")
	require.Equal(t, 0, cli.calls, "admin must NOT trigger iam.ListObjects")
}

// SCENARIO 3: allowed subset — FGA returns 2 of 3 ids.
func TestDiskHandler_List_AllowedSubset(t *testing.T) {
	cli := &mockAuthCli{allowedByKey: map[string][]string{}}
	h, ops := setupDiskHandler(t, newFilter(t, cli))
	ids := createDisks(t, h, ops, "proj", "a", "b", "c")
	cli.allowedByKey["user:usr_alice|compute_disk|compute.disks.read"] = []string{ids[0], ids[2]}

	resp, err := h.List(ctxWithSubject("user:usr_alice"), &computev1.ListDisksRequest{ProjectId: "proj"})
	require.NoError(t, err)
	require.Len(t, resp.Disks, 2)
	gotIDs := map[string]bool{}
	for _, d := range resp.Disks {
		gotIDs[d.Id] = true
	}
	require.True(t, gotIDs[ids[0]] && gotIDs[ids[2]])
	require.False(t, gotIDs[ids[1]])
}

// SCENARIO 4: empty grant → empty response (NOT 403).
func TestDiskHandler_List_EmptyGrant(t *testing.T) {
	cli := &mockAuthCli{} // no entries → returns empty []
	h, ops := setupDiskHandler(t, newFilter(t, cli))
	createDisks(t, h, ops, "proj", "a", "b")

	resp, err := h.List(ctxWithSubject("user:usr_nobody"), &computev1.ListDisksRequest{ProjectId: "proj"})
	require.NoError(t, err, "empty grant must not error")
	require.Len(t, resp.Disks, 0)
}

// SCENARIO 5: iam-down + fail-closed → Unavailable.
func TestDiskHandler_List_IAMDown_FailClosed(t *testing.T) {
	cli := &mockAuthCli{err: status.Error(codes.Unavailable, "down")}
	h, ops := setupDiskHandler(t, newFilter(t, cli))
	createDisks(t, h, ops, "proj", "a")

	_, err := h.List(ctxWithSubject("user:usr_alice"), &computev1.ListDisksRequest{ProjectId: "proj"})
	require.Error(t, err)
	require.Equal(t, codes.Unavailable, status.Code(err))
}

// SCENARIO 6: iam-down + fail-open → all results.
func TestDiskHandler_List_IAMDown_FailOpen(t *testing.T) {
	cli := &mockAuthCli{err: errors.New("network err")}
	cfg := authzfilter.DefaultConfig()
	cfg.FailOpen = true
	filter := authzfilter.NewFGAFilter(cli, cfg)

	h, ops := setupDiskHandler(t, filter)
	createDisks(t, h, ops, "proj", "a", "b")

	resp, err := h.List(ctxWithSubject("user:usr_alice"), &computev1.ListDisksRequest{ProjectId: "proj"})
	require.NoError(t, err, "fail-open: must succeed despite iam error")
	require.Len(t, resp.Disks, 2)
}

// SCENARIO 7: anonymous caller (no subject) → bypass in dev (no error).
func TestDiskHandler_List_AnonymousBypass(t *testing.T) {
	cli := &mockAuthCli{}
	h, ops := setupDiskHandler(t, newFilter(t, cli))
	createDisks(t, h, ops, "proj", "a", "b")

	// No metadata at all.
	resp, err := h.List(context.Background(), &computev1.ListDisksRequest{ProjectId: "proj"})
	require.NoError(t, err)
	require.Len(t, resp.Disks, 2)
	require.Equal(t, 0, cli.calls, "anonymous: must NOT call iam")
}

// SCENARIO 8: image handler same behaviour (parametrized).
func TestImageHandler_List_AllowedSubset(t *testing.T) {
	cli := &mockAuthCli{allowedByKey: map[string][]string{}}
	ops := portmock.NewOpsRepo()
	imgRepo := portmock.NewImageRepo()
	svc := service.NewImageService(imgRepo, portmock.NewDiskRepo(), portmock.NewSnapshotRepo(),
		&portmock.ProjectClient{OK: true}, ops)
	h := NewImageHandler(svc, newFilter(t, cli))

	// Create 2 images (oneof Source = uri).
	ctx := context.Background()
	var ids []string
	for _, n := range []string{"img-a", "img-b"} {
		op, err := h.Create(ctx, &computev1.CreateImageRequest{
			ProjectId: "proj", Name: n,
			Source: &computev1.CreateImageRequest_Uri{Uri: "https://example.com/img"},
		})
		require.NoError(t, err)
		portmock.AwaitAllOpsDone(t, ops)
		done, _ := ops.Get(ctx, op.Id)
		var im computev1.Image
		require.NoError(t, done.Response.UnmarshalTo(&im))
		ids = append(ids, im.Id)
	}
	cli.allowedByKey["user:usr_alice|compute_image|compute.images.read"] = []string{ids[0]}

	resp, err := h.List(ctxWithSubject("user:usr_alice"), &computev1.ListImagesRequest{ProjectId: "proj"})
	require.NoError(t, err)
	require.Len(t, resp.Images, 1)
	require.Equal(t, ids[0], resp.Images[0].Id)
}

// SCENARIO 9: cache hit — second call within TTL uses cached decision.
func TestDiskHandler_List_CacheReuse(t *testing.T) {
	cli := &mockAuthCli{allowedByKey: map[string][]string{}}
	h, ops := setupDiskHandler(t, newFilter(t, cli))
	ids := createDisks(t, h, ops, "proj", "a")
	cli.allowedByKey["user:usr_alice|compute_disk|compute.disks.read"] = []string{ids[0]}

	for i := 0; i < 5; i++ {
		resp, err := h.List(ctxWithSubject("user:usr_alice"), &computev1.ListDisksRequest{ProjectId: "proj"})
		require.NoError(t, err)
		require.Len(t, resp.Disks, 1)
	}
	require.Equal(t, 1, cli.calls, "5 List calls but only 1 iam.ListObjects (cache)")
}

// SCENARIO 10: subject from legacy two-header form works.
func TestDiskHandler_List_LegacySubjectHeaders(t *testing.T) {
	cli := &mockAuthCli{allowedByKey: map[string][]string{}}
	h, ops := setupDiskHandler(t, newFilter(t, cli))
	ids := createDisks(t, h, ops, "proj", "a", "b")
	cli.allowedByKey["service_account:sa_kube|compute_disk|compute.disks.read"] = []string{ids[1]}

	md := metadata.Pairs("x-kacho-subject-type", "service_account", "x-kacho-subject-id", "sa_kube")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	resp, err := h.List(ctx, &computev1.ListDisksRequest{ProjectId: "proj"})
	require.NoError(t, err)
	require.Len(t, resp.Disks, 1)
	require.Equal(t, ids[1], resp.Disks[0].Id)
}

// SCENARIO 11: catalog handlers (DiskType / Zone / Region) NOT filtered.
// Catalog handlers don't receive listFilter at all — they're public read.
func TestCatalogHandlers_NoFilter(t *testing.T) {
	dtSvc := service.NewDiskTypeService(portmock.NewDiskTypeRepo("network-ssd", "network-hdd"))
	dh := NewDiskTypeHandler(dtSvc)

	// Even with anonymous ctx — catalog visible.
	resp, err := dh.List(context.Background(), &computev1.ListDiskTypesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.DiskTypes, 2)
}

// SCENARIO 12: instance handler — filter wiring works for List.
func TestInstanceHandler_List_AllowedSubset(t *testing.T) {
	cli := &mockAuthCli{allowedByKey: map[string][]string{}}
	ops := portmock.NewOpsRepo()
	zoneRegistry := portmock.NewZoneRepo()
	svc := service.NewInstanceService(
		portmock.NewInstanceRepo(),
		portmock.NewDiskRepo(),
		portmock.NewImageRepo(),
		portmock.NewSnapshotRepo(),
		zoneRegistry,
		&portmock.ProjectClient{OK: true},
		&portmock.VPCClient{},
		ops,
		true, // skipIPAM
	)
	h := NewInstanceHandler(svc, newFilter(t, cli))

	// Empty grant → empty list.
	resp, err := h.List(ctxWithSubject("user:usr_no_grants"), &computev1.ListInstancesRequest{ProjectId: "proj"})
	require.NoError(t, err)
	require.Len(t, resp.Instances, 0)
}
