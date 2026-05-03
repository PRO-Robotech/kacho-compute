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

func newInstanceService() *svc.InstanceService {
	return svc.NewInstanceService(
		newMockInstanceRepo(),
		newMockDiskRepo(),
		newMockFolderClient("folder-1"),
		newMockSubnetClient("subnet-1"),
		newMockOpsRepo(),
	)
}

func TestInstanceService_Create_MissingFolder(t *testing.T) {
	s := newInstanceService()
	ctx := context.Background()

	_, err := s.Create(ctx, svc.CreateInstanceReq{
		FolderID: "",
		Name:     "test",
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestInstanceService_Create_MissingName(t *testing.T) {
	s := newInstanceService()
	ctx := context.Background()

	_, err := s.Create(ctx, svc.CreateInstanceReq{
		FolderID: "folder-1",
		Name:     "",
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestInstanceService_Create_FolderNotFound(t *testing.T) {
	s := svc.NewInstanceService(
		newMockInstanceRepo(),
		newMockDiskRepo(),
		newMockFolderClient(), // нет folder-2
		newMockSubnetClient(),
		newMockOpsRepo(),
	)
	ctx := context.Background()

	// Operation должна быть создана, но goroutine вернёт ошибку через MarkError.
	// Проверяем только что Op создаётся без ошибки при вызове.
	op, err := s.Create(ctx, svc.CreateInstanceReq{
		FolderID: "folder-2",
		Name:     "test",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, op.ID)
	assert.False(t, op.Done)
}

func TestInstanceService_Create_Success(t *testing.T) {
	s := newInstanceService()
	ctx := context.Background()

	op, err := s.Create(ctx, svc.CreateInstanceReq{
		FolderID:   "folder-1",
		Name:       "my-instance",
		ZoneID:     "kacho-zone-a",
		PlatformID: "standard-v1",
		Resources: domain.Resources{
			Cores:  2,
			Memory: "4Gi",
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, op.ID)
	assert.Equal(t, false, op.Done) // async
}

func TestInstanceService_Get_NotFound(t *testing.T) {
	s := newInstanceService()
	ctx := context.Background()

	_, err := s.Get(ctx, "nonexistent")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

func TestInstanceService_List_Empty(t *testing.T) {
	s := newInstanceService()
	ctx := context.Background()

	insts, token, err := s.List(ctx, svc.InstanceFilter{FolderID: "folder-1"}, svc.Pagination{PageSize: 10})
	require.NoError(t, err)
	assert.Empty(t, insts)
	assert.Empty(t, token)
}

func TestInstanceService_Delete_NotFound(t *testing.T) {
	s := newInstanceService()
	ctx := context.Background()

	_, err := s.Delete(ctx, "nonexistent")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

func TestInstanceService_Start_Stop(t *testing.T) {
	instRepo := newMockInstanceRepo()
	s := svc.NewInstanceService(instRepo, newMockDiskRepo(), newMockFolderClient("f1"), newMockSubnetClient(), newMockOpsRepo())
	ctx := context.Background()

	// Сначала создаём инстанс напрямую в репо.
	inst := &domain.Instance{
		ID:       "inst-1",
		FolderID: "f1",
		Name:     "test",
		Status:   domain.InstanceStatusRunning,
	}
	_, err := instRepo.Insert(ctx, inst)
	require.NoError(t, err)

	// Stop.
	op, err := s.Stop(ctx, "inst-1")
	require.NoError(t, err)
	assert.NotEmpty(t, op.ID)

	// Убеждаемся, что статус изменился в репо.
	updated, err := instRepo.Get(ctx, "inst-1")
	require.NoError(t, err)
	assert.Equal(t, domain.InstanceStatusStopping, updated.Status)
	assert.Equal(t, domain.PowerStateStopped, updated.DesiredPowerState)

	// Start.
	updated.Status = domain.InstanceStatusStopped
	_, err = instRepo.Update(ctx, updated)
	require.NoError(t, err)

	op, err = s.Start(ctx, "inst-1")
	require.NoError(t, err)
	assert.NotEmpty(t, op.ID)

	afterStart, err := instRepo.Get(ctx, "inst-1")
	require.NoError(t, err)
	assert.Equal(t, domain.InstanceStatusStarting, afterStart.Status)
}

func TestInstanceService_Update_VersionConflict(t *testing.T) {
	instRepo := newMockInstanceRepo()
	s := svc.NewInstanceService(instRepo, newMockDiskRepo(), newMockFolderClient("f1"), newMockSubnetClient(), newMockOpsRepo())
	ctx := context.Background()

	inst := &domain.Instance{
		ID:              "inst-2",
		FolderID:        "f1",
		Name:            "test",
		ResourceVersion: "rv-original",
	}
	_, err := instRepo.Insert(ctx, inst)
	require.NoError(t, err)

	_, err = s.Update(ctx, svc.UpdateInstanceReq{
		InstanceID:      "inst-2",
		ResourceVersion: "wrong-rv",
		Name:            "new-name",
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Aborted, st.Code())
}
