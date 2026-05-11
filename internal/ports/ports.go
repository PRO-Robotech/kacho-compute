// Package ports содержит port-интерфейсы (Clean Architecture boundaries) и
// связанные value-объекты (Pagination, *Filter) для kacho-compute.
//
// Это leaf-пакет: импортирует только `internal/domain`. Импортируется
// `internal/service` (use-cases — ре-экспортирует через type-alias'ы),
// `internal/repo` / `internal/clients` (adapters реализуют эти интерфейсы) и
// `internal/ports/portmock` (общие fake'и для unit-тестов). Так избегается
// дублирование mock-реализаций и не создаётся import-cycle. Зеркалит
// kacho-vpc/internal/ports.
package ports

import (
	"context"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
)

// Pagination — постраничная навигация.
type Pagination struct {
	PageToken string
	PageSize  int64
}

// DiskFilter — фильтр для списка дисков.
type DiskFilter struct {
	FolderID string
	// Filter — raw filter expression (YC-syntax: `name="<value>"`).
	Filter string
}

// ImageFilter — фильтр для списка образов.
type ImageFilter struct {
	FolderID string
	Filter   string
}

// SnapshotFilter — фильтр для списка снапшотов.
type SnapshotFilter struct {
	FolderID string
	Filter   string
}

// InstanceFilter — фильтр для списка ВМ.
type InstanceFilter struct {
	FolderID string
	Filter   string
}

// DiskRepo — port-интерфейс репозитория дисков.
type DiskRepo interface {
	Get(ctx context.Context, id string) (*domain.Disk, error)
	List(ctx context.Context, f DiskFilter, p Pagination) ([]*domain.Disk, string, error)
	Insert(ctx context.Context, d *domain.Disk) (*domain.Disk, error)
	Update(ctx context.Context, d *domain.Disk) (*domain.Disk, error)
	Delete(ctx context.Context, id string) error
	SetFolderID(ctx context.Context, id, folderID string) (*domain.Disk, error)
	// SetZoneID меняет zone_id (для Relocate).
	SetZoneID(ctx context.Context, id, zoneID string) (*domain.Disk, error)
	// IsAttached — true если есть строка attached_disks для disk_id.
	IsAttached(ctx context.Context, id string) (bool, error)
}

// ImageRepo — port-интерфейс репозитория образов.
type ImageRepo interface {
	Get(ctx context.Context, id string) (*domain.Image, error)
	GetLatestByFamily(ctx context.Context, folderID, family string) (*domain.Image, error)
	List(ctx context.Context, f ImageFilter, p Pagination) ([]*domain.Image, string, error)
	Insert(ctx context.Context, i *domain.Image) (*domain.Image, error)
	Update(ctx context.Context, i *domain.Image) (*domain.Image, error)
	Delete(ctx context.Context, id string) error
}

// SnapshotRepo — port-интерфейс репозитория снапшотов.
type SnapshotRepo interface {
	Get(ctx context.Context, id string) (*domain.Snapshot, error)
	List(ctx context.Context, f SnapshotFilter, p Pagination) ([]*domain.Snapshot, string, error)
	Insert(ctx context.Context, s *domain.Snapshot) (*domain.Snapshot, error)
	Update(ctx context.Context, s *domain.Snapshot) (*domain.Snapshot, error)
	Delete(ctx context.Context, id string) error
}

// InstanceRepo — port-интерфейс репозитория ВМ.
type InstanceRepo interface {
	Get(ctx context.Context, id string) (*domain.Instance, error)
	List(ctx context.Context, f InstanceFilter, p Pagination) ([]*domain.Instance, string, error)
	// Insert вставляет ВМ + NIC-и + attached_disks в одной TX. inlineDisks —
	// диски, созданные из disk_spec (вставляются в этой же TX). Возвращает
	// созданную ВМ (с заполненными NICs/AttachedDisks).
	Insert(ctx context.Context, in *domain.Instance, inlineDisks []*domain.Disk) (*domain.Instance, error)
	// Update обновляет mutable поля + status (для lifecycle-операций).
	Update(ctx context.Context, in *domain.Instance) (*domain.Instance, error)
	// SetStatus меняет только status (Start/Stop/Restart). Возвращает обновлённую ВМ.
	SetStatus(ctx context.Context, id string, status domain.InstanceStatus) (*domain.Instance, error)
	// SetFolderID меняет folder_id (для Move).
	SetFolderID(ctx context.Context, id, folderID string) (*domain.Instance, error)
	// AttachDisk добавляет строку attached_disks. Возвращает обновлённую ВМ.
	AttachDisk(ctx context.Context, id string, ad domain.AttachedDisk) (*domain.Instance, error)
	// DetachDisk удаляет строку attached_disks по disk_id. Возвращает обновлённую ВМ.
	DetachDisk(ctx context.Context, id, diskID string) (*domain.Instance, error)
	// ReplaceNIC заменяет одну строку instance_network_interfaces (для NAT/SG-операций).
	ReplaceNIC(ctx context.Context, id string, nic domain.NetworkInterface) (*domain.Instance, error)
	// SetMetadata заменяет map metadata. Возвращает обновлённую ВМ.
	SetMetadata(ctx context.Context, id string, metadata map[string]string) (*domain.Instance, error)
	// Delete удаляет ВМ; autoDeleteDiskIDs — диски с auto_delete=true (удаляются
	// в той же TX до DELETE instance; остальные строки attached_disks/NIC чистит CASCADE).
	Delete(ctx context.Context, id string, autoDeleteDiskIDs []string) error
}

// DiskTypeRepo — port-интерфейс репозитория типов дисков (read + admin CRUD).
type DiskTypeRepo interface {
	Get(ctx context.Context, id string) (*domain.DiskType, error)
	List(ctx context.Context, p Pagination) ([]*domain.DiskType, string, error)
	Insert(ctx context.Context, t *domain.DiskType) (*domain.DiskType, error)
	Update(ctx context.Context, t *domain.DiskType) (*domain.DiskType, error)
	Delete(ctx context.Context, id string) error
}

// ZoneRepo — port-интерфейс репозитория зон (read + admin CRUD).
type ZoneRepo interface {
	Get(ctx context.Context, id string) (*domain.Zone, error)
	List(ctx context.Context, p Pagination) ([]*domain.Zone, string, error)
	Insert(ctx context.Context, z *domain.Zone) (*domain.Zone, error)
	Update(ctx context.Context, z *domain.Zone) (*domain.Zone, error)
	Delete(ctx context.Context, id string) error
}

// FolderClient — port для проверки существования Folder в kacho-resource-manager.
type FolderClient interface {
	Exists(ctx context.Context, folderID string) (bool, error)
}

// VPCClient — port для проверки существования cross-service VPC-ресурсов
// (subnet / security group / address) при валидации Instance NIC-spec.
type VPCClient interface {
	// GetSubnet возвращает (zoneID, found, error). found=false если subnet
	// не существует на стороне VPC; zoneID — для проверки совпадения с zone ВМ.
	GetSubnet(ctx context.Context, subnetID string) (zoneID string, found bool, err error)
	// SecurityGroupExists — true если SG существует.
	SecurityGroupExists(ctx context.Context, sgID string) (bool, error)
	// AddressExists — true если Address существует.
	AddressExists(ctx context.Context, addressID string) (bool, error)
}
