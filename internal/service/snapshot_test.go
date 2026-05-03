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

func TestSnapshotService_Create_MissingDisk(t *testing.T) {
	s := svc.NewSnapshotService(newMockSnapshotRepo(), newMockDiskRepo(), newMockOpsRepo())
	ctx := context.Background()

	_, err := s.Create(ctx, svc.CreateSnapshotReq{
		FolderID: "f1",
		DiskID:   "disk-nonexistent",
		Name:     "snap",
	})
	// Операция создаётся, но goroutine завершится ошибкой через MarkError.
	// Проверяем что хотя бы операция создаётся.
	require.NoError(t, err)
}

func TestSnapshotService_Create_DiskNotReady(t *testing.T) {
	diskRepo := newMockDiskRepo()
	ctx := context.Background()

	d := &domain.Disk{
		ID:       "disk-not-ready",
		FolderID: "f1",
		Name:     "my-disk",
		Status:   domain.DiskStatusCreating, // не READY
	}
	_, _ = diskRepo.Insert(ctx, d)

	s := svc.NewSnapshotService(newMockSnapshotRepo(), diskRepo, newMockOpsRepo())

	// Операция создаётся немедленно, но goroutine провалится.
	op, err := s.Create(ctx, svc.CreateSnapshotReq{
		FolderID: "f1",
		DiskID:   "disk-not-ready",
		Name:     "snap",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, op.ID)
}

func TestSnapshotService_Create_Success(t *testing.T) {
	diskRepo := newMockDiskRepo()
	ctx := context.Background()

	d := &domain.Disk{
		ID:       "disk-ready",
		FolderID: "f1",
		Name:     "ready-disk",
		Size:     "10Gi",
		Status:   domain.DiskStatusReady,
	}
	_, _ = diskRepo.Insert(ctx, d)

	s := svc.NewSnapshotService(newMockSnapshotRepo(), diskRepo, newMockOpsRepo())

	op, err := s.Create(ctx, svc.CreateSnapshotReq{
		FolderID: "f1",
		DiskID:   "disk-ready",
		Name:     "snap-1",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, op.ID)
	assert.False(t, op.Done)
}

func TestSnapshotService_Get_NotFound(t *testing.T) {
	s := svc.NewSnapshotService(newMockSnapshotRepo(), newMockDiskRepo(), newMockOpsRepo())
	_, err := s.Get(context.Background(), "nonexistent")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

func TestSnapshotService_Delete(t *testing.T) {
	snapRepo := newMockSnapshotRepo()
	ctx := context.Background()

	snap := &domain.Snapshot{
		ID:       "snap-1",
		FolderID: "f1",
		Name:     "old-snap",
		Status:   domain.SnapshotStatusReady,
	}
	_, _ = snapRepo.Insert(ctx, snap)

	s := svc.NewSnapshotService(snapRepo, newMockDiskRepo(), newMockOpsRepo())
	op, err := s.Delete(ctx, "snap-1")
	require.NoError(t, err)
	assert.NotEmpty(t, op.ID)

	updated, err := snapRepo.Get(ctx, "snap-1")
	require.NoError(t, err)
	assert.Equal(t, domain.SnapshotStatusDeleting, updated.Status)
}
