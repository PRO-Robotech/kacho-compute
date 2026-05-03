package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

func newTestSnapshotSvc(snapRepo service.SnapshotRepo, diskRepo service.DiskRepo) *service.SnapshotService {
	return service.NewSnapshotService(snapRepo, diskRepo)
}

// TestSnapshot_K1_UpsertRequiresDiskReady проверяет ошибку при CREATING диске.
func TestSnapshot_K1_UpsertRequiresDiskReady(t *testing.T) {
	diskRepo := newMockDiskRepo()
	diskRepo.data[testDiskUID] = &domain.Disk{
		UID:   testDiskUID,
		State: domain.DiskStateCreating,
	}

	svc := newTestSnapshotSvc(newMockSnapshotRepo(), diskRepo)

	_, err := svc.Upsert(context.Background(), &domain.Snapshot{
		Name:     "snap-01",
		FolderID: testFolderUID,
		DiskID:   testDiskUID,
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
}

// TestSnapshot_K2_UpsertWithReadyDisk проверяет успешное создание снапшота.
func TestSnapshot_K2_UpsertWithReadyDisk(t *testing.T) {
	diskRepo := newMockDiskRepo()
	diskRepo.data[testDiskUID] = &domain.Disk{
		UID:   testDiskUID,
		State: domain.DiskStateReady,
	}

	svc := newTestSnapshotSvc(newMockSnapshotRepo(), diskRepo)

	result, err := svc.Upsert(context.Background(), &domain.Snapshot{
		Name:     "snap-01",
		FolderID: testFolderUID,
		DiskID:   testDiskUID,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, result.UID)
	assert.Equal(t, domain.SnapshotStateCreating, result.State)
	assert.Equal(t, int32(0), result.ProgressPercent)
}

// TestSnapshot_DiskNotFound проверяет ошибку при несуществующем диске.
func TestSnapshot_DiskNotFound(t *testing.T) {
	svc := newTestSnapshotSvc(newMockSnapshotRepo(), newMockDiskRepo())
	_, err := svc.Upsert(context.Background(), &domain.Snapshot{
		Name:     "snap-01",
		FolderID: testFolderUID,
		DiskID:   "nonexistent-disk",
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// TestSnapshot_K3_NoOpIdempotent проверяет идемпотентность.
func TestSnapshot_K3_NoOpIdempotent(t *testing.T) {
	diskRepo := newMockDiskRepo()
	diskRepo.data[testDiskUID] = &domain.Disk{
		UID:   testDiskUID,
		State: domain.DiskStateReady,
	}

	snapRepo := newMockSnapshotRepo()
	svc := newTestSnapshotSvc(snapRepo, diskRepo)

	snap := &domain.Snapshot{
		Name:        "snap-01",
		FolderID:    testFolderUID,
		DiskID:      testDiskUID,
		DisplayName: "My Snapshot",
	}

	first, err := svc.Upsert(context.Background(), snap)
	require.NoError(t, err)

	second, err := svc.Upsert(context.Background(), &domain.Snapshot{
		Name:        "snap-01",
		FolderID:    testFolderUID,
		DiskID:      testDiskUID,
		DisplayName: "My Snapshot",
	})
	require.NoError(t, err)
	assert.Equal(t, first.ResourceVersion, second.ResourceVersion)
}
