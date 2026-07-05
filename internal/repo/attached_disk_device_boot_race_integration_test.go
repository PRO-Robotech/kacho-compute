// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package repo_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/ids"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/repo"
)

// seedInstanceForAttach вставляет один Instance (без attached-disks) и
// возвращает его id — заготовка под прямые INSERT INTO attached_disks.
func seedInstanceForAttach(t *testing.T, ctx context.Context, instRepo *repo.InstanceRepo, folder string) string {
	t.Helper()
	inID := ids.NewID(ids.PrefixInstance)
	_, err := instRepo.Insert(ctx, &domain.Instance{
		ID: inID, ProjectID: folder, CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		ZoneID: "ru-central1-a", PlatformID: "standard-v3", Cores: 2, Memory: 2 << 30, CoreFraction: 100,
		Status: domain.InstanceStatusRunning, FQDN: inID + ".auto.internal", NetworkSettingsType: "STANDARD",
		NetworkInterfaces: []domain.NetworkInterface{{Index: "0", SubnetID: "e9bsub", PrimaryV4Address: "10.0.0.10"}},
	}, nil)
	require.NoError(t, err)
	return inID
}

// seedDiskForAttach вставляет один Disk и возвращает его id.
func seedDiskForAttach(t *testing.T, ctx context.Context, diskRepo *repo.DiskRepo, folder string) string {
	t.Helper()
	diskID := ids.NewID(ids.PrefixDisk)
	_, err := diskRepo.Insert(ctx, &domain.Disk{
		ID: diskID, ProjectID: folder, CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		ZoneID: "ru-central1-a", Size: 4194304, BlockSize: 4096, Status: domain.DiskStatusReady,
	})
	require.NoError(t, err)
	return diskID
}

// TestIntegration_AttachedDisksDeviceUniq_Negative — negative-кейс инварианта
// (миграция 0001 attached_disks_device_uniq): два разных диска, прикреплённые к
// ОДНОМУ instance с ОДНИМ И ТЕМ ЖЕ непустым device_name, должны отбиваться на
// DB-уровне (SQLSTATE 23505). Пустой device_name ('') под partial-UNIQUE
// `WHERE device_name <> ''` не участвует — множественные '' допускаются.
func TestIntegration_AttachedDisksDeviceUniq_Negative(t *testing.T) {
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

	instID := seedInstanceForAttach(t, ctx, instRepo, "f-dev-neg")
	diskA := seedDiskForAttach(t, ctx, diskRepo, "f-dev-neg")
	diskB := seedDiskForAttach(t, ctx, diskRepo, "f-dev-neg")

	// первый attach c device_name='sdb' — успех.
	_, err = pool.Exec(ctx, `INSERT INTO attached_disks
		(instance_id, disk_id, is_boot, mode, device_name, auto_delete, attached_at)
		VALUES ($1, $2, false, 'READ_WRITE', 'sdb', false, now())`, instID, diskA)
	require.NoError(t, err)

	// второй диск на тот же instance c ТЕМ ЖЕ device_name='sdb' — отбой 23505.
	_, err = pool.Exec(ctx, `INSERT INTO attached_disks
		(instance_id, disk_id, is_boot, mode, device_name, auto_delete, attached_at)
		VALUES ($1, $2, false, 'READ_WRITE', 'sdb', false, now())`, instID, diskB)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "expected pg error, got: %v", err)
	assert.Equal(t, "23505", pgErr.Code)
	assert.Equal(t, "attached_disks_device_uniq", pgErr.ConstraintName)

	// пустой device_name НЕ подпадает под partial-UNIQUE — два '' допустимы.
	_, err = pool.Exec(ctx, `INSERT INTO attached_disks
		(instance_id, disk_id, is_boot, mode, device_name, auto_delete, attached_at)
		VALUES ($1, $2, false, 'READ_WRITE', '', false, now())`, instID, diskB)
	require.NoError(t, err, "empty device_name must not collide (partial UNIQUE WHERE device_name <> '')")
}

// TestIntegration_AttachedDisksDeviceUniq_ConcurrentRace — N concurrent
// прямых INSERT-ов разных дисков в ОДИН instance с ОДНИМ device_name. Ровно
// один winner; остальные — 23505 на attached_disks_device_uniq. Software-цикл
// в service.AttachDisk TOCTOU-prone под гонкой — единственная race-proof
// защита на DB-уровне (within-service-инвариант).
func TestIntegration_AttachedDisksDeviceUniq_ConcurrentRace(t *testing.T) {
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

	instID := seedInstanceForAttach(t, ctx, instRepo, "f-dev-race")

	const N = 5
	diskIDs := make([]string, N)
	for i := 0; i < N; i++ {
		diskIDs[i] = seedDiskForAttach(t, ctx, diskRepo, "f-dev-race")
	}

	var (
		wg           sync.WaitGroup
		successCnt   atomic.Int32
		uniqViolCnt  atomic.Int32
		otherErrs    []error
		otherErrsMu  sync.Mutex
		startBarrier = make(chan struct{})
	)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(diskID string) {
			defer wg.Done()
			<-startBarrier
			_, err := pool.Exec(ctx, `INSERT INTO attached_disks
				(instance_id, disk_id, is_boot, mode, device_name, auto_delete, attached_at)
				VALUES ($1, $2, false, 'READ_WRITE', 'sdx', false, now())`, instID, diskID)
			if err == nil {
				successCnt.Add(1)
				return
			}
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "attached_disks_device_uniq" {
				uniqViolCnt.Add(1)
				return
			}
			otherErrsMu.Lock()
			otherErrs = append(otherErrs, err)
			otherErrsMu.Unlock()
		}(diskIDs[i])
	}
	close(startBarrier)
	wg.Wait()

	assert.Equal(t, int32(1), successCnt.Load(), "exactly 1 concurrent attach with device_name='sdx' may win")
	assert.Equal(t, int32(N-1), uniqViolCnt.Load(), "expected %d device_uniq violations", N-1)
	assert.Empty(t, otherErrs, "no non-23505 errors expected")

	var rowCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM attached_disks WHERE instance_id = $1 AND device_name = 'sdx'`, instID).Scan(&rowCount))
	assert.Equal(t, 1, rowCount, "exactly one row with device_name='sdx'")
}

// TestIntegration_AttachedDisksBootUniq_ConcurrentRace — инвариант
// attached_disks_boot_uniq (миграция 0001): ровно один boot-disk на instance.
// N concurrent INSERT-ов разных дисков c is_boot=true в один instance — ровно
// один winner, остальные 23505.
func TestIntegration_AttachedDisksBootUniq_ConcurrentRace(t *testing.T) {
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

	instID := seedInstanceForAttach(t, ctx, instRepo, "f-boot-race")

	const N = 5
	diskIDs := make([]string, N)
	for i := 0; i < N; i++ {
		diskIDs[i] = seedDiskForAttach(t, ctx, diskRepo, "f-boot-race")
	}

	var (
		wg           sync.WaitGroup
		successCnt   atomic.Int32
		uniqViolCnt  atomic.Int32
		otherErrs    []error
		otherErrsMu  sync.Mutex
		startBarrier = make(chan struct{})
	)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(diskID string, idx int) {
			defer wg.Done()
			<-startBarrier
			// device_name уникален per-row, чтобы гонка была именно на boot-инвариант,
			// а не на device_uniq.
			_, err := pool.Exec(ctx, `INSERT INTO attached_disks
				(instance_id, disk_id, is_boot, mode, device_name, auto_delete, attached_at)
				VALUES ($1, $2, true, 'READ_WRITE', $3, false, now())`,
				instID, diskID, "boot"+diskID)
			if err == nil {
				successCnt.Add(1)
				return
			}
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "attached_disks_boot_uniq" {
				uniqViolCnt.Add(1)
				return
			}
			otherErrsMu.Lock()
			otherErrs = append(otherErrs, err)
			otherErrsMu.Unlock()
		}(diskIDs[i], i)
	}
	close(startBarrier)
	wg.Wait()

	assert.Equal(t, int32(1), successCnt.Load(), "exactly 1 boot disk may win per instance")
	assert.Equal(t, int32(N-1), uniqViolCnt.Load(), "expected %d boot_uniq violations", N-1)
	assert.Empty(t, otherErrs, "no non-23505 errors expected")

	var bootCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM attached_disks WHERE instance_id = $1 AND is_boot`, instID).Scan(&bootCount))
	assert.Equal(t, 1, bootCount, "exactly one boot disk row per instance")
}
