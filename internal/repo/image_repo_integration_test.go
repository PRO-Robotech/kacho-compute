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

func newTestImage(id, projectID string, labels map[string]string) *domain.Image {
	return &domain.Image{
		ID: id, ProjectID: projectID, CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		Name: "img-" + id[len(id)-4:], Labels: labels, Family: "ubuntu", StorageSize: 4194304,
		MinDiskSize: 4194304, Status: domain.ImageStatusReady,
	}
}

// TestImageRepo_T31Revoke03Image_LabelRemoveEmitsMirrorUpsert — T3.1-REVOKE-03-image:
// removing an Image label on Update (labels in mask) must emit an fga.register
// (mirror.upsert) intent carrying the CURRENT (now empty) labels, in the SAME
// writer-tx as the UPDATE — NOT an unregister. RED before image_repo.go Update
// emits a labels-gated register intent (compute.image Update-on-label-change
// does NOT refresh the IAM resource_mirror).
func TestImageRepo_T31Revoke03Image_LabelRemoveEmitsMirrorUpsert(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	imgRepo := repo.NewImageRepo(pool)
	imgID := ids.NewID(ids.PrefixImage)
	projectID := "proj-img-revoke03ab"
	created, err := imgRepo.Insert(ctx, newTestImage(imgID, projectID, map[string]string{"tier": "treska"}))
	require.NoError(t, err)

	regsAfterCreate := registerIntents(queryFGARegisterRows(ctx, t, pool, imgID))
	require.Len(t, regsAfterCreate, 1, "Create emits one register intent")
	assert.Equal(t, map[string]string{"tier": "treska"}, payloadMirror(t, regsAfterCreate[0].payload).Labels)

	created.Labels = map[string]string{}
	_, err = imgRepo.Update(ctx, created, true)
	require.NoError(t, err)

	regs := registerIntents(queryFGARegisterRows(ctx, t, pool, imgID))
	require.Len(t, regs, 2, "Create + Update-on-labels both emit a register intent (mirror.upsert, not unregister)")

	last := payloadMirror(t, regs[len(regs)-1].payload)
	assert.Empty(t, last.Labels, "label-remove emits mirror.upsert with empty labels (G-3)")
	assert.Equal(t, projectID, last.ParentProjectID, "parent_project_id preserved")
	require.Len(t, last.Tuples, 1)
	assert.Equal(t, "compute_image:"+imgID, last.Tuples[0].Object, "owner-tuple stays — upsert, NOT unregister (G-3)")

	v1 := payloadMirror(t, regs[0].payload).SourceVersion
	v2 := last.SourceVersion
	require.False(t, v2.IsZero(), "Update intent stamps a source_version")
	require.True(t, v2.After(v1) || v2.Equal(v1), "monotonic source_version: Update (%s) >= Create (%s)", v2, v1)

	for _, r := range queryFGARegisterRows(ctx, t, pool, imgID) {
		assert.NotEqual(t, fgaintent.EventUnregister, r.eventType, "label-remove must NOT emit an unregister")
	}
}

// TestImageRepo_T31Idm03Image_NonLabelUpdateNoEmit — G-2: a non-label Image Update
// (emitLabelsRegister = false) must NOT emit an extra register intent.
func TestImageRepo_T31Idm03Image_NonLabelUpdateNoEmit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	imgRepo := repo.NewImageRepo(pool)
	imgID := ids.NewID(ids.PrefixImage)
	projectID := "proj-img-idm03aaaa"
	created, err := imgRepo.Insert(ctx, newTestImage(imgID, projectID, map[string]string{"tier": "treska"}))
	require.NoError(t, err)

	created.Name = "img-renamed"
	_, err = imgRepo.Update(ctx, created, false)
	require.NoError(t, err)

	regs := registerIntents(queryFGARegisterRows(ctx, t, pool, imgID))
	assert.Len(t, regs, 1, "non-label Update must NOT emit a register intent (G-2)")
}
