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
	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// TestIntegration_OperationsAccountIDColumn verifies that the compute operations
// table carries the additive, nullable account_id column that corelib
// operations.Repo.CreateWithPrincipal now INSERTs unconditionally. Without the
// column every async mutation (Create/Update/Delete → Operation row) fails with
// SQLSTATE 42703 undefined_column. account_id stays NULL for compute (IAM-only
// denormalization).
func TestIntegration_OperationsAccountIDColumn(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	// Schema "public" — same qualifier as the composition root
	// (cmd/compute/main.go: operations.NewRepo(pool, "public")). The compute
	// operations table is created unqualified → lands in the public schema.
	opsRepo := operations.NewRepo(pool, "public")

	op, err := operations.New(ids.PrefixOperationCompute, "Create disk op-acct-test", nil)
	require.NoError(t, err)
	op.CreatedAt = time.Now().UTC().Truncate(time.Second)
	op.ModifiedAt = op.CreatedAt

	// RED witness: before migration 0012 the INSERT references a non-existent
	// account_id column → 42703. After the migration it succeeds.
	require.NoError(t, opsRepo.CreateWithPrincipal(ctx, op, operations.SystemPrincipal()))

	// Row persisted and readable back (account_id absent from the read path,
	// stays NULL — compute leaves it unset).
	got, err := opsRepo.Get(ctx, op.ID)
	require.NoError(t, err)
	require.Equal(t, op.ID, got.ID)

	// account_id is NULL for compute (no IAM denormalization).
	var accountID *string
	err = pool.QueryRow(ctx, "SELECT account_id FROM operations WHERE id = $1", op.ID).Scan(&accountID)
	require.NoError(t, err)
	require.Nil(t, accountID)
}
