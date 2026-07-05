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

// TestIntegration_Disk_Resize_ConcurrentMonotonic проверяет, что DiskRepo.Update
// применяет инвариант «размер диска может только увеличиваться» атомарно на
// DB-уровне (conditional UPDATE … WHERE size <= $new + проверка кардинальности),
// а не software-side stale-read check-then-act.
//
// Инцидент-сценарий (TOCTOU, до фикса): use-case читал размер (Get=10), сравнивал
// req.Size с устаревшим снимком и делал безусловный UPDATE disks SET size=$N. Две
// конкурентные grow-операции (→20 и →15) обе проходили software-проверку (обе >10)
// и делали безусловный UPDATE; last-writer-wins давал итог 15 — усадку уже
// подтверждённого роста до 20 (в data-plane это могло бы обрезать ФС, выросшую до 20).
//
// Гонка: resize→20 против resize→15 (обе от базовых 10). row-level lock на
// disks(id) сериализует два single-statement UPDATE. Инвариант: итоговый размер
// ВСЕГДА равен максимальной цели (20) — независимо от порядка коммита:
//   - →20 первым: диск=20; затем →15 видит size(20) ≤ 15 == false → 0 строк →
//     FailedPrecondition (усадка отбита).
//   - →15 первым: диск=15; затем →20 видит size(15) ≤ 20 == true → диск=20.
// Software check-then-act этот инвариант нарушает (при порядке «→20, затем →15»
// итог = 15).
func TestIntegration_Disk_Resize_ConcurrentMonotonic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	const (
		base = int64(10 << 30) // 10 GiB
		big  = int64(20 << 30) // 20 GiB (max target)
		mid  = int64(15 << 30) // 15 GiB
	)
	diskRepo := repo.NewDiskRepo(pool)

	diskID := ids.NewID(ids.PrefixDisk)
	_, err = diskRepo.Insert(ctx, &domain.Disk{
		ID: diskID, ProjectID: "f-resize", CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		ZoneID: "ru-central1-a", Size: base, BlockSize: 4096, Status: domain.DiskStatusReady,
	})
	require.NoError(t, err)

	// resize — column-scoped size-only Update. true = применён; false =
	// FailedPrecondition (усадка отбита CAS). Любая другая ошибка — фатальна.
	resize := func(target int64) (bool, error) {
		_, uerr := diskRepo.Update(ctx, &domain.Disk{ID: diskID, Size: target}, false, []string{"size"})
		if uerr != nil {
			if errors.Is(uerr, service.ErrFailedPrecondition) {
				return false, nil
			}
			return false, uerr
		}
		return true, nil
	}

	const iterations = 80
	for i := 0; i < iterations; i++ {
		// reset: диск обратно к базовым 10 GiB.
		_, err = pool.Exec(ctx, `UPDATE disks SET size = $1 WHERE id = $2`, base, diskID)
		require.NoError(t, err)

		var (
			wg           sync.WaitGroup
			okBig, okMid bool
			errBig, eMid error
			start        = make(chan struct{})
		)
		wg.Add(2)
		go func() { defer wg.Done(); <-start; okBig, errBig = resize(big) }()
		go func() { defer wg.Done(); <-start; okMid, eMid = resize(mid) }()
		close(start)
		wg.Wait()

		require.NoErrorf(t, errBig, "iteration %d: resize→20 returned unexpected error", i)
		require.NoErrorf(t, eMid, "iteration %d: resize→15 returned unexpected error", i)

		var sizeNow int64
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT size FROM disks WHERE id = $1`, diskID).Scan(&sizeNow))

		// Ключевой инвариант: итог ВСЕГДА равен максимальной цели. Именно это
		// нарушал software check-then-act (усадка до 15 при порядке «→20, →15»).
		assert.Equalf(t, big, sizeNow,
			"iteration %d: final size must equal max target (monotonic), got %d", i, sizeNow)
		// grow→20 не может быть отвергнут (20 ≥ любого промежуточного состояния).
		assert.Truef(t, okBig, "iteration %d: resize→20 must always succeed", i)
		// Никогда обе не «усаживают»: как минимум один writer применён.
		assert.Truef(t, okBig || okMid, "iteration %d: at least one resize must apply", i)
	}
}
