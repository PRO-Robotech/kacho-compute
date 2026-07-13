// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package repo_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/ids"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/repo"
)

// TestIntegration_InstanceCPUGuaranteeImage_RoundTrip — S5-03/05: the new columns
// cpu_guarantee_percent / image / image_digest round-trip through Insert→Get, and a
// resize (resources_spec) on a STOPPED instance rewrites cpu_guarantee_percent under
// the same STOPPED CAS as cores/memory (sizing). image is mutable in any status.
func TestIntegration_InstanceCPUGuaranteeImage_RoundTrip(t *testing.T) {
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

	in := newRunningInstance(inID)
	in.CPUGuaranteePercent = 50
	in.Image = "cr.kacho.cloud/library/ubuntu:24.04"
	_, err = instRepo.Insert(ctx, in)
	require.NoError(t, err)

	got, err := instRepo.Get(ctx, inID)
	require.NoError(t, err)
	assert.Equal(t, int32(50), got.CPUGuaranteePercent)
	assert.Equal(t, "cr.kacho.cloud/library/ubuntu:24.04", got.Image)
	assert.Empty(t, got.ImageDigest, "image_digest defaults to '' (registry-resolve deferred)")

	// image re-pin is allowed while RUNNING (not sizing).
	got.Image = "cr.kacho.cloud/library/debian:12"
	_, err = instRepo.Update(ctx, got, false, []string{"image"})
	require.NoError(t, err)

	// resize incl. cpu_guarantee_percent requires STOPPED.
	stopped, err := instRepo.SetStatusCAS(ctx, inID, domain.InstanceStatusRunning, domain.InstanceStatusStopped)
	require.NoError(t, err)
	stopped.Cores = 4
	stopped.Memory = 4 << 30
	stopped.CPUGuaranteePercent = 20
	resized, err := instRepo.Update(ctx, stopped, false, []string{"resources_spec"})
	require.NoError(t, err)
	assert.Equal(t, int32(20), resized.CPUGuaranteePercent)
	assert.Equal(t, int64(4), resized.Cores)
	assert.Equal(t, "cr.kacho.cloud/library/debian:12", resized.Image, "image persisted across resize")
}

// TestIntegration_InstanceCPUGuarantee_CheckConstraint — the DB CHECK (0..100)
// rejects an out-of-range cpu_guarantee_percent at Insert (defence-in-depth beyond
// the service-layer validation).
func TestIntegration_InstanceCPUGuarantee_CheckConstraint(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	instRepo := repo.NewInstanceRepo(pool)
	in := newRunningInstance(ids.NewID(ids.PrefixInstance))
	in.CPUGuaranteePercent = 101 // violates CHECK (cpu_guarantee_percent BETWEEN 0 AND 100)
	_, err = instRepo.Insert(ctx, in)
	require.Error(t, err, "cpu_guarantee_percent=101 must be rejected by the DB CHECK")
}
