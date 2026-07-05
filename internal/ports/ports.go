// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

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
	// Filter — raw filter expression (синтаксис Kachō: `name="<value>"`).
	Filter string
	// AllowedIDs — если non-nil, ограничивает выборку этим id-множеством
	// (FGA-фильтр). nil = без фильтра (bypass).
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
	// Update — emitLabelsRegister эмитит mirror.upsert при labels-в-маске (parity с Instance).
	Update(ctx context.Context, d *domain.Disk, emitLabelsRegister bool, changed []string) (*domain.Disk, error)
	Delete(ctx context.Context, id string) error
	// SetZoneIfDetached атомарно меняет zone_id, но только если диск НЕ attached
	// (Relocate). Гонка с AttachDisk закрывается на DB-уровне (row-lock на disks),
	// а не software-check-then-act: detached → новый zone_id; attached →
	// ErrFailedPrecondition; нет диска → ErrNotFound.
	SetZoneIfDetached(ctx context.Context, id, zoneID string) (*domain.Disk, error)
	// IsAttached — true если есть строка attached_disks для disk_id.
	IsAttached(ctx context.Context, id string) (bool, error)
}

// ImageRepo — port-интерфейс репозитория образов.
type ImageRepo interface {
	Get(ctx context.Context, id string) (*domain.Image, error)
	GetLatestByFamily(ctx context.Context, folderID, family string) (*domain.Image, error)
	List(ctx context.Context, f ImageFilter, p Pagination) ([]*domain.Image, string, error)
	Insert(ctx context.Context, i *domain.Image) (*domain.Image, error)
	// Update — emitLabelsRegister эмитит mirror.upsert при labels-в-маске (parity с Instance).
	Update(ctx context.Context, i *domain.Image, emitLabelsRegister bool, changed []string) (*domain.Image, error)
	Delete(ctx context.Context, id string) error
}

// SnapshotRepo — port-интерфейс репозитория снапшотов.
type SnapshotRepo interface {
	Get(ctx context.Context, id string) (*domain.Snapshot, error)
	List(ctx context.Context, f SnapshotFilter, p Pagination) ([]*domain.Snapshot, string, error)
	Insert(ctx context.Context, s *domain.Snapshot) (*domain.Snapshot, error)
	// Update — emitLabelsRegister эмитит mirror.upsert при labels-в-маске (parity с Instance).
	Update(ctx context.Context, s *domain.Snapshot, emitLabelsRegister bool, changed []string) (*domain.Snapshot, error)
	Delete(ctx context.Context, id string) error
}

// InstanceRepo — port-интерфейс репозитория ВМ.
type InstanceRepo interface {
	Get(ctx context.Context, id string) (*domain.Instance, error)
	List(ctx context.Context, f InstanceFilter, p Pagination) ([]*domain.Instance, string, error)
	// Insert вставляет ВМ + attached_disks в одной TX. inlineDisks — диски,
	// созданные из disk_spec (вставляются в этой же TX). Возвращает созданную
	// ВМ (с заполненными AttachedDisks).
	Insert(ctx context.Context, in *domain.Instance, inlineDisks []*domain.Disk) (*domain.Instance, error)
	// Update обновляет mutable descriptive/resource поля (status НЕ трогает —
	// им владеет SetStatusCAS). emitLabelsRegister: true когда "labels" присутствует
	// в update-mask (или full-object PATCH применяет labels) → repo эмитит свежий FGA
	// register-intent с обновлёнными labels в той же writer-tx (refresh IAM
	// resource_mirror); false → register-intent НЕ эмитится.
	//
	// changed — фактически изменённые mask-поля; repo пишет ТОЛЬКО их колонки
	// (column-scoped UPDATE). Без scoping конкурентный Update по другому полю
	// затирается значением из устаревшего Get-снимка (lost update) — read-modify-write
	// вне одной TX. Пустой changed → no-op reload (behaviour-preserving).
	Update(ctx context.Context, in *domain.Instance, emitLabelsRegister bool, changed []string) (*domain.Instance, error)
	// SetStatusCAS атомарно переводит instance из expected-status в next-status
	// (CAS на DB-уровне: conditional UPDATE WHERE id=$1 AND status=$expected).
	// Если row не существует → ErrNotFound; если status не совпадает с
	// expected → ErrFailedPrecondition (state transition not allowed). Возвращает
	// обновлённую ВМ (+ outbox UPDATED в той же TX). Within-service-инвариант на
	// DB-уровне (CAS), не software check-then-act — защита от second-writer-wins.
	SetStatusCAS(ctx context.Context, id string, expected, next domain.InstanceStatus) (*domain.Instance, error)
	// AttachDisk добавляет строку attached_disks. Возвращает обновлённую ВМ.
	AttachDisk(ctx context.Context, id string, ad domain.AttachedDisk) (*domain.Instance, error)
	// DetachDisk удаляет строку attached_disks по disk_id. Возвращает обновлённую ВМ.
	DetachDisk(ctx context.Context, id, diskID string) (*domain.Instance, error)
	// MergeMetadata атомарно применяет delete+upsert дельту к map metadata одним
	// SQL-statement'ом (within-service-инвариант на DB-уровне, project-rule 10 —
	// не Go-side read-modify-write, иначе second-writer-wins под concurrency).
	// Возвращает обновлённую ВМ.
	MergeMetadata(ctx context.Context, id string, del []string, upsert map[string]string) (*domain.Instance, error)
	// Delete удаляет ВМ; autoDeleteDiskIDs — диски с auto_delete=true (удаляются
	// в той же TX до DELETE instance; остальные строки attached_disks чистит CASCADE).
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
// kacho-compute хранится в колонке `project_id`; переименована из legacy
// `folder_id` миграцией 0009_rename_folder_to_project).
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
