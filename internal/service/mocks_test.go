package service_test

import (
	"context"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// mockInstanceRepo — мок для service.InstanceRepo.
type mockInstanceRepo struct {
	data       map[string]*domain.Instance
	byFolderName map[string]*domain.Instance
}

func newMockInstanceRepo() *mockInstanceRepo {
	return &mockInstanceRepo{
		data:         make(map[string]*domain.Instance),
		byFolderName: make(map[string]*domain.Instance),
	}
}

func (m *mockInstanceRepo) GetByUID(_ context.Context, uid string) (*domain.Instance, error) {
	return m.data[uid], nil
}

func (m *mockInstanceRepo) GetByFolderAndName(_ context.Context, folderID, name string) (*domain.Instance, error) {
	return m.byFolderName[folderID+"/"+name], nil
}

func (m *mockInstanceRepo) List(_ context.Context, _ []service.Selector, _ service.Pagination) ([]*domain.Instance, string, int64, error) {
	var result []*domain.Instance
	for _, v := range m.data {
		result = append(result, v)
	}
	return result, "", 1, nil
}

func (m *mockInstanceRepo) SnapshotResourceVersion(_ context.Context) (int64, error) { return 1, nil }

func (m *mockInstanceRepo) Insert(_ context.Context, inst *domain.Instance) (*domain.Instance, error) {
	m.data[inst.UID] = inst
	m.byFolderName[inst.FolderID+"/"+inst.Name] = inst
	return inst, nil
}

func (m *mockInstanceRepo) Update(_ context.Context, inst *domain.Instance) (*domain.Instance, error) {
	m.data[inst.UID] = inst
	return inst, nil
}

func (m *mockInstanceRepo) UpdateStatus(_ context.Context, inst *domain.Instance) (*domain.Instance, error) {
	if existing, ok := m.data[inst.UID]; ok {
		existing.State = inst.State
		existing.StateLastTransitionAt = inst.StateLastTransitionAt
		return existing, nil
	}
	return inst, nil
}

func (m *mockInstanceRepo) UpdateMetadata(_ context.Context, uid string, finalizers []string, updateFinalizers bool, _ *string) (*domain.Instance, error) {
	if inst, ok := m.data[uid]; ok {
		if updateFinalizers {
			inst.Finalizers = finalizers
		}
		return inst, nil
	}
	return nil, nil
}

func (m *mockInstanceRepo) SoftDelete(_ context.Context, uid string) error {
	delete(m.data, uid)
	return nil
}

func (m *mockInstanceRepo) HardDelete(_ context.Context, uid string) error {
	delete(m.data, uid)
	return nil
}

func (m *mockInstanceRepo) ListPendingReconcile(_ context.Context) ([]*domain.Instance, error) {
	return nil, nil
}

func (m *mockInstanceRepo) SetRestart(_ context.Context, uid string) (*domain.Instance, error) {
	if inst, ok := m.data[uid]; ok {
		return inst, nil
	}
	return nil, nil
}

// mockDiskRepo — мок для service.DiskRepo.
type mockDiskRepo struct {
	data         map[string]*domain.Disk
	byFolderName map[string]*domain.Disk
}

func newMockDiskRepo() *mockDiskRepo {
	return &mockDiskRepo{
		data:         make(map[string]*domain.Disk),
		byFolderName: make(map[string]*domain.Disk),
	}
}

func (m *mockDiskRepo) GetByUID(_ context.Context, uid string) (*domain.Disk, error) {
	return m.data[uid], nil
}

func (m *mockDiskRepo) GetByFolderAndName(_ context.Context, folderID, name string) (*domain.Disk, error) {
	return m.byFolderName[folderID+"/"+name], nil
}

func (m *mockDiskRepo) List(_ context.Context, _ []service.Selector, _ service.Pagination) ([]*domain.Disk, string, int64, error) {
	return nil, "", 1, nil
}

func (m *mockDiskRepo) SnapshotResourceVersion(_ context.Context) (int64, error) { return 1, nil }

func (m *mockDiskRepo) Insert(_ context.Context, disk *domain.Disk) (*domain.Disk, error) {
	m.data[disk.UID] = disk
	m.byFolderName[disk.FolderID+"/"+disk.Name] = disk
	return disk, nil
}

func (m *mockDiskRepo) Update(_ context.Context, disk *domain.Disk) (*domain.Disk, error) {
	m.data[disk.UID] = disk
	return disk, nil
}

func (m *mockDiskRepo) UpdateStatus(_ context.Context, disk *domain.Disk) (*domain.Disk, error) {
	if existing, ok := m.data[disk.UID]; ok {
		existing.State = disk.State
		return existing, nil
	}
	return disk, nil
}

func (m *mockDiskRepo) SoftDelete(_ context.Context, uid string) error {
	delete(m.data, uid)
	return nil
}

func (m *mockDiskRepo) HardDelete(_ context.Context, uid string) error {
	delete(m.data, uid)
	return nil
}

func (m *mockDiskRepo) ListPendingReconcile(_ context.Context) ([]*domain.Disk, error) { return nil, nil }

func (m *mockDiskRepo) ListAttachedToInstance(_ context.Context, _ string) ([]*domain.Disk, error) {
	return nil, nil
}

func (m *mockDiskRepo) HasSnapshots(_ context.Context, _ string) (bool, error) { return false, nil }

// mockImageRepo — мок для service.ImageRepo.
type mockImageRepo struct {
	data map[string]*domain.Image
}

func newMockImageRepo() *mockImageRepo {
	return &mockImageRepo{data: make(map[string]*domain.Image)}
}

func (m *mockImageRepo) GetByUID(_ context.Context, uid string) (*domain.Image, error) {
	return m.data[uid], nil
}

func (m *mockImageRepo) List(_ context.Context, _ []service.Selector, _ service.Pagination) ([]*domain.Image, string, int64, error) {
	return nil, "", 1, nil
}

func (m *mockImageRepo) SnapshotResourceVersion(_ context.Context) (int64, error) { return 1, nil }

// mockSnapshotRepo — мок для service.SnapshotRepo.
type mockSnapshotRepo struct {
	data         map[string]*domain.Snapshot
	byFolderName map[string]*domain.Snapshot
}

func newMockSnapshotRepo() *mockSnapshotRepo {
	return &mockSnapshotRepo{
		data:         make(map[string]*domain.Snapshot),
		byFolderName: make(map[string]*domain.Snapshot),
	}
}

func (m *mockSnapshotRepo) GetByUID(_ context.Context, uid string) (*domain.Snapshot, error) {
	return m.data[uid], nil
}

func (m *mockSnapshotRepo) GetByFolderAndName(_ context.Context, folderID, name string) (*domain.Snapshot, error) {
	return m.byFolderName[folderID+"/"+name], nil
}

func (m *mockSnapshotRepo) List(_ context.Context, _ []service.Selector, _ service.Pagination) ([]*domain.Snapshot, string, int64, error) {
	return nil, "", 1, nil
}

func (m *mockSnapshotRepo) SnapshotResourceVersion(_ context.Context) (int64, error) { return 1, nil }

func (m *mockSnapshotRepo) Insert(_ context.Context, snap *domain.Snapshot) (*domain.Snapshot, error) {
	m.data[snap.UID] = snap
	m.byFolderName[snap.FolderID+"/"+snap.Name] = snap
	return snap, nil
}

func (m *mockSnapshotRepo) Update(_ context.Context, snap *domain.Snapshot) (*domain.Snapshot, error) {
	m.data[snap.UID] = snap
	return snap, nil
}

func (m *mockSnapshotRepo) UpdateStatus(_ context.Context, snap *domain.Snapshot) (*domain.Snapshot, error) {
	if existing, ok := m.data[snap.UID]; ok {
		existing.State = snap.State
		existing.ProgressPercent = snap.ProgressPercent
		return existing, nil
	}
	return snap, nil
}

func (m *mockSnapshotRepo) SoftDelete(_ context.Context, uid string) error {
	delete(m.data, uid)
	return nil
}

func (m *mockSnapshotRepo) HardDelete(_ context.Context, uid string) error {
	delete(m.data, uid)
	return nil
}

func (m *mockSnapshotRepo) ListPendingReconcile(_ context.Context) ([]*domain.Snapshot, error) {
	return nil, nil
}

// mockFolderClient — мок для service.FolderClient.
type mockFolderClient struct {
	existsFunc func(uid string) bool
}

func (m *mockFolderClient) Exists(_ context.Context, uid string) (bool, error) {
	if m.existsFunc != nil {
		return m.existsFunc(uid), nil
	}
	return true, nil
}

// mockSubnetClient — мок для service.SubnetClient.
type mockSubnetClient struct {
	existsFunc func(uid string) bool
}

func (m *mockSubnetClient) Exists(_ context.Context, uid string) (bool, error) {
	if m.existsFunc != nil {
		return m.existsFunc(uid), nil
	}
	return true, nil
}
