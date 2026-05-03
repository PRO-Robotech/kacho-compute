package service

import (
	"context"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
)

// ImageService — бизнес-логика каталога образов (read-only).
type ImageService struct {
	repo ImageRepo
}

// NewImageService создаёт ImageService.
func NewImageService(repo ImageRepo) *ImageService {
	return &ImageService{repo: repo}
}

// Get возвращает Image по ID.
func (s *ImageService) Get(ctx context.Context, id string) (*domain.Image, error) {
	img, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return img, nil
}

// List возвращает список Image.
func (s *ImageService) List(ctx context.Context, filter string, page Pagination) ([]*domain.Image, string, error) {
	return s.repo.List(ctx, filter, page)
}
