// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package repo_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/ids"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/repo"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// seedInstanceWithBoot вставляет ВМ с одним attached boot-диском (auto_delete по
// флагу) и возвращает (instanceID, bootDiskID).
func seedInstanceWithBoot(t *testing.T, ctx context.Context, instRepo *repo.InstanceRepo, folder string, bootAutoDelete bool) (string, string) {
	t.Helper()
	inID := ids.NewID(ids.PrefixInstance)
	bootID := ids.NewID(ids.PrefixDisk)
	in := &domain.Instance{
		ID: inID, ProjectID: folder, CreatedAt: time.Now().UTC().Truncate(time.Microsecond), Name: "",
		ZoneID: "ru-central1-a", PlatformID: "standard-v3", Cores: 2, Memory: 2 << 30, CoreFraction: 100,
		Status: domain.InstanceStatusRunning, FQDN: inID + ".auto.internal", NetworkSettingsType: "STANDARD",
		AttachedDisks: []domain.AttachedDisk{{DiskID: bootID, IsBoot: true, AutoDelete: bootAutoDelete}},
	}
	inlineBoot := &domain.Disk{
		ID: bootID, ProjectID: folder, CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		ZoneID: "ru-central1-a", Size: 4194304, BlockSize: 4096, Status: domain.DiskStatusReady,
	}
	_, err := instRepo.Insert(ctx, in, []*domain.Disk{inlineBoot})
	require.NoError(t, err)
	return inID, bootID
}

// TestIntegration_InstanceDelete_AutoDeleteComputedInTx — verifies that
// InstanceRepo.Delete derives the auto-delete disk-set from the CURRENT
// attached_disks rows inside its own transaction, NOT from a caller-supplied
// (potentially stale) snapshot. Seeds an instance with an auto_delete boot disk
// plus an auto_delete data disk plus a non-auto-delete data disk; Delete must
// remove both auto_delete disks and leave the non-auto-delete one detached.
// Guards against the stale pre-tx auto-delete snapshot that leaked a disk.
func TestIntegration_InstanceDelete_AutoDeleteComputedInTx(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	instRepo := repo.NewInstanceRepo(pool)
	diskRepo := repo.NewDiskRepo(pool)

	inID, bootID := seedInstanceWithBoot(t, ctx, instRepo, "f-autodel", true)

	// data disk attached with auto_delete=true.
	autoID := seedDiskForAttach(t, ctx, diskRepo, "f-autodel")
	_, err = instRepo.AttachDisk(ctx, inID, domain.AttachedDisk{DiskID: autoID, DeviceName: "auto0", AutoDelete: true})
	require.NoError(t, err)

	// data disk attached with auto_delete=false → must survive detached.
	keepID := seedDiskForAttach(t, ctx, diskRepo, "f-autodel")
	_, err = instRepo.AttachDisk(ctx, inID, domain.AttachedDisk{DiskID: keepID, DeviceName: "keep0", AutoDelete: false})
	require.NoError(t, err)

	require.NoError(t, instRepo.Delete(ctx, inID))

	_, err = instRepo.Get(ctx, inID)
	require.ErrorIs(t, err, service.ErrNotFound)
	_, err = diskRepo.Get(ctx, bootID)
	require.ErrorIs(t, err, service.ErrNotFound, "auto_delete boot disk must be removed")
	_, err = diskRepo.Get(ctx, autoID)
	require.ErrorIs(t, err, service.ErrNotFound, "auto_delete data disk must be removed")
	_, err = diskRepo.Get(ctx, keepID)
	require.NoError(t, err, "non-auto-delete disk must survive detached")

	var attachRows int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM attached_disks WHERE instance_id=$1`, inID).Scan(&attachRows))
	assert.Equal(t, 0, attachRows, "all attached_disks rows removed on instance delete")
}

// TestIntegration_InstanceDelete_vs_AttachDisk_Race — contested path: one
// InstanceRepo.Delete races several InstanceRepo.AttachDisk of distinct
// auto_delete disks onto the SAME instance. The instance-row FOR UPDATE lock
// taken by Delete serializes against the FK KEY-SHARE lock the concurrent
// AttachDisk INSERT takes on instances(id). Exactly two consistent outcomes per
// racer, and the invariant NEVER violated: no auto_delete disk is left as an
// orphaned, detached resource after its attachment row is removed by the delete.
func TestIntegration_InstanceDelete_vs_AttachDisk_Race(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	instRepo := repo.NewInstanceRepo(pool)
	diskRepo := repo.NewDiskRepo(pool)

	const iterations = 12
	for it := 0; it < iterations; it++ {
		inID, bootID := seedInstanceWithBoot(t, ctx, instRepo, "f-del-race", true)

		const A = 4
		diskIDs := make([]string, A)
		for i := 0; i < A; i++ {
			diskIDs[i] = seedDiskForAttach(t, ctx, diskRepo, "f-del-race")
		}

		var (
			wg           sync.WaitGroup
			startBarrier = make(chan struct{})
			attachOK     = make([]bool, A)
			otherErrs    []error
			mu           sync.Mutex
		)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startBarrier
			if derr := instRepo.Delete(ctx, inID); derr != nil {
				mu.Lock()
				otherErrs = append(otherErrs, derr)
				mu.Unlock()
			}
		}()
		for i := 0; i < A; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-startBarrier
				_, aerr := instRepo.AttachDisk(ctx, inID, domain.AttachedDisk{
					DiskID: diskIDs[idx], DeviceName: "", AutoDelete: true,
					Mode: domain.AttachedDiskModeReadWrite,
				})
				switch {
				case aerr == nil:
					attachOK[idx] = true
				case errors.Is(aerr, service.ErrFailedPrecondition), errors.Is(aerr, service.ErrNotFound):
					// attach lost the race (instance already gone / FK) — acceptable.
				default:
					mu.Lock()
					otherErrs = append(otherErrs, aerr)
					mu.Unlock()
				}
			}(i)
		}
		close(startBarrier)
		wg.Wait()

		require.Empty(t, otherErrs, "iter %d: only nil / FailedPrecondition / NotFound expected, got: %v", it, otherErrs)

		// Instance is gone (Delete always removes an existing instance).
		_, err = instRepo.Get(ctx, inID)
		require.ErrorIs(t, err, service.ErrNotFound, "iter %d: instance must be deleted", it)

		// Boot disk (auto_delete) always removed.
		_, err = diskRepo.Get(ctx, bootID)
		require.ErrorIs(t, err, service.ErrNotFound, "iter %d: auto_delete boot disk must be removed", it)

		// Invariant: for each racer disk, it either (a) was never successfully
		// attached and survives standalone, or (b) was attached-then-swept with the
		// instance and is gone. It must NEVER be left orphaned (survives) while its
		// attachment row was removed. Since no attached_disks rows remain (instance
		// gone), a surviving disk must be one whose attach LOST → attachOK[idx]=false.
		var danglingRows int
		require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM attached_disks WHERE instance_id=$1`, inID).Scan(&danglingRows))
		require.Equal(t, 0, danglingRows, "iter %d: no attached_disks rows may reference the deleted instance", it)

		for i := 0; i < A; i++ {
			_, gerr := diskRepo.Get(ctx, diskIDs[i])
			exists := gerr == nil
			if exists {
				assert.False(t, attachOK[i],
					"iter %d disk %d: an auto_delete disk that was successfully attached must not survive as an orphan", it, i)
				// cleanup surviving standalone disk.
				_ = diskRepo.Delete(ctx, diskIDs[i])
			} else {
				require.ErrorIs(t, gerr, service.ErrNotFound, "iter %d disk %d: expected NotFound for swept disk", it, i)
			}
		}
	}
}
