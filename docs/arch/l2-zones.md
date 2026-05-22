---
level: functionality
repo: kacho-compute
anchors:
  - rpc: kacho.cloud.compute.v1.ZoneService/Get
  - rpc: kacho.cloud.compute.v1.ZoneService/List
  - rpc: kacho.cloud.compute.v1.InternalZoneService/Create
  - rpc: kacho.cloud.compute.v1.InternalZoneService/Delete
  - rpc: kacho.cloud.compute.v1.InternalZoneService/Update
status: implemented
source_sha: ""
---

# Zones

Справочник зон доступности. `Zone` — единица географии внутри региона
(например `ru-central1-a`): id (admin-assigned, immutable PK), `region_id`
(FK → Region), `status` (`UP` / `DOWN`) и человекочитаемое имя.

## Зачем

Зона — единица размещения ресурсов: на зону ссылаются `Instance.zone_id`,
`Disk.zone_id`, `Subnet.zone_id` (kacho-vpc). Это самая «горячая» точка
Geography — каждый `Create` инстанса/диска/подсети валидирует `zone_id`
через `ZoneService.Get`.

## Контракт

- `ZoneService` (public, 9090): `Get` / `List` — sync read-only.
- `InternalZoneService` (internal, 9091): `Create` / `Update` /
  `Delete` — admin-мутации справочника, не на external TLS (§Запрет #6).

## Lifecycle

Зона заводится оператором через `InternalZoneService.Create` с
admin-assigned id и привязкой к региону. `Update` меняет `status` (вывод
зоны в `DOWN` при недоступности) и имя. `Delete` снимает зону из каталога.

## Gotchas

- `ZoneService.Get` — точка кросс-доменной валидации: `kacho-vpc` зовёт его
  при `Subnet.Create` (ребро `kacho-vpc → kacho-compute`, перевёрнуто в
  `KAC-15` — раньше compute проксировал зоны из vpc).
- Consumer'ы (vpc/compute-сам) хранят `zone_id` как строку без cross-DB FK
  (§Запрет #8) — валидация на request-path, dangling-ref переживается.
- `status = DOWN` — сигнал недоступности зоны; новые ресурсы в неё не
  размещаются, существующие переживают деградацию грациозно.
- `id` зоны — admin-assigned, immutable PK; `region_id` — FK RESTRICT.
