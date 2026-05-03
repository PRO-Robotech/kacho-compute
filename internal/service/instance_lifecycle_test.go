package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	svc "github.com/PRO-Robotech/kacho-compute/internal/service"
)

func TestInstanceService_Restart(t *testing.T) {
	instRepo := newMockInstanceRepo()
	s := svc.NewInstanceService(instRepo, newMockDiskRepo(), newMockFolderClient("f1"), newMockSubnetClient(), newMockOpsRepo())
	ctx := context.Background()

	now := time.Now().UTC()
	inst := &domain.Instance{
		ID:       "inst-restart",
		FolderID: "f1",
		Name:     "test",
		Status:   domain.InstanceStatusRunning,
		CreatedAt: now,
		StatusLastTransitionAt: now,
	}
	_, err := instRepo.Insert(ctx, inst)
	require.NoError(t, err)

	op, err := s.Restart(ctx, "inst-restart")
	require.NoError(t, err)
	assert.NotEmpty(t, op.ID)

	// После Restart инстанс должен быть в STOPPING (первый шаг restart cycle).
	updated, err := instRepo.Get(ctx, "inst-restart")
	require.NoError(t, err)
	assert.Equal(t, domain.InstanceStatusStopping, updated.Status)
	// DesiredPowerState должен быть RUNNING (reconciler вернёт в RUNNING после restart).
	assert.Equal(t, domain.PowerStateRunning, updated.DesiredPowerState)
}

func TestInstanceService_Update_Success(t *testing.T) {
	instRepo := newMockInstanceRepo()
	s := svc.NewInstanceService(instRepo, newMockDiskRepo(), newMockFolderClient("f1"), newMockSubnetClient(), newMockOpsRepo())
	ctx := context.Background()

	inst := &domain.Instance{
		ID:              "inst-upd",
		FolderID:        "f1",
		Name:            "old-name",
		Status:          domain.InstanceStatusRunning,
		ResourceVersion: "rv-original",
	}
	_, err := instRepo.Insert(ctx, inst)
	require.NoError(t, err)

	op, err := s.Update(ctx, svc.UpdateInstanceReq{
		InstanceID:      "inst-upd",
		ResourceVersion: "rv-original",
		Name:            "new-name",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, op.ID)
}

func TestInstanceService_Delete_Success(t *testing.T) {
	instRepo := newMockInstanceRepo()
	s := svc.NewInstanceService(instRepo, newMockDiskRepo(), newMockFolderClient("f1"), newMockSubnetClient(), newMockOpsRepo())
	ctx := context.Background()

	now := time.Now().UTC()
	inst := &domain.Instance{
		ID:       "inst-del",
		FolderID: "f1",
		Name:     "to-delete",
		Status:   domain.InstanceStatusRunning,
		CreatedAt: now,
		StatusLastTransitionAt: now,
	}
	_, err := instRepo.Insert(ctx, inst)
	require.NoError(t, err)

	op, err := s.Delete(ctx, "inst-del")
	require.NoError(t, err)
	assert.NotEmpty(t, op.ID)

	updated, err := instRepo.Get(ctx, "inst-del")
	require.NoError(t, err)
	assert.Equal(t, domain.InstanceStatusDeleting, updated.Status)
	assert.NotNil(t, updated.DeletedAt)
}

func TestDiskService_Update_Success(t *testing.T) {
	diskRepo := newMockDiskRepo()
	s := svc.NewDiskService(diskRepo, newMockImageRepo(), newMockFolderClient("f1"), newMockOpsRepo())
	ctx := context.Background()

	d := &domain.Disk{
		ID:              "disk-upd",
		FolderID:        "f1",
		Name:            "old-name",
		Status:          domain.DiskStatusReady,
		ResourceVersion: "rv1",
	}
	_, err := diskRepo.Insert(ctx, d)
	require.NoError(t, err)

	op, err := s.Update(ctx, svc.UpdateDiskReq{
		DiskID:          "disk-upd",
		ResourceVersion: "rv1",
		Name:            "new-name",
		Description:     "updated",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, op.ID)
}

func TestDiskService_Update_Conflict(t *testing.T) {
	diskRepo := newMockDiskRepo()
	s := svc.NewDiskService(diskRepo, newMockImageRepo(), newMockFolderClient("f1"), newMockOpsRepo())
	ctx := context.Background()

	d := &domain.Disk{
		ID:              "disk-conflict",
		FolderID:        "f1",
		Name:            "disk",
		Status:          domain.DiskStatusReady,
		ResourceVersion: "rv1",
	}
	_, err := diskRepo.Insert(ctx, d)
	require.NoError(t, err)

	_, err = s.Update(ctx, svc.UpdateDiskReq{
		DiskID:          "disk-conflict",
		ResourceVersion: "wrong-rv",
		Name:            "new",
	})
	require.Error(t, err)
}

func TestInstanceService_List_WithFolderFilter(t *testing.T) {
	instRepo := newMockInstanceRepo()
	s := svc.NewInstanceService(instRepo, newMockDiskRepo(), newMockFolderClient("f1"), newMockSubnetClient(), newMockOpsRepo())
	ctx := context.Background()

	for i, fid := range []string{"f1", "f1", "f2"} {
		inst := &domain.Instance{
			ID:       "inst-" + string(rune('a'+i)),
			FolderID: fid,
			Name:     "inst",
		}
		_, _ = instRepo.Insert(ctx, inst)
	}

	insts, _, err := s.List(ctx, svc.InstanceFilter{FolderID: "f1"}, svc.Pagination{PageSize: 10})
	require.NoError(t, err)
	assert.Len(t, insts, 2)
}
