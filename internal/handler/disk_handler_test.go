package handler_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/handler"
	svc "github.com/PRO-Robotech/kacho-compute/internal/service"
	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
)

func newTestDiskHandler() *handler.DiskHandler {
	diskRepo := newMemDiskRepo()
	opsRepo := newMemOpsRepo()
	imageRepo := newMemImageRepo()
	s := svc.NewDiskService(diskRepo, imageRepo, &alwaysExistsFolder{}, opsRepo)
	return handler.NewDiskHandler(s)
}

type memImageRepo struct{}

func newMemImageRepo() *memImageRepo { return &memImageRepo{} }

func (m *memImageRepo) Get(ctx context.Context, id string) (*domain.Image, error) {
	return nil, svc.ErrNotFound
}
func (m *memImageRepo) List(ctx context.Context, filter string, page svc.Pagination) ([]*domain.Image, string, error) {
	return nil, "", nil
}

func TestDiskHandler_Get_InvalidArg(t *testing.T) {
	h := newTestDiskHandler()
	_, err := h.Get(context.Background(), &computev1.GetDiskRequest{DiskId: ""})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestDiskHandler_Get_NotFound(t *testing.T) {
	h := newTestDiskHandler()
	_, err := h.Get(context.Background(), &computev1.GetDiskRequest{DiskId: "nonexistent"})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

func TestDiskHandler_List(t *testing.T) {
	h := newTestDiskHandler()
	resp, err := h.List(context.Background(), &computev1.ListDisksRequest{FolderId: "f1"})
	require.NoError(t, err)
	assert.Empty(t, resp.Disks)
}

func TestDiskHandler_Create_NoFolder(t *testing.T) {
	h := newTestDiskHandler()
	_, err := h.Create(context.Background(), &computev1.CreateDiskRequest{Name: "disk"})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestDiskHandler_Create_Success(t *testing.T) {
	h := newTestDiskHandler()
	op, err := h.Create(context.Background(), &computev1.CreateDiskRequest{
		FolderId:   "folder-1",
		Name:       "my-disk",
		DiskTypeId: "network-ssd",
		ZoneId:     "kacho-zone-a",
		Size:       "10Gi",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, op.Id)
}

func TestDiskHandler_Update_InvalidArg(t *testing.T) {
	h := newTestDiskHandler()
	_, err := h.Update(context.Background(), &computev1.UpdateDiskRequest{})
	require.Error(t, err)
}

func TestDiskHandler_Delete_InvalidArg(t *testing.T) {
	h := newTestDiskHandler()
	_, err := h.Delete(context.Background(), &computev1.DeleteDiskRequest{})
	require.Error(t, err)
}
