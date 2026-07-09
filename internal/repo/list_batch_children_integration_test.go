// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package repo_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/ids"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/repo"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// attachedDisksQueryCounter is a pgx.QueryTracer that counts read queries against
// the attached_disks table (`... FROM attached_disks ...`). It is used to lock the
// N+1 fix behaviourally: a ListInstances / ListDisks over a page of N rows must
// fetch children in a SINGLE batched round-trip (WHERE ... = ANY(ids)), not one
// SELECT per row.
type attachedDisksQueryCounter struct {
	mu    sync.Mutex
	count int
}

func (c *attachedDisksQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "FROM attached_disks") {
		c.mu.Lock()
		c.count++
		c.mu.Unlock()
	}
	return ctx
}

func (c *attachedDisksQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {
}

func (c *attachedDisksQueryCounter) reset() {
	c.mu.Lock()
	c.count = 0
	c.mu.Unlock()
}

func (c *attachedDisksQueryCounter) get() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

// tracedPool builds a pgxpool whose connections trace every query through counter.
func tracedPool(t *testing.T, ctx context.Context, dsn string, counter *attachedDisksQueryCounter) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	cfg.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	return pool
}

// TestIntegration_InstanceRepo_ListBatchesChildren locks the N+1 fix for
// InstanceRepo.List: fetching attached_disks for a page of N instances must issue
// exactly ONE attached_disks query (batched WHERE instance_id = ANY(...)), not N.
// RED before the fix — the per-row fillChildren loop fires one query per instance
// (3 for a 3-instance page).
func TestIntegration_InstanceRepo_ListBatchesChildren(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	counter := &attachedDisksQueryCounter{}
	pool := tracedPool(t, ctx, dsn, counter)
	defer pool.Close()

	instRepo := repo.NewInstanceRepo(pool)

	const projectID = "proj-list-batch-inst"
	const n = 3
	base := time.Now().UTC().Truncate(time.Microsecond)
	bootByInstance := make(map[string]string, n)
	for i := 0; i < n; i++ {
		bootDiskID := ids.NewID(ids.PrefixDisk)
		inID := ids.NewID(ids.PrefixInstance)
		bootByInstance[inID] = bootDiskID
		in := &domain.Instance{
			ID: inID, ProjectID: projectID, CreatedAt: base.Add(time.Duration(i) * time.Second),
			Name: "vm-batch-" + inID[len(inID)-4:], ZoneID: "ru-central1-a", PlatformID: "standard-v3",
			Cores: 2, Memory: 2 << 30, CoreFraction: 100, Status: domain.InstanceStatusRunning,
			FQDN: inID + ".auto.internal", NetworkSettingsType: "STANDARD",
			AttachedDisks: []domain.AttachedDisk{{DiskID: bootDiskID, IsBoot: true, AutoDelete: true}},
		}
		inlineBoot := &domain.Disk{ID: bootDiskID, ProjectID: projectID, CreatedAt: base, ZoneID: "ru-central1-a", Size: 4194304, BlockSize: 4096, Status: domain.DiskStatusReady}
		_, err := instRepo.Insert(ctx, in, []*domain.Disk{inlineBoot})
		require.NoError(t, err)
	}

	counter.reset()
	list, _, err := instRepo.List(ctx, service.InstanceFilter{ProjectID: projectID}, service.Pagination{})
	require.NoError(t, err)
	require.Len(t, list, n)

	// Correctness: every instance carries exactly its own boot disk.
	for _, in := range list {
		require.Len(t, in.AttachedDisks, 1, "instance %s children", in.ID)
		assert.Equal(t, bootByInstance[in.ID], in.AttachedDisks[0].DiskID)
		assert.True(t, in.AttachedDisks[0].IsBoot)
	}

	// Behavioural lock: children fetched in ONE batched round-trip, not N.
	assert.Equal(t, 1, counter.get(), "ListInstances must batch attached_disks fetch into a single query, got N+1")
}

// TestIntegration_DiskRepo_ListBatchesInstanceIDs locks the N+1 fix for
// DiskRepo.List: resolving instance_ids for a page of N disks must issue exactly
// ONE attached_disks query (batched WHERE disk_id = ANY(...)), not N. RED before
// the fix — the per-row fillInstanceIDs loop fires one query per disk.
func TestIntegration_DiskRepo_ListBatchesInstanceIDs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	counter := &attachedDisksQueryCounter{}
	pool := tracedPool(t, ctx, dsn, counter)
	defer pool.Close()

	diskRepo := repo.NewDiskRepo(pool)
	instRepo := repo.NewInstanceRepo(pool)

	const projectID = "proj-list-batch-disk"
	const n = 3
	base := time.Now().UTC().Truncate(time.Microsecond)
	// One instance owns all data disks (each attached → one attached_disks row per disk).
	inID := ids.NewID(ids.PrefixInstance)
	bootDiskID := ids.NewID(ids.PrefixDisk)
	in := &domain.Instance{
		ID: inID, ProjectID: projectID, CreatedAt: base, Name: "vm-disk-batch",
		ZoneID: "ru-central1-a", PlatformID: "standard-v3", Cores: 2, Memory: 2 << 30, CoreFraction: 100,
		Status: domain.InstanceStatusRunning, FQDN: inID + ".auto.internal", NetworkSettingsType: "STANDARD",
		AttachedDisks: []domain.AttachedDisk{{DiskID: bootDiskID, IsBoot: true, AutoDelete: true}},
	}
	inlineBoot := &domain.Disk{ID: bootDiskID, ProjectID: projectID, CreatedAt: base, ZoneID: "ru-central1-a", Size: 4194304, BlockSize: 4096, Status: domain.DiskStatusReady}
	_, err := instRepo.Insert(ctx, in, []*domain.Disk{inlineBoot})
	require.NoError(t, err)

	dataDiskIDs := make([]string, 0, n)
	for i := 0; i < n; i++ {
		dID := ids.NewID(ids.PrefixDisk)
		dataDiskIDs = append(dataDiskIDs, dID)
		_, err := diskRepo.Insert(ctx, &domain.Disk{
			ID: dID, ProjectID: projectID, CreatedAt: base.Add(time.Duration(i+1) * time.Second),
			Name: "data-" + dID[len(dID)-4:], ZoneID: "ru-central1-a", Size: 4194304, BlockSize: 4096,
			Status: domain.DiskStatusReady,
		})
		require.NoError(t, err)
		_, err = instRepo.AttachDisk(ctx, inID, domain.AttachedDisk{DiskID: dID, DeviceName: "data" + dID[len(dID)-2:]})
		require.NoError(t, err)
	}

	counter.reset()
	list, _, err := diskRepo.List(ctx, service.DiskFilter{ProjectID: projectID}, service.Pagination{})
	require.NoError(t, err)
	// boot disk + n data disks.
	require.Len(t, list, n+1)

	// Correctness: each data disk resolves back to the owning instance.
	attachedSeen := 0
	for _, d := range list {
		if d.ID == bootDiskID {
			require.Len(t, d.InstanceIDs, 1)
			assert.Equal(t, inID, d.InstanceIDs[0])
			continue
		}
		require.Len(t, d.InstanceIDs, 1, "disk %s instance_ids", d.ID)
		assert.Equal(t, inID, d.InstanceIDs[0])
		attachedSeen++
	}
	assert.Equal(t, n, attachedSeen)

	// Behavioural lock: instance_ids fetched in ONE batched round-trip, not N.
	assert.Equal(t, 1, counter.get(), "ListDisks must batch attached_disks fetch into a single query, got N+1")
}
