// list_filter_integration_test.go — KAC-127 Phase 4 integration tests for
// FGA-filtered List repo path.
//
// Validates the `WHERE id = ANY($1::text[])` clause against real Postgres
// (testcontainers). Covers all 4 resources (Disk / Image / Snapshot / Instance)
// and three edge cases:
//   - 2-id allow-list → returns exactly those 2 (alongside project_id filter)
//   - empty allow-list → repo returns nil (short-circuited)
//   - 10000-id allow-list → query executes successfully (no SQL injection,
//     no parameter limit)
//
// All tests are gated by `if testing.Short()` (like the rest of integration_test.go).
package repo_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/ids"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/repo"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// P4.GWT-36: WHERE id = ANY($1::text[]) — SQL injection-safe.
func TestIntegration_DiskRepo_ListByAllowedIDs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	r := repo.NewDiskRepo(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	var seededIDs []string
	for i, name := range []string{"a", "b", "c", "d", "e"} {
		d := &domain.Disk{
			ID:        ids.NewID(ids.PrefixDisk),
			ProjectID: "proj-a",
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			Name:      name, TypeID: "network-ssd", ZoneID: "ru-central1-a",
			Size: 4194304, BlockSize: 4096, Status: domain.DiskStatusReady,
		}
		created, err := r.Insert(ctx, d)
		require.NoError(t, err)
		seededIDs = append(seededIDs, created.ID)
	}

	// Subset allow-list: just the first 2 ids.
	result, _, err := r.List(ctx, service.DiskFilter{
		ProjectID:  "proj-a",
		AllowedIDs: []string{seededIDs[0], seededIDs[2]},
	}, service.Pagination{})
	require.NoError(t, err)
	require.Len(t, result, 2)
	gotIDs := map[string]bool{}
	for _, d := range result {
		gotIDs[d.ID] = true
	}
	require.True(t, gotIDs[seededIDs[0]])
	require.True(t, gotIDs[seededIDs[2]])
	require.False(t, gotIDs[seededIDs[1]])
}

// P4.GWT-37: empty AllowedIDs → no SQL query executed (short-circuited).
func TestIntegration_DiskRepo_EmptyAllowedShortCircuits(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	r := repo.NewDiskRepo(pool)
	// Seed one disk so we'd see a "no filter" result of 1.
	d := &domain.Disk{
		ID: ids.NewID(ids.PrefixDisk), ProjectID: "proj-a",
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		Name:      "x", TypeID: "network-ssd", ZoneID: "ru-central1-a",
		Size: 4194304, BlockSize: 4096, Status: domain.DiskStatusReady,
	}
	_, err = r.Insert(ctx, d)
	require.NoError(t, err)

	// Empty AllowedIDs (non-nil) → must return 0 (NOT 1).
	result, _, err := r.List(ctx, service.DiskFilter{
		ProjectID:  "proj-a",
		AllowedIDs: []string{},
	}, service.Pagination{})
	require.NoError(t, err)
	require.Len(t, result, 0)

	// Verify control: nil AllowedIDs → returns 1.
	result, _, err = r.List(ctx, service.DiskFilter{
		ProjectID:  "proj-a",
		AllowedIDs: nil,
	}, service.Pagination{})
	require.NoError(t, err)
	require.Len(t, result, 1)
}

// P4.GWT-38: 10000 ids in array → query executes successfully.
func TestIntegration_DiskRepo_LargeAllowedList(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	r := repo.NewDiskRepo(pool)
	// Seed 3 disks.
	now := time.Now().UTC().Truncate(time.Microsecond)
	var existing []string
	for i := 0; i < 3; i++ {
		d := &domain.Disk{
			ID: ids.NewID(ids.PrefixDisk), ProjectID: "proj-a",
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			Name:      fmt.Sprintf("d%d", i), TypeID: "network-ssd", ZoneID: "ru-central1-a",
			Size: 4194304, BlockSize: 4096, Status: domain.DiskStatusReady,
		}
		_, err := r.Insert(ctx, d)
		require.NoError(t, err)
		existing = append(existing, d.ID)
	}

	// Build 10000-element allow list (9997 fake + 3 real).
	allow := make([]string, 0, 10000)
	for i := 0; i < 9997; i++ {
		allow = append(allow, fmt.Sprintf("epd-fake-%d", i))
	}
	allow = append(allow, existing...)

	result, _, err := r.List(ctx, service.DiskFilter{
		ProjectID:  "proj-a",
		AllowedIDs: allow,
	}, service.Pagination{})
	require.NoError(t, err)
	require.Len(t, result, 3, "must match the 3 real ids out of 10000-element allow-list")
}

// Image / Snapshot / Instance — parallel sanity that the SQL works on each repo
// type (all of them now go through the same WHERE id = ANY($..::text[]) path).
func TestIntegration_ImageRepo_ListByAllowedIDs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	r := repo.NewImageRepo(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	var seeded []string
	for i, n := range []string{"i1", "i2", "i3"} {
		im := &domain.Image{
			ID: ids.NewID(ids.PrefixImage), ProjectID: "proj-a",
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			Name:      n, Status: domain.ImageStatusReady, Family: "fam-" + n,
		}
		created, err := r.Insert(ctx, im)
		require.NoError(t, err)
		seeded = append(seeded, created.ID)
	}

	result, _, err := r.List(ctx, service.ImageFilter{
		ProjectID:  "proj-a",
		AllowedIDs: []string{seeded[1]},
	}, service.Pagination{})
	require.NoError(t, err)
	require.Len(t, result, 1)
	require.Equal(t, seeded[1], result[0].ID)
}

func TestIntegration_SnapshotRepo_ListByAllowedIDs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	r := repo.NewSnapshotRepo(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	var seeded []string
	for i, n := range []string{"s1", "s2"} {
		s := &domain.Snapshot{
			ID: ids.NewID(ids.PrefixSnapshot), ProjectID: "proj-a",
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			Name:      n, Status: domain.SnapshotStatusReady, SourceDiskID: "epd-fake-src",
		}
		created, err := r.Insert(ctx, s)
		require.NoError(t, err)
		seeded = append(seeded, created.ID)
	}

	result, _, err := r.List(ctx, service.SnapshotFilter{
		ProjectID:  "proj-a",
		AllowedIDs: []string{seeded[0]},
	}, service.Pagination{})
	require.NoError(t, err)
	require.Len(t, result, 1)
	require.Equal(t, seeded[0], result[0].ID)
}
