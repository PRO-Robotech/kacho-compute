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

func newTestDiskSvc(diskRepo service.DiskRepo, imageRepo service.ImageRepo, folderExists bool) *service.DiskService {
	folderClient := &mockFolderClient{
		existsFunc: func(_ string) bool { return folderExists },
	}
	return service.NewDiskService(diskRepo, imageRepo, folderClient)
}

// TestDisk_B1_UpsertCreatesDisk проверяет создание нового диска.
func TestDisk_B1_UpsertCreatesDisk(t *testing.T) {
	diskRepo := newMockDiskRepo()
	svc := newTestDiskSvc(diskRepo, newMockImageRepo(), true)

	result, err := svc.Upsert(context.Background(), &domain.Disk{
		Name:       "my-disk-01",
		FolderID:   testFolderUID,
		DiskTypeID: "network-ssd",
		ZoneID:     "kacho-zone-a",
		Size:       "50Gi",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, result.UID)
	assert.Equal(t, "my-disk-01", result.Name)
	assert.Equal(t, domain.DiskStateCreating, result.State)
}

// TestDisk_B3_UpsertIdempotentNoOp проверяет идемпотентность.
func TestDisk_B3_UpsertIdempotentNoOp(t *testing.T) {
	diskRepo := newMockDiskRepo()
	svc := newTestDiskSvc(diskRepo, newMockImageRepo(), true)

	disk := &domain.Disk{
		Name:        "my-disk-01",
		FolderID:    testFolderUID,
		DiskTypeID:  "network-ssd",
		ZoneID:      "kacho-zone-a",
		Size:        "50Gi",
		DisplayName: "Test Disk",
	}

	first, err := svc.Upsert(context.Background(), disk)
	require.NoError(t, err)

	second, err := svc.Upsert(context.Background(), &domain.Disk{
		Name:        "my-disk-01",
		FolderID:    testFolderUID,
		DiskTypeID:  "network-ssd",
		ZoneID:      "kacho-zone-a",
		Size:        "50Gi",
		DisplayName: "Test Disk",
	})
	require.NoError(t, err)
	assert.Equal(t, first.ResourceVersion, second.ResourceVersion, "no-op upsert should not change resource_version")
}

// TestDisk_ValidationRequiresName проверяет валидацию name.
func TestDisk_ValidationRequiresName(t *testing.T) {
	svc := newTestDiskSvc(newMockDiskRepo(), newMockImageRepo(), true)
	_, err := svc.Upsert(context.Background(), &domain.Disk{FolderID: testFolderUID, DiskTypeID: "x", ZoneID: "z", Size: "10Gi"})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

// TestDisk_ValidationFolderNotFound проверяет ошибку при отсутствии Folder.
func TestDisk_ValidationFolderNotFound(t *testing.T) {
	svc := newTestDiskSvc(newMockDiskRepo(), newMockImageRepo(), false)
	_, err := svc.Upsert(context.Background(), &domain.Disk{
		Name:       "d",
		FolderID:   testFolderUID,
		DiskTypeID: "network-ssd",
		ZoneID:     "z",
		Size:       "10Gi",
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// TestDisk_ImageNotFound проверяет ошибку при несуществующем образе.
func TestDisk_ImageNotFound(t *testing.T) {
	svc := newTestDiskSvc(newMockDiskRepo(), newMockImageRepo(), true)
	_, err := svc.Upsert(context.Background(), &domain.Disk{
		Name:       "d",
		FolderID:   testFolderUID,
		DiskTypeID: "network-ssd",
		ZoneID:     "z",
		Size:       "10Gi",
		ImageID:    "nonexistent-image",
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}
