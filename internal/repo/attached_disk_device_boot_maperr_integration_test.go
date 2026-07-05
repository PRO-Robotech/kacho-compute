// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package repo_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/repo"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// TestIntegration_AttachDisk_DeviceUniq_MapsToFailedPrecondition — при коллизии
// attached_disks_device_uniq через repo.AttachDisk (mutateAndReload) маппинг
// ошибки должен совпадать с sequential software-check в service.AttachDisk:
// FailedPrecondition, а не generic AlreadyExists. Закрывает несогласованность
// error-контракта между sequential и concurrent путями (audit finding
// «device_name/boot uniqueness invariants … software check returns a different
// gRPC code than the DB backstop»).
func TestIntegration_AttachDisk_DeviceUniq_MapsToFailedPrecondition(t *testing.T) {
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

	instID := seedInstanceForAttach(t, ctx, instRepo, "f-dev-maperr")
	diskA := seedDiskForAttach(t, ctx, diskRepo, "f-dev-maperr")
	diskB := seedDiskForAttach(t, ctx, diskRepo, "f-dev-maperr")

	// первый attach с device_name='sdb' — успех.
	_, err = instRepo.AttachDisk(ctx, instID, domain.AttachedDisk{
		DiskID: diskA, Mode: domain.AttachedDiskModeReadWrite, DeviceName: "sdb",
	})
	require.NoError(t, err)

	// второй диск, тот же device_name → 23505 attached_disks_device_uniq.
	// Ожидаем FailedPrecondition (как sequential-путь), НЕ AlreadyExists.
	_, err = instRepo.AttachDisk(ctx, instID, domain.AttachedDisk{
		DiskID: diskB, Mode: domain.AttachedDiskModeReadWrite, DeviceName: "sdb",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, service.ErrFailedPrecondition),
		"device_name collision must map to FailedPrecondition (consistent with sequential path), got: %v", err)
	assert.False(t, errors.Is(err, service.ErrAlreadyExists),
		"device_name collision must NOT map to generic AlreadyExists, got: %v", err)
}

// TestIntegration_AttachDisk_BootUniq_MapsToFailedPrecondition — коллизия
// attached_disks_boot_uniq (второй boot-disk на instance) через repo.AttachDisk
// маппится в FailedPrecondition, а не AlreadyExists.
func TestIntegration_AttachDisk_BootUniq_MapsToFailedPrecondition(t *testing.T) {
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

	instID := seedInstanceForAttach(t, ctx, instRepo, "f-boot-maperr")
	diskA := seedDiskForAttach(t, ctx, diskRepo, "f-boot-maperr")
	diskB := seedDiskForAttach(t, ctx, diskRepo, "f-boot-maperr")

	_, err = instRepo.AttachDisk(ctx, instID, domain.AttachedDisk{
		DiskID: diskA, IsBoot: true, Mode: domain.AttachedDiskModeReadWrite, DeviceName: "boot-a",
	})
	require.NoError(t, err)

	// второй boot-disk → 23505 attached_disks_boot_uniq → FailedPrecondition.
	_, err = instRepo.AttachDisk(ctx, instID, domain.AttachedDisk{
		DiskID: diskB, IsBoot: true, Mode: domain.AttachedDiskModeReadWrite, DeviceName: "boot-b",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, service.ErrFailedPrecondition),
		"second boot disk must map to FailedPrecondition, got: %v", err)
	assert.False(t, errors.Is(err, service.ErrAlreadyExists),
		"second boot disk must NOT map to generic AlreadyExists, got: %v", err)
}
