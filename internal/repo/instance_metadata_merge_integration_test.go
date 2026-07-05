// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package repo_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/ids"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/repo"
)

// seedInstanceForMetadata вставляет один Instance с заданной начальной metadata.
func seedInstanceForMetadata(t *testing.T, ctx context.Context, instRepo *repo.InstanceRepo, md map[string]string) string {
	t.Helper()
	inID := ids.NewID(ids.PrefixInstance)
	_, err := instRepo.Insert(ctx, &domain.Instance{
		ID: inID, ProjectID: "f-md", CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		ZoneID: "ru-central1-a", PlatformID: "standard-v3", Cores: 2, Memory: 2 << 30, CoreFraction: 100,
		Status: domain.InstanceStatusRunning, FQDN: inID + ".auto.internal", NetworkSettingsType: "STANDARD",
		Metadata:          md,
		NetworkInterfaces: []domain.NetworkInterface{{Index: "0", SubnetID: "e9bsub", PrimaryV4Address: "10.0.0.10"}},
	}, nil)
	require.NoError(t, err)
	return inID
}

// TestIntegration_MergeMetadata_DeleteUpsert — базовая семантика атомарного
// merge: удаляет перечисленные ключи, вставляет/перезаписывает upsert-ключи,
// остальные сохраняет.
func TestIntegration_MergeMetadata_DeleteUpsert(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	instRepo := repo.NewInstanceRepo(pool)
	inID := seedInstanceForMetadata(t, ctx, instRepo, map[string]string{"keep": "1", "drop": "2", "over": "old"})

	got, err := instRepo.MergeMetadata(ctx, inID, []string{"drop"}, map[string]string{"over": "new", "add": "3"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"keep": "1", "over": "new", "add": "3"}, got.Metadata)
}

// TestIntegration_MergeMetadata_ConcurrentNoLostUpdate — N конкурентных
// MergeMetadata с непересекающимися upsert-ключами: каждый ключ обязан выжить.
//
// Регрессия до фикса (project-rule 10): service читал metadata через Get,
// мёржил дельту в Go и звал SetMetadata с безусловным full-map overwrite — два
// конкурентных UpdateMetadata читали одну базовую map, второй коммит затирал
// дельту первого (second-writer-wins / lost update). Атомарный single-statement
// `metadata = (metadata - $del) || $upsert` сериализуется row-level-lock'ом →
// потерь нет. Это RED-before/GREEN-after для finding «UpdateInstanceMetadata
// non-atomic read-modify-write merge».
func TestIntegration_MergeMetadata_ConcurrentNoLostUpdate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	instRepo := repo.NewInstanceRepo(pool)
	inID := seedInstanceForMetadata(t, ctx, instRepo, map[string]string{"base": "0"})

	const N = 16
	var wg sync.WaitGroup
	startBarrier := make(chan struct{})
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-startBarrier
			key := fmt.Sprintf("k%02d", i)
			_, errs[i] = instRepo.MergeMetadata(ctx, inID, nil, map[string]string{key: fmt.Sprintf("v%02d", i)})
		}(i)
	}
	close(startBarrier)
	wg.Wait()

	for i, e := range errs {
		require.NoError(t, e, "MergeMetadata goroutine %d failed", i)
	}

	got, err := instRepo.Get(ctx, inID)
	require.NoError(t, err)
	// base + все N disjoint-ключей должны присутствовать (никакой lost update).
	assert.Equal(t, "0", got.Metadata["base"], "pre-existing key must survive")
	for i := 0; i < N; i++ {
		key := fmt.Sprintf("k%02d", i)
		assert.Equalf(t, fmt.Sprintf("v%02d", i), got.Metadata[key],
			"concurrent upsert %s was lost (lost-update regression)", key)
	}
	assert.Len(t, got.Metadata, N+1, "exactly base + N keys, no clobber")
}
