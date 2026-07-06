// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package repo_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/repo"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// TestIntegration_DiskTypeRepo_AdminCRUD_ErrorMapping — real-Postgres coverage of
// the DiskTypeService admin CRUD write-path (Create/Update/Delete), which was
// previously exercised only through portmock. Verifies the actual SQLSTATE→sentinel
// translation of DiskTypeRepo (PK 23505, RETURNING/RowsAffected 0-rows) end-to-end
// through mapRepoErr:
//   - duplicate id Insert → service.ErrAlreadyExists (real PK constraint, not a mock convention),
//   - Update of an existing id → applied; Update of a missing id → NotFound,
//   - Delete of an existing id → ok; Delete of a missing id → NotFound.
func TestIntegration_DiskTypeRepo_AdminCRUD_ErrorMapping(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	dtr := repo.NewDiskTypeRepo(pool)
	svc := service.NewDiskTypeService(dtr)

	const id = "network-nvme-test"

	// Insert new disk type.
	created, err := dtr.Insert(ctx, &domain.DiskType{ID: id, Description: "nvme", ZoneIDs: []string{"ru-central1-a", "ru-central1-b"}})
	require.NoError(t, err)
	assert.Equal(t, id, created.ID)
	assert.ElementsMatch(t, []string{"ru-central1-a", "ru-central1-b"}, created.ZoneIDs)

	// Duplicate id → real PK 23505 → ErrAlreadyExists sentinel (repo layer)…
	_, err = dtr.Insert(ctx, &domain.DiskType{ID: id, Description: "dup"})
	require.ErrorIs(t, err, service.ErrAlreadyExists)
	// …and AlreadyExists gRPC code through the service (mapRepoErr end-to-end).
	_, err = svc.Create(ctx, id, "dup", nil)
	require.Error(t, err)
	assert.Equal(t, codes.AlreadyExists, status.Code(err))

	// Update existing id → applied (Get-then-Update read-modify-write path).
	updated, err := svc.Update(ctx, id, "nvme-v2", []string{"ru-central1-d"})
	require.NoError(t, err)
	assert.Equal(t, "nvme-v2", updated.Description)
	assert.ElementsMatch(t, []string{"ru-central1-d"}, updated.ZoneIDs)
	got, err := dtr.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "nvme-v2", got.Description)

	// Update missing id → NotFound (repo RETURNING 0 rows → ErrNotFound; svc → gRPC NotFound).
	_, err = dtr.Update(ctx, &domain.DiskType{ID: "does-not-exist", Description: "x"})
	require.ErrorIs(t, err, service.ErrNotFound)
	_, err = svc.Update(ctx, "does-not-exist", "x", nil)
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))

	// Delete existing → ok.
	require.NoError(t, dtr.Delete(ctx, id))
	_, err = dtr.Get(ctx, id)
	require.ErrorIs(t, err, service.ErrNotFound)

	// Delete missing id → NotFound (RowsAffected 0 → ErrNotFound; svc → gRPC NotFound).
	err = dtr.Delete(ctx, "does-not-exist")
	require.ErrorIs(t, err, service.ErrNotFound)
	err = svc.Delete(ctx, "does-not-exist")
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}
