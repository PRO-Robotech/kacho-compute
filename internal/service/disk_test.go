package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	svc "github.com/PRO-Robotech/kacho-compute/internal/service"
)

func newDiskService() *svc.DiskService {
	return svc.NewDiskService(
		newMockDiskRepo(),
		newMockImageRepo(),
		newMockFolderClient("folder-1"),
		newMockOpsRepo(),
	)
}

func TestDiskService_Create_MissingFolder(t *testing.T) {
	s := newDiskService()
	ctx := context.Background()

	_, err := s.Create(ctx, svc.CreateDiskReq{Name: "disk-1"})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestDiskService_Create_Success(t *testing.T) {
	s := newDiskService()
	ctx := context.Background()

	op, err := s.Create(ctx, svc.CreateDiskReq{
		FolderID:   "folder-1",
		Name:       "my-disk",
		DiskTypeID: "network-ssd",
		ZoneID:     "kacho-zone-a",
		Size:       "10Gi",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, op.ID)
	assert.False(t, op.Done)
}

func TestDiskService_Delete_AttachedDisk(t *testing.T) {
	diskRepo := newMockDiskRepo()
	s := svc.NewDiskService(diskRepo, newMockImageRepo(), newMockFolderClient("f1"), newMockOpsRepo())
	ctx := context.Background()

	d := &domain.Disk{
		ID:                   "disk-1",
		FolderID:             "f1",
		Name:                 "attached",
		Status:               domain.DiskStatusReady,
		AttachedToInstanceID: "inst-1",
	}
	_, err := diskRepo.Insert(ctx, d)
	require.NoError(t, err)

	_, err = s.Delete(ctx, "disk-1")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
}

func TestDiskService_Delete_Success(t *testing.T) {
	diskRepo := newMockDiskRepo()
	s := svc.NewDiskService(diskRepo, newMockImageRepo(), newMockFolderClient("f1"), newMockOpsRepo())
	ctx := context.Background()

	d := &domain.Disk{
		ID:       "disk-2",
		FolderID: "f1",
		Name:     "free-disk",
		Status:   domain.DiskStatusReady,
	}
	_, err := diskRepo.Insert(ctx, d)
	require.NoError(t, err)

	op, err := s.Delete(ctx, "disk-2")
	require.NoError(t, err)
	assert.NotEmpty(t, op.ID)

	updated, err := diskRepo.Get(ctx, "disk-2")
	require.NoError(t, err)
	assert.Equal(t, domain.DiskStatusDeleting, updated.Status)
	assert.NotNil(t, updated.DeletedAt)
}

func TestDiskService_Get_NotFound(t *testing.T) {
	s := newDiskService()
	_, err := s.Get(context.Background(), "nonexistent")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}
