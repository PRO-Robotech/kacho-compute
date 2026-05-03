package service

import (
	"context"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
)

// Pagination — параметры постраничной выборки.
type Pagination struct {
	PageToken string
	PageSize  int64
}

// InstanceFilter — фильтр для списка инстансов.
type InstanceFilter struct {
	FolderID string
	Filter   string
	OrderBy  string
}

// DiskFilter — фильтр для списка дисков.
type DiskFilter struct {
	FolderID string
	Filter   string
	OrderBy  string
}

// SnapshotFilter — фильтр для списка снимков.
type SnapshotFilter struct {
	FolderID string
	Filter   string
	OrderBy  string
}

// InstanceRepo — port-интерфейс репозитория инстансов.
type InstanceRepo interface {
	Get(ctx context.Context, id string) (*domain.Instance, error)
	List(ctx context.Context, f InstanceFilter, page Pagination) ([]*domain.Instance, string, error)
	Insert(ctx context.Context, inst *domain.Instance) (*domain.Instance, error)
	Update(ctx context.Context, inst *domain.Instance) (*domain.Instance, error)
	// ListPendingReconcile возвращает инстансы, требующие работы reconciler-а.
	ListPendingReconcile(ctx context.Context, limit int) ([]*domain.Instance, error)
}

// DiskRepo — port-интерфейс репозитория дисков.
type DiskRepo interface {
	Get(ctx context.Context, id string) (*domain.Disk, error)
	List(ctx context.Context, f DiskFilter, page Pagination) ([]*domain.Disk, string, error)
	Insert(ctx context.Context, d *domain.Disk) (*domain.Disk, error)
	Update(ctx context.Context, d *domain.Disk) (*domain.Disk, error)
	// ListPendingReconcile возвращает диски, требующие работы reconciler-а.
	ListPendingReconcile(ctx context.Context, limit int) ([]*domain.Disk, error)
}

// ImageRepo — port-интерфейс репозитория образов (read-only).
type ImageRepo interface {
	Get(ctx context.Context, id string) (*domain.Image, error)
	List(ctx context.Context, filter string, page Pagination) ([]*domain.Image, string, error)
}

// SnapshotRepo — port-интерфейс репозитория снимков.
type SnapshotRepo interface {
	Get(ctx context.Context, id string) (*domain.Snapshot, error)
	List(ctx context.Context, f SnapshotFilter, page Pagination) ([]*domain.Snapshot, string, error)
	Insert(ctx context.Context, s *domain.Snapshot) (*domain.Snapshot, error)
	Update(ctx context.Context, s *domain.Snapshot) (*domain.Snapshot, error)
	// ListPendingReconcile возвращает снимки, требующие работы reconciler-а.
	ListPendingReconcile(ctx context.Context, limit int) ([]*domain.Snapshot, error)
}

// FolderClient — port для проверки существования Folder.
type FolderClient interface {
	Exists(ctx context.Context, folderID string) (bool, error)
}

// SubnetClient — port для проверки существования Subnet.
type SubnetClient interface {
	Exists(ctx context.Context, subnetID string) (bool, error)
}
