// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package service

import "github.com/PRO-Robotech/kacho-compute/internal/ports"

// Port-интерфейсы и связанные value-объекты вынесены в leaf-пакет
// `internal/ports` — это позволяет переиспользовать общий test-helper
// `internal/ports/portmock` без import-cycle. Здесь — type-alias'ы для
// удобства: service-код и adapter'ы (`internal/repo`, `internal/clients`)
// ссылаются на `service.*` имена. Зеркалит kacho-vpc/internal/service/ports.go.

type (
	// Pagination — постраничная навигация.
	Pagination = ports.Pagination

	// DiskFilter — фильтр для списка дисков.
	DiskFilter = ports.DiskFilter
	// ImageFilter — фильтр для списка образов.
	ImageFilter = ports.ImageFilter
	// SnapshotFilter — фильтр для списка снапшотов.
	SnapshotFilter = ports.SnapshotFilter
	// InstanceFilter — фильтр для списка ВМ.
	InstanceFilter = ports.InstanceFilter

	// DiskRepo — port-интерфейс репозитория дисков.
	DiskRepo = ports.DiskRepo
	// ImageRepo — port-интерфейс репозитория образов.
	ImageRepo = ports.ImageRepo
	// SnapshotRepo — port-интерфейс репозитория снапшотов.
	SnapshotRepo = ports.SnapshotRepo
	// InstanceRepo — port-интерфейс репозитория ВМ.
	InstanceRepo = ports.InstanceRepo
	// DiskTypeRepo — port-интерфейс репозитория типов дисков.
	DiskTypeRepo = ports.DiskTypeRepo

	// ProjectClient — port для проверки существования Folder.
	ProjectClient = ports.ProjectClient
	// ZoneRegistry — port existence-check zone_id (Disk/Instance Create, Disk Relocate).
	ZoneRegistry = ports.ZoneRegistry
)
