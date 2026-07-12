// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/ports/portmock"
)

// TestInstance_Create_CPUGuarantee_Bounds — S5-03: cpu_guarantee_percent валиден в
// [0,100]. 0 (best-effort) и 100 (граница) проходят; 101 (вне диапазона) → sync
// InvalidArgument "Illegal argument cpu_guarantee_percent" (контракт api-conventions).
func TestInstance_Create_CPUGuarantee_Bounds(t *testing.T) {
	for _, v := range []int32{0, 100} {
		k := newInstanceSvc(t, true)
		req := baseCreateReq()
		req.CPUGuaranteePercent = v
		op, err := k.svc.Create(context.Background(), req)
		require.NoError(t, err, "cpu_guarantee_percent=%d must be accepted", v)
		in := instanceFromOp(t, portmock.AwaitOpDone(t, k.ops, op.ID))
		require.Equal(t, v, in.CpuGuaranteePercent)
	}

	k := newInstanceSvc(t, true)
	bad := baseCreateReq()
	bad.CPUGuaranteePercent = 101
	_, err := k.svc.Create(context.Background(), bad)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Equal(t, "Illegal argument cpu_guarantee_percent", status.Convert(err).Message())
}

// TestInstance_Create_Image_Stored — S5-05: image (OCI-ref) сохраняется и
// проецируется на output Instance.image; image_digest остаётся ПУСТЫМ (registry-
// resolve отложен, acceptance sec.0.3 — не резолвим на этой фазе).
func TestInstance_Create_Image_Stored(t *testing.T) {
	k := newInstanceSvc(t, true)
	req := baseCreateReq()
	req.Image = "cr.kacho.cloud/library/ubuntu:24.04"
	op, err := k.svc.Create(context.Background(), req)
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, k.ops, op.ID))
	require.Equal(t, "cr.kacho.cloud/library/ubuntu:24.04", in.Image)
	require.Empty(t, in.ImageDigest, "image_digest must be empty until registry-resolve slice")
}

// TestInstance_Update_Image_AnyStatus — S5-05: image mutable в любом статусе (как
// name/description) — re-pin OS-образа не требует STOPPED.
func TestInstance_Update_Image_AnyStatus(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusRunning)
	op, err := k.svc.Update(context.Background(), UpdateInstanceReq{
		InstanceID: "epdvm1", Image: "cr.kacho.cloud/library/debian:12", UpdateMask: []string{"image"},
	})
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, k.ops, op.ID))
	require.Equal(t, "cr.kacho.cloud/library/debian:12", in.Image)
}

// TestInstance_Update_CPUGuarantee_RequiresStopped — S5-03: cpu_guarantee_percent —
// часть sizing (resources_spec). Изменение sizing на RUNNING → FailedPrecondition
// "Instance must be STOPPED to change sizing"; на STOPPED — применяется.
func TestInstance_Update_CPUGuarantee_RequiresStopped(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusRunning)
	op, err := k.svc.Update(context.Background(), UpdateInstanceReq{
		InstanceID: "epdvm1", Cores: 2, Memory: 2 << 30, CoreFraction: 100,
		CPUGuaranteePercent: 50, UpdateMask: []string{"resources_spec"},
	})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, k.ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.FailedPrecondition), done.Error.Code)
	require.Equal(t, "Instance must be STOPPED to change sizing", done.Error.Message)

	seedRunningInstance(k.repo, domain.InstanceStatusStopped)
	op, err = k.svc.Update(context.Background(), UpdateInstanceReq{
		InstanceID: "epdvm1", Cores: 2, Memory: 2 << 30, CoreFraction: 100,
		CPUGuaranteePercent: 50, UpdateMask: []string{"resources_spec"},
	})
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, k.ops, op.ID))
	require.Equal(t, int32(50), in.CpuGuaranteePercent)
}

// TestInstance_Update_CPUGuarantee_Bounds — S5-03: cpu_guarantee_percent=101 в
// resources_spec на STOPPED → Operation error InvalidArgument (валидация sizing в
// worker'е, как cores/memory; см. TestInstance_Update_ResourcesRequiresStopped).
func TestInstance_Update_CPUGuarantee_Bounds(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusStopped)
	op, err := k.svc.Update(context.Background(), UpdateInstanceReq{
		InstanceID: "epdvm1", Cores: 2, Memory: 2 << 30, CoreFraction: 100,
		CPUGuaranteePercent: 101, UpdateMask: []string{"resources_spec"},
	})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, k.ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.InvalidArgument), done.Error.Code)
	require.Equal(t, "Illegal argument cpu_guarantee_percent", done.Error.Message)
}

// TestInstance_Update_ImageDigest_Rejected — S5-05: image_digest — output-only
// (resolved digest), НЕ принимается на вход Update → sync InvalidArgument immutable.
func TestInstance_Update_ImageDigest_Rejected(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusRunning)
	_, err := k.svc.Update(context.Background(), UpdateInstanceReq{
		InstanceID: "epdvm1", UpdateMask: []string{"image_digest"},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "image_digest is immutable after Instance.Create")
}
