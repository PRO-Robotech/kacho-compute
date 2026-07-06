// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package repo_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/repo"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// TestIntegration_AttachDisk_CrossProject_RejectedAtDB — sec-hardening-r8 BOLA:
// even if the service-layer project guard were bypassed, the DB-level attach
// invariant (`insertAttachedDiskTx` CTE predicate `ld.project_id = i.project_id`)
// must refuse to attach a disk owned by ONE project to an instance owned by
// ANOTHER. Without the predicate the CTE would insert the row (disk resolves by
// primary key regardless of project) → cross-project disk takeover +, on the
// owning-instance Delete, auto_delete destruction of a victim's disk.
//
// RED before the predicate: the cross-project AttachDisk SUCCEEDS. GREEN after:
// 0 rows from the CTE → FailedPrecondition, and no attached_disks row is written.
func TestIntegration_AttachDisk_CrossProject_RejectedAtDB(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	diskRepo := repo.NewDiskRepo(pool)
	instRepo := repo.NewInstanceRepo(pool)

	// Attacker instance + attacker's own disk (same project) — control.
	attackerInst := seedInstanceForAttach(t, ctx, instRepo, "proj-attacker")
	ownDisk := seedDiskForAttach(t, ctx, diskRepo, "proj-attacker")
	// Victim's disk — same zone + READY so ONLY the project predicate can reject.
	victimDisk := seedDiskForAttach(t, ctx, diskRepo, "proj-victim")

	// Cross-project attach → rejected at DB level.
	_, err = instRepo.AttachDisk(ctx, attackerInst, domain.AttachedDisk{DiskID: victimDisk, DeviceName: "data0"})
	require.Error(t, err)
	assert.ErrorIs(t, err, service.ErrFailedPrecondition,
		"cross-project attach must be refused by the DB-level predicate, got: %v", err)

	// No row leaked for the victim disk.
	var rows int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM attached_disks WHERE disk_id = $1`, victimDisk).Scan(&rows))
	assert.Equal(t, 0, rows, "no attached_disks row must be written for a cross-project attach")

	// Positive control: same-project attach still succeeds.
	updated, err := instRepo.AttachDisk(ctx, attackerInst, domain.AttachedDisk{DiskID: ownDisk, DeviceName: "data1"})
	require.NoError(t, err, "same-project attach must still succeed")
	require.Len(t, updated.AttachedDisks, 1)

	attached, err := diskRepo.IsAttached(ctx, ownDisk)
	require.NoError(t, err)
	assert.True(t, attached)
	// Victim disk remains unattached.
	victimAttached, err := diskRepo.IsAttached(ctx, victimDisk)
	require.NoError(t, err)
	assert.False(t, victimAttached, "victim disk must remain unattached")
}

// TestIntegration_AttachDisk_CrossProject_ConcurrentRace — N attacker instances
// (project A) race to attach the SAME victim disk (project B) via the repo path.
// The DB-level predicate must hold under concurrency: ZERO attaches succeed,
// every attempt returns FailedPrecondition, and the disk stays unattached.
// Afterwards the legitimate owner (project B) can attach it, proving the predicate
// rejects only the cross-project attempts, not the resource itself.
func TestIntegration_AttachDisk_CrossProject_ConcurrentRace(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	diskRepo := repo.NewDiskRepo(pool)
	instRepo := repo.NewInstanceRepo(pool)

	victimDisk := seedDiskForAttach(t, ctx, diskRepo, "proj-victim")

	const N = 5
	attackerInsts := make([]string, N)
	for i := 0; i < N; i++ {
		attackerInsts[i] = seedInstanceForAttach(t, ctx, instRepo, "proj-attacker")
	}

	var (
		wg           sync.WaitGroup
		successCnt   atomic.Int32
		precondCnt   atomic.Int32
		otherErrs    []error
		otherErrsMu  sync.Mutex
		startBarrier = make(chan struct{})
	)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(instID string) {
			defer wg.Done()
			<-startBarrier
			_, aerr := instRepo.AttachDisk(ctx, instID, domain.AttachedDisk{DiskID: victimDisk, DeviceName: "data0"})
			switch {
			case aerr == nil:
				successCnt.Add(1)
			case errors.Is(aerr, service.ErrFailedPrecondition):
				precondCnt.Add(1)
			default:
				otherErrsMu.Lock()
				otherErrs = append(otherErrs, aerr)
				otherErrsMu.Unlock()
			}
		}(attackerInsts[i])
	}
	close(startBarrier)
	wg.Wait()

	assert.Equal(t, int32(0), successCnt.Load(), "no cross-project attach may succeed under concurrency")
	assert.Equal(t, int32(N), precondCnt.Load(), "every cross-project attempt must be FailedPrecondition")
	assert.Empty(t, otherErrs, "no unexpected errors")

	var rows int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM attached_disks WHERE disk_id = $1`, victimDisk).Scan(&rows))
	assert.Equal(t, 0, rows, "victim disk must remain unattached after the race")

	// The legitimate owner can attach it.
	ownerInst := seedInstanceForAttach(t, ctx, instRepo, "proj-victim")
	updated, err := instRepo.AttachDisk(ctx, ownerInst, domain.AttachedDisk{DiskID: victimDisk, DeviceName: "data0"})
	require.NoError(t, err, "the owning project must be able to attach its own disk")
	require.Len(t, updated.AttachedDisks, 1)
}
