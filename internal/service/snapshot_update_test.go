package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	svc "github.com/PRO-Robotech/kacho-compute/internal/service"
)

func TestSnapshotService_Update_Success(t *testing.T) {
	snapRepo := newMockSnapshotRepo()
	ctx := context.Background()

	snap := &domain.Snapshot{
		ID:              "snap-update",
		FolderID:        "f1",
		Name:            "original",
		Status:          domain.SnapshotStatusReady,
		Generation:      1,
		ResourceVersion: "rv1",
	}
	_, _ = snapRepo.Insert(ctx, snap)

	s := svc.NewSnapshotService(snapRepo, newMockDiskRepo(), newMockOpsRepo())
	op, err := s.Update(ctx, svc.UpdateSnapshotReq{
		SnapshotID:      "snap-update",
		ResourceVersion: "rv1",
		Name:            "updated",
		Description:     "new desc",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, op.ID)
}

func TestSnapshotService_Update_Conflict(t *testing.T) {
	snapRepo := newMockSnapshotRepo()
	ctx := context.Background()

	snap := &domain.Snapshot{
		ID:              "snap-conflict",
		FolderID:        "f1",
		Name:            "original",
		Status:          domain.SnapshotStatusReady,
		ResourceVersion: "rv1",
	}
	_, _ = snapRepo.Insert(ctx, snap)

	s := svc.NewSnapshotService(snapRepo, newMockDiskRepo(), newMockOpsRepo())
	_, err := s.Update(ctx, svc.UpdateSnapshotReq{
		SnapshotID:      "snap-conflict",
		ResourceVersion: "wrong-rv",
		Name:            "updated",
	})
	require.Error(t, err)
}

func TestSnapshotService_List(t *testing.T) {
	snapRepo := newMockSnapshotRepo()
	ctx := context.Background()

	snap := &domain.Snapshot{
		ID:       "snap-1",
		FolderID: "f1",
		Name:     "snap",
		Status:   domain.SnapshotStatusReady,
	}
	_, _ = snapRepo.Insert(ctx, snap)

	s := svc.NewSnapshotService(snapRepo, newMockDiskRepo(), newMockOpsRepo())
	snaps, _, err := s.List(ctx, svc.SnapshotFilter{FolderID: "f1"}, svc.Pagination{PageSize: 10})
	require.NoError(t, err)
	assert.Len(t, snaps, 1)
}
