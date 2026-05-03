package service

import (
	"context"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
)

// Pagination описывает параметры постраничной выборки.
type Pagination struct {
	PageToken string
	PageSize  int32
}

// Selector описывает фильтр при List/Watch.
type Selector struct {
	Name     string
	FolderID string
	Labels   map[string]string
}

// InstanceRepo — port-интерфейс для репозитория инстансов.
type InstanceRepo interface {
	GetByUID(ctx context.Context, uid string) (*domain.Instance, error)
	GetByFolderAndName(ctx context.Context, folderID, name string) (*domain.Instance, error)
	List(ctx context.Context, selectors []Selector, page Pagination) ([]*domain.Instance, string, int64, error)
	SnapshotResourceVersion(ctx context.Context) (int64, error)
	Insert(ctx context.Context, inst *domain.Instance) (*domain.Instance, error)
	Update(ctx context.Context, inst *domain.Instance) (*domain.Instance, error)
	UpdateStatus(ctx context.Context, inst *domain.Instance) (*domain.Instance, error)
	UpdateMetadata(ctx context.Context, uid string, finalizers []string, updateFinalizers bool, restartedAt *string) (*domain.Instance, error)
	SoftDelete(ctx context.Context, uid string) error
	HardDelete(ctx context.Context, uid string) error
	ListPendingReconcile(ctx context.Context) ([]*domain.Instance, error)
	SetRestart(ctx context.Context, uid string) (*domain.Instance, error)
}

// DiskRepo — port-интерфейс для репозитория дисков.
type DiskRepo interface {
	GetByUID(ctx context.Context, uid string) (*domain.Disk, error)
	GetByFolderAndName(ctx context.Context, folderID, name string) (*domain.Disk, error)
	List(ctx context.Context, selectors []Selector, page Pagination) ([]*domain.Disk, string, int64, error)
	SnapshotResourceVersion(ctx context.Context) (int64, error)
	Insert(ctx context.Context, disk *domain.Disk) (*domain.Disk, error)
	Update(ctx context.Context, disk *domain.Disk) (*domain.Disk, error)
	UpdateStatus(ctx context.Context, disk *domain.Disk) (*domain.Disk, error)
	SoftDelete(ctx context.Context, uid string) error
	HardDelete(ctx context.Context, uid string) error
	ListPendingReconcile(ctx context.Context) ([]*domain.Disk, error)
	ListAttachedToInstance(ctx context.Context, instanceUID string) ([]*domain.Disk, error)
	HasSnapshots(ctx context.Context, uid string) (bool, error)
}

// ImageRepo — port-интерфейс для репозитория образов (read-only).
type ImageRepo interface {
	GetByUID(ctx context.Context, uid string) (*domain.Image, error)
	List(ctx context.Context, selectors []Selector, page Pagination) ([]*domain.Image, string, int64, error)
	SnapshotResourceVersion(ctx context.Context) (int64, error)
}

// SnapshotRepo — port-интерфейс для репозитория снапшотов.
type SnapshotRepo interface {
	GetByUID(ctx context.Context, uid string) (*domain.Snapshot, error)
	GetByFolderAndName(ctx context.Context, folderID, name string) (*domain.Snapshot, error)
	List(ctx context.Context, selectors []Selector, page Pagination) ([]*domain.Snapshot, string, int64, error)
	SnapshotResourceVersion(ctx context.Context) (int64, error)
	Insert(ctx context.Context, snap *domain.Snapshot) (*domain.Snapshot, error)
	Update(ctx context.Context, snap *domain.Snapshot) (*domain.Snapshot, error)
	UpdateStatus(ctx context.Context, snap *domain.Snapshot) (*domain.Snapshot, error)
	SoftDelete(ctx context.Context, uid string) error
	HardDelete(ctx context.Context, uid string) error
	ListPendingReconcile(ctx context.Context) ([]*domain.Snapshot, error)
}

// FolderClient — port-интерфейс для проверки существования Folder.
type FolderClient interface {
	Exists(ctx context.Context, folderUID string) (bool, error)
}

// SubnetClient — port-интерфейс для проверки существования Subnet.
type SubnetClient interface {
	Exists(ctx context.Context, subnetUID string) (bool, error)
}
