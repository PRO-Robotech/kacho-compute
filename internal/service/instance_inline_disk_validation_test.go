// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/ports/portmock"
)

// Inline boot/secondary-disk materialization (Instance.Create) must enforce the
// SAME within-service invariants as the standalone DiskService.Create path:
// disk-type existence, size bounds, and source min-size. disks.type_id has no
// cross-table FK, so an unvalidated inline path would persist an invalid/
// undersized disk row that CreateDisk itself would reject — parity of
// enforcement across both creation paths.

func TestInstance_Create_InlineDisk_UnknownType_NotFound(t *testing.T) {
	svc, _, _, _, ops := newInstanceSvc(t, true)
	req := baseCreateReq() // project "f"
	req.BootDisk = DiskSourceSpec{NewDiskSizeBytes: diskSizeMin, NewDiskTypeID: "bogus"}
	op, err := svc.Create(context.Background(), req)
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error, "inline disk with a nonexistent disk type must be rejected")
	require.Equal(t, int32(codes.NotFound), done.Error.Code)
	require.Equal(t, "Disk type bogus not found", done.Error.Message)
}

func TestInstance_Create_InlineDisk_SizeAboveMax_InvalidArgument(t *testing.T) {
	svc, _, _, _, ops := newInstanceSvc(t, true)
	req := baseCreateReq() // project "f"
	req.BootDisk = DiskSourceSpec{NewDiskSizeBytes: diskSizeMaxCreate + 1}
	op, err := svc.Create(context.Background(), req)
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error, "inline disk exceeding the max create size must be rejected")
	require.Equal(t, int32(codes.InvalidArgument), done.Error.Code)
	require.Contains(t, done.Error.Message, "size must be in range")
}

func TestInstance_Create_InlineDisk_SizeBelowImageMinDiskSize_InvalidArgument(t *testing.T) {
	svc, _, _, imgRepo, ops := newInstanceSvc(t, true)
	imgRepo.Seed(&domain.Image{ID: "iok", ProjectID: "f", MinDiskSize: diskSizeMin * 10})
	req := baseCreateReq() // project "f"
	req.BootDisk = DiskSourceSpec{NewDiskSizeBytes: diskSizeMin, NewSourceImage: "iok"}
	op, err := svc.Create(context.Background(), req)
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error, "inline boot disk smaller than image min_disk_size must be rejected")
	require.Equal(t, int32(codes.InvalidArgument), done.Error.Code)
	require.Contains(t, done.Error.Message, "less than image min_disk_size")
}
