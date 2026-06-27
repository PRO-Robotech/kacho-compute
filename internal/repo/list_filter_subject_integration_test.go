// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// list_filter_subject_integration_test.go — end-to-end (testcontainers Postgres)
// guard for the public List label-scope over-show leak fix.
//
// Wires a REAL DiskRepo (real SQL) into DiskService → DiskHandler → real
// FGAFilter (mock AuthorizeClient) and drives handler.List through the request
// Principal path — the SAME identity source per-RPC Check uses. Proves the fix
// against real rows:
//   - CLL-02: no-principal / system principal → fail-closed (0 rows) DESPITE
//     seeded rows. Before the fix the handler short-circuited to bypass-all and
//     leaked every row.
//   - CLL-01: label-scoped principal → exactly the FGA-allowed subset hits the
//     `WHERE id = ANY(...)` SQL path, not the whole project.
package repo_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	computev1 "github.com/PRO-Robotech/kacho-compute/proto/gen/go/kacho/cloud/compute/v1"
	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	iamv1 "github.com/PRO-Robotech/kacho-iam/proto/gen/go/kacho/cloud/iam/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/authzfilter"
	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/handler"
	"github.com/PRO-Robotech/kacho-compute/internal/ports/portmock"
	"github.com/PRO-Robotech/kacho-compute/internal/repo"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// listObjectsStub — minimal authzfilter.AuthorizeClient returning a fixed
// allow-list keyed by subject (so subject-source mismatches are observable: a
// "" subject never reaches here, and an unexpected subject yields empty).
type listObjectsStub struct {
	allowBySubject map[string][]string
	calls          int
}

func (s *listObjectsStub) ListObjects(_ context.Context, in *iamv1.ListObjectsRequest, _ ...grpc.CallOption) (*iamv1.ListObjectsResponse, error) {
	s.calls++
	return &iamv1.ListObjectsResponse{ResourceIds: s.allowBySubject[in.Subject]}, nil
}

// newDiskHandlerOnRealRepo — DiskService over a real (testcontainers) repo +
// real FGAFilter (mock AuthorizeClient). Returns the handler, the repo (for
// deterministic seeding) and the pool (for cleanup).
func newDiskHandlerOnRealRepo(t *testing.T, cli authzfilter.AuthorizeClient) (*handler.DiskHandler, *repo.DiskRepo, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)

	diskRepo := repo.NewDiskRepo(pool)
	svc := service.NewDiskService(
		diskRepo,
		repo.NewImageRepo(pool),
		repo.NewSnapshotRepo(pool),
		repo.NewDiskTypeRepo(pool),
		portmock.NewZoneRegistry("ru-central1-a"),
		&portmock.ProjectClient{OK: true},
		portmock.NewOpsRepo(),
	)
	cfg := authzfilter.DefaultConfig()
	cfg.Timeout = 500 * time.Millisecond
	filter := authzfilter.NewFGAFilter(cli, cfg)
	return handler.NewDiskHandler(svc, filter), diskRepo, pool
}

// seedDisks — insert N disks directly via the real repo for deterministic ids.
func seedDisks(t *testing.T, r *repo.DiskRepo, projectID string, names ...string) []string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	var out []string
	for i, n := range names {
		d := &domain.Disk{
			ID:        ids.NewID(ids.PrefixDisk),
			ProjectID: projectID,
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			Name:      n, TypeID: "network-ssd", ZoneID: "ru-central1-a",
			Size: 4194304, BlockSize: 4096, Status: domain.DiskStatusReady,
		}
		created, err := r.Insert(ctx, d)
		require.NoError(t, err)
		out = append(out, created.ID)
	}
	return out
}

// CLL-02 (integration): no-principal / system principal → fail-closed 0 rows
// despite real seeded rows. This is the production leak reproduced end-to-end:
// a List with no caller-identity must NOT return the project's disks.
func TestIntegration_DiskHandler_NoPrincipal_FailClosed_NoLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	stub := &listObjectsStub{allowBySubject: map[string][]string{}}
	h, r, pool := newDiskHandlerOnRealRepo(t, stub)
	defer pool.Close()

	ids := seedDisks(t, r, "proj-a", "a", "b", "c")
	require.Len(t, ids, 3)

	// No principal in ctx → SystemPrincipal → subject="" → fail-closed.
	resp, err := h.List(context.Background(), &computev1.ListDisksRequest{ProjectId: "proj-a"})
	require.NoError(t, err, "no-principal List must be fail-closed empty, not 5xx")
	require.Len(t, resp.Disks, 0, "LEAK: no-principal List must NOT return any disk")
	require.Equal(t, 0, stub.calls, "fail-closed: filter must not be consulted for empty subject")

	// Explicit SystemPrincipal → same fail-closed behaviour.
	sysCtx := operations.WithPrincipal(context.Background(), operations.SystemPrincipal())
	resp, err = h.List(sysCtx, &computev1.ListDisksRequest{ProjectId: "proj-a"})
	require.NoError(t, err)
	require.Len(t, resp.Disks, 0, "LEAK: system principal List must NOT return any disk")
}

// CLL-01 (integration): label-scoped principal → exactly the FGA-allowed subset
// flows through the real `WHERE id = ANY(...)` SQL path; not-granted → empty.
func TestIntegration_DiskHandler_LabelScoped_SubsetOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	stub := &listObjectsStub{allowBySubject: map[string][]string{}}
	h, r, pool := newDiskHandlerOnRealRepo(t, stub)
	defer pool.Close()

	ids := seedDisks(t, r, "proj-a", "a", "b", "c")
	require.Len(t, ids, 3)

	// alice's label-grant covers only 2 of 3 disks.
	stub.allowBySubject["user:usr_alice"] = []string{ids[0], ids[2]}
	ctx := operations.WithPrincipal(context.Background(), operations.Principal{Type: "user", ID: "usr_alice"})

	resp, err := h.List(ctx, &computev1.ListDisksRequest{ProjectId: "proj-a"})
	require.NoError(t, err)
	require.Len(t, resp.Disks, 2, "label-scoped subject must see only the granted subset")
	got := map[string]bool{}
	for _, d := range resp.Disks {
		got[d.Id] = true
	}
	require.True(t, got[ids[0]] && got[ids[2]])
	require.False(t, got[ids[1]], "LEAK: non-granted disk must not appear")

	// not-granted subject → empty (no existence leak).
	other := operations.WithPrincipal(context.Background(), operations.Principal{Type: "user", ID: "usr_nobody"})
	resp, err = h.List(other, &computev1.ListDisksRequest{ProjectId: "proj-a"})
	require.NoError(t, err)
	require.Len(t, resp.Disks, 0, "LEAK: not-granted subject must see nothing")
}
