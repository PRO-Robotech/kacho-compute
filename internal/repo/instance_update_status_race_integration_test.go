// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package repo_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/ids"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/repo"
)

// newRunningInstance is a small helper for the Update-vs-lifecycle tests.
func newRunningInstance(id string) *domain.Instance {
	return &domain.Instance{
		ID: id, ProjectID: "f-upd-status-race", CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		ZoneID: "ru-central1-a", PlatformID: "standard-v3", Cores: 2, Memory: 2 << 30, CoreFraction: 100,
		Status: domain.InstanceStatusRunning,
		FQDN:   id + ".auto.internal", NetworkSettingsType: "STANDARD",
	}
}

// TestIntegration_InstanceUpdate_DoesNotClobberLifecycleStatus reproduces the
// stale-status clobber: the Update use-case is a read-modify-write — it Get()s
// the instance (capturing status=RUNNING), mutates only descriptive fields, then
// calls repo.Update. If repo.Update writes the status column back from that stale
// snapshot, a lifecycle transition (Stop → STOPPED) that commits in between is
// silently reverted to RUNNING.
//
// Deterministic interleave mirroring the failure scenario:
//  1. read snapshot (status=RUNNING) — as the Update use-case does at start;
//  2. Stop commits RUNNING→STOPPED via SetStatusCAS;
//  3. repo.Update runs with the stale in-memory snapshot (still RUNNING).
//
// The instance MUST remain STOPPED. Update never carries a status change (no
// update-mask field maps to status), so it must not touch the status column.
func TestIntegration_InstanceUpdate_DoesNotClobberLifecycleStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	instRepo := repo.NewInstanceRepo(pool)

	inID := ids.NewID(ids.PrefixInstance)
	_, err = instRepo.Insert(ctx, newRunningInstance(inID))
	require.NoError(t, err)

	// (1) Update use-case reads the instance up-front — captures status=RUNNING.
	snapshot, err := instRepo.Get(ctx, inID)
	require.NoError(t, err)
	require.Equal(t, domain.InstanceStatusRunning, snapshot.Status)

	// (2) Concurrent Stop commits RUNNING→STOPPED.
	stopped, err := instRepo.SetStatusCAS(ctx, inID, domain.InstanceStatusRunning, domain.InstanceStatusStopped)
	require.NoError(t, err)
	require.Equal(t, domain.InstanceStatusStopped, stopped.Status)

	// (3) The Update runs on the now-stale snapshot (still RUNNING in memory),
	// mutating only a descriptive field.
	snapshot.Name = "renamed-after-stop"
	updated, err := instRepo.Update(ctx, snapshot, false, []string{"name"})
	require.NoError(t, err)
	assert.Equal(t, "renamed-after-stop", updated.Name, "descriptive field must be persisted")

	// The status must NOT have been reverted to RUNNING by the Update.
	assert.Equal(t, domain.InstanceStatusStopped, updated.Status,
		"Update must not clobber the lifecycle status back to the stale RUNNING value")

	got, err := instRepo.Get(ctx, inID)
	require.NoError(t, err)
	assert.Equal(t, domain.InstanceStatusStopped, got.Status,
		"final DB state must remain STOPPED (Stop transition survives the concurrent Update)")
}

// TestIntegration_InstanceUpdate_ConcurrentWithStop_ExactlyOneStatusOutcome runs
// many descriptive Updates concurrently with a single Stop and asserts the
// lifecycle transition is never lost: once Stop wins, no concurrent Update may
// flip the instance back to RUNNING. Runs under -race.
func TestIntegration_InstanceUpdate_ConcurrentWithStop_ExactlyOneStatusOutcome(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	instRepo := repo.NewInstanceRepo(pool)

	inID := ids.NewID(ids.PrefixInstance)
	_, err = instRepo.Insert(ctx, newRunningInstance(inID))
	require.NoError(t, err)

	// Every Updater starts from the SAME stale RUNNING snapshot (as the real
	// use-case would if it read before the Stop committed).
	staleSnapshot, err := instRepo.Get(ctx, inID)
	require.NoError(t, err)
	require.Equal(t, domain.InstanceStatusRunning, staleSnapshot.Status)

	const updaters = 8
	var (
		wg           sync.WaitGroup
		updateErrs   atomic.Int32
		startBarrier = make(chan struct{})
	)
	// One Stop goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-startBarrier
		_, _ = instRepo.SetStatusCAS(ctx, inID, domain.InstanceStatusRunning, domain.InstanceStatusStopped)
	}()
	// N descriptive Update goroutines, each on its own copy of the stale snapshot.
	for i := 0; i < updaters; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-startBarrier
			cp := *staleSnapshot
			cp.Name = "u"
			if _, err := instRepo.Update(ctx, &cp, false, []string{"name"}); err != nil {
				updateErrs.Add(1)
			}
		}(i)
	}
	close(startBarrier)
	wg.Wait()

	require.Equal(t, int32(0), updateErrs.Load(), "no Update should error")

	// Regardless of interleaving, once Stop committed STOPPED the status must
	// stay STOPPED — a status-writing Update would race it back to RUNNING.
	got, err := instRepo.Get(ctx, inID)
	require.NoError(t, err)
	assert.Equal(t, domain.InstanceStatusStopped, got.Status,
		"lifecycle status must be STOPPED; a concurrent Update must not revert it to RUNNING")
}
