// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// list_filter_integration_test.go — integration tests for the FGA-filtered List
// repo path.
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

// TestIntegration_InstanceRepo_PaginationAfterFilter — D-46 (LST-6): pagination
// must apply to the FILTERED set, not the raw rows. Seed 150 instances in a
// project, grant access (allow-list) to 60 of them, then page with page_size=25.
// The 3 pages must cover EXACTLY the 60 accessible instances (25+25+10) with no
// holes and no inaccessible instance ever leaking onto a page. This is the
// compute.instance arm of the FGA-filtered paginated List contract.
//
// RED-safety: if pagination were applied BEFORE the id=ANY filter (raw LIMIT
// then filter), pages would be "holey" — a 25-row raw page could contain <25
// accessible ids — and the union across pages would miss accessible rows. This
// test fails in that mode and passes only when WHERE id = ANY precedes
// ORDER BY ... LIMIT (the current repo construction).
func TestIntegration_InstanceRepo_PaginationAfterFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	r := repo.NewInstanceRepo(pool)
	base := time.Now().UTC().Truncate(time.Microsecond)

	const total = 150
	const granted = 60
	allIDs := make([]string, 0, total)
	for i := 0; i < total; i++ {
		id := ids.NewID(ids.PrefixInstance)
		_, err := r.Insert(ctx, &domain.Instance{
			ID: id, ProjectID: "proj-a",
			CreatedAt: base.Add(time.Duration(i) * time.Millisecond),
			Name:      fmt.Sprintf("vm-%03d", i),
			ZoneID:    "ru-central1-a", PlatformID: "standard-v3",
			Cores: 2, Memory: 2 << 30, CoreFraction: 100,
			Status: domain.InstanceStatusRunning, FQDN: id + ".auto.internal",
			NetworkSettingsType: "STANDARD",
		}, nil)
		require.NoError(t, err)
		allIDs = append(allIDs, id)
	}

	// Grant access to a deterministic subset: every (total/granted)-th... simpler:
	// take the first `granted` ids by creation order but interleaved so the
	// accessible set is scattered across the full ordered range (exercises the
	// "filter then paginate" path, not "first N rows happen to be accessible").
	accessible := make([]string, 0, granted)
	accessibleSet := map[string]bool{}
	for i := 0; i < total && len(accessible) < granted; i += 2 { // every other → 75 candidates, take 60
		if len(accessible) < granted {
			accessible = append(accessible, allIDs[i])
			accessibleSet[allIDs[i]] = true
		}
	}
	require.Len(t, accessible, granted)

	// Page through the FILTERED set with page_size=25.
	const pageSize = 25
	seen := map[string]bool{}
	var pages int
	token := ""
	for {
		res, next, err := r.List(ctx, service.InstanceFilter{
			ProjectID:  "proj-a",
			AllowedIDs: accessible,
		}, service.Pagination{PageSize: pageSize, PageToken: token})
		require.NoError(t, err)
		pages++
		require.LessOrEqual(t, len(res), pageSize, "page must not exceed page_size")
		for _, in := range res {
			require.True(t, accessibleSet[in.ID], "inaccessible instance leaked onto a page: %s", in.ID)
			require.False(t, seen[in.ID], "duplicate across pages: %s", in.ID)
			seen[in.ID] = true
		}
		if next == "" {
			break
		}
		token = next
		require.LessOrEqual(t, pages, 10, "pagination did not terminate")
	}

	require.Equal(t, granted, len(seen), "pages must cover exactly the accessible set (no holes)")
	require.Equal(t, 3, pages, "60 accessible / page_size 25 → 25+25+10 = 3 pages")
}
