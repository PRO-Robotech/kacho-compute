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

const testFolderUID = "11111111-1111-1111-1111-111111111111"
const testDiskUID = "22222222-2222-2222-2222-222222222222"

func newTestInstanceSvc(instanceRepo service.InstanceRepo, diskRepo service.DiskRepo, folderExists bool) *service.InstanceService {
	folderClient := &mockFolderClient{
		existsFunc: func(_ string) bool { return folderExists },
	}
	subnetClient := &mockSubnetClient{
		existsFunc: func(_ string) bool { return true },
	}
	return service.NewInstanceService(instanceRepo, diskRepo, folderClient, subnetClient)
}

// TestInstance_E1_UpsertRequiresName проверяет валидацию имени.
func TestInstance_E1_UpsertRequiresName(t *testing.T) {
	svc := newTestInstanceSvc(newMockInstanceRepo(), newMockDiskRepo(), true)

	_, err := svc.Upsert(context.Background(), &domain.Instance{
		FolderID: testFolderUID,
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

// TestInstance_E2_UpsertRequiresFolderID проверяет валидацию folder_id.
func TestInstance_E2_UpsertRequiresFolderID(t *testing.T) {
	svc := newTestInstanceSvc(newMockInstanceRepo(), newMockDiskRepo(), true)

	_, err := svc.Upsert(context.Background(), &domain.Instance{
		Name: "test-instance",
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

// TestInstance_E3_UpsertFolderNotFound проверяет ошибку при отсутствии Folder.
func TestInstance_E3_UpsertFolderNotFound(t *testing.T) {
	svc := newTestInstanceSvc(newMockInstanceRepo(), newMockDiskRepo(), false)

	_, err := svc.Upsert(context.Background(), &domain.Instance{
		Name:     "test-instance",
		FolderID: testFolderUID,
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// TestInstance_E4_UpsertBootDiskNotFound проверяет ошибку при отсутствии boot disk.
func TestInstance_E4_UpsertBootDiskNotFound(t *testing.T) {
	diskRepo := newMockDiskRepo()
	svc := newTestInstanceSvc(newMockInstanceRepo(), diskRepo, true)

	_, err := svc.Upsert(context.Background(), &domain.Instance{
		Name:     "test-instance",
		FolderID: testFolderUID,
		BootDisk: &domain.AttachedDisk{DiskID: "nonexistent-disk"},
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// TestInstance_E6extra_BootDiskNotReady проверяет nit-2: boot disk не в READY.
func TestInstance_E6extra_BootDiskNotReady(t *testing.T) {
	diskRepo := newMockDiskRepo()
	// Добавляем диск в CREATING
	diskRepo.data[testDiskUID] = &domain.Disk{
		UID:   testDiskUID,
		State: domain.DiskStateCreating,
	}

	svc := newTestInstanceSvc(newMockInstanceRepo(), diskRepo, true)

	_, err := svc.Upsert(context.Background(), &domain.Instance{
		Name:     "test-instance",
		FolderID: testFolderUID,
		BootDisk: &domain.AttachedDisk{DiskID: testDiskUID},
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
}

// TestInstance_E5_UpsertWithReadyDisk проверяет успешное создание с READY диском.
func TestInstance_E5_UpsertWithReadyDisk(t *testing.T) {
	diskRepo := newMockDiskRepo()
	diskRepo.data[testDiskUID] = &domain.Disk{
		UID:   testDiskUID,
		State: domain.DiskStateReady,
	}

	instanceRepo := newMockInstanceRepo()
	svc := newTestInstanceSvc(instanceRepo, diskRepo, true)

	result, err := svc.Upsert(context.Background(), &domain.Instance{
		Name:     "test-instance",
		FolderID: testFolderUID,
		BootDisk: &domain.AttachedDisk{DiskID: testDiskUID},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, result.UID)
	assert.Equal(t, domain.InstanceStateProvisioning, result.State)
	// finalizer должен быть установлен
	assert.Contains(t, result.Finalizers, "compute.kacho.io/disk-detach")
}

// TestInstance_B3_UpsertIdempotentNoOp проверяет идемпотентность upsert без изменений.
func TestInstance_B3_UpsertIdempotentNoOp(t *testing.T) {
	diskRepo := newMockDiskRepo()
	diskRepo.data[testDiskUID] = &domain.Disk{
		UID:   testDiskUID,
		State: domain.DiskStateReady,
	}

	instanceRepo := newMockInstanceRepo()
	svc := newTestInstanceSvc(instanceRepo, diskRepo, true)

	inst := &domain.Instance{
		Name:        "test-instance",
		FolderID:    testFolderUID,
		DisplayName: "Test Instance",
		BootDisk:    &domain.AttachedDisk{DiskID: testDiskUID},
	}

	first, err := svc.Upsert(context.Background(), inst)
	require.NoError(t, err)
	firstRV := first.ResourceVersion

	second, err := svc.Upsert(context.Background(), &domain.Instance{
		Name:        "test-instance",
		FolderID:    testFolderUID,
		DisplayName: "Test Instance",
		BootDisk:    &domain.AttachedDisk{DiskID: testDiskUID},
	})
	require.NoError(t, err)
	assert.Equal(t, firstRV, second.ResourceVersion, "no-op should not change resource_version")
}

// TestInstance_Delete_NotFound проверяет ошибку при удалении несуществующего инстанса.
func TestInstance_Delete_NotFound(t *testing.T) {
	svc := newTestInstanceSvc(newMockInstanceRepo(), newMockDiskRepo(), true)

	err := svc.Delete(context.Background(), "nonexistent-uid")
	require.Error(t, err)
}

// TestInstance_Restart_OnlyRunning проверяет, что Restart доступен только из RUNNING.
func TestInstance_Restart_OnlyRunning(t *testing.T) {
	instanceRepo := newMockInstanceRepo()
	instanceRepo.data["test-uid"] = &domain.Instance{
		UID:   "test-uid",
		State: domain.InstanceStateStopped,
	}

	svc := newTestInstanceSvc(instanceRepo, newMockDiskRepo(), true)

	_, err := svc.Restart(context.Background(), "test-uid")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
}
