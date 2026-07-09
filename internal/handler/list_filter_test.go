// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// list_filter_test.go — handler-level tests for FGA-filtered List handlers
// (Disk / Image / Snapshot / Instance).
//
// Uses portmock repos + in-memory authzfilter.Filter (no real iam needed).
// Identity source is the request Principal (operations.WithPrincipal), the SAME
// source per-RPC Check uses — NOT the dead x-kacho-subject* headers. Covers the
// label-scope over-show leak fix:
//   - CLL-01 label-scoped subject → EXACTLY the allowed subset (not all)
//   - CLL-02 subject=="" (system / no principal) → fail-closed empty (NOT bypass)
//   - CLL-03 cluster-admin / owner → all (iam ListObjects returns all ids)
//   - CLL-04 adversarial not-granted subject → empty (no existence leak)
//   - CLL-05 same semantics across Disk / Image / Snapshot / Instance
//   - CLL-06 catalog (DiskType) NOT filtered
//   - CLL-07 iam-down + fail-closed → Unavailable
package handler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	iamv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/iam/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/authzfilter"
	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/ports/portmock"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// newImageHandlerWithFilter — Image handler over portmock repos + given filter.
func newImageHandlerWithFilter(t *testing.T, filter authzfilter.Filter) (*ImageHandler, *portmock.ImageRepo) {
	t.Helper()
	imgRepo := portmock.NewImageRepo()
	svc := service.NewImageService(imgRepo, portmock.NewDiskRepo(), portmock.NewSnapshotRepo(),
		&portmock.ProjectClient{OK: true}, portmock.NewOpsRepo())
	return NewImageHandler(svc, filter), imgRepo
}

// GLBF-01 (leak fix): a caller holding only project-tier `viewer` but WITHOUT the
// per-object `v_get` grant on the newest image in a family must NOT read that
// image via GetLatestByFamily. Interceptor gates this RPC only on project-tier
// viewer (the image id is unknown until the family is resolved), so — like public
// List — the handler must narrow to the per-object allow-list on the RESOLVED
// image. Without it a project viewer over-shows the image contents whenever it is
// newest in its family (BOLA-lite / over-show, CWE-863). The denial must hide
// existence (NotFound "Image <family> not found") — indistinguishable from a
// genuinely empty family — not over-show and not a 403 existence-oracle.
func TestImageHandler_GetLatestByFamily_GLBF01_Denied_NoLeak(t *testing.T) {
	cli := &mockAuthCli{allowedByKey: map[string][]string{}} // empty grant
	h, imgRepo := newImageHandlerWithFilter(t, newFilter(t, cli))
	imgRepo.Seed(&domain.Image{ID: "epdimgx", ProjectID: "proj", Name: "i", Family: "ubuntu", Status: domain.ImageStatusReady})

	resp, err := h.GetLatestByFamily(ctxWithSubject("user:usr_nobody"),
		&computev1.GetImageLatestByFamilyRequest{ProjectId: "proj", Family: "ubuntu"})
	require.Nil(t, resp, "LEAK: unauthorized caller must NOT receive the resolved image")
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err),
		"unauthorized resolved image must hide existence (NotFound), not over-show")
	require.Equal(t, "Image ubuntu not found", status.Convert(err).Message(),
		"denial must be indistinguishable from a genuinely empty family (no existence oracle)")
}

// GLBF-02: a caller granted `v_get` on the resolved image (id in the allow-list)
// receives it.
func TestImageHandler_GetLatestByFamily_GLBF02_Granted(t *testing.T) {
	cli := &mockAuthCli{allowedByKey: map[string][]string{}}
	h, imgRepo := newImageHandlerWithFilter(t, newFilter(t, cli))
	imgRepo.Seed(&domain.Image{ID: "epdimgy", ProjectID: "proj", Name: "i", Family: "ubuntu", Status: domain.ImageStatusReady})
	cli.allowedByKey["user:usr_alice|compute_image|compute.images.list"] = []string{"epdimgy"}

	resp, err := h.GetLatestByFamily(ctxWithSubject("user:usr_alice"),
		&computev1.GetImageLatestByFamilyRequest{ProjectId: "proj", Family: "ubuntu"})
	require.NoError(t, err)
	require.Equal(t, "epdimgy", resp.Id)
}

// GLBF-03: filter == nil (FGA disabled config-gate / dev) → bypass, back-compat.
func TestImageHandler_GetLatestByFamily_GLBF03_FilterNil_Bypass(t *testing.T) {
	h, imgRepo := newImageHandlerWithFilter(t, nil)
	imgRepo.Seed(&domain.Image{ID: "epdimgz", ProjectID: "proj", Name: "i", Family: "ubuntu", Status: domain.ImageStatusReady})

	resp, err := h.GetLatestByFamily(ctxWithSubject("user:usr_nobody"),
		&computev1.GetImageLatestByFamilyRequest{ProjectId: "proj", Family: "ubuntu"})
	require.NoError(t, err)
	require.Equal(t, "epdimgz", resp.Id)
}

// mockAuthCli — handler-test mirror of authzfilter.mockAuthClient (private there).
type mockAuthCli struct {
	allowedByKey map[string][]string
	err          error
	calls        int
	lastAction   string // captured so read==enforce tests can assert the verb
	lastResType  string
}

func (m *mockAuthCli) ListObjects(_ context.Context, in *iamv1.ListObjectsRequest, _ ...grpc.CallOption) (*iamv1.ListObjectsResponse, error) {
	m.calls++
	m.lastAction = in.Action
	m.lastResType = in.ResourceType
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

// ctxWithSubject — кладёт в ctx Principal, эквивалентный FGA-subject "type:id".
// Это ЕДИНЫЙ источник identity (как api-gateway principal-extract); прежний
// x-kacho-subject header больше не источник. subject вида "user:usr_alice".
func ctxWithSubject(subject string) context.Context {
	t, id, ok := strings.Cut(subject, ":")
	if !ok {
		return context.Background()
	}
	return operations.WithPrincipal(context.Background(), operations.Principal{Type: t, ID: id})
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
		portmock.NewZoneRegistry(),
		&portmock.ProjectClient{OK: true},
		ops,
	)
	return NewDiskHandler(svc, filter), ops
}

// SCENARIO 1: filter == nil → handler bypasses (FGA disabled config-gate / dev).
// This is the deliberate config-off bypass, NOT the missing-identity bypass.
func TestDiskHandler_List_FilterNil_Bypass(t *testing.T) {
	h, ops := setupDiskHandler(t, nil)
	createDisks(t, h, ops, "proj", "d1", "d2", "d3")

	resp, err := h.List(ctxWithSubject("user:usr_alice"), &computev1.ListDisksRequest{ProjectId: "proj"})
	require.NoError(t, err)
	require.Len(t, resp.Disks, 3, "filter=nil must return all disks")
}

// CLL-03: cluster-admin / owner → all. The IAM ListObjects returns ALL ids for
// an owner/cluster-admin subject (owner→viewer FGA derivation), so the handler
// passes through the full set. No compute-side header-bypass exists anymore.
func TestDiskHandler_List_CLL03_OwnerSeesAll(t *testing.T) {
	cli := &mockAuthCli{allowedByKey: map[string][]string{}}
	h, ops := setupDiskHandler(t, newFilter(t, cli))
	ids := createDisks(t, h, ops, "proj", "a", "b", "c")
	// owner/cluster-admin: iam returns every id.
	cli.allowedByKey["user:usr_owner|compute_disk|compute.disks.list"] = ids

	resp, err := h.List(ctxWithSubject("user:usr_owner"), &computev1.ListDisksRequest{ProjectId: "proj"})
	require.NoError(t, err)
	require.Len(t, resp.Disks, 3, "owner/cluster-admin must see all")
}

// CLL-01: label-scoped subject → EXACTLY the allowed subset (the over-show leak
// fix anchor). FGA returns 2 of 3 ids; the response MUST NOT include the third.
func TestDiskHandler_List_CLL01_AllowedSubset(t *testing.T) {
	cli := &mockAuthCli{allowedByKey: map[string][]string{}}
	h, ops := setupDiskHandler(t, newFilter(t, cli))
	ids := createDisks(t, h, ops, "proj", "a", "b", "c")
	cli.allowedByKey["user:usr_alice|compute_disk|compute.disks.list"] = []string{ids[0], ids[2]}

	resp, err := h.List(ctxWithSubject("user:usr_alice"), &computev1.ListDisksRequest{ProjectId: "proj"})
	require.NoError(t, err)
	require.Len(t, resp.Disks, 2)
	gotIDs := map[string]bool{}
	for _, d := range resp.Disks {
		gotIDs[d.Id] = true
	}
	require.True(t, gotIDs[ids[0]] && gotIDs[ids[2]])
	require.False(t, gotIDs[ids[1]], "leak: non-granted disk must NOT appear")
}

// CLL-04: empty grant (not-granted subject) → empty response (NOT 403, NOT all).
// Adversarial: the existence of other-tenant disks must not be revealed.
func TestDiskHandler_List_CLL04_EmptyGrant(t *testing.T) {
	cli := &mockAuthCli{} // no entries → returns empty []
	h, ops := setupDiskHandler(t, newFilter(t, cli))
	createDisks(t, h, ops, "proj", "a", "b")

	resp, err := h.List(ctxWithSubject("user:usr_nobody"), &computev1.ListDisksRequest{ProjectId: "proj"})
	require.NoError(t, err, "empty grant must not error")
	require.Len(t, resp.Disks, 0, "leak: not-granted subject must see nothing")
}

// CLL-07: iam-down + fail-closed → Unavailable (non-regression).
func TestDiskHandler_List_CLL07_IAMDown_FailClosed(t *testing.T) {
	cli := &mockAuthCli{err: status.Error(codes.Unavailable, "down")}
	h, ops := setupDiskHandler(t, newFilter(t, cli))
	createDisks(t, h, ops, "proj", "a")

	_, err := h.List(ctxWithSubject("user:usr_alice"), &computev1.ListDisksRequest{ProjectId: "proj"})
	require.Error(t, err)
	require.Equal(t, codes.Unavailable, status.Code(err))
}

// iam-down + fail-open → all results (degraded-mode bypass, opt-in config).
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

// CLL-02 (the leak root): subject=="" (no principal / system) → fail-closed.
// Previously this short-circuited to bypass-all and leaked every disk. The fix
// must return an EMPTY list (existence of disks must stay unknowable) and must
// NOT short-circuit to bypass. The filter is consulted with subject="" which is
// fail-closed at the FGA layer, OR the handler returns empty directly — either
// way the response is empty.
func TestDiskHandler_List_CLL02_NoPrincipal_FailClosed(t *testing.T) {
	cli := &mockAuthCli{}
	h, ops := setupDiskHandler(t, newFilter(t, cli))
	createDisks(t, h, ops, "proj", "a", "b", "c")

	// No principal in ctx at all → SystemPrincipal → subject="".
	resp, err := h.List(context.Background(), &computev1.ListDisksRequest{ProjectId: "proj"})
	require.NoError(t, err, "no-principal must not be a 5xx; it is fail-closed empty")
	require.Len(t, resp.Disks, 0, "LEAK: no-principal must NOT bypass to all disks")
}

// CLL-02 variant: explicit SystemPrincipal → fail-closed empty (not bypass-all).
func TestDiskHandler_List_CLL02_SystemPrincipal_FailClosed(t *testing.T) {
	cli := &mockAuthCli{}
	h, ops := setupDiskHandler(t, newFilter(t, cli))
	createDisks(t, h, ops, "proj", "a", "b")

	ctx := operations.WithPrincipal(context.Background(), operations.SystemPrincipal())
	resp, err := h.List(ctx, &computev1.ListDisksRequest{ProjectId: "proj"})
	require.NoError(t, err)
	require.Len(t, resp.Disks, 0, "LEAK: system principal must NOT bypass to all disks")
}

// CLL-05 (image): label-scoped subject → exactly the allowed subset.
func TestImageHandler_List_CLL05_AllowedSubset(t *testing.T) {
	cli := &mockAuthCli{allowedByKey: map[string][]string{}}
	ops := portmock.NewOpsRepo()
	imgRepo := portmock.NewImageRepo()
	svc := service.NewImageService(imgRepo, portmock.NewDiskRepo(), portmock.NewSnapshotRepo(),
		&portmock.ProjectClient{OK: true}, ops)
	h := NewImageHandler(svc, newFilter(t, cli))

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
	cli.allowedByKey["user:usr_alice|compute_image|compute.images.list"] = []string{ids[0]}

	resp, err := h.List(ctxWithSubject("user:usr_alice"), &computev1.ListImagesRequest{ProjectId: "proj"})
	require.NoError(t, err)
	require.Len(t, resp.Images, 1)
	require.Equal(t, ids[0], resp.Images[0].Id)
}

// CLL-05 (image): no-principal → fail-closed empty (leak guard).
func TestImageHandler_List_CLL02_NoPrincipal_FailClosed(t *testing.T) {
	cli := &mockAuthCli{}
	ops := portmock.NewOpsRepo()
	svc := service.NewImageService(portmock.NewImageRepo(), portmock.NewDiskRepo(), portmock.NewSnapshotRepo(),
		&portmock.ProjectClient{OK: true}, ops)
	h := NewImageHandler(svc, newFilter(t, cli))

	ctx := context.Background()
	for _, n := range []string{"img-a", "img-b"} {
		op, err := h.Create(ctx, &computev1.CreateImageRequest{
			ProjectId: "proj", Name: n,
			Source: &computev1.CreateImageRequest_Uri{Uri: "https://example.com/img"},
		})
		require.NoError(t, err)
		portmock.AwaitAllOpsDone(t, ops)
		_ = op
	}
	resp, err := h.List(context.Background(), &computev1.ListImagesRequest{ProjectId: "proj"})
	require.NoError(t, err)
	require.Len(t, resp.Images, 0, "LEAK: no-principal must NOT bypass to all images")
}

// SCENARIO: cache hit — second call within TTL uses cached decision.
func TestDiskHandler_List_CacheReuse(t *testing.T) {
	cli := &mockAuthCli{allowedByKey: map[string][]string{}}
	h, ops := setupDiskHandler(t, newFilter(t, cli))
	ids := createDisks(t, h, ops, "proj", "a")
	cli.allowedByKey["user:usr_alice|compute_disk|compute.disks.list"] = []string{ids[0]}

	for i := 0; i < 5; i++ {
		resp, err := h.List(ctxWithSubject("user:usr_alice"), &computev1.ListDisksRequest{ProjectId: "proj"})
		require.NoError(t, err)
		require.Len(t, resp.Disks, 1)
	}
	require.Equal(t, 1, cli.calls, "5 List calls but only 1 iam.ListObjects (cache)")
}

// CLL-05 (snapshot): label-scoped subject → exactly the allowed subset + no-principal fail-closed.
func TestSnapshotHandler_List_CLL05(t *testing.T) {
	cli := &mockAuthCli{allowedByKey: map[string][]string{}}
	ops := portmock.NewOpsRepo()
	diskRepo := portmock.NewDiskRepo()
	snapSvc := service.NewSnapshotService(portmock.NewSnapshotRepo(), diskRepo,
		&portmock.ProjectClient{OK: true}, ops)
	h := NewSnapshotHandler(snapSvc, newFilter(t, cli))

	// no-principal → empty (leak guard).
	resp, err := h.List(context.Background(), &computev1.ListSnapshotsRequest{ProjectId: "proj"})
	require.NoError(t, err)
	require.Len(t, resp.Snapshots, 0, "LEAK: no-principal must NOT bypass to all snapshots")

	// not-granted subject → empty.
	resp, err = h.List(ctxWithSubject("user:usr_nobody"), &computev1.ListSnapshotsRequest{ProjectId: "proj"})
	require.NoError(t, err)
	require.Len(t, resp.Snapshots, 0)
}

// CLL-06: catalog handler (DiskType) NOT filtered — public read, unaffected.
func TestCatalogHandlers_NoFilter(t *testing.T) {
	dtSvc := service.NewDiskTypeService(portmock.NewDiskTypeRepo("network-ssd", "network-hdd"))
	dh := NewDiskTypeHandler(dtSvc)

	// Even with no principal — catalog visible.
	resp, err := dh.List(context.Background(), &computev1.ListDiskTypesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.DiskTypes, 2)
}

// CLL-05 (instance): empty grant → empty; no-principal → empty (leak guard).
func TestInstanceHandler_List_CLL05(t *testing.T) {
	cli := &mockAuthCli{allowedByKey: map[string][]string{}}
	ops := portmock.NewOpsRepo()
	zoneRegistry := portmock.NewZoneRegistry()
	svc := service.NewInstanceService(
		portmock.NewInstanceRepo(),
		portmock.NewDiskRepo(),
		portmock.NewImageRepo(),
		portmock.NewSnapshotRepo(),
		portmock.NewDiskTypeRepo(),
		zoneRegistry,
		&portmock.ProjectClient{OK: true},
		ops,
	)
	h := NewInstanceHandler(svc, newFilter(t, cli))

	// Empty grant → empty list.
	resp, err := h.List(ctxWithSubject("user:usr_no_grants"), &computev1.ListInstancesRequest{ProjectId: "proj"})
	require.NoError(t, err)
	require.Len(t, resp.Instances, 0)

	// No principal → empty (leak guard, NOT bypass-all).
	resp, err = h.List(context.Background(), &computev1.ListInstancesRequest{ProjectId: "proj"})
	require.NoError(t, err)
	require.Len(t, resp.Instances, 0, "LEAK: no-principal must NOT bypass to all instances")
}

// verbOf returns the last dot-segment of a "<domain>.<resource>.<verb>" action.
func verbOf(action string) string {
	last := -1
	for i := 0; i < len(action); i++ {
		if action[i] == '.' {
			last = i
		}
	}
	if last < 0 {
		return action
	}
	return action[last+1:]
}

// read==enforce: the action each public List handler sends to iam ListObjects
// MUST carry the "list" verb (which the iam server resolves to the "viewer"
// relation — the SAME relation the per-RPC Check gate uses for Get).
func TestListHandlers_SendViewerResolvingAction(t *testing.T) {
	t.Run("disk", func(t *testing.T) {
		cli := &mockAuthCli{allowedByKey: map[string][]string{}}
		h, ops := setupDiskHandler(t, newFilter(t, cli))
		createDisks(t, h, ops, "proj", "a")
		_, err := h.List(ctxWithSubject("user:usr_alice"), &computev1.ListDisksRequest{ProjectId: "proj"})
		require.NoError(t, err)
		require.Equal(t, "compute_disk", cli.lastResType)
		require.Equal(t, "list", verbOf(cli.lastAction),
			"disk List must send a viewer-resolving verb (read==enforce); got action %q", cli.lastAction)
	})
	t.Run("instance", func(t *testing.T) {
		cli := &mockAuthCli{allowedByKey: map[string][]string{}}
		ops := portmock.NewOpsRepo()
		svc := service.NewInstanceService(
			portmock.NewInstanceRepo(), portmock.NewDiskRepo(), portmock.NewImageRepo(),
			portmock.NewSnapshotRepo(), portmock.NewDiskTypeRepo(), portmock.NewZoneRegistry(),
			&portmock.ProjectClient{OK: true}, ops,
		)
		h := NewInstanceHandler(svc, newFilter(t, cli))
		_, err := h.List(ctxWithSubject("user:usr_alice"), &computev1.ListInstancesRequest{ProjectId: "proj"})
		require.NoError(t, err)
		require.Equal(t, "compute_instance", cli.lastResType)
		require.Equal(t, "list", verbOf(cli.lastAction),
			"instance List must send a viewer-resolving verb (read==enforce); got action %q", cli.lastAction)
	})
}
