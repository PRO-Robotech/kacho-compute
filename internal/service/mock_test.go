package service_test

import (
	"context"
	"sync"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	svc "github.com/PRO-Robotech/kacho-compute/internal/service"
	grpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/types/known/anypb"
)

// ---- mock InstanceRepo ----

type mockInstanceRepo struct {
	mu        sync.Mutex
	instances map[string]*domain.Instance
	pending   []*domain.Instance
}

func newMockInstanceRepo() *mockInstanceRepo {
	return &mockInstanceRepo{instances: make(map[string]*domain.Instance)}
}

func (m *mockInstanceRepo) Get(ctx context.Context, id string) (*domain.Instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst, ok := m.instances[id]; ok {
		cp := *inst
		return &cp, nil
	}
	return nil, svc.ErrNotFound
}

func (m *mockInstanceRepo) List(ctx context.Context, f svc.InstanceFilter, page svc.Pagination) ([]*domain.Instance, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*domain.Instance
	for _, inst := range m.instances {
		if f.FolderID == "" || inst.FolderID == f.FolderID {
			cp := *inst
			result = append(result, &cp)
		}
	}
	return result, "", nil
}

func (m *mockInstanceRepo) Insert(ctx context.Context, inst *domain.Instance) (*domain.Instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *inst
	m.instances[inst.ID] = &cp
	return &cp, nil
}

func (m *mockInstanceRepo) Update(ctx context.Context, inst *domain.Instance) (*domain.Instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *inst
	m.instances[inst.ID] = &cp
	return &cp, nil
}

func (m *mockInstanceRepo) ListPendingReconcile(ctx context.Context, limit int) ([]*domain.Instance, error) {
	return m.pending, nil
}

// ---- mock DiskRepo ----

type mockDiskRepo struct {
	mu    sync.Mutex
	disks map[string]*domain.Disk
}

func newMockDiskRepo() *mockDiskRepo {
	return &mockDiskRepo{disks: make(map[string]*domain.Disk)}
}

func (m *mockDiskRepo) Get(ctx context.Context, id string) (*domain.Disk, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.disks[id]; ok {
		cp := *d
		return &cp, nil
	}
	return nil, svc.ErrNotFound
}

func (m *mockDiskRepo) List(ctx context.Context, f svc.DiskFilter, page svc.Pagination) ([]*domain.Disk, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*domain.Disk
	for _, d := range m.disks {
		cp := *d
		result = append(result, &cp)
	}
	return result, "", nil
}

func (m *mockDiskRepo) Insert(ctx context.Context, d *domain.Disk) (*domain.Disk, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *d
	m.disks[d.ID] = &cp
	return &cp, nil
}

func (m *mockDiskRepo) Update(ctx context.Context, d *domain.Disk) (*domain.Disk, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *d
	m.disks[d.ID] = &cp
	return &cp, nil
}

func (m *mockDiskRepo) ListPendingReconcile(ctx context.Context, limit int) ([]*domain.Disk, error) {
	return nil, nil
}

// ---- mock ImageRepo ----

type mockImageRepo struct {
	images map[string]*domain.Image
}

func newMockImageRepo() *mockImageRepo {
	return &mockImageRepo{images: make(map[string]*domain.Image)}
}

func (m *mockImageRepo) Get(ctx context.Context, id string) (*domain.Image, error) {
	if img, ok := m.images[id]; ok {
		return img, nil
	}
	return nil, svc.ErrNotFound
}

func (m *mockImageRepo) List(ctx context.Context, filter string, page svc.Pagination) ([]*domain.Image, string, error) {
	var result []*domain.Image
	for _, img := range m.images {
		result = append(result, img)
	}
	return result, "", nil
}

// ---- mock SnapshotRepo ----

type mockSnapshotRepo struct {
	mu        sync.Mutex
	snapshots map[string]*domain.Snapshot
}

func newMockSnapshotRepo() *mockSnapshotRepo {
	return &mockSnapshotRepo{snapshots: make(map[string]*domain.Snapshot)}
}

func (m *mockSnapshotRepo) Get(ctx context.Context, id string) (*domain.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.snapshots[id]; ok {
		cp := *s
		return &cp, nil
	}
	return nil, svc.ErrNotFound
}

func (m *mockSnapshotRepo) List(ctx context.Context, f svc.SnapshotFilter, page svc.Pagination) ([]*domain.Snapshot, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*domain.Snapshot
	for _, s := range m.snapshots {
		cp := *s
		result = append(result, &cp)
	}
	return result, "", nil
}

func (m *mockSnapshotRepo) Insert(ctx context.Context, s *domain.Snapshot) (*domain.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *s
	m.snapshots[s.ID] = &cp
	return &cp, nil
}

func (m *mockSnapshotRepo) Update(ctx context.Context, s *domain.Snapshot) (*domain.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *s
	m.snapshots[s.ID] = &cp
	return &cp, nil
}

func (m *mockSnapshotRepo) ListPendingReconcile(ctx context.Context, limit int) ([]*domain.Snapshot, error) {
	return nil, nil
}

// ---- mock FolderClient ----

type mockFolderClient struct {
	exists map[string]bool
}

func newMockFolderClient(folderIDs ...string) *mockFolderClient {
	m := &mockFolderClient{exists: make(map[string]bool)}
	for _, id := range folderIDs {
		m.exists[id] = true
	}
	return m
}

func (m *mockFolderClient) Exists(ctx context.Context, folderID string) (bool, error) {
	return m.exists[folderID], nil
}

// ---- mock SubnetClient ----

type mockSubnetClient struct {
	exists map[string]bool
}

func newMockSubnetClient(subnetIDs ...string) *mockSubnetClient {
	m := &mockSubnetClient{exists: make(map[string]bool)}
	for _, id := range subnetIDs {
		m.exists[id] = true
	}
	return m
}

func (m *mockSubnetClient) Exists(ctx context.Context, subnetID string) (bool, error) {
	return m.exists[subnetID], nil
}

// ---- mock OpsRepo ----

type mockOpsRepo struct {
	mu  sync.Mutex
	ops map[string]*operations.Operation
}

func newMockOpsRepo() *mockOpsRepo {
	return &mockOpsRepo{ops: make(map[string]*operations.Operation)}
}

func (m *mockOpsRepo) Create(ctx context.Context, op operations.Operation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := op
	m.ops[op.ID] = &cp
	return nil
}

func (m *mockOpsRepo) Get(ctx context.Context, id string) (*operations.Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if op, ok := m.ops[id]; ok {
		cp := *op
		return &cp, nil
	}
	return nil, operations.ErrNotFound
}

func (m *mockOpsRepo) List(ctx context.Context, filter operations.ListFilter) ([]operations.Operation, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []operations.Operation
	for _, op := range m.ops {
		result = append(result, *op)
	}
	return result, "", nil
}

func (m *mockOpsRepo) MarkDone(ctx context.Context, id string, _ *anypb.Any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if op, ok := m.ops[id]; ok {
		op.Done = true
	}
	return nil
}

func (m *mockOpsRepo) MarkError(ctx context.Context, id string, _ *grpcstatus.Status) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if op, ok := m.ops[id]; ok {
		op.Done = true
	}
	return nil
}

func (m *mockOpsRepo) Cancel(ctx context.Context, id string) error {
	return nil
}
