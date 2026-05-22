---
level: functionality
repo: kacho-compute
anchors:
  - rpc: kacho.cloud.compute.v1.ImageService/Create
  - rpc: kacho.cloud.compute.v1.ImageService/Delete
  - rpc: kacho.cloud.compute.v1.ImageService/Get
  - rpc: kacho.cloud.compute.v1.ImageService/GetLatestByFamily
  - rpc: kacho.cloud.compute.v1.ImageService/List
  - rpc: kacho.cloud.compute.v1.ImageService/ListAccessBindings
  - rpc: kacho.cloud.compute.v1.ImageService/ListOperations
  - rpc: kacho.cloud.compute.v1.ImageService/SetAccessBindings
  - rpc: kacho.cloud.compute.v1.ImageService/Update
  - rpc: kacho.cloud.compute.v1.ImageService/UpdateAccessBindings
status: implemented
source_sha: ""
---

# Images

CRUD загрузочных образов. `Image` — неизменяемый шаблон содержимого диска
(ОС + предустановленный софт) с метаданными `family`, `os`, `min_disk_size`,
`storage_size`, `product_ids` (лицензии). Из образа создаётся загрузочный
`Disk`.

## Зачем

Образ — источник для boot-диска инстанса. Группировка по `family` позволяет
ссылаться на «последнюю стабильную» версию ОС без жёсткой привязки к
конкретному id — ключевой паттерн для воспроизводимого provisioning'а VM.

## Контракт

- `Get` / `List` / `ListOperations` / `ListAccessBindings` — sync read.
- `GetLatestByFamily` — sync read; возвращает самый свежий `READY`-образ
  указанного семейства (по `created_at`).
- `Create` / `Update` / `Delete` — async (`Operation`); `Create` принимает
  источник содержимого (URI / snapshot / disk) и `family`.
- `SetAccessBindings` / `UpdateAccessBindings` — async управление доступом.

## Lifecycle

`Status`: `CREATING → READY`, терминальный `DELETING`, аварийный `ERROR`.
`Create` валидирует `project_id` (kacho-iam); worker импортирует/конвертирует
содержимое, после чего образ переходит в `READY` и может быть источником
для `Disk.Create`.

## Gotchas

- `GetLatestByFamily` учитывает только `READY`-образы; `CREATING` / `ERROR`
  не выбираются — иначе provisioning стартовал бы с недостроенного образа.
- `Delete` образа, на который ссылаются диски (`Disk.source_image_id`) —
  YC-семантика: образ можно удалить (диск уже скопировал содержимое);
  `source_image_id` у диска становится dangling-ref, переживается грациозно.
- `product_ids` (лицензии) наследуются дисками/инстансами, созданными из
  образа — влияет на биллинг.
- `hardware_generation` образа форсит feature-set для boot-инстанса.

## Связанные заметки

<!-- archgraph:links -->
- ↑ Приклад: [[_l1-kacho-compute]]
- ↓ Функции (call-дерево, RPC contract): [[l3-l2-images]]
- Переменные: [[l4-kacho-compute]]
<!-- /archgraph:links -->
