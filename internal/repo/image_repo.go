package repo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	coreerrors "github.com/PRO-Robotech/kacho-corelib/errors"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	sqlcgen "github.com/PRO-Robotech/kacho-compute/internal/repo/gen"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// imageSpec хранит spec образа в JSONB.
type imageSpec struct {
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Family      string `json:"family"`
	ZoneID      string `json:"zone_id"`
	Size        string `json:"size"`
}

// ImageRepo реализует service.ImageRepo.
type ImageRepo struct {
	pool *pgxpool.Pool
}

// NewImageRepo создаёт ImageRepo.
func NewImageRepo(pool *pgxpool.Pool) *ImageRepo {
	return &ImageRepo{pool: pool}
}

func (r *ImageRepo) GetByUID(ctx context.Context, uid string) (*domain.Image, error) {
	pgUID, err := strToUUID(uid)
	if err != nil {
		return nil, coreerrors.InvalidArgument().AddFieldViolation("uid", "invalid uuid").Err()
	}
	q := sqlcgen.New(r.pool)
	row, err := q.GetImageByUID(ctx, pgUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return sqlcImageToDomain(row), nil
}

func (r *ImageRepo) List(ctx context.Context, _ []service.Selector, page service.Pagination) ([]*domain.Image, string, int64, error) {
	pageSize := int(page.PageSize)
	if pageSize <= 0 || pageSize > 1000 {
		pageSize = 100
	}

	snapshotRV, err := r.SnapshotResourceVersion(ctx)
	if err != nil {
		return nil, "", 0, err
	}

	q := sqlcgen.New(r.pool)
	rows, err := q.ListImages(ctx)
	if err != nil {
		return nil, "", 0, err
	}

	var images []*domain.Image
	for _, row := range rows {
		images = append(images, sqlcImageToDomain(row))
	}

	// simple truncation without pagination token for images catalog
	var nextToken string
	if len(images) > pageSize {
		images = images[:pageSize]
		last := images[len(images)-1]
		nextToken = fmt.Sprintf("%d", last.ResourceVersion)
	}

	return images, nextToken, snapshotRV, nil
}

func (r *ImageRepo) SnapshotResourceVersion(ctx context.Context) (int64, error) {
	q := sqlcgen.New(r.pool)
	return q.SnapshotResourceVersion(ctx)
}

func sqlcImageToDomain(row sqlcgen.ImagesCatalog) *domain.Image {
	var sp imageSpec
	jsonbToAny(row.Spec, &sp)

	return &domain.Image{
		UID:               uuidToStr(row.Uid),
		Name:              row.Name,
		Labels:            jsonbToMap(row.Labels),
		CreationTimestamp: tsToTime(row.CreationTimestamp),
		ResourceVersion:   row.ResourceVersion,
		Generation:        row.Generation,

		DisplayName: sp.DisplayName,
		Description: sp.Description,
		Family:      sp.Family,
		ZoneID:      sp.ZoneID,
		Size:        sp.Size,

		State: domain.ImageStateReady,
	}
}
