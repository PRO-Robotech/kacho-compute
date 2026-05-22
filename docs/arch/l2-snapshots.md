---
level: functionality
repo: kacho-compute
anchors:
  - rpc: kacho.cloud.compute.v1.SnapshotService/Create
  - rpc: kacho.cloud.compute.v1.SnapshotService/Delete
  - rpc: kacho.cloud.compute.v1.SnapshotService/Get
  - rpc: kacho.cloud.compute.v1.SnapshotService/List
  - rpc: kacho.cloud.compute.v1.SnapshotService/ListAccessBindings
  - rpc: kacho.cloud.compute.v1.SnapshotService/ListOperations
  - rpc: kacho.cloud.compute.v1.SnapshotService/SetAccessBindings
  - rpc: kacho.cloud.compute.v1.SnapshotService/Update
  - rpc: kacho.cloud.compute.v1.SnapshotService/UpdateAccessBindings
status: implemented
source_sha: ""
---

# Snapshots

CRUD моментальных снимков дисков. `Snapshot` — point-in-time копия диска
(`source_disk_id`), хранится инкрементально: `storage_size` — дельта от
предыдущего снимка того же диска, `disk_size` — полный размер диска на момент
снятия. Снимок может быть источником для нового диска.

## Зачем

Снимок — механизм резервного копирования и клонирования: из снимка через
`Disk.Create(source_snapshot_id)` восстанавливается состояние диска.
Инкрементальное хранение экономит место для серии снимков одного диска.

## Контракт

- `Get` / `List` / `ListOperations` / `ListAccessBindings` — sync read.
- `Create` / `Update` / `Delete` — async (`Operation`); `Create` принимает
  `source_disk_id` — диск, с которого снимается снимок.
- `SetAccessBindings` / `UpdateAccessBindings` — async управление доступом.

## Lifecycle

`Status`: `CREATING → READY`, терминальный `DELETING`, аварийный `ERROR`.
`Create` валидирует `project_id` (kacho-iam) и `source_disk_id`
(существование диска); worker создаёт consistent point-in-time копию.

## Gotchas

- `Delete` снимка, на который ссылаются диски (`Disk.source_snapshot_id`) —
  допустим (диск уже скопировал содержимое); ссылка становится dangling-ref.
- Инкрементальная цепочка: удаление промежуточного снимка серии требует
  merge дельт — обрабатывается worker'ом, прозрачно для клиента.
- Снятие снимка с прикреплённого к работающей VM диска — допускается
  (crash-consistent); полная consistency — на остановленной VM.
- Снимки расписанием создаются через snapshot-schedules (см.
  `DiskService.ListSnapshotSchedules` в `l2-disk-lifecycle`).

## Связанные заметки

<!-- archgraph:links -->
- ↑ Приклад: [[_l1-kacho-compute]]
- ↓ Функции (call-дерево, RPC contract): [[l3-l2-snapshots]]
- Переменные: [[l4-kacho-compute]]
<!-- /archgraph:links -->
