package handler_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/handler"
	svc "github.com/PRO-Robotech/kacho-compute/internal/service"
	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	grpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/types/known/anypb"
)

// ---- mock repos and clients ----

type memInstanceRepo struct {
	mu        sync.Mutex
	instances map[string]*domain.Instance
}

func newMemInstanceRepo() *memInstanceRepo {
	return &memInstanceRepo{instances: make(map[string]*domain.Instance)}
}

func (m *memInstanceRepo) Get(ctx context.Context, id string) (*domain.Instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.instances[id]; ok {
		cp := *v
		return &cp, nil
	}
	return nil, svc.ErrNotFound
}
func (m *memInstanceRepo) List(ctx context.Context, f svc.InstanceFilter, p svc.Pagination) ([]*domain.Instance, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*domain.Instance
	for _, v := range m.instances {
		if f.FolderID == "" || v.FolderID == f.FolderID {
			cp := *v
			result = append(result, &cp)
		}
	}
	return result, "", nil
}
func (m *memInstanceRepo) Insert(ctx context.Context, inst *domain.Instance) (*domain.Instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *inst
	m.instances[inst.ID] = &cp
	return &cp, nil
}
func (m *memInstanceRepo) Update(ctx context.Context, inst *domain.Instance) (*domain.Instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *inst
	m.instances[inst.ID] = &cp
	return &cp, nil
}
func (m *memInstanceRepo) ListPendingReconcile(ctx context.Context, limit int) ([]*domain.Instance, error) {
	return nil, nil
}

type memDiskRepo struct {
	mu    sync.Mutex
	disks map[string]*domain.Disk
}

func newMemDiskRepo() *memDiskRepo {
	return &memDiskRepo{disks: make(map[string]*domain.Disk)}
}

func (m *memDiskRepo) Get(ctx context.Context, id string) (*domain.Disk, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.disks[id]; ok {
		cp := *v
		return &cp, nil
	}
	return nil, svc.ErrNotFound
}
func (m *memDiskRepo) List(ctx context.Context, f svc.DiskFilter, p svc.Pagination) ([]*domain.Disk, string, error) {
	return nil, "", nil
}
func (m *memDiskRepo) Insert(ctx context.Context, d *domain.Disk) (*domain.Disk, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *d
	m.disks[d.ID] = &cp
	return &cp, nil
}
func (m *memDiskRepo) Update(ctx context.Context, d *domain.Disk) (*domain.Disk, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *d
	m.disks[d.ID] = &cp
	return &cp, nil
}
func (m *memDiskRepo) ListPendingReconcile(ctx context.Context, limit int) ([]*domain.Disk, error) {
	return nil, nil
}

type alwaysExistsFolder struct{}
func (a *alwaysExistsFolder) Exists(ctx context.Context, id string) (bool, error) { return true, nil }

type alwaysExistsSubnet struct{}
func (a *alwaysExistsSubnet) Exists(ctx context.Context, id string) (bool, error) { return true, nil }

type memOpsRepo struct {
	mu  sync.Mutex
	ops map[string]*operations.Operation
}

func newMemOpsRepo() *memOpsRepo {
	return &memOpsRepo{ops: make(map[string]*operations.Operation)}
}
func (m *memOpsRepo) Create(ctx context.Context, op operations.Operation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := op
	m.ops[op.ID] = &cp
	return nil
}
func (m *memOpsRepo) Get(ctx context.Context, id string) (*operations.Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.ops[id]; ok {
		cp := *v
		return &cp, nil
	}
	return nil, operations.ErrNotFound
}
func (m *memOpsRepo) List(ctx context.Context, f operations.ListFilter) ([]operations.Operation, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []operations.Operation
	for _, op := range m.ops {
		result = append(result, *op)
	}
	return result, "", nil
}
func (m *memOpsRepo) MarkDone(ctx context.Context, id string, _ *anypb.Any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if op, ok := m.ops[id]; ok {
		op.Done = true
	}
	return nil
}
func (m *memOpsRepo) MarkError(ctx context.Context, id string, _ *grpcstatus.Status) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if op, ok := m.ops[id]; ok {
		op.Done = true
	}
	return nil
}
func (m *memOpsRepo) Cancel(ctx context.Context, id string) error { return nil }

// ---- helper ----

func newTestInstanceHandler() *handler.InstanceHandler {
	instRepo := newMemInstanceRepo()
	diskRepo := newMemDiskRepo()
	opsRepo := newMemOpsRepo()
	s := svc.NewInstanceService(instRepo, diskRepo, &alwaysExistsFolder{}, &alwaysExistsSubnet{}, opsRepo)
	return handler.NewInstanceHandler(s)
}

// ---- tests ----

func TestInstanceHandler_Get_InvalidArg(t *testing.T) {
	h := newTestInstanceHandler()
	_, err := h.Get(context.Background(), &computev1.GetInstanceRequest{InstanceId: ""})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestInstanceHandler_Get_NotFound(t *testing.T) {
	h := newTestInstanceHandler()
	_, err := h.Get(context.Background(), &computev1.GetInstanceRequest{InstanceId: "nonexistent"})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

func TestInstanceHandler_List_Empty(t *testing.T) {
	h := newTestInstanceHandler()
	resp, err := h.List(context.Background(), &computev1.ListInstancesRequest{FolderId: "folder-1"})
	require.NoError(t, err)
	assert.Empty(t, resp.Instances)
}

func TestInstanceHandler_Create_NoFolder(t *testing.T) {
	h := newTestInstanceHandler()
	_, err := h.Create(context.Background(), &computev1.CreateInstanceRequest{
		Name: "test",
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestInstanceHandler_Create_Success(t *testing.T) {
	h := newTestInstanceHandler()
	op, err := h.Create(context.Background(), &computev1.CreateInstanceRequest{
		FolderId: "folder-1",
		Name:     "my-instance",
		ZoneId:   "kacho-zone-a",
		Resources: &computev1.Resources{
			Cores:  2,
			Memory: "4Gi",
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, op.Id)
	assert.False(t, op.Done)
}

func TestInstanceHandler_Delete_InvalidArg(t *testing.T) {
	h := newTestInstanceHandler()
	_, err := h.Delete(context.Background(), &computev1.DeleteInstanceRequest{InstanceId: ""})
	require.Error(t, err)
}

func TestInstanceHandler_Start_InvalidArg(t *testing.T) {
	h := newTestInstanceHandler()
	_, err := h.Start(context.Background(), &computev1.StartInstanceRequest{InstanceId: ""})
	require.Error(t, err)
}

func TestInstanceHandler_Stop_InvalidArg(t *testing.T) {
	h := newTestInstanceHandler()
	_, err := h.Stop(context.Background(), &computev1.StopInstanceRequest{InstanceId: ""})
	require.Error(t, err)
}

func TestInstanceHandler_Restart_InvalidArg(t *testing.T) {
	h := newTestInstanceHandler()
	_, err := h.Restart(context.Background(), &computev1.RestartInstanceRequest{InstanceId: ""})
	require.Error(t, err)
}
