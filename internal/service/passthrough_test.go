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

// ---- Instance pass-through coverage ----

func TestInstance_GetByUID_Empty(t *testing.T) {
	svc := newTestInstanceSvc(newMockInstanceRepo(), newMockDiskRepo(), true)
	_, err := svc.GetByUID(context.Background(), "")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestInstance_GetByUID_Existing(t *testing.T) {
	repo := newMockInstanceRepo()
	repo.data["inst-uid-1"] = &domain.Instance{UID: "inst-uid-1", Name: "test"}
	svc := newTestInstanceSvc(repo, newMockDiskRepo(), true)

	got, err := svc.GetByUID(context.Background(), "inst-uid-1")
	require.NoError(t, err)
	assert.Equal(t, "inst-uid-1", got.UID)
}

func TestInstance_List(t *testing.T) {
	repo := newMockInstanceRepo()
	repo.data["uid-1"] = &domain.Instance{UID: "uid-1"}
	svc := newTestInstanceSvc(repo, newMockDiskRepo(), true)

	items, _, _, err := svc.List(context.Background(), nil, service.Pagination{PageSize: 10})
	require.NoError(t, err)
	assert.Len(t, items, 1)
}

func TestInstance_SnapshotResourceVersion(t *testing.T) {
	svc := newTestInstanceSvc(newMockInstanceRepo(), newMockDiskRepo(), true)
	rv, err := svc.SnapshotResourceVersion(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), rv)
}

func TestInstance_UpdateStatus(t *testing.T) {
	repo := newMockInstanceRepo()
	repo.data["uid-x"] = &domain.Instance{UID: "uid-x", State: domain.InstanceStateProvisioning}
	svc := newTestInstanceSvc(repo, newMockDiskRepo(), true)

	updated, err := svc.UpdateStatus(context.Background(), &domain.Instance{
		UID:   "uid-x",
		State: domain.InstanceStateRunning,
	})
	require.NoError(t, err)
	assert.Equal(t, domain.InstanceStateRunning, updated.State)
}

func TestInstance_UpdateMetadata(t *testing.T) {
	repo := newMockInstanceRepo()
	repo.data["uid-x"] = &domain.Instance{UID: "uid-x"}
	svc := newTestInstanceSvc(repo, newMockDiskRepo(), true)

	updated, err := svc.UpdateMetadata(context.Background(), "uid-x",
		[]string{"compute.kacho.io/disk-detach"}, true, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"compute.kacho.io/disk-detach"}, updated.Finalizers)
}

func TestInstance_HasDependents(t *testing.T) {
	svc := newTestInstanceSvc(newMockInstanceRepo(), newMockDiskRepo(), true)
	has, deps, err := svc.HasDependents(context.Background(), "any-uid")
	require.NoError(t, err)
	assert.False(t, has)
	assert.Empty(t, deps)
}

func TestInstance_Restart_NotFound(t *testing.T) {
	svc := newTestInstanceSvc(newMockInstanceRepo(), newMockDiskRepo(), true)
	_, err := svc.Restart(context.Background(), "nonexistent")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

func TestInstance_Restart_Running(t *testing.T) {
	repo := newMockInstanceRepo()
	repo.data["uid-r"] = &domain.Instance{UID: "uid-r", State: domain.InstanceStateRunning}
	svc := newTestInstanceSvc(repo, newMockDiskRepo(), true)

	_, err := svc.Restart(context.Background(), "uid-r")
	require.NoError(t, err)
}

// ---- Disk pass-through coverage ----

func TestDisk_GetByUID_Empty(t *testing.T) {
	svc := newTestDiskSvc(newMockDiskRepo(), newMockImageRepo(), true)
	_, err := svc.GetByUID(context.Background(), "")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestDisk_GetByUID_Existing(t *testing.T) {
	repo := newMockDiskRepo()
	repo.data["disk-uid-1"] = &domain.Disk{UID: "disk-uid-1", Name: "d"}
	svc := newTestDiskSvc(repo, newMockImageRepo(), true)

	got, err := svc.GetByUID(context.Background(), "disk-uid-1")
	require.NoError(t, err)
	assert.Equal(t, "disk-uid-1", got.UID)
}

func TestDisk_List(t *testing.T) {
	svc := newTestDiskSvc(newMockDiskRepo(), newMockImageRepo(), true)
	items, _, _, err := svc.List(context.Background(), nil, service.Pagination{PageSize: 10})
	require.NoError(t, err)
	assert.Empty(t, items)
}

func TestDisk_SnapshotResourceVersion(t *testing.T) {
	svc := newTestDiskSvc(newMockDiskRepo(), newMockImageRepo(), true)
	rv, err := svc.SnapshotResourceVersion(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), rv)
}

func TestDisk_Delete_NotFound(t *testing.T) {
	svc := newTestDiskSvc(newMockDiskRepo(), newMockImageRepo(), true)
	err := svc.Delete(context.Background(), "nonexistent")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

func TestDisk_Delete_WithSnapshots(t *testing.T) {
	// Use a custom mock that returns HasSnapshots=true
	repo := &mockDiskRepoWithSnapshots{mockDiskRepo: newMockDiskRepo()}
	repo.data["disk-1"] = &domain.Disk{UID: "disk-1", State: domain.DiskStateReady}
	svc := newTestDiskSvc(repo, newMockImageRepo(), true)

	err := svc.Delete(context.Background(), "disk-1")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
}

func TestDisk_Delete_OK(t *testing.T) {
	repo := newMockDiskRepo()
	repo.data["disk-del"] = &domain.Disk{UID: "disk-del", State: domain.DiskStateReady}
	svc := newTestDiskSvc(repo, newMockImageRepo(), true)

	err := svc.Delete(context.Background(), "disk-del")
	require.NoError(t, err)
}

func TestDisk_UpdateStatus(t *testing.T) {
	repo := newMockDiskRepo()
	repo.data["disk-uid"] = &domain.Disk{UID: "disk-uid", State: domain.DiskStateCreating}
	svc := newTestDiskSvc(repo, newMockImageRepo(), true)

	updated, err := svc.UpdateStatus(context.Background(), &domain.Disk{
		UID:   "disk-uid",
		State: domain.DiskStateReady,
	})
	require.NoError(t, err)
	assert.Equal(t, domain.DiskStateReady, updated.State)
}

// mockDiskRepoWithSnapshots переопределяет HasSnapshots для возврата true.
type mockDiskRepoWithSnapshots struct {
	*mockDiskRepo
}

func (m *mockDiskRepoWithSnapshots) HasSnapshots(_ context.Context, _ string) (bool, error) {
	return true, nil
}

// ---- Snapshot pass-through coverage ----

func TestSnapshot_GetByUID_Empty(t *testing.T) {
	svc := newTestSnapshotSvc(newMockSnapshotRepo(), newMockDiskRepo())
	_, err := svc.GetByUID(context.Background(), "")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestSnapshot_List(t *testing.T) {
	svc := newTestSnapshotSvc(newMockSnapshotRepo(), newMockDiskRepo())
	items, _, _, err := svc.List(context.Background(), nil, service.Pagination{PageSize: 10})
	require.NoError(t, err)
	assert.Empty(t, items)
}

func TestSnapshot_SnapshotResourceVersion(t *testing.T) {
	svc := newTestSnapshotSvc(newMockSnapshotRepo(), newMockDiskRepo())
	rv, err := svc.SnapshotResourceVersion(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), rv)
}

func TestSnapshot_Delete_NotFound(t *testing.T) {
	svc := newTestSnapshotSvc(newMockSnapshotRepo(), newMockDiskRepo())
	err := svc.Delete(context.Background(), "nonexistent")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

func TestSnapshot_Delete_OK(t *testing.T) {
	diskRepo := newMockDiskRepo()
	diskRepo.data[testDiskUID] = &domain.Disk{UID: testDiskUID, State: domain.DiskStateReady}

	snapRepo := newMockSnapshotRepo()
	svc := newTestSnapshotSvc(snapRepo, diskRepo)

	snap, err := svc.Upsert(context.Background(), &domain.Snapshot{
		Name:     "snap-to-delete",
		FolderID: testFolderUID,
		DiskID:   testDiskUID,
	})
	require.NoError(t, err)

	err = svc.Delete(context.Background(), snap.UID)
	require.NoError(t, err)
}

func TestSnapshot_UpdateStatus(t *testing.T) {
	diskRepo := newMockDiskRepo()
	diskRepo.data[testDiskUID] = &domain.Disk{UID: testDiskUID, State: domain.DiskStateReady}

	snapRepo := newMockSnapshotRepo()
	svc := newTestSnapshotSvc(snapRepo, diskRepo)

	snap, err := svc.Upsert(context.Background(), &domain.Snapshot{
		Name:     "snap-status",
		FolderID: testFolderUID,
		DiskID:   testDiskUID,
	})
	require.NoError(t, err)

	updated, err := svc.UpdateStatus(context.Background(), &domain.Snapshot{
		UID:             snap.UID,
		State:           domain.SnapshotStateReady,
		ProgressPercent: 100,
	})
	require.NoError(t, err)
	assert.Equal(t, domain.SnapshotStateReady, updated.State)
}
