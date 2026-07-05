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

// registerIntents filters compute_fga_register_outbox rows down to fga.register
// events (drops unregister), preserving id-order (used by the T3.1 label-revoke
// integration tests across disk/image/snapshot).
func registerIntents(rows []fgaRegisterRow) []fgaRegisterRow {
	var out []fgaRegisterRow
	for _, r := range rows {
		if r.eventType == fgaintent.EventRegister {
			out = append(out, r)
		}
	}
	return out
}

func newTestDisk(id, projectID string, labels map[string]string) *domain.Disk {
	return &domain.Disk{
		ID: id, ProjectID: projectID, CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		Name: "disk-" + id[len(id)-4:], Labels: labels, TypeID: "network-ssd", ZoneID: "ru-central1-a",
		Size: 4194304, BlockSize: 4096, Status: domain.DiskStatusReady,
	}
}

// TestDiskRepo_T31Revoke03Disk_LabelRemoveEmitsMirrorUpsert — T3.1-REVOKE-03-disk:
// removing a Disk label on Update (labels in mask) must emit an fga.register
// (mirror.upsert) intent carrying the CURRENT (now empty) labels, in the SAME
// writer-tx as the UPDATE — NOT an unregister (G-3: the resource still exists, the
// mirror row must stay with labels={} so ARM_LABELS selectors stop matching while
// the owner-tuple/containment survive). RED before disk_repo.go Update emits a
// labels-gated register intent (the Update signature has no emitLabelsRegister flag
// yet — compute.disk Update-on-label-change does NOT refresh the mirror).
func TestDiskRepo_T31Revoke03Disk_LabelRemoveEmitsMirrorUpsert(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	diskRepo := repo.NewDiskRepo(pool)
	dID := ids.NewID(ids.PrefixDisk)
	projectID := "proj-disk-revoke03aa"
	created, err := diskRepo.Insert(ctx, newTestDisk(dID, projectID, map[string]string{"tier": "treska"}))
	require.NoError(t, err)

	// Sanity: Create already feeds labels (disk has no bare-create bug).
	regsAfterCreate := registerIntents(queryFGARegisterRows(ctx, t, pool, dID))
	require.Len(t, regsAfterCreate, 1, "Create emits one register intent")
	assert.Equal(t, map[string]string{"tier": "treska"}, payloadMirror(t, regsAfterCreate[0].payload).Labels)

	// Remove the label on Update (labels in mask → emitLabelsRegister = true).
	created.Labels = map[string]string{}
	_, err = diskRepo.Update(ctx, created, true, []string{"labels"})
	require.NoError(t, err)

	regs := registerIntents(queryFGARegisterRows(ctx, t, pool, dID))
	require.Len(t, regs, 2, "Create + Update-on-labels both emit a register intent (mirror.upsert, not unregister)")

	last := payloadMirror(t, regs[len(regs)-1].payload)
	assert.Empty(t, last.Labels, "label-remove emits mirror.upsert with empty labels (G-3)")
	assert.Equal(t, projectID, last.ParentProjectID, "parent_project_id preserved")
	require.Len(t, last.Tuples, 1)
	assert.Equal(t, "compute_disk:"+dID, last.Tuples[0].Object, "owner-tuple stays — upsert, NOT unregister (G-3)")

	// Monotonic source_version: Update intent >= Create intent.
	v1 := payloadMirror(t, regs[0].payload).SourceVersion
	v2 := last.SourceVersion
	require.False(t, v2.IsZero(), "Update intent stamps a source_version")
	require.True(t, v2.After(v1) || v2.Equal(v1), "monotonic source_version: Update (%s) >= Create (%s)", v2, v1)

	// No unregister intent leaked (G-3: live resource is never UnregisterResource'd).
	for _, r := range queryFGARegisterRows(ctx, t, pool, dID) {
		assert.NotEqual(t, fgaintent.EventUnregister, r.eventType, "label-remove must NOT emit an unregister")
	}
}

// TestDiskRepo_T31Idm03Disk_NonLabelUpdateNoEmit — G-2: a non-label Disk Update
// (name only, labels NOT in mask → emitLabelsRegister = false) must NOT emit an
// extra register intent (no reconcile churn). RED until disk Update is labels-gated.
func TestDiskRepo_T31Idm03Disk_NonLabelUpdateNoEmit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	diskRepo := repo.NewDiskRepo(pool)
	dID := ids.NewID(ids.PrefixDisk)
	projectID := "proj-disk-idm03aaaa"
	created, err := diskRepo.Insert(ctx, newTestDisk(dID, projectID, map[string]string{"tier": "treska"}))
	require.NoError(t, err)

	created.Name = "disk-renamed"
	_, err = diskRepo.Update(ctx, created, false, []string{"name"})
	require.NoError(t, err)

	regs := registerIntents(queryFGARegisterRows(ctx, t, pool, dID))
	assert.Len(t, regs, 1, "non-label Update must NOT emit a register intent (G-2)")
}
