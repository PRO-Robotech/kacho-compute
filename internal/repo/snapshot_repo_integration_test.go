// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package repo_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/ids"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/fgaintent"
	"github.com/PRO-Robotech/kacho-compute/internal/repo"
)

func newTestSnapshot(id, projectID string, labels map[string]string) *domain.Snapshot {
	return &domain.Snapshot{
		ID: id, ProjectID: projectID, CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		Name: "snap-" + id[len(id)-4:], Labels: labels, StorageSize: 4194304, DiskSize: 4194304,
		Status: domain.SnapshotStatusReady, SourceDiskID: ids.NewID(ids.PrefixDisk),
	}
}

// TestSnapshotRepo_T31Revoke03Snapshot_LabelRemoveEmitsMirrorUpsert —
// T3.1-REVOKE-03-snapshot: removing a Snapshot label on Update (labels in mask)
// must emit an fga.register (mirror.upsert) intent carrying the CURRENT (now empty)
// labels, in the SAME writer-tx as the UPDATE — NOT an unregister. RED before
// snapshot_repo.go Update emits a labels-gated register intent
// (compute.snapshot Update-on-label-change does NOT refresh the IAM resource_mirror).
func TestSnapshotRepo_T31Revoke03Snapshot_LabelRemoveEmitsMirrorUpsert(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	snapRepo := repo.NewSnapshotRepo(pool)
	snapID := ids.NewID(ids.PrefixSnapshot)
	projectID := "proj-snap-revoke3ab"
	created, err := snapRepo.Insert(ctx, newTestSnapshot(snapID, projectID, map[string]string{"tier": "treska"}))
	require.NoError(t, err)

	regsAfterCreate := registerIntents(queryFGARegisterRows(ctx, t, pool, snapID))
	require.Len(t, regsAfterCreate, 1, "Create emits one register intent")
	assert.Equal(t, map[string]string{"tier": "treska"}, payloadMirror(t, regsAfterCreate[0].payload).Labels)

	created.Labels = map[string]string{}
	_, err = snapRepo.Update(ctx, created, true)
	require.NoError(t, err)

	regs := registerIntents(queryFGARegisterRows(ctx, t, pool, snapID))
	require.Len(t, regs, 2, "Create + Update-on-labels both emit a register intent (mirror.upsert, not unregister)")

	last := payloadMirror(t, regs[len(regs)-1].payload)
	assert.Empty(t, last.Labels, "label-remove emits mirror.upsert with empty labels (G-3)")
	assert.Equal(t, projectID, last.ParentProjectID, "parent_project_id preserved")
	require.Len(t, last.Tuples, 1)
	assert.Equal(t, "compute_snapshot:"+snapID, last.Tuples[0].Object, "owner-tuple stays — upsert, NOT unregister (G-3)")

	v1 := payloadMirror(t, regs[0].payload).SourceVersion
	v2 := last.SourceVersion
	require.False(t, v2.IsZero(), "Update intent stamps a source_version")
	require.True(t, v2.After(v1) || v2.Equal(v1), "monotonic source_version: Update (%s) >= Create (%s)", v2, v1)

	for _, r := range queryFGARegisterRows(ctx, t, pool, snapID) {
		assert.NotEqual(t, fgaintent.EventUnregister, r.eventType, "label-remove must NOT emit an unregister")
	}
}

// TestSnapshotRepo_T31Idm03Snapshot_NonLabelUpdateNoEmit — G-2: a non-label
// Snapshot Update (emitLabelsRegister = false) must NOT emit an extra register intent.
func TestSnapshotRepo_T31Idm03Snapshot_NonLabelUpdateNoEmit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	snapRepo := repo.NewSnapshotRepo(pool)
	snapID := ids.NewID(ids.PrefixSnapshot)
	projectID := "proj-snap-idm03aaa"
	created, err := snapRepo.Insert(ctx, newTestSnapshot(snapID, projectID, map[string]string{"tier": "treska"}))
	require.NoError(t, err)

	created.Name = "snap-renamed"
	_, err = snapRepo.Update(ctx, created, false)
	require.NoError(t, err)

	regs := registerIntents(queryFGARegisterRows(ctx, t, pool, snapID))
	assert.Len(t, regs, 1, "non-label Update must NOT emit a register intent (G-2)")
}
