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

func TestImageService_Get_NotFound(t *testing.T) {
	s := svc.NewImageService(newMockImageRepo())
	_, err := s.Get(context.Background(), "nonexistent")
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

func TestImageService_Get_Found(t *testing.T) {
	imgRepo := newMockImageRepo()
	imgRepo.images["img-1"] = &domain.Image{
		ID:     "img-1",
		Name:   "ubuntu-22-04-lts",
		Family: "ubuntu-2204-lts",
		Status: domain.ImageStatusReady,
	}

	s := svc.NewImageService(imgRepo)
	img, err := s.Get(context.Background(), "img-1")
	require.NoError(t, err)
	assert.Equal(t, "ubuntu-22-04-lts", img.Name)
}

func TestImageService_List_Empty(t *testing.T) {
	s := svc.NewImageService(newMockImageRepo())
	imgs, token, err := s.List(context.Background(), "", svc.Pagination{PageSize: 10})
	require.NoError(t, err)
	assert.Empty(t, imgs)
	assert.Empty(t, token)
}
