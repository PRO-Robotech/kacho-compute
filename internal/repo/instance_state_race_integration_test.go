// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package repo_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
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

// TestIntegration_InstanceSetStatusCAS_ConcurrentStopOnStopped: ВМ в STOPPED;
// 5 concurrent Stop (CAS RUNNING → STOPPED) — все 5 должны получить
// ErrFailedPrecondition, т.к. instance не в RUNNING. Закрывает TOCTOU-гонку
// `Get → check status → SetStatus` (software check-then-act).
func TestIntegration_InstanceSetStatusCAS_ConcurrentStopOnStopped(t *testing.T) {
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
	in := &domain.Instance{
		ID: inID, ProjectID: "f-state-race", CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		ZoneID: "ru-central1-a", PlatformID: "standard-v3", Cores: 2, Memory: 2 << 30, CoreFraction: 100,
		Status: domain.InstanceStatusStopped, // <-- начальный state: STOPPED
		FQDN:   inID + ".auto.internal", NetworkSettingsType: "STANDARD",
	}
	_, err = instRepo.Insert(ctx, in, nil)
	require.NoError(t, err)

	const N = 5
	var (
		wg           sync.WaitGroup
		successCnt   atomic.Int32
		preCondCnt   atomic.Int32
		otherErrs    []error
		otherErrsMu  sync.Mutex
		startBarrier = make(chan struct{})
	)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startBarrier
			// Stop = CAS RUNNING → STOPPED. Текущий state — STOPPED → 0 rows → FailedPrecondition.
			_, err := instRepo.SetStatusCAS(ctx, inID,
				domain.InstanceStatusRunning, domain.InstanceStatusStopped)
			if err == nil {
				successCnt.Add(1)
				return
			}
			if errors.Is(err, service.ErrFailedPrecondition) {
				preCondCnt.Add(1)
				return
			}
			otherErrsMu.Lock()
			otherErrs = append(otherErrs, err)
			otherErrsMu.Unlock()
		}()
	}
	close(startBarrier)
	wg.Wait()

	assert.Equal(t, int32(0), successCnt.Load(), "no CAS should win — instance is not in RUNNING")
	assert.Equal(t, int32(N), preCondCnt.Load(), "all %d concurrent Stops should return FailedPrecondition", N)
	assert.Empty(t, otherErrs, "no other (non-FailedPrecondition) errors expected")

	// Final state — всё ещё STOPPED.
	got, err := instRepo.Get(ctx, inID)
	require.NoError(t, err)
	assert.Equal(t, domain.InstanceStatusStopped, got.Status)
}

// TestIntegration_InstanceSetStatusCAS_ConcurrentRestartOnRunning: ВМ в
// RUNNING; 5 concurrent CAS RUNNING → RESTARTING. Postgres row-level lock
// сериализует UPDATE-ы: первый writer переводит state в RESTARTING, остальные
// 4 после ожидания commit'а видят RESTARTING, WHERE не matches, 0 rows →
// ErrFailedPrecondition.
func TestIntegration_InstanceSetStatusCAS_ConcurrentRestartOnRunning(t *testing.T) {
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
	in := &domain.Instance{
		ID: inID, ProjectID: "f-state-race", CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		ZoneID: "ru-central1-a", PlatformID: "standard-v3", Cores: 2, Memory: 2 << 30, CoreFraction: 100,
		Status: domain.InstanceStatusRunning, // <-- начальный state: RUNNING
		FQDN:   inID + ".auto.internal", NetworkSettingsType: "STANDARD",
	}
	_, err = instRepo.Insert(ctx, in, nil)
	require.NoError(t, err)

	const N = 5
	var (
		wg           sync.WaitGroup
		successCnt   atomic.Int32
		preCondCnt   atomic.Int32
		otherErrs    []error
		otherErrsMu  sync.Mutex
		startBarrier = make(chan struct{})
	)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startBarrier
			// Шаг 1 Restart-flow: CAS RUNNING → RESTARTING. Concurrent: row-level lock
			// сериализует; ровно один winning UPDATE, остальные после ожидания commit'а
			// первого видят RESTARTING → 0 rows → FailedPrecondition.
			_, err := instRepo.SetStatusCAS(ctx, inID,
				domain.InstanceStatusRunning, domain.InstanceStatusRestarting)
			if err == nil {
				successCnt.Add(1)
				return
			}
			if errors.Is(err, service.ErrFailedPrecondition) {
				preCondCnt.Add(1)
				return
			}
			otherErrsMu.Lock()
			otherErrs = append(otherErrs, err)
			otherErrsMu.Unlock()
		}()
	}
	close(startBarrier)
	wg.Wait()

	assert.Equal(t, int32(1), successCnt.Load(), "exactly one CAS should win (RUNNING→RESTARTING)")
	assert.Equal(t, int32(N-1), preCondCnt.Load(), "remaining %d concurrent Restarts should return FailedPrecondition", N-1)
	assert.Empty(t, otherErrs, "no other (non-FailedPrecondition) errors expected")

	// Final state — RESTARTING (worker step 2 в service-слое переведёт обратно в
	// RUNNING; здесь мы тестируем только repo-CAS, поэтому остаётся RESTARTING).
	got, err := instRepo.Get(ctx, inID)
	require.NoError(t, err)
	assert.Equal(t, domain.InstanceStatusRestarting, got.Status)
}

// TestIntegration_InstanceSetStatusCAS_StopRestartRace: ВМ в RUNNING; одна
// goroutine делает Stop (CAS RUNNING→STOPPED), другая — Restart-step-1 (CAS
// RUNNING→RESTARTING). Один winner, второй — FailedPrecondition; финальный
// state — что у winner'а (не second-writer-wins / lost-state). Парирует
// race-сценарий: Get→check OK на обеих goroutine-ах, потом unconditional
// UPDATE — стерильный second-writer-wins.
func TestIntegration_InstanceSetStatusCAS_StopRestartRace(t *testing.T) {
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
	in := &domain.Instance{
		ID: inID, ProjectID: "f-state-race", CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		ZoneID: "ru-central1-a", PlatformID: "standard-v3", Cores: 2, Memory: 2 << 30, CoreFraction: 100,
		Status: domain.InstanceStatusRunning,
		FQDN:   inID + ".auto.internal", NetworkSettingsType: "STANDARD",
	}
	_, err = instRepo.Insert(ctx, in, nil)
	require.NoError(t, err)

	var (
		wg           sync.WaitGroup
		stopErr      error
		restartErr   error
		stopFinal    *domain.Instance
		restartFinal *domain.Instance
		startBarrier = make(chan struct{})
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-startBarrier
		stopFinal, stopErr = instRepo.SetStatusCAS(ctx, inID, domain.InstanceStatusRunning, domain.InstanceStatusStopped)
	}()
	go func() {
		defer wg.Done()
		<-startBarrier
		restartFinal, restartErr = instRepo.SetStatusCAS(ctx, inID, domain.InstanceStatusRunning, domain.InstanceStatusRestarting)
	}()
	close(startBarrier)
	wg.Wait()

	stopWon := stopErr == nil
	restartWon := restartErr == nil
	assert.True(t, stopWon != restartWon,
		"exactly one of Stop/Restart should win (stopWon=%v restartWon=%v stopErr=%v restartErr=%v)",
		stopWon, restartWon, stopErr, restartErr)

	got, err := instRepo.Get(ctx, inID)
	require.NoError(t, err)
	if stopWon {
		assert.ErrorIs(t, restartErr, service.ErrFailedPrecondition)
		require.NotNil(t, stopFinal)
		assert.Equal(t, domain.InstanceStatusStopped, stopFinal.Status)
		assert.Equal(t, domain.InstanceStatusStopped, got.Status, "final DB state must match winner (Stop)")
	} else {
		assert.ErrorIs(t, stopErr, service.ErrFailedPrecondition)
		require.NotNil(t, restartFinal)
		assert.Equal(t, domain.InstanceStatusRestarting, restartFinal.Status)
		assert.Equal(t, domain.InstanceStatusRestarting, got.Status, "final DB state must match winner (Restart)")
	}
}

// TestIntegration_InstanceSetStatusCAS_NotFound: SetStatusCAS на
// несуществующий instance возвращает ErrNotFound (не FailedPrecondition).
// Отдельный кейс важен, потому что в SetStatusCAS «0 rows» может означать
// либо «instance не существует», либо «status != expected»; они должны
// различаться корректно.
func TestIntegration_InstanceSetStatusCAS_NotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	instRepo := repo.NewInstanceRepo(pool)
	_, err = instRepo.SetStatusCAS(ctx, "epdNONEXISTENT0000000",
		domain.InstanceStatusRunning, domain.InstanceStatusStopped)
	require.ErrorIs(t, err, service.ErrNotFound)
}
