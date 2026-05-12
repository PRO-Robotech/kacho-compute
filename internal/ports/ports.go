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

// HypervisorRepo — port-интерфейс реестра гипервизоров (internal-only ресурс;
// см. workspace CLAUDE.md §«Инфра-чувствительные данные»).
type HypervisorRepo interface {
	Get(ctx context.Context, id string) (*domain.Hypervisor, error)
	List(ctx context.Context, zoneID string, state domain.HypervisorState, p Pagination) ([]*domain.Hypervisor, string, error)
	Insert(ctx context.Context, h *domain.Hypervisor) (*domain.Hypervisor, error)
	Update(ctx context.Context, h *domain.Hypervisor) (*domain.Hypervisor, error)
	Delete(ctx context.Context, id string) error
}

// RegionRepo — port-интерфейс репозитория регионов (read + admin CRUD).
// kacho-compute — owner Geography (Region/Zone); см. workspace CLAUDE.md
// §«Кросс-доменные ссылки на ресурсы».
type RegionRepo interface {
	Get(ctx context.Context, id string) (*domain.Region, error)
	List(ctx context.Context, p Pagination) ([]*domain.Region, string, error)
	Insert(ctx context.Context, r *domain.Region) (*domain.Region, error)
	Update(ctx context.Context, r *domain.Region) (*domain.Region, error)
	Delete(ctx context.Context, id string) error
	// CountZones — сколько зон ссылаются на этот регион (для delete-RESTRICT).
	CountZones(ctx context.Context, regionID string) (int, error)
}

// FolderClient — port для проверки существования Folder в kacho-resource-manager.
type FolderClient interface {
	Exists(ctx context.Context, folderID string) (bool, error)
}

// ZoneInfo — минимальные данные о зоне, нужные compute'у: id + region.
type ZoneInfo struct {
	ID       string
	RegionID string
}

// ZoneRegistry — port для existence-check zone_id в Disk.Create / Instance.Create
// (и Disk.Relocate). Реализуется поверх локальной таблицы `zones` (kacho-compute
// — owner Geography). GetZone возвращает ErrNotFound, если зона неизвестна.
type ZoneRegistry interface {
	GetZone(ctx context.Context, zoneID string) (ZoneInfo, error)
}

// ZoneSource — port для публичного ZoneService.Get/List. Реализуется поверх
// локальной таблицы `zones`. GetZone → ErrNotFound при неизвестной зоне.
type ZoneSource interface {
	ZoneRegistry
	ListZones(ctx context.Context, pageSize int64, pageToken string) (zones []ZoneInfo, nextPageToken string, err error)
}

// SubnetInfo — минимальные данные о subnet, нужные compute'у при материализации
// Instance NIC-spec: zone (для cross-zone-проверки) и v4-CIDR-блоки (для
// валидации manual primary_v4_address и как контекст в ошибках).
type SubnetInfo struct {
	ZoneID       string
	V4CidrBlocks []string
}

// VPCAddress — выделенный IP-адрес VPC (результат CreateInternal/ExternalAddress
// или GetExternalAddress): сам IP + id Address-ресурса в kacho-vpc.
type VPCAddress struct {
	IP        string
	AddressID string
}

// VPCClient — port для cross-service взаимодействия с kacho-vpc: валидация
// NIC-spec (subnet / security group), IPAM-аллокация реальных IPv4 (создание
// эфемерных Address-ресурсов через AddressService.Create, который inline
// выделяет IP из CIDR подсети / из AddressPool), и teardown этих ресурсов.
type VPCClient interface {
	// GetSubnet возвращает (info, found, error). found=false если subnet
	// не существует на стороне VPC.
	GetSubnet(ctx context.Context, subnetID string) (info SubnetInfo, found bool, err error)
	// SecurityGroupExists — true если SG существует.
	SecurityGroupExists(ctx context.Context, sgID string) (bool, error)
	// CreateInternalAddress создаёт эфемерный internal Address в указанном
	// folder с привязкой к subnetID; kacho-vpc inline выделяет IPv4 из CIDR
	// подсети. Поллит Operation до завершения и возвращает выделенный IP + id.
	CreateInternalAddress(ctx context.Context, folderID, name, subnetID string) (VPCAddress, error)
	// CreateExternalAddress создаёт эфемерный external Address в указанном
	// folder/zone; kacho-vpc inline выделяет публичный IPv4 из AddressPool
	// (cascade resolve). Поллит Operation до завершения и возвращает IP + id.
	CreateExternalAddress(ctx context.Context, folderID, name, zoneID string) (VPCAddress, error)
	// GetExternalAddress возвращает (addr, found, error) для уже существующего
	// (reserved) Address-ресурса: его id и выделенный external IPv4.
	GetExternalAddress(ctx context.Context, addressID string) (addr VPCAddress, found bool, err error)
	// DeleteAddress удаляет Address-ресурс (best-effort: поллит Operation;
	// NotFound трактуется как успех — ресурс уже удалён).
	DeleteAddress(ctx context.Context, addressID string) error
	// SetAddressReference привязывает referrer к Address-ресурсу (кто его
	// использует — type=compute_instance, id=instance id, name=instance name).
	// Идемпотентно. НЕ меняет reserved-флаг адреса — используется для reserved
	// пользовательских адресов (one-to-one NAT по address_id). Вызывается
	// best-effort из instance.go (ошибка не валит Instance.Create — IP уже выделен).
	SetAddressReference(ctx context.Context, addressID, referrerType, referrerID, referrerName string) error
	// MarkAddressEphemeralInUse атомарно помечает Address как «эфемерный, в
	// работе»: reserved=false, used=true + upsert referrer (type=compute_instance,
	// id/name инстанса). Используется для эфемерных NIC/NAT-адресов, которые
	// compute создаёт сам через CreateInternal/ExternalAddress (а не для reserved
	// пользовательских — у тех reserved не трогаем). Best-effort, как и
	// SetAddressReference.
	MarkAddressEphemeralInUse(ctx context.Context, addressID, referrerType, referrerID, referrerName string) error
	// ClearAddressReference снимает referrer с Address-ресурса (best-effort;
	// NotFound = адрес уже удалён → успех). Вызывается при отвязке
	// reserved-адреса от ВМ (для эфемерных адресов referrer уходит через FK
	// CASCADE при DeleteAddress).
	ClearAddressReference(ctx context.Context, addressID string) error
}
