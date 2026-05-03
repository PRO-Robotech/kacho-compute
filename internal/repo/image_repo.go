package repo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// ImageRepo — реализация service.ImageRepo поверх pgxpool (read-only).
type ImageRepo struct {
	pool *pgxpool.Pool
}

// NewImageRepo создаёт ImageRepo.
func NewImageRepo(pool *pgxpool.Pool) *ImageRepo {
	return &ImageRepo{pool: pool}
}

func (r *ImageRepo) Get(ctx context.Context, id string) (*domain.Image, error) {
	const q = `SELECT id, name, description, family, os_type, size, status FROM images WHERE id = $1`
	row := r.pool.QueryRow(ctx, q, id)
	img, err := scanImage(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, service.ErrNotFound
	}
	return img, err
}

func (r *ImageRepo) List(ctx context.Context, filter string, page service.Pagination) ([]*domain.Image, string, error) {
	pageSize := page.PageSize
	if pageSize <= 0 || pageSize > 1000 {
		pageSize = 50
	}

	args := []any{}
	conditions := []string{"status = 1"} // IMAGE_STATUS_READY
	argIdx := 1

	// Простой фильтр по family: "family=ubuntu-2204-lts"
	if filter != "" {
		if strings.HasPrefix(filter, "family=") {
			family := strings.TrimPrefix(filter, "family=")
			conditions = append(conditions, fmt.Sprintf("family = $%d", argIdx))
			args = append(args, family)
			argIdx++
		}
	}

	// page_token для images использует (name, id) курсор
	if page.PageToken != "" {
		_, id, err := decodePageToken(page.PageToken)
		if err != nil {
			return nil, "", fmt.Errorf("invalid page_token: %w", err)
		}
		conditions = append(conditions, fmt.Sprintf("id > $%d", argIdx))
		args = append(args, id)
		argIdx++
	}

	where := "WHERE " + strings.Join(conditions, " AND ")
	q := fmt.Sprintf(`SELECT id, name, description, family, os_type, size, status FROM images %s ORDER BY name ASC, id ASC LIMIT $%d`,
		where, argIdx)
	args = append(args, pageSize+1)

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var result []*domain.Image
	for rows.Next() {
		img, err := scanImage(rows)
		if err != nil {
			return nil, "", err
		}
		result = append(result, img)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var nextToken string
	if int64(len(result)) > pageSize {
		last := result[pageSize-1]
		// Для images используем нулевое время, ID как курсор
		nextToken = encodePageToken(last.CreatedAt(), last.ID)
		result = result[:pageSize]
	}
	return result, nextToken, nil
}

func scanImage(row scannable) (*domain.Image, error) {
	var img domain.Image
	var statusInt int32
	err := row.Scan(&img.ID, &img.Name, &img.Description, &img.Family, &img.OsType, &img.Size, &statusInt)
	if err != nil {
		return nil, err
	}
	img.Status = domain.ImageStatus(statusInt)
	return &img, nil
}
