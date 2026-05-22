---
level: container
repo: kacho-compute
---

# kacho-compute — приклад

`kacho-compute` — сервис управления вычислительными ресурсами платформы Kachō.
Канонический владелец доменных ресурсов **Instance / Disk / Image / Snapshot /
DiskType**, а также — после эпика `KAC-15` — **Geography** (`Region` / `Zone`),
перенесённой сюда из `kacho-vpc`. Дополнительно ведёт internal-ресурс
**Hypervisor** (инвентарь физических узлов; internal-only — §«Инфра-чувствительные
данные»). Сервис — control-plane: хранит намерение и состояние ресурсов, реальный
data-plane (запуск VM на гипервизорах) выполняется отдельными impl-компонентами.

## Зона ответственности

- **Compute-ресурсы** — `Instance` (виртуальная машина: ресурсы, диски, NIC,
  placement, метаданные, lifecycle Start/Stop/Restart), `Disk` (блочное
  хранилище), `Image` (загрузочный образ + family-каталог), `Snapshot`
  (моментальный снимок диска). Все четыре — tenant-facing, project-scoped.
- **Каталог типов** — `DiskType` — справочник доступных типов дисков с
  привязкой к зонам; read-only для tenant'а, мутации — через `Internal*`.
- **Geography** — `Region` / `Zone` — глобальный географический справочник,
  не привязанный к project/account. Канонический владелец — kacho-compute
  (перенесён из kacho-vpc, `KAC-15`); другие сервисы ссылаются по id и
  валидируют через `ZoneService.Get`.
- **Hypervisor (internal)** — инвентарь физических узлов и их `node_index`;
  раскрывается только через internal-API, никогда на публичной поверхности.

Все мутации возвращают `operation.Operation` (async LRO — §Запрет #9); чтения
синхронны. Инфра-чувствительные / admin-операции вынесены в `Internal*`-сервисы
на internal-listener (port 9091) и не публикуются на external TLS (§Запрет #6).

## Контракт — 12 gRPC-сервисов

| Сервис | Назначение |
|---|---|
| `InstanceService` | CRUD VM + lifecycle (Start/Stop/Restart) + attach/detach дисков, ФС, NIC, one-to-one NAT, Move/Relocate, serial-port, maintenance |
| `DiskService` | CRUD блочных дисков + Move/Relocate + snapshot-schedules |
| `DiskTypeService` | read-only справочник типов дисков |
| `InternalDiskTypeService` | admin-мутации каталога типов дисков |
| `ImageService` | CRUD образов + `GetLatestByFamily` |
| `SnapshotService` | CRUD снимков дисков |
| `RegionService` | read-only справочник регионов |
| `InternalRegionService` | admin-мутации регионов |
| `ZoneService` | read-only справочник зон |
| `InternalZoneService` | admin-мутации зон |
| `InternalWatchService` | server-stream события из `compute_outbox` |
| `OperationService` | поллинг async-операций (`Get` / `Cancel`) |

**Видимость.** Public-listener (9090): Instance / Disk / DiskType / Image /
Snapshot / Region / Zone / Operation. Internal-listener (9091, не на external
TLS — §Запрет #6): InternalDiskType / InternalRegion / InternalZone /
InternalWatch. Каждый ресурс может иметь публичную (lean) и internal (full,
с инфра-полями: placement/`node_index`/SID) проекцию.

## Связи

`kacho-compute` — **owner Geography** и compute-домена; одновременно — consumer
ресурсов VPC и IAM.

**Кого зовёт kacho-compute:**

- `kacho-compute → kacho-vpc` — валидация NIC-spec при `Create` / attach
  (`Subnet` / `SecurityGroup` существуют и пригодны) + IPAM-аллокация
  эфемерных `Address` (`AddressService` / `InternalAddressService`).
- `kacho-compute → kacho-iam` — `ProjectService.Get` (валидация `project_id`
  на request-path) + `InternalIAMService.Check` (per-RPC authz-gate перед
  мутацией ресурса).

**Кто зовёт kacho-compute:**

- `kacho-vpc → kacho-compute` — `ZoneService.Get` (валидация `zone_id`
  подсети; ребро перевёрнуто в `KAC-15` — раньше было наоборот).
- `kacho-vpc-implement → kacho-compute` — чтение internal-вью `Hypervisor`
  (`node_index`) для data-plane-wiring.
- `kacho-api-gateway → kacho-compute` — проксирование public REST + admin-UI
  Internal-ресурсов (Region/Zone/DiskType) на cluster-internal listener.

Циклы запрещены: ребро `kacho-vpc → kacho-compute` (zone-валидация) и
`kacho-compute → kacho-vpc` (NIC-spec/IPAM) адресуют **разные** домены
(Geography vs VPC), поэтому A↔B-цикла нет. Все кросс-доменные вызовы —
runtime-зависимости (не build), идут сервис→сервис напрямую.
