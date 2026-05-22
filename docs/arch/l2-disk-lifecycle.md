---
level: functionality
repo: kacho-compute
anchors:
  - rpc: kacho.cloud.compute.v1.DiskService/Create
  - rpc: kacho.cloud.compute.v1.DiskService/Delete
  - rpc: kacho.cloud.compute.v1.DiskService/Get
  - rpc: kacho.cloud.compute.v1.DiskService/List
  - rpc: kacho.cloud.compute.v1.DiskService/ListAccessBindings
  - rpc: kacho.cloud.compute.v1.DiskService/ListOperations
  - rpc: kacho.cloud.compute.v1.DiskService/ListSnapshotSchedules
  - rpc: kacho.cloud.compute.v1.DiskService/Move
  - rpc: kacho.cloud.compute.v1.DiskService/Relocate
  - rpc: kacho.cloud.compute.v1.DiskService/SetAccessBindings
  - rpc: kacho.cloud.compute.v1.DiskService/Update
  - rpc: kacho.cloud.compute.v1.DiskService/UpdateAccessBindings
status: implemented
source_sha: ""
---

# Disk lifecycle

CRUD блочных дисков. `Disk` — единица постоянного блочного хранилища: имеет
размер, block-size, тип (`type_id` → `DiskType`), зону и опциональный источник
(образ или снимок). Диск может быть прикреплён к одному или нескольким
инстансам (`instance_ids`).

## Зачем

Disk отделён от Instance как самостоятельный ресурс: его жизненный цикл
независим от VM — диск переживает удаление инстанса (если не `auto_delete`),
может переноситься между проектами и зонами, быть источником для снимков.

## Контракт

- `Get` / `List` / `ListOperations` / `ListAccessBindings` /
  `ListSnapshotSchedules` — sync read; `ListSnapshotSchedules` отдаёт
  расписания снимков, привязанные к диску.
- `Create` / `Update` / `Delete` — async (`Operation`); `Create` принимает
  `source_image_id` либо `source_snapshot_id` (oneof) либо пустой источник.
- `Move` (между проектами) / `Relocate` (между зонами) — async; atomic CAS,
  0 rows → `FailedPrecondition`.
- `SetAccessBindings` / `UpdateAccessBindings` — async управление доступом.

## Lifecycle

`Status`: `CREATING → READY`, терминальный `DELETING`, аварийный `ERROR`.
`Create` валидирует `project_id` (kacho-iam), `zone_id` (через
`ZoneService.Get` — внутридоменно), `type_id` (DiskType-каталог). Источник
(image/snapshot) проверяется на существование и `READY`-статус.

## Gotchas

- `Delete` диска, прикреплённого к инстансу (`instance_ids` непуст) →
  `FailedPrecondition` (нет cross-resource cascade — §Запрет #4).
- `Relocate` между зонами — фактический перенос данных; долгая async-операция.
- `instance_ids` — multi-attach: один диск может быть прикреплён к нескольким
  VM (read-only режим); ownership-инвариант — на DB-уровне kacho-compute.
- `zone_id` — cross-domain ссылка на Geography; immutable после Create
  (смена зоны — только через `Relocate`).
