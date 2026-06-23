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
	ProjectID string
	// Filter — raw filter expression (YC-syntax: `name="<value>"`).
	Filter string
	// AllowedIDs — если non-nil, ограничивает выборку этим id-множеством
	// (FGA-фильтр, KAC-127 Phase 4). nil = без фильтра (bypass).
	AllowedIDs []string
}

// ImageFilter — фильтр для списка образов.
type ImageFilter struct {
	ProjectID  string
	Filter     string
	AllowedIDs []string
}

// SnapshotFilter — фильтр для списка снапшотов.
type SnapshotFilter struct {
	ProjectID  string
	Filter     string
	AllowedIDs []string
}

// InstanceFilter — фильтр для списка ВМ.
type InstanceFilter struct {
	ProjectID  string
	Filter     string
	AllowedIDs []string
}

// DiskRepo — port-интерфейс репозитория дисков.
type DiskRepo interface {
	Get(ctx context.Context, id string) (*domain.Disk, error)
	List(ctx context.Context, f DiskFilter, p Pagination) ([]*domain.Disk, string, error)
	Insert(ctx context.Context, d *domain.Disk) (*domain.Disk, error)
	// Update — emitLabelsRegister эмитит mirror.upsert при labels-в-маске (#113/T3.1, parity с Instance).
	Update(ctx context.Context, d *domain.Disk, emitLabelsRegister bool) (*domain.Disk, error)
	Delete(ctx context.Context, id string) error
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
	// Update — emitLabelsRegister эмитит mirror.upsert при labels-в-маске (#113/T3.1, parity с Instance).
	Update(ctx context.Context, i *domain.Image, emitLabelsRegister bool) (*domain.Image, error)
	Delete(ctx context.Context, id string) error
}

// SnapshotRepo — port-интерфейс репозитория снапшотов.
type SnapshotRepo interface {
	Get(ctx context.Context, id string) (*domain.Snapshot, error)
	List(ctx context.Context, f SnapshotFilter, p Pagination) ([]*domain.Snapshot, string, error)
	Insert(ctx context.Context, s *domain.Snapshot) (*domain.Snapshot, error)
	// Update — emitLabelsRegister эмитит mirror.upsert при labels-в-маске (#113/T3.1, parity с Instance).
	Update(ctx context.Context, s *domain.Snapshot, emitLabelsRegister bool) (*domain.Snapshot, error)
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
	// emitLabelsRegister (epic RSAB β, D-β6): true когда "labels" присутствует в
	// update-mask (или full-object PATCH применяет labels) → repo эмитит свежий
	// FGA register-intent с обновлёнными labels в той же writer-tx (IAM
	// resource_mirror refresh, ban #10); false → register-intent НЕ эмитится.
	Update(ctx context.Context, in *domain.Instance, emitLabelsRegister bool) (*domain.Instance, error)
	// SetStatusCAS атомарно переводит instance из expected-status в next-status
	// (CAS на DB-уровне: conditional UPDATE WHERE id=$1 AND status=$expected).
	// Если row не существует → ErrNotFound; если status не совпадает с
	// expected → ErrFailedPrecondition (state transition not allowed). Возвращает
	// обновлённую ВМ (+ outbox UPDATED в той же TX). Workspace CLAUDE.md
	// §«Within-service refs — DB-уровень обязателен» (KAC-91/KAC-87 G2,
	// parity c kacho-vpc KAC-52 NIC-attach race).
	SetStatusCAS(ctx context.Context, id string, expected, next domain.InstanceStatus) (*domain.Instance, error)
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

// ProjectClient — port для проверки существования Project в kacho-iam
// (ProjectService.Get). Аргумент projectID — id владельца-проекта (в схеме
// kacho-compute хранится в legacy-именованной колонке `folder_id`).
type ProjectClient interface {
	Exists(ctx context.Context, projectID string) (bool, error)
}

// ZoneInfo — минимальные данные о зоне, нужные compute'у: id + region.
type ZoneInfo struct {
	ID       string
	RegionID string
}

// ZoneRegistry — port для existence-check zone_id в Disk.Create / Instance.Create
// (и Disk.Relocate). Реализуется поверх kacho-geo (geo.v1.ZoneService.Get) через
// clients.GeoClient — Geography (Region/Zone) принадлежит kacho-geo. GetZone
// возвращает ErrNotFound, если зона неизвестна.
type ZoneRegistry interface {
	GetZone(ctx context.Context, zoneID string) (ZoneInfo, error)
}

// VPCAddress — выделенный IP-адрес VPC (результат CreateExternalAddress или
// GetExternalAddress): сам IP + id Address-ресурса в kacho-vpc.
type VPCAddress struct {
	IP        string
	AddressID string
}

// VPCClient — port для cross-service взаимодействия с kacho-vpc: IPAM-аллокация
// эфемерных external Address-ресурсов под one-to-one NAT (AddOneToOneNat),
// teardown этих ресурсов и referrer-tracking адресов. NIC-привязка убрана из
// lifecycle Instance (KAC-266, no auto-NIC) — методов управления NIC здесь нет.
type VPCClient interface {
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
	// best-effort из instance.go (ошибка не валит операцию — IP уже выделен).
	SetAddressReference(ctx context.Context, addressID, referrerType, referrerID, referrerName string) error
	// MarkAddressEphemeralInUse атомарно помечает Address как «эфемерный, в
	// работе»: reserved=false, used=true + upsert referrer (type=compute_instance,
	// id/name инстанса). Используется для эфемерных NAT-адресов, которые compute
	// создаёт сам через CreateExternalAddress (а не для reserved пользовательских
	// — у тех reserved не трогаем). Best-effort, как и SetAddressReference.
	MarkAddressEphemeralInUse(ctx context.Context, addressID, referrerType, referrerID, referrerName string) error
	// ClearAddressReference снимает referrer с Address-ресурса (best-effort;
	// NotFound = адрес уже удалён → успех). Вызывается при отвязке
	// reserved-адреса от ВМ (для эфемерных адресов referrer уходит через FK
	// CASCADE при DeleteAddress).
	ClearAddressReference(ctx context.Context, addressID string) error
}
