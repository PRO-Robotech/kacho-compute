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
	"github.com/PRO-Robotech/kacho-corelib/ids"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/ports"
	"github.com/PRO-Robotech/kacho-compute/internal/repo"
)

// stopInstance — helper: insert RUNNING then transition to STOPPED, returning a
// fresh STOPPED snapshot.
func stopInstance(ctx context.Context, t *testing.T, r *repo.InstanceRepo, id string) *domain.Instance {
	t.Helper()
	_, err := r.Insert(ctx, newRunningInstance(id))
	require.NoError(t, err)
	stopped, err := r.SetStatusCAS(ctx, id, domain.InstanceStatusRunning, domain.InstanceStatusStopped)
	require.NoError(t, err)
	require.Equal(t, domain.InstanceStatusStopped, stopped.Status)
	return stopped
}

// TestIntegration_InstanceResize_RequiresStopped_ConcurrentStart reproduces the
// resize-vs-Start TOCTOU: the Update use-case Get()s a STOPPED instance, passes
// the software `must be STOPPED` check, but a concurrent Start commits
// STOPPED→RUNNING before the column UPDATE lands. A resize UPDATE with no
// status predicate would silently mutate cores/memory/platform on a now-RUNNING
// instance (a forbidden live-resize). The DB-level CAS (`AND status='STOPPED'`)
// must reject it with FailedPrecondition and leave the resize columns untouched.
func TestIntegration_InstanceResize_RequiresStopped_ConcurrentStart(t *testing.T) {
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
	stale := stopInstance(ctx, t, instRepo, inID) // stale STOPPED snapshot (cores=2)

	// (1) Concurrent Start commits STOPPED→RUNNING.
	running, err := instRepo.SetStatusCAS(ctx, inID, domain.InstanceStatusStopped, domain.InstanceStatusRunning)
	require.NoError(t, err)
	require.Equal(t, domain.InstanceStatusRunning, running.Status)

	// (2) Resize runs on the now-stale STOPPED snapshot: cores 2 → 8.
	stale.Cores = 8
	stale.Memory = 8 << 30
	_, err = instRepo.Update(ctx, stale, false, []string{"resources_spec"})

	// Must be rejected — the instance is RUNNING, resize requires STOPPED.
	require.Error(t, err, "resize on a RUNNING instance must be rejected")
	assert.True(t, errors.Is(err, ports.ErrFailedPrecondition),
		"resize-while-running must map to FailedPrecondition, got: %v", err)

	// The resize columns must NOT have been written.
	got, err := instRepo.Get(ctx, inID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), got.Cores, "cores must remain unchanged after a rejected resize")
	assert.Equal(t, domain.InstanceStatusRunning, got.Status)
}

// TestIntegration_InstanceResize_WhileStopped_OK — positive path: a resize on a
// genuinely STOPPED instance succeeds and persists the new resources_spec.
func TestIntegration_InstanceResize_WhileStopped_OK(t *testing.T) {
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
	stopped := stopInstance(ctx, t, instRepo, inID)

	stopped.Cores = 8
	stopped.Memory = 8 << 30
	updated, err := instRepo.Update(ctx, stopped, false, []string{"resources_spec"})
	require.NoError(t, err, "resize on a STOPPED instance must succeed")
	assert.Equal(t, int64(8), updated.Cores)

	got, err := instRepo.Get(ctx, inID)
	require.NoError(t, err)
	assert.Equal(t, int64(8), got.Cores)
	assert.Equal(t, domain.InstanceStatusStopped, got.Status)
}

// TestIntegration_InstanceResize_MissingInstance_NotFound — the status-CAS path
// still distinguishes a missing instance (NotFound) from a running one
// (FailedPrecondition).
func TestIntegration_InstanceResize_MissingInstance_NotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	instRepo := repo.NewInstanceRepo(pool)

	ghost := newRunningInstance(ids.NewID(ids.PrefixInstance)) // never inserted
	ghost.Status = domain.InstanceStatusStopped
	ghost.Cores = 8
	_, err = instRepo.Update(ctx, ghost, false, []string{"resources_spec"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ports.ErrNotFound),
		"resize of a nonexistent instance must map to NotFound, got: %v", err)
}

// TestIntegration_InstanceResize_ConcurrentResizersOnRunning_AllRejected starts
// the instance first (STOPPED→RUNNING committed), then fires N concurrent
// resizers on the stale STOPPED snapshot. Under the DB-level CAS every one must
// be rejected with FailedPrecondition and the resize columns must stay untouched
// — no resize may slip onto the RUNNING row under contention. Runs under -race.
func TestIntegration_InstanceResize_ConcurrentResizersOnRunning_AllRejected(t *testing.T) {
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
	stale := stopInstance(ctx, t, instRepo, inID)

	// Start commits STOPPED→RUNNING before any resizer runs.
	running, err := instRepo.SetStatusCAS(ctx, inID, domain.InstanceStatusStopped, domain.InstanceStatusRunning)
	require.NoError(t, err)
	require.Equal(t, domain.InstanceStatusRunning, running.Status)

	const resizers = 8
	var (
		wg           sync.WaitGroup
		resizeOK     atomic.Int32
		wrongErr     atomic.Int32
		startBarrier = make(chan struct{})
	)
	for i := 0; i < resizers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startBarrier
			cp := *stale // stale STOPPED snapshot
			cp.Cores = 8
			cp.Memory = 8 << 30
			_, uerr := instRepo.Update(ctx, &cp, false, []string{"resources_spec"})
			switch {
			case uerr == nil:
				resizeOK.Add(1)
			case errors.Is(uerr, ports.ErrFailedPrecondition):
				// expected
			default:
				wrongErr.Add(1)
			}
		}()
	}
	close(startBarrier)
	wg.Wait()

	assert.Equal(t, int32(0), resizeOK.Load(), "no resize may succeed on a RUNNING instance")
	assert.Equal(t, int32(0), wrongErr.Load(), "every rejected resize must be FailedPrecondition")

	got, err := instRepo.Get(ctx, inID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), got.Cores, "resize columns must be untouched on the RUNNING instance")
	assert.Equal(t, domain.InstanceStatusRunning, got.Status)
}
