package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/ports/portmock"
)

func newDiskSvc(t *testing.T, folderOK bool) (*DiskService, *portmock.DiskRepo, *portmock.ImageRepo, *portmock.SnapshotRepo, *portmock.OpsRepo) {
	t.Helper()
	diskRepo := portmock.NewDiskRepo()
	imgRepo := portmock.NewImageRepo()
	snapRepo := portmock.NewSnapshotRepo()
	ops := portmock.NewOpsRepo()
	svc := NewDiskService(diskRepo, imgRepo, snapRepo, portmock.NewDiskTypeRepo(), portmock.NewZoneRepo(),
		&portmock.FolderClient{OK: folderOK}, ops)
	return svc, diskRepo, imgRepo, snapRepo, ops
}

func diskFromOp(t *testing.T, op *operations.Operation) *computev1.Disk {
	t.Helper()
	require.NotNil(t, op.Response, "operation response is nil; error=%v", op.Error)
	var d computev1.Disk
	require.NoError(t, op.Response.UnmarshalTo(&d))
	return &d
}

func TestDisk_Create_OK(t *testing.T) {
	svc, repo, _, _, ops := newDiskSvc(t, true)
	op, err := svc.Create(context.Background(), CreateDiskReq{
		FolderID: "fld1", Name: "my-disk", ZoneID: "ru-central1-a", Size: diskSizeMin,
	})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	d := diskFromOp(t, done)
	require.Equal(t, "my-disk", d.Name)
	require.Equal(t, "ru-central1-a", d.ZoneId)
	require.Equal(t, "network-ssd", d.TypeId) // default
	require.Equal(t, int64(4096), d.BlockSize)
	require.Equal(t, computev1.Disk_READY, d.Status)
	stored, err := repo.Get(context.Background(), d.Id)
	require.NoError(t, err)
	require.Equal(t, "my-disk", stored.Name)
}

func TestDisk_Create_SyncValidation(t *testing.T) {
	svc, _, _, _, _ := newDiskSvc(t, true)
	cases := []struct {
		name string
		req  CreateDiskReq
		code codes.Code
	}{
		{"no folder", CreateDiskReq{ZoneID: "ru-central1-a", Size: diskSizeMin}, codes.InvalidArgument},
		{"no zone", CreateDiskReq{FolderID: "f", Size: diskSizeMin}, codes.InvalidArgument},
		{"size too small", CreateDiskReq{FolderID: "f", ZoneID: "ru-central1-a", Size: 100}, codes.InvalidArgument},
		{"size too big", CreateDiskReq{FolderID: "f", ZoneID: "ru-central1-a", Size: diskSizeMaxCreate + 1}, codes.InvalidArgument},
		{"uppercase name", CreateDiskReq{FolderID: "f", ZoneID: "ru-central1-a", Size: diskSizeMin, Name: "Bad"}, codes.InvalidArgument},
		{"both sources", CreateDiskReq{FolderID: "f", ZoneID: "ru-central1-a", Size: diskSizeMin, ImageID: "i", SnapshotID: "s"}, codes.InvalidArgument},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Create(context.Background(), tc.req)
			require.Equal(t, tc.code, status.Code(err))
		})
	}
}

func TestDisk_Create_FolderNotFound(t *testing.T) {
	svc, _, _, _, ops := newDiskSvc(t, false)
	op, err := svc.Create(context.Background(), CreateDiskReq{FolderID: "missing", ZoneID: "ru-central1-a", Size: diskSizeMin})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.NotFound), done.Error.Code)
}

func TestDisk_Create_FromImage_SizeTooSmall(t *testing.T) {
	svc, _, imgRepo, _, ops := newDiskSvc(t, true)
	imgRepo.Seed(&domain.Image{ID: "img1", FolderID: "f", MinDiskSize: diskSizeMin * 4})
	op, err := svc.Create(context.Background(), CreateDiskReq{FolderID: "f", ZoneID: "ru-central1-a", Size: diskSizeMin, ImageID: "img1"})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.InvalidArgument), done.Error.Code)
}

func TestDisk_Get_NotFound(t *testing.T) {
	svc, _, _, _, _ := newDiskSvc(t, true)
	_, err := svc.Get(context.Background(), "missing")
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestDisk_List_RequiresFolder(t *testing.T) {
	svc, _, _, _, _ := newDiskSvc(t, true)
	_, _, err := svc.List(context.Background(), DiskFilter{}, Pagination{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestDisk_Update_SizeDecrease_Rejected(t *testing.T) {
	svc, repo, _, _, ops := newDiskSvc(t, true)
	repo.Seed(&domain.Disk{ID: "d1", FolderID: "f", Size: diskSizeMin * 2, Status: domain.DiskStatusReady})
	op, err := svc.Update(context.Background(), UpdateDiskReq{DiskID: "d1", Size: diskSizeMin, UpdateMask: []string{"size"}})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.InvalidArgument), done.Error.Code)
	require.Contains(t, done.Error.Message, "can only be increased")
}

func TestDisk_Update_ImmutableField_Rejected(t *testing.T) {
	svc, repo, _, _, _ := newDiskSvc(t, true)
	repo.Seed(&domain.Disk{ID: "d1", FolderID: "f", Size: diskSizeMin})
	_, err := svc.Update(context.Background(), UpdateDiskReq{DiskID: "d1", UpdateMask: []string{"zone_id"}})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestDisk_Update_NameAndDescription(t *testing.T) {
	svc, repo, _, _, ops := newDiskSvc(t, true)
	repo.Seed(&domain.Disk{ID: "d1", FolderID: "f", Size: diskSizeMin, Name: "old"})
	op, err := svc.Update(context.Background(), UpdateDiskReq{DiskID: "d1", Name: "new", Description: "hi", UpdateMask: []string{"name", "description"}})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	d := diskFromOp(t, done)
	require.Equal(t, "new", d.Name)
	require.Equal(t, "hi", d.Description)
}

func TestDisk_Delete_OK(t *testing.T) {
	svc, repo, _, _, ops := newDiskSvc(t, true)
	repo.Seed(&domain.Disk{ID: "d1", FolderID: "f", Size: diskSizeMin})
	op, err := svc.Delete(context.Background(), "d1")
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.Nil(t, done.Error)
	_, err = repo.Get(context.Background(), "d1")
	require.Error(t, err)
}

func TestDisk_Delete_Attached_FailedPrecondition(t *testing.T) {
	svc, repo, _, _, ops := newDiskSvc(t, true)
	repo.Seed(&domain.Disk{ID: "d1", FolderID: "f", Size: diskSizeMin})
	repo.SetAttached("d1", true)
	op, err := svc.Delete(context.Background(), "d1")
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.FailedPrecondition), done.Error.Code)
}

func TestDisk_Move_OK(t *testing.T) {
	svc, repo, _, _, ops := newDiskSvc(t, true)
	repo.Seed(&domain.Disk{ID: "d1", FolderID: "f1", Size: diskSizeMin})
	op, err := svc.Move(context.Background(), "d1", "f2")
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	d := diskFromOp(t, done)
	require.Equal(t, "f2", d.FolderId)
}

func TestDisk_Relocate_Attached_Rejected(t *testing.T) {
	svc, repo, _, _, ops := newDiskSvc(t, true)
	repo.Seed(&domain.Disk{ID: "d1", FolderID: "f", ZoneID: "ru-central1-a", Size: diskSizeMin})
	repo.SetAttached("d1", true)
	op, err := svc.Relocate(context.Background(), "d1", "ru-central1-b")
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.FailedPrecondition), done.Error.Code)
}

func TestDisk_Operations_Always_HasComputePrefix(t *testing.T) {
	svc, _, _, _, _ := newDiskSvc(t, true)
	op, err := svc.Create(context.Background(), CreateDiskReq{FolderID: "f", ZoneID: "ru-central1-a", Size: diskSizeMin})
	require.NoError(t, err)
	require.Equal(t, "epd", op.ID[:3], "all compute operations must use the epd prefix")
}
