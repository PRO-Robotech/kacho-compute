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

// TestIntegration_Disk_Relocate_ConcurrentAttachRace проверяет, что Disk.Relocate
// (DiskRepo.SetZoneIfDetached) переносит диск в другую зону строго атомарно
// относительно конкурентного attach — на DB-уровне, а не software-side
// check-then-act.
//
// Инцидент-сценарий: relocate сначала читал «не attached», затем безусловно менял
// zone_id; параллельный attach между check и update прикреплял диск, и relocate
// уводил уже-attached диск в чужую зону (instance в zone-a — диск в zone-b).
//
// Гонка: relocate (zone-a → zone-b) против attach (instance в zone-a). Attach в
// тесте — кооперативный peer: берёт row-lock на disks(id) (`SELECT … FOR UPDATE`)
// и отказывается, если диск уже не в зоне инстанса. Корректный relocate тоже берёт
// конфликтующий row-lock на disks(id), поэтому две операции сериализуются:
//   - relocate первым: диск detached → уезжает в zone-b; затем attach видит zone-b
//     ≠ zone-a и отказывается.
//   - attach первым: диск attached в zone-a; затем relocate видит attached и
//     получает FailedPrecondition.
//
// Инвариант (для каждой итерации): успех ровно у одной из операций, и итоговое
// состояние согласовано — никогда не бывает «диск attached И в zone-b». Software
// check-then-act (TOCTOU) этот инвариант нарушает (обе операции «успешны»,
// attached-диск оказывается в zone-b).
func TestIntegration_Disk_Relocate_ConcurrentAttachRace(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	const (
		zoneA = "ru-central1-a"
		zoneB = "ru-central1-b"
	)
	diskRepo := repo.NewDiskRepo(pool)
	instRepo := repo.NewInstanceRepo(pool)

	diskID := ids.NewID(ids.PrefixDisk)
	_, err = diskRepo.Insert(ctx, &domain.Disk{
		ID: diskID, ProjectID: "f-reloc", CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		ZoneID: zoneA, Size: 4194304, BlockSize: 4096, Status: domain.DiskStatusReady,
	})
	require.NoError(t, err)

	instID := ids.NewID(ids.PrefixInstance)
	_, err = instRepo.Insert(ctx, &domain.Instance{
		ID: instID, ProjectID: "f-reloc", CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		ZoneID: zoneA, PlatformID: "standard-v3", Cores: 2, Memory: 2 << 30, CoreFraction: 100,
		Status: domain.InstanceStatusRunning, FQDN: instID + ".auto.internal", NetworkSettingsType: "STANDARD",
		NetworkInterfaces: []domain.NetworkInterface{{Index: "0", SubnetID: "e9bsub", PrimaryV4Address: "10.0.0.10"}},
	}, nil)
	require.NoError(t, err)

	// relocate: zone-a → zone-b. true = диск перенесён; false = FailedPrecondition
	// (диск attached). Любая другая ошибка — фатальна.
	relocate := func() (bool, error) {
		_, rerr := diskRepo.SetZoneIfDetached(ctx, diskID, zoneB)
		if rerr != nil {
			if errors.Is(rerr, service.ErrFailedPrecondition) {
				return false, nil
			}
			return false, rerr
		}
		return true, nil
	}

	// attach: кооперативный peer. Берёт row-lock на disks(id) и прикрепляет диск к
	// инстансу только если диск всё ещё в зоне инстанса (zone-a). true = attached;
	// false = диск ушёл из zone-a (relocate выиграл).
	attach := func() (bool, error) {
		tx, terr := pool.Begin(ctx)
		if terr != nil {
			return false, terr
		}
		defer func() { _ = tx.Rollback(ctx) }()
		var zone string
		if serr := tx.QueryRow(ctx, `SELECT zone_id FROM disks WHERE id = $1 FOR UPDATE`, diskID).Scan(&zone); serr != nil {
			return false, serr
		}
		if zone != zoneA {
			return false, nil
		}
		if _, eerr := tx.Exec(ctx, `INSERT INTO attached_disks
			(instance_id, disk_id, is_boot, mode, device_name, auto_delete, attached_at)
			VALUES ($1, $2, false, 'READ_WRITE', '', false, now())`, instID, diskID); eerr != nil {
			return false, eerr
		}
		if cerr := tx.Commit(ctx); cerr != nil {
			return false, cerr
		}
		return true, nil
	}

	const iterations = 80
	for i := 0; i < iterations; i++ {
		// reset: диск detached, в zone-a.
		_, err = pool.Exec(ctx, `DELETE FROM attached_disks WHERE disk_id = $1`, diskID)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `UPDATE disks SET zone_id = $1 WHERE id = $2`, zoneA, diskID)
		require.NoError(t, err)

		var (
			wg               sync.WaitGroup
			relocated, attd  bool
			relocErr, attErr error
			start            = make(chan struct{})
		)
		wg.Add(2)
		go func() { defer wg.Done(); <-start; relocated, relocErr = relocate() }()
		go func() { defer wg.Done(); <-start; attd, attErr = attach() }()
		close(start)
		wg.Wait()

		require.NoErrorf(t, relocErr, "iteration %d: relocate returned unexpected error", i)
		require.NoErrorf(t, attErr, "iteration %d: attach returned unexpected error", i)

		// итоговое состояние.
		var attachedNow bool
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM attached_disks WHERE disk_id = $1)`, diskID).Scan(&attachedNow))
		var zoneNow string
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT zone_id FROM disks WHERE id = $1`, diskID).Scan(&zoneNow))

		// Ровно одна операция выигрывает гонку.
		require.Truef(t, relocated != attd,
			"iteration %d: exactly one of {relocate=%v, attach=%v} must win", i, relocated, attd)
		// Никогда: attached-диск в чужой зоне (zone-b). Это и есть нарушение,
		// которое допускал software check-then-act.
		assert.Falsef(t, attachedNow && zoneNow == zoneB,
			"iteration %d: attached disk relocated out of its zone (attached=%v zone=%s)", i, attachedNow, zoneNow)
		// Состояние согласовано с исходом гонки.
		if relocated {
			assert.Equalf(t, zoneB, zoneNow, "iteration %d: relocate won → zone must be zone-b", i)
			assert.Falsef(t, attachedNow, "iteration %d: relocate won → disk must stay detached", i)
		} else {
			assert.Equalf(t, zoneA, zoneNow, "iteration %d: attach won → zone must stay zone-a", i)
			assert.Truef(t, attachedNow, "iteration %d: attach won → disk must be attached", i)
		}
	}
}
