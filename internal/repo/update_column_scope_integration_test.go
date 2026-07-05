// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package repo_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/ids"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/repo"
)

// TestIntegration_DiskUpdate_ColumnScoped_NoLostUpdate — воспроизводит classic
// read-modify-write lost-update: два Update-use-case'а читают одну и ту же строку,
// каждый применяет свою маску к своему СТАЛОМУ снимку, оба пишут. Раньше repo.Update
// писал ВЕСЬ column-set, поэтому второй writer затирал независимое поле, изменённое
// первым (name или description). Column-scoped Update пишет только колонки из
// фактически изменённых полей → оба независимых редактирования выживают.
//
// Последовательность детерминированная (эмулирует interleave use-case'ов), без
// гонки: доказывает семантику scoping, а не тайминг.
func TestIntegration_DiskUpdate_ColumnScoped_NoLostUpdate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	diskRepo := repo.NewDiskRepo(pool)

	id := ids.NewID(ids.PrefixDisk)
	_, err = diskRepo.Insert(ctx, &domain.Disk{
		ID: id, ProjectID: "f-lost-upd", CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		Name: "old-name", Description: "old-desc",
		ZoneID: "ru-central1-a", Size: 4194304, BlockSize: 4096, Status: domain.DiskStatusReady,
	})
	require.NoError(t, err)

	// Client A reads, mutates ТОЛЬКО name (mask=[name]).
	dA, err := diskRepo.Get(ctx, id)
	require.NoError(t, err)
	dA.Name = "new-name"

	// Client B reads (stale snapshot), mutates ТОЛЬКО description (mask=[description]).
	dB, err := diskRepo.Get(ctx, id)
	require.NoError(t, err)
	dB.Description = "new-desc"

	// A commits its name change (только колонка name).
	_, err = diskRepo.Update(ctx, dA, false, []string{"name"})
	require.NoError(t, err)

	// B commits its description change (только колонка description). Со старым
	// full-column UPDATE это затёрло бы name обратно в "old-name".
	_, err = diskRepo.Update(ctx, dB, false, []string{"description"})
	require.NoError(t, err)

	final, err := diskRepo.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "new-name", final.Name, "A's name edit must survive B's description-only update (no lost update)")
	require.Equal(t, "new-desc", final.Description, "B's description edit must be applied")
}

// TestIntegration_InstanceUpdate_ColumnScoped_NoLostUpdate — то же для Instance:
// mask=[name] и mask=[description] на устаревших снимках не должны затирать друг
// друга.
func TestIntegration_InstanceUpdate_ColumnScoped_NoLostUpdate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	instRepo := repo.NewInstanceRepo(pool)

	id := ids.NewID(ids.PrefixInstance)
	_, err = instRepo.Insert(ctx, &domain.Instance{
		ID: id, ProjectID: "f-lost-upd", CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		Name: "old-name", Description: "old-desc",
		ZoneID: "ru-central1-a", PlatformID: "standard-v3", Cores: 2, Memory: 2 << 30, CoreFraction: 100,
		Status: domain.InstanceStatusRunning, FQDN: id + ".auto.internal", NetworkSettingsType: "STANDARD",
		NetworkInterfaces: []domain.NetworkInterface{{Index: "0", SubnetID: "e9bsub", PrimaryV4Address: "10.0.0.10"}},
	}, nil)
	require.NoError(t, err)

	inA, err := instRepo.Get(ctx, id)
	require.NoError(t, err)
	inA.Name = "new-name"

	inB, err := instRepo.Get(ctx, id)
	require.NoError(t, err)
	inB.Description = "new-desc"

	_, err = instRepo.Update(ctx, inA, false, []string{"name"})
	require.NoError(t, err)
	_, err = instRepo.Update(ctx, inB, false, []string{"description"})
	require.NoError(t, err)

	final, err := instRepo.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "new-name", final.Name, "A's name edit must survive B's description-only update")
	require.Equal(t, "new-desc", final.Description, "B's description edit must be applied")
}
