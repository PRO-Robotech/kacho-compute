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

// TestIntegration_AttachDisk_DiskIDUniq_ConcurrentViaRepo — в отличие от
// TestIntegration_AttachedDisksDiskIDUniq_ConcurrentAttachRace (который бьёт
// raw pool.Exec и проверяет SQLSTATE напрямую), этот тест гоняет РЕАЛЬНЫЙ
// production-путь InstanceRepo.AttachDisk из N goroutine-ов на один и тот же
// диск и утверждает, что проигравшие получают именно service.ErrFailedPrecondition
// через repo-метод — т.е. проверяется SQLSTATE→sentinel маппинг под contention,
// а не только DB-индекс. Закрывает finding «concurrent attach race test bypasses
// the production repo/service code path».
func TestIntegration_AttachDisk_DiskIDUniq_ConcurrentViaRepo(t *testing.T) {
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

	diskID := seedDiskForAttach(t, ctx, diskRepo, "f-repo-race")

	const N = 5
	instIDs := make([]string, N)
	for i := 0; i < N; i++ {
		instIDs[i] = seedInstanceForAttach(t, ctx, instRepo, "f-repo-race")
	}

	var (
		wg           sync.WaitGroup
		successCnt   atomic.Int32
		fpCnt        atomic.Int32
		otherErrs    []error
		otherErrsMu  sync.Mutex
		startBarrier = make(chan struct{})
	)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(instID string) {
			defer wg.Done()
			<-startBarrier
			_, aerr := instRepo.AttachDisk(ctx, instID, domain.AttachedDisk{
				DiskID: diskID, Mode: domain.AttachedDiskModeReadWrite,
			})
			switch {
			case aerr == nil:
				successCnt.Add(1)
			case errors.Is(aerr, service.ErrFailedPrecondition):
				fpCnt.Add(1)
			default:
				otherErrsMu.Lock()
				otherErrs = append(otherErrs, aerr)
				otherErrsMu.Unlock()
			}
		}(instIDs[i])
	}
	close(startBarrier)
	wg.Wait()

	assert.Equal(t, int32(1), successCnt.Load(), "exactly one AttachDisk wins")
	assert.Equal(t, int32(N-1), fpCnt.Load(),
		"the %d losers must map to service.ErrFailedPrecondition through the real repo method", N-1)
	assert.Empty(t, otherErrs, "no non-FailedPrecondition errors expected: %v", otherErrs)

	var rows int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM attached_disks WHERE disk_id = $1`, diskID).Scan(&rows))
	assert.Equal(t, 1, rows, "exactly one attached_disks row for the contended disk")
}

// TestIntegration_DiskDelete_vs_AttachDisk_Race — contested path: одна
// DiskRepo.Delete гонится с несколькими InstanceRepo.AttachDisk одного и того же
// READY-диска. Инвариант держит FK attached_disks.disk_id → disks ON DELETE
// RESTRICT (миграция 0001) + disk_id_uniq. Ровно один из двух исходов:
//   - delete выиграл: диск удалён, ВСЕ attach упали FailedPrecondition;
//   - attach выиграл: РОВНО один attach прошёл, delete упал FailedPrecondition,
//     остальные attach упали FailedPrecondition (disk_id uniq / FK).
//
// Закрывает finding «no concurrent race integration test for Disk.Delete vs
// AttachDisk contested path».
func TestIntegration_DiskDelete_vs_AttachDisk_Race(t *testing.T) {
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

	diskID := seedDiskForAttach(t, ctx, diskRepo, "f-del-attach")

	const A = 4
	instIDs := make([]string, A)
	for i := 0; i < A; i++ {
		instIDs[i] = seedInstanceForAttach(t, ctx, instRepo, "f-del-attach")
	}

	var (
		wg            sync.WaitGroup
		deleteOK      atomic.Bool
		deleteFP      atomic.Bool
		attachSuccess atomic.Int32
		attachFP      atomic.Int32
		otherErrs     []error
		otherErrsMu   sync.Mutex
		startBarrier  = make(chan struct{})
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-startBarrier
		derr := diskRepo.Delete(ctx, diskID)
		switch {
		case derr == nil:
			deleteOK.Store(true)
		case errors.Is(derr, service.ErrFailedPrecondition):
			deleteFP.Store(true)
		default:
			otherErrsMu.Lock()
			otherErrs = append(otherErrs, derr)
			otherErrsMu.Unlock()
		}
	}()
	for i := 0; i < A; i++ {
		wg.Add(1)
		go func(instID string) {
			defer wg.Done()
			<-startBarrier
			_, aerr := instRepo.AttachDisk(ctx, instID, domain.AttachedDisk{
				DiskID: diskID, Mode: domain.AttachedDiskModeReadWrite,
			})
			switch {
			case aerr == nil:
				attachSuccess.Add(1)
			case errors.Is(aerr, service.ErrFailedPrecondition):
				attachFP.Add(1)
			default:
				otherErrsMu.Lock()
				otherErrs = append(otherErrs, aerr)
				otherErrsMu.Unlock()
			}
		}(instIDs[i])
	}
	close(startBarrier)
	wg.Wait()

	require.Empty(t, otherErrs, "only nil or FailedPrecondition expected, got: %v", otherErrs)

	// Verify final DB state is self-consistent with the observed outcome.
	var diskExists bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM disks WHERE id=$1)`, diskID).Scan(&diskExists))
	var attachRows int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM attached_disks WHERE disk_id=$1`, diskID).Scan(&attachRows))

	if deleteOK.Load() {
		// Delete won: disk gone, no attach could have succeeded, none dangling.
		assert.False(t, diskExists, "disk must be gone when delete won")
		assert.Equal(t, 0, attachRows, "no attached_disks row may reference a deleted disk (no dangling attach)")
		assert.Equal(t, int32(0), attachSuccess.Load(), "no attach may succeed when delete won")
		assert.Equal(t, int32(A), attachFP.Load(), "all attaches must fail FailedPrecondition when delete won")
	} else {
		// An attach won: exactly one attach row, disk still present, delete failed FP.
		assert.True(t, diskExists, "disk must survive when an attach won")
		assert.Equal(t, int32(1), attachSuccess.Load(), "exactly one attach may win")
		assert.Equal(t, 1, attachRows, "exactly one attached_disks row for the disk")
		assert.True(t, deleteFP.Load(), "delete must fail FailedPrecondition when an attach won")
		assert.Equal(t, int32(A-1), attachFP.Load(), "the other attaches must fail FailedPrecondition")
	}
}
