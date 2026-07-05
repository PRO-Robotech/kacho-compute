// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package repo_test

import (
	"context"
	"errors"
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

// TestIntegration_InstanceCreate_DuplicateDeviceName_MapsToFailedPrecondition —
// Instance.Create с двумя attached-дисками, несущими ОДИН непустой device_name на
// одном instance, должен отбиваться на DB-уровне (attached_disks_device_uniq,
// миграция 0001) и мапиться в service.ErrFailedPrecondition — тот же error-контракт,
// что и concurrent AttachDisk-путь (mutateAndReload).
//
// Регрессия до фикса: Insert-attach-loop классифицировал только disk_id_uniq, а
// device/boot uniq 23505 возвращался raw → mapRepoErr не имел sentinel →
// codes.Internal "internal database error" (mis-map, info-poor). Этот тест —
// RED-before/GREEN-after для audit-finding «Instance.Create attach-path
// device_name collision mis-maps to Internal».
func TestIntegration_InstanceCreate_DuplicateDeviceName_MapsToFailedPrecondition(t *testing.T) {
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

	// Два уже существующих READY-диска (FK attached_disks.disk_id → disks).
	bootID := seedDiskForAttach(t, ctx, diskRepo, "f-dup-dev")
	dataID := seedDiskForAttach(t, ctx, diskRepo, "f-dup-dev")

	inID := ids.NewID(ids.PrefixInstance)
	in := &domain.Instance{
		ID: inID, ProjectID: "f-dup-dev", CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		ZoneID: "ru-central1-a", PlatformID: "standard-v3", Cores: 2, Memory: 2 << 30, CoreFraction: 100,
		Status: domain.InstanceStatusRunning, FQDN: inID + ".auto.internal", NetworkSettingsType: "STANDARD",
		NetworkInterfaces: []domain.NetworkInterface{{Index: "0", SubnetID: "e9bsub", PrimaryV4Address: "10.0.0.10"}},
		// Оба attached-диска — device_name="sda" на одном instance → 23505.
		AttachedDisks: []domain.AttachedDisk{
			{DiskID: bootID, IsBoot: true, DeviceName: "sda", Mode: domain.AttachedDiskModeReadWrite},
			{DiskID: dataID, DeviceName: "sda", Mode: domain.AttachedDiskModeReadWrite},
		},
	}
	_, err = instRepo.Insert(ctx, in, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, service.ErrFailedPrecondition),
		"duplicate device_name on Create must map to FailedPrecondition (consistent with AttachDisk path), got: %v", err)
	assert.False(t, errors.Is(err, service.ErrInternal),
		"duplicate device_name must NOT fall through to Internal, got: %v", err)

	// TX откатилась целиком — instance не создан.
	_, err = instRepo.Get(ctx, inID)
	assert.ErrorIs(t, err, service.ErrNotFound, "failed Create must roll back the instance row")
}

// TestIntegration_InstanceCreate_SecondBootDisk_MapsToFailedPrecondition —
// Instance.Create с двумя boot-дисками (attached_disks_boot_uniq) тоже маппится
// в FailedPrecondition, не Internal.
func TestIntegration_InstanceCreate_SecondBootDisk_MapsToFailedPrecondition(t *testing.T) {
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

	bootA := seedDiskForAttach(t, ctx, diskRepo, "f-two-boot")
	bootB := seedDiskForAttach(t, ctx, diskRepo, "f-two-boot")

	inID := ids.NewID(ids.PrefixInstance)
	in := &domain.Instance{
		ID: inID, ProjectID: "f-two-boot", CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		ZoneID: "ru-central1-a", PlatformID: "standard-v3", Cores: 2, Memory: 2 << 30, CoreFraction: 100,
		Status: domain.InstanceStatusRunning, FQDN: inID + ".auto.internal", NetworkSettingsType: "STANDARD",
		NetworkInterfaces: []domain.NetworkInterface{{Index: "0", SubnetID: "e9bsub", PrimaryV4Address: "10.0.0.10"}},
		AttachedDisks: []domain.AttachedDisk{
			{DiskID: bootA, IsBoot: true, DeviceName: "boot-a", Mode: domain.AttachedDiskModeReadWrite},
			{DiskID: bootB, IsBoot: true, DeviceName: "boot-b", Mode: domain.AttachedDiskModeReadWrite},
		},
	}
	_, err = instRepo.Insert(ctx, in, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, service.ErrFailedPrecondition),
		"second boot disk on Create must map to FailedPrecondition, got: %v", err)
	assert.False(t, errors.Is(err, service.ErrInternal),
		"second boot disk must NOT fall through to Internal, got: %v", err)
}
