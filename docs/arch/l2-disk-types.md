---
level: functionality
repo: kacho-compute
anchors:
  - rpc: kacho.cloud.compute.v1.DiskTypeService/Get
  - rpc: kacho.cloud.compute.v1.DiskTypeService/List
  - rpc: kacho.cloud.compute.v1.InternalDiskTypeService/Create
  - rpc: kacho.cloud.compute.v1.InternalDiskTypeService/Delete
  - rpc: kacho.cloud.compute.v1.InternalDiskTypeService/Update
status: implemented
source_sha: ""
---

# Disk types

Каталог типов дисков. `DiskType` — справочный ресурс: id типа (например
`network-ssd`), описание и список зон (`zone_ids`), в которых тип доступен.
Не привязан к project/account — глобальный платформенный справочник.

## Зачем

DiskType фиксирует, какие классы хранилища платформа предлагает и где. При
`Disk.Create` поле `type_id` валидируется по этому каталогу; UI показывает
доступные типы. Каталог редко меняется и управляется только оператором.

## Контракт

- `DiskTypeService` (public, 9090): `Get` / `List` — sync read-only.
  `List` фильтрует по зоне через YC-style syntax + page_token.
- `InternalDiskTypeService` (internal, 9091): `Create` / `Update` /
  `Delete` — admin-мутации каталога. Не публикуются на external TLS
  (§Запрет #6) — это admin-функция, не tenant-API.

## Lifecycle

Тип создаётся оператором через `InternalDiskTypeService.Create`, далее
доступен всем tenant'ам через public `DiskTypeService`. `Update` меняет
описание / список зон; `Delete` снимает тип из каталога.

## Gotchas

- `Delete` типа, на который ссылаются существующие диски (`Disk.type_id`) —
  должен быть заблокирован (FailedPrecondition); ссылка within-DB
  фиксируется на DB-уровне.
- Разделение public read / internal write — паттерн §Запрет #6: tenant видит
  каталог, но не может его править; admin-мутации только на internal-listener.
- `zone_ids` — список id зон Geography; ссылается на ресурс `Zone` того же
  сервиса (within-domain).
