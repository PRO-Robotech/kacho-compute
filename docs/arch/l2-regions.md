---
level: functionality
repo: kacho-compute
anchors:
  - rpc: kacho.cloud.compute.v1.RegionService/Get
  - rpc: kacho.cloud.compute.v1.RegionService/List
  - rpc: kacho.cloud.compute.v1.InternalRegionService/Create
  - rpc: kacho.cloud.compute.v1.InternalRegionService/Delete
  - rpc: kacho.cloud.compute.v1.InternalRegionService/Update
status: implemented
source_sha: ""
---

# Regions

Справочник регионов. `Region` — верхний уровень географической иерархии
(например `ru-central1`): id (admin-assigned, immutable PK), человекочитаемое
имя и время создания. Содержит зоны (`Zone.region_id → Region.id`).

## Зачем

Регион — крупная географическая единица платформы. Geography (Region/Zone) —
домен kacho-compute (перенесён из kacho-vpc эпиком `KAC-15`); справочник не
привязан к project/account и общий для всех tenant'ов.

## Контракт

- `RegionService` (public, 9090): `Get` / `List` — sync read-only.
- `InternalRegionService` (internal, 9091): `Create` / `Update` /
  `Delete` — admin-мутации справочника. Не публикуются на external TLS
  (§Запрет #6) — наполнение Geography-каталога делает только оператор.

## Lifecycle

Регион заводится оператором через `InternalRegionService.Create` с
admin-assigned id (id immutable — это PK). `Update` меняет только
человекочитаемое имя. `Delete` снимает регион из каталога.

## Gotchas

- `id` региона — admin-assigned и immutable: это часть стабильного контракта,
  на него ссылаются зоны и (косвенно) ресурсы всех сервисов.
- `Delete` региона с существующими зонами (`Zone.region_id`) — заблокирован
  на DB-уровне (FK RESTRICT) → `FailedPrecondition`.
- Public/Internal split — паттерн §Запрет #6: tenant видит каталог через
  `RegionService`, правит его только оператор через `Internal*`.
