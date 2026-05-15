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

// TestIntegration_AttachedDisksDiskIDUniq_ConcurrentAttachRace проверяет
// инвариант KAC-90 (миграция 0007): один disk_id может присутствовать в
// attached_disks ровно один раз. 5 concurrent goroutines пытаются прикрепить
// один и тот же Disk к 5 разным Instance — DB должна пропустить ровно одну
// INSERT, остальные — отбить SQLSTATE 23505 на индексе
// attached_disks_disk_id_uniq.
//
// Это parity с NIC-attach race инцидентом KAC-52: software-side guard
// (IsAttached / cycle по AttachedDisks) — TOCTOU-prone; единственная
// race-proof защита — DB-уровень.
func TestIntegration_AttachedDisksDiskIDUniq_ConcurrentAttachRace(t *testing.T) {
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

	// 1 shared disk.
	diskID := ids.NewID(ids.PrefixDisk)
	_, err = diskRepo.Insert(ctx, &domain.Disk{
		ID: diskID, FolderID: "f-race", CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		ZoneID: "ru-central1-a", Size: 4194304, BlockSize: 4096, Status: domain.DiskStatusReady,
	})
	require.NoError(t, err)

	// 5 Instances (без attached disks — диск будет привязан concurrent-INSERT-ами ниже).
	const N = 5
	instanceIDs := make([]string, N)
	for i := 0; i < N; i++ {
		inID := ids.NewID(ids.PrefixInstance)
		instanceIDs[i] = inID
		in := &domain.Instance{
			ID: inID, FolderID: "f-race", CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
			ZoneID: "ru-central1-a", PlatformID: "standard-v3", Cores: 2, Memory: 2 << 30, CoreFraction: 100,
			Status: domain.InstanceStatusRunning, FQDN: inID + ".auto.internal", NetworkSettingsType: "STANDARD",
			NetworkInterfaces: []domain.NetworkInterface{{Index: "0", SubnetID: "e9bsub", PrimaryV4Address: "10.0.0.10"}},
		}
		_, err := instRepo.Insert(ctx, in, nil)
		require.NoError(t, err)
	}

	// 5 concurrent goroutines — каждая делает прямой INSERT INTO attached_disks
	// с одним и тем же disk_id, но разными instance_id. Используем pool.Exec
	// напрямую (а не InstanceRepo.AttachDisk) — нам нужна именно гонка на
	// уровне INSERT-statement без software-pre-check'ов.
	var (
		wg            sync.WaitGroup
		successCnt    atomic.Int32
		uniqViolCnt   atomic.Int32
		otherErrs     []error
		otherErrsMu   sync.Mutex
		startBarrier  = make(chan struct{})
	)

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(instID string) {
			defer wg.Done()
			<-startBarrier // одновременный старт всех goroutine-ов
			_, err := pool.Exec(ctx, `INSERT INTO attached_disks
				(instance_id, disk_id, is_boot, mode, device_name, auto_delete, attached_at)
				VALUES ($1, $2, false, 'READ_WRITE', '', false, now())`,
				instID, diskID)
			if err == nil {
				successCnt.Add(1)
				return
			}
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "attached_disks_disk_id_uniq" {
				uniqViolCnt.Add(1)
				return
			}
			otherErrsMu.Lock()
			otherErrs = append(otherErrs, err)
			otherErrsMu.Unlock()
		}(instanceIDs[i])
	}

	close(startBarrier)
	wg.Wait()

	// Ровно один winner; остальные N-1 — UNIQUE violation на нашем индексе.
	assert.Equal(t, int32(1), successCnt.Load(), "expected exactly 1 successful concurrent attach")
	assert.Equal(t, int32(N-1), uniqViolCnt.Load(),
		"expected exactly %d UNIQUE violations on attached_disks_disk_id_uniq", N-1)
	assert.Empty(t, otherErrs, "no other (non-23505) errors expected")

	// Verify: в DB ровно одна строка для этого disk_id.
	var rowCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM attached_disks WHERE disk_id = $1`, diskID).Scan(&rowCount))
	assert.Equal(t, 1, rowCount, "exactly one attached_disks row for this disk_id")
}
