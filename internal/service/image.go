package service

import (
	"context"

	coreerrors "github.com/PRO-Robotech/kacho-corelib/errors"
	"github.com/PRO-Robotech/kacho-compute/internal/domain"
)

// ImageService реализует use-cases для Image (read-only).
type ImageService struct {
	repo ImageRepo
}

// NewImageService создаёт ImageService.
func NewImageService(repo ImageRepo) *ImageService {
	return &ImageService{repo: repo}
}

// GetByUID возвращает образ по UID.
func (s *ImageService) GetByUID(ctx context.Context, uid string) (*domain.Image, error) {
	if uid == "" {
		return nil, coreerrors.InvalidArgument().AddFieldViolation("uid", "uid is required").Err()
	}
	img, err := s.repo.GetByUID(ctx, uid)
	if err != nil {
		return nil, err
	}
	if img == nil {
		return nil, coreerrors.NotFound("Image", uid).Err()
	}
	return img, nil
}

// List возвращает список образов.
func (s *ImageService) List(ctx context.Context, selectors []Selector, page Pagination) ([]*domain.Image, string, int64, error) {
	return s.repo.List(ctx, selectors, page)
}

// SnapshotResourceVersion возвращает текущий resource version.
func (s *ImageService) SnapshotResourceVersion(ctx context.Context) (int64, error) {
	return s.repo.SnapshotResourceVersion(ctx)
}
