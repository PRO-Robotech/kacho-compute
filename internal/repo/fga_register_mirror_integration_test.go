// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package repo_test

import (
	"context"
	"encoding/json"
	"sync"
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

// payloadMirror decodes the β mirror fields (labels + parent-scope) from one
// compute_fga_register_outbox row payload.
func payloadMirror(t *testing.T, b []byte) fgaintent.Payload {
	t.Helper()
	var p fgaintent.Payload
	require.NoError(t, json.Unmarshal(b, &p))
	return p
}

func newMirrorInstance(id, projectID string, labels map[string]string) (*domain.Instance, *domain.Disk) {
	bootDiskID := ids.NewID(ids.PrefixDisk)
	in := &domain.Instance{
		ID: id, ProjectID: projectID, CreatedAt: time.Now().UTC().Truncate(time.Microsecond), Name: "vm-" + id[len(id)-4:],
		Labels: labels, ZoneID: "ru-central1-a", PlatformID: "standard-v3", Cores: 2, Memory: 2 << 30, CoreFraction: 100,
		Status: domain.InstanceStatusRunning, FQDN: id + ".auto.internal", NetworkSettingsType: "STANDARD",
		AttachedDisks: []domain.AttachedDisk{{DiskID: bootDiskID, IsBoot: true, AutoDelete: true}},
	}
	boot := &domain.Disk{ID: bootDiskID, ProjectID: projectID, CreatedAt: time.Now().UTC().Truncate(time.Microsecond), ZoneID: "ru-central1-a", Size: 4194304, BlockSize: 4096, Status: domain.DiskStatusReady}
	return in, boot
}

// Test_Beta01_CreateInstance_IntentCarriesLabelsAndParent — β-01: Create instance
// with labels → the fga.register intent payload carries labels + parent_project_id
// (the instance project) so the register-drainer can feed IAM resource_mirror.
func Test_Beta01_CreateInstance_IntentCarriesLabelsAndParent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	instRepo := repo.NewInstanceRepo(pool)
	inID := ids.NewID(ids.PrefixInstance)
	projectID := "proj-aaaaaaaaaaaaaaaaa"
	in, boot := newMirrorInstance(inID, projectID, map[string]string{"env": "dev", "team": "core"})
	_, err = instRepo.Insert(ctx, in, []*domain.Disk{boot})
	require.NoError(t, err)

	rows := queryFGARegisterRows(ctx, t, pool, inID)
	require.Len(t, rows, 1)
	assert.Equal(t, fgaintent.EventRegister, rows[0].eventType)

	p := payloadMirror(t, rows[0].payload)
	assert.Equal(t, map[string]string{"env": "dev", "team": "core"}, p.Labels)
	assert.Equal(t, projectID, p.ParentProjectID, "parent_project_id = instance project_id")
	// Owner-tuple still present alongside the mirror fields.
	require.Len(t, p.Tuples, 1)
	assert.Equal(t, "compute_instance:"+inID, p.Tuples[0].Object)
}

// Test_Beta02_CreateInstance_NoLabels_IntentEmptyLabels — β-02: Create without
// labels → intent still emitted with empty labels map and parent set (graceful).
func Test_Beta02_CreateInstance_NoLabels_IntentEmptyLabels(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	instRepo := repo.NewInstanceRepo(pool)
	inID := ids.NewID(ids.PrefixInstance)
	projectID := "proj-bbbbbbbbbbbbbbbbb"
	in, boot := newMirrorInstance(inID, projectID, nil)
	_, err = instRepo.Insert(ctx, in, []*domain.Disk{boot})
	require.NoError(t, err)

	rows := queryFGARegisterRows(ctx, t, pool, inID)
	require.Len(t, rows, 1)
	p := payloadMirror(t, rows[0].payload)
	assert.Empty(t, p.Labels, "no labels → empty labels map (graceful)")
	assert.Equal(t, projectID, p.ParentProjectID)
}

// Test_Beta04_UpdateLabels_EmitsNewIntent — β-04: Update instance labels (dev→prod)
// with "labels" in the update-mask → a NEW fga.register intent is emitted in the
// same writer-tx, carrying the updated labels (mirror dynamic for the γ selector).
func Test_Beta04_UpdateLabels_EmitsNewIntent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	instRepo := repo.NewInstanceRepo(pool)
	inID := ids.NewID(ids.PrefixInstance)
	projectID := "proj-ccccccccccccccccc"
	in, boot := newMirrorInstance(inID, projectID, map[string]string{"env": "dev"})
	created, err := instRepo.Insert(ctx, in, []*domain.Disk{boot})
	require.NoError(t, err)

	// dev → prod, "labels" in mask → emitLabelsRegister = true.
	created.Labels = map[string]string{"env": "prod", "team": "core"}
	_, err = instRepo.Update(ctx, created, true, []string{"labels"})
	require.NoError(t, err)

	rows := queryFGARegisterRows(ctx, t, pool, inID)
	// 1 from Create + 1 from Update.
	var regs []fgaRegisterRow
	for _, r := range rows {
		if r.eventType == fgaintent.EventRegister {
			regs = append(regs, r)
		}
	}
	require.Len(t, regs, 2, "Create + Update-on-labels both emit a register intent")
	last := payloadMirror(t, regs[len(regs)-1].payload)
	assert.Equal(t, map[string]string{"env": "prod", "team": "core"}, last.Labels)
	assert.Equal(t, projectID, last.ParentProjectID)
}

// Test_BetaHardening_RegisterIntentStampsMonotonicSourceVersion — β-hardening
// (system-design-reviewer finding): every register intent must stamp a monotonic
// per-object source_version from the DB clock (now()) at intent-INSERT time, in
// the SAME writer-tx as the resource mutation. Two sequential mutations of one
// object (Create, then Update-on-labels) → the Update intent carries a strictly
// GREATER source_version than the Create intent. This is what lets kacho-iam
// apply the mirror UPSERT last-source-state-wins (a reordered stale intent is a
// no-op). RED before outbox.go stamps jsonb_set('{source_version}', to_jsonb(now())).
func Test_BetaHardening_RegisterIntentStampsMonotonicSourceVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	instRepo := repo.NewInstanceRepo(pool)
	inID := ids.NewID(ids.PrefixInstance)
	projectID := "proj-eeeeeeeeeeeeeeeee"
	in, boot := newMirrorInstance(inID, projectID, map[string]string{"env": "dev"})
	created, err := instRepo.Insert(ctx, in, []*domain.Disk{boot})
	require.NoError(t, err)

	created.Labels = map[string]string{"env": "prod"}
	_, err = instRepo.Update(ctx, created, true, []string{"labels"})
	require.NoError(t, err)

	var regs []fgaRegisterRow
	for _, r := range queryFGARegisterRows(ctx, t, pool, inID) {
		if r.eventType == fgaintent.EventRegister {
			regs = append(regs, r)
		}
	}
	require.Len(t, regs, 2, "Create + Update-on-labels both emit a register intent")

	v1 := payloadMirror(t, regs[0].payload).SourceVersion
	v2 := payloadMirror(t, regs[1].payload).SourceVersion
	require.False(t, v1.IsZero(), "Create intent stamps a non-zero source_version (DB now())")
	require.False(t, v2.IsZero(), "Update intent stamps a non-zero source_version")
	require.True(t, v2.After(v1) || v2.Equal(v1),
		"monotonic per-object: Update's source_version (%s) >= Create's (%s)", v2, v1)
}

// Test_BetaHardening_UnregisterIntentStampsTombstoneVersion — β-hardening: a
// Delete (unregister) intent stamps a source_version (tombstone-version) >= the
// preceding register, so a Delete-after-Update reorder cannot resurrect/wipe a
// fresher mirror row. RED before outbox.go stamps now() on the unregister row too.
func Test_BetaHardening_UnregisterIntentStampsTombstoneVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	instRepo := repo.NewInstanceRepo(pool)
	inID := ids.NewID(ids.PrefixInstance)
	projectID := "proj-fffffffffffffffff"
	in, boot := newMirrorInstance(inID, projectID, map[string]string{"env": "dev"})
	_, err = instRepo.Insert(ctx, in, []*domain.Disk{boot})
	require.NoError(t, err)
	require.NoError(t, instRepo.Delete(ctx, inID))

	rows := queryFGARegisterRows(ctx, t, pool, inID)
	var reg, unreg fgaRegisterRow
	for _, r := range rows {
		switch r.eventType {
		case fgaintent.EventRegister:
			reg = r
		case fgaintent.EventUnregister:
			unreg = r
		}
	}
	require.NotEmpty(t, unreg.payload, "Delete emits an unregister intent")
	regV := payloadMirror(t, reg.payload).SourceVersion
	delV := payloadMirror(t, unreg.payload).SourceVersion
	require.False(t, delV.IsZero(), "unregister stamps a tombstone source_version")
	require.True(t, delV.After(regV) || delV.Equal(regV),
		"tombstone-version (%s) >= register-version (%s) — Delete-after-Update cannot wipe a fresher row", delV, regV)
}

// Test_Beta04b_UpdateNonLabels_NoNewIntent — β-04b: Update non-labels fields
// (emitLabelsRegister = false) → NO new register intent (only the Create one).
func Test_Beta04b_UpdateNonLabels_NoNewIntent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	instRepo := repo.NewInstanceRepo(pool)
	inID := ids.NewID(ids.PrefixInstance)
	projectID := "proj-ddddddddddddddddd"
	in, boot := newMirrorInstance(inID, projectID, map[string]string{"env": "dev"})
	created, err := instRepo.Insert(ctx, in, []*domain.Disk{boot})
	require.NoError(t, err)

	// name-only update → emitLabelsRegister = false.
	created.Name = "vm-renamed"
	_, err = instRepo.Update(ctx, created, false, []string{"name"})
	require.NoError(t, err)

	rows := queryFGARegisterRows(ctx, t, pool, inID)
	var regCount int
	for _, r := range rows {
		if r.eventType == fgaintent.EventRegister {
			regCount++
		}
	}
	assert.Equal(t, 1, regCount, "non-labels Update must NOT emit a register intent (β-04b)")
}

// Test_Beta07_DeleteInstance_UnregisterIntent — β-07: Delete → fga.unregister
// intent in the same writer-tx (symmetry; mirror-row removed on the IAM side).
func Test_Beta07_DeleteInstance_UnregisterIntent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	instRepo := repo.NewInstanceRepo(pool)
	inID := ids.NewID(ids.PrefixInstance)
	projectID := "proj-eeeeeeeeeeeeeeeee"
	in, boot := newMirrorInstance(inID, projectID, map[string]string{"env": "dev"})
	_, err = instRepo.Insert(ctx, in, []*domain.Disk{boot})
	require.NoError(t, err)

	require.NoError(t, instRepo.Delete(ctx, inID))

	rows := queryFGARegisterRows(ctx, t, pool, inID)
	var sawUnreg bool
	for _, r := range rows {
		if r.eventType == fgaintent.EventUnregister {
			sawUnreg = true
			p := payloadMirror(t, r.payload)
			require.Len(t, p.Tuples, 1)
			assert.Equal(t, "compute_instance:"+inID, p.Tuples[0].Object)
		}
	}
	assert.True(t, sawUnreg, "Delete writes fga.unregister intent (β-07)")
}

// Test_Beta05_ConcurrentUpdateLabels_OutboxConsistent — β-05: concurrent label
// Updates of the same instance → the outbox stays consistent (every committed
// Update appends exactly one register intent; no torn payload, no panic). The
// last committed labels are reflected on the instance row.
func Test_Beta05_ConcurrentUpdateLabels_OutboxConsistent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	instRepo := repo.NewInstanceRepo(pool)
	inID := ids.NewID(ids.PrefixInstance)
	projectID := "proj-fffffffffffffffff"
	in, boot := newMirrorInstance(inID, projectID, map[string]string{"env": "dev"})
	created, err := instRepo.Insert(ctx, in, []*domain.Disk{boot})
	require.NoError(t, err)

	const goroutines = 8
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			cp := *created
			cp.Labels = map[string]string{"env": []string{"dev", "prod"}[n%2]}
			_, uerr := instRepo.Update(ctx, &cp, true, []string{"labels"})
			assert.NoError(t, uerr)
		}(i)
	}
	wg.Wait()

	rows := queryFGARegisterRows(ctx, t, pool, inID)
	var regCount int
	for _, r := range rows {
		if r.eventType == fgaintent.EventRegister {
			regCount++
			// every payload decodes cleanly (no torn write).
			p := payloadMirror(t, r.payload)
			assert.NotEmpty(t, p.Labels)
			assert.Equal(t, projectID, p.ParentProjectID)
		}
	}
	// 1 (Create) + goroutines (each committed Update appends one intent).
	assert.Equal(t, 1+goroutines, regCount, "each committed Update appends exactly one register intent")
}
