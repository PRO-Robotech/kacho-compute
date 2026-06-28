// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	computev1 "github.com/PRO-Robotech/kacho-compute/proto/gen/go/kacho/cloud/compute/v1"
	"github.com/PRO-Robotech/kacho-corelib/operations"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/ports/portmock"
)

func newImageSvc(t *testing.T, folderOK bool) (*ImageService, *portmock.ImageRepo, *portmock.DiskRepo, *portmock.SnapshotRepo, *portmock.OpsRepo) {
	t.Helper()
	imgRepo := portmock.NewImageRepo()
	diskRepo := portmock.NewDiskRepo()
	snapRepo := portmock.NewSnapshotRepo()
	ops := portmock.NewOpsRepo()
	return NewImageService(imgRepo, diskRepo, snapRepo, &portmock.ProjectClient{OK: folderOK}, ops), imgRepo, diskRepo, snapRepo, ops
}

func imageFromOp(t *testing.T, op *operations.Operation) *computev1.Image {
	t.Helper()
	require.NotNil(t, op.Response, "operation error=%v", op.Error)
	var i computev1.Image
	require.NoError(t, op.Response.UnmarshalTo(&i))
	return &i
}

func TestImage_Create_FromURI_OK(t *testing.T) {
	svc, repo, _, _, ops := newImageSvc(t, true)
	op, err := svc.Create(context.Background(), CreateImageReq{ProjectID: "f", Name: "img-x", URI: "https://storage/x.qcow2"})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	img := imageFromOp(t, done)
	require.Equal(t, "img-x", img.Name)
	require.Equal(t, computev1.Image_READY, img.Status)
	require.Equal(t, "fd8", img.Id[:3])
	stored, err := repo.Get(context.Background(), img.Id)
	require.NoError(t, err)
	require.Equal(t, "https://storage/x.qcow2", stored.SourceURI)
}

func TestImage_Create_SourceCount(t *testing.T) {
	svc, _, _, _, _ := newImageSvc(t, true)
	_, err := svc.Create(context.Background(), CreateImageReq{ProjectID: "f"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = svc.Create(context.Background(), CreateImageReq{ProjectID: "f", ImageID: "a", DiskID: "b"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestImage_Create_FromDisk_NotReady(t *testing.T) {
	svc, _, diskRepo, _, ops := newImageSvc(t, true)
	diskRepo.Seed(&domain.Disk{ID: "d1", ProjectID: "f", Status: domain.DiskStatusCreating})
	op, err := svc.Create(context.Background(), CreateImageReq{ProjectID: "f", DiskID: "d1"})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.FailedPrecondition), done.Error.Code)
}

func TestImage_GetLatestByFamily(t *testing.T) {
	svc, repo, _, _, _ := newImageSvc(t, true)
	old := time.Now().Add(-time.Hour)
	repo.Seed(&domain.Image{ID: "fd8a", ProjectID: "f", Family: "ubuntu", CreatedAt: old})
	repo.Seed(&domain.Image{ID: "fd8b", ProjectID: "f", Family: "ubuntu", CreatedAt: time.Now()})
	img, err := svc.GetLatestByFamily(context.Background(), "f", "ubuntu")
	require.NoError(t, err)
	require.Equal(t, "fd8b", img.ID)
	_, err = svc.GetLatestByFamily(context.Background(), "f", "missing")
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestImage_Update_Immutable(t *testing.T) {
	svc, repo, _, _, _ := newImageSvc(t, true)
	repo.Seed(&domain.Image{ID: "i1", ProjectID: "f", Family: "x"})
	_, err := svc.Update(context.Background(), UpdateImageReq{ImageID: "i1", UpdateMask: []string{"family"}})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestImage_Delete_OK(t *testing.T) {
	svc, repo, _, _, ops := newImageSvc(t, true)
	repo.Seed(&domain.Image{ID: "i1", ProjectID: "f"})
	op, err := svc.Delete(context.Background(), "i1")
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.Nil(t, done.Error)
}

// ---- Snapshot ----

func newSnapshotSvc(t *testing.T, folderOK bool) (*SnapshotService, *portmock.SnapshotRepo, *portmock.DiskRepo, *portmock.OpsRepo) {
	t.Helper()
	snapRepo := portmock.NewSnapshotRepo()
	diskRepo := portmock.NewDiskRepo()
	ops := portmock.NewOpsRepo()
	return NewSnapshotService(snapRepo, diskRepo, &portmock.ProjectClient{OK: folderOK}, ops), snapRepo, diskRepo, ops
}

func snapshotFromOp(t *testing.T, op *operations.Operation) *computev1.Snapshot {
	t.Helper()
	require.NotNil(t, op.Response, "operation error=%v", op.Error)
	var s computev1.Snapshot
	require.NoError(t, op.Response.UnmarshalTo(&s))
	return &s
}

func TestSnapshot_Create_OK(t *testing.T) {
	svc, repo, diskRepo, ops := newSnapshotSvc(t, true)
	diskRepo.Seed(&domain.Disk{ID: "d1", ProjectID: "f", Size: diskSizeMin * 5, Status: domain.DiskStatusReady})
	op, err := svc.Create(context.Background(), CreateSnapshotReq{ProjectID: "f", DiskID: "d1", Name: "snap-1"})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	s := snapshotFromOp(t, done)
	require.Equal(t, "snap-1", s.Name)
	require.Equal(t, "d1", s.SourceDiskId)
	require.Equal(t, int64(diskSizeMin*5), s.DiskSize)
	require.Equal(t, computev1.Snapshot_READY, s.Status)
	stored, err := repo.Get(context.Background(), s.Id)
	require.NoError(t, err)
	require.Equal(t, int64(diskSizeMin*5), stored.StorageSize)
}

func TestSnapshot_Create_DiskRequired(t *testing.T) {
	svc, _, _, _ := newSnapshotSvc(t, true)
	_, err := svc.Create(context.Background(), CreateSnapshotReq{ProjectID: "f"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestSnapshot_Create_DiskNotReady(t *testing.T) {
	svc, _, diskRepo, ops := newSnapshotSvc(t, true)
	diskRepo.Seed(&domain.Disk{ID: "d1", ProjectID: "f", Status: domain.DiskStatusCreating})
	op, err := svc.Create(context.Background(), CreateSnapshotReq{ProjectID: "f", DiskID: "d1"})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.FailedPrecondition), done.Error.Code)
}

func TestSnapshot_Update_Immutable(t *testing.T) {
	svc, repo, _, _ := newSnapshotSvc(t, true)
	repo.Seed(&domain.Snapshot{ID: "s1", ProjectID: "f"})
	_, err := svc.Update(context.Background(), UpdateSnapshotReq{SnapshotID: "s1", UpdateMask: []string{"disk_size"}})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
