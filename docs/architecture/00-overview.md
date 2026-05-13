# 00 — Overview

## Роль сервиса

`kacho-compute` — доменный сервис Kachō (control-plane only), отвечающий за
вычислительные ресурсы: **Instance** (виртуальные машины), **Disk** (тома),
**Image** (образы), **Snapshot** (снимки дисков) + read-only справочники
**DiskType**, **Region**, **Zone** (Geography — owner kacho-compute с эпика
`KAC-15`) + internal-only инфра-реестр **Hypervisor** (физические хосты;
placement / HW инвентарь — на публичной поверхности не появляется). Реального
data-plane нет — сервис только хранит конфигурацию, валидирует её, имитирует
жизненный цикл (state-машина Instance) и эмитит события об изменениях через outbox.
Compute-NIC бэкуется ресурсом kacho-vpc `NetworkInterface` (`nic_id`, эпик `KAC-9`).

Внешний контракт повторяет Yandex Cloud Compute API (`kacho.cloud.compute.v1`
== зеркало `yandex.cloud.compute.v1`): proto-форма, имена полей, enum-значения,
`google.api.http` annotations, `(kacho.cloud.api.operation)` options, error
texts, status codes, timestamp precision, regex'ы, behavioural semantics.

```
                  ┌───────────────────────────────────────────────┐
                  │                kacho-compute                  │
                  │                                               │
     public  ──►  │   verbatim-YC API (folder-scoped)             │
                  │   ├─ Instance (ВМ; state-машина статуса)       │
                  │   ├─ Disk (тома; type → DiskType)              │
                  │   ├─ Image (образы; family / GetLatestByFamily)│
                  │   └─ Snapshot (снимки дисков)                  │
                  │                                               │
     public  ──►  │   read-only справочники                       │
                  │   ├─ DiskType (Get / List)                    │
                  │   ├─ Region   (Get / List) — Geography (KAC-15)│
                  │   └─ Zone     (Get / List) — Geography (KAC-15)│
                  │                                               │
     internal ──► │   InternalWatchService (outbox stream :9091)  │
                  │   InternalDiskTypeService (admin CRUD :9091)  │
                  │   InternalRegionService   (admin CRUD :9091)  │
                  │   InternalZoneService     (admin CRUD :9091)  │
                  │   InternalHypervisorService (infra-registry,  │
                  │     sync RPC, :9091; НЕ на external endpoint)  │
                  └───────────────────────────────────────────────┘
```

## Скоуп

### В скоупе (public, verbatim-YC)

**Disk** — Get / List / Create / Update / Delete / ListOperations / Move /
Relocate (частично — cross-zone disk move) / ListSnapshotSchedules
(`blocked:kacho-snapshot-schedule`) + access-bindings RPC (no-op скелет под AAA).

**Image** — Get / GetLatestByFamily / List / Create / Update / Delete /
ListOperations + access-bindings. Create-источники (`oneof source`): `image_id`,
`snapshot_id`, `disk_id`, `uri` (downloads via signed URL — для control-plane
заглушка: статус сразу `READY`); `os_product_ids` → blocked (kacho-marketplace).

**Snapshot** — Get / List / Create (из Disk в `READY`) / Update / Delete /
ListOperations + access-bindings.

**Instance** — Get / List / Create / Update / Delete / Start / Stop / Restart /
Move / ListOperations / AttachDisk / DetachDisk / AddOneToOneNat /
RemoveOneToOneNat / UpdateNetworkInterface / AttachNetworkInterface /
DetachNetworkInterface / UpdateMetadata / GetSerialPortOutput +
access-bindings. **Cross-service refs** валидируются через peer-сервисы (VPC:
`subnet_id` / `security_group_id` / NAT `address`; resource-manager:
`folder_id`). `AttachFilesystem` / `DetachFilesystem` →
`blocked:kacho-filesystem` (нет ресурса Filesystem). `Relocate` → blocked
(нужен cross-zone disk move + restart-семантика). `SimulateMaintenanceEvent` →
no-op (operation сразу done, response Empty).

**DiskType** — Get / List (read-only справочник; seed: `network-hdd`,
`network-ssd`, `network-ssd-nonreplicated`, `network-ssd-io-m3`).

**Region / Zone** — Get / List (read-only публичный справочник Geography).
⚠️ **kacho-compute — owner Geography** (эпик `KAC-15`: перенесено из kacho-vpc;
больше нет proxy / `skipPeer`-fallback — compute читает зоны/регионы из своих
таблиц `regions`/`zones`; другие сервисы валидируют `zone_id` вызовом нашего
`ZoneService.Get`). Seed: `ru-central1` + `ru-central1-{a,b,d}`. Admin-CRUD —
`InternalRegionService` / `InternalZoneService` на `:9091`.

**OperationService** — Get / Cancel (per-сервисная таблица `operations`,
prefix `epd`).

### Internal endpoints (порт `:9091`, НЕ на external TLS endpoint)

- `InternalWatchService.Watch` — outbox stream через LISTEN/NOTIFY
  (`compute_outbox`), для будущих consumer'ов / observability / admin-tooling.
- `InternalDiskTypeService.{Create,Update,Delete}` /
  `InternalRegionService.{Create,Update,Delete}` /
  `InternalZoneService.{Create,Update,Delete}` — admin CRUD справочников
  (kacho-only). Проброшено через api-gateway internal mux на
  `/compute/v1/diskTypes`, `/compute/v1/regions`, `/compute/v1/zones` — только на
  cluster-internal listener, НЕ на external TLS endpoint (`api.kacho.local:443`,
  advertised для внешних клиентов — см. workspace `CLAUDE.md` §запрет 6).
- `InternalHypervisorService.{RegisterHypervisor,GetHypervisor,ListHypervisors,
  UpdateHypervisorState,DeregisterHypervisor}` — **синхронные RPC (не Operation)**:
  инфра-реестр гипервизоров (placement / HW инвентарь — internal-only ресурс, см.
  workspace `CLAUDE.md` §«Инфра-чувствительные данные»). Проброшено через
  api-gateway internal mux на `/compute/v1/hypervisors...`, `:updateState` —
  **только** cluster-internal listener; на external TLS endpoint `GET
  /compute/v1/hypervisors` → 404. `node_index` (0-based) аллоцируется next-free из
  sequence + free-list; consumers — compute reconciler (placement), kacho-vpc-implement
  (читает `node_index` → SRv6-локатор), admin-UI.

## Вне скоупа (proto vendored, реализация отложена)

- **Реальный data plane** — control plane only, как и весь Kachō. Instance.status
  переходит детерминированной state-машиной без реальных гипервизоров; disk data
  не существует; serial-port output синтетический; image download (uri-source)
  мгновенный. См. [`07-known-divergences.md`](07-known-divergences.md) §5.
- **`InstanceGroupService`** (`kacho.cloud.compute.v1.instancegroup`) — отдельный
  крупный домен, отложен (метка `enhancement`, не `blocked`).
- **`DiskPlacementGroupService` / `PlacementGroupService` / `HostGroupService` /
  `HostTypeService` / `GpuClusterService` / `FilesystemService` /
  `SnapshotScheduleService` / `ReservedInstancePoolService` /
  `MaintenanceService`** — proto-файлы есть в `kacho-proto/proto/kacho/cloud/
  compute/v1/`, но реализация отложена (`blocked:*` либо `enhancement`). Связанные
  поля в реализуемых ресурсах помечены `blocked:*` (например `kms_key_id` →
  `blocked:kacho-kms`, `os_product_ids` → `blocked:kacho-marketplace`,
  `snapshot_schedule_ids` → `blocked:kacho-snapshot-schedule`,
  `filesystem_specs[]` → `blocked:kacho-filesystem`); в схеме БД соответствующие
  колонки присутствуют (JSONB nullable), но в worker'е не используются /
  отвергаются `Unimplemented` при попытке задать.

## Шесть базовых принципов проекта

1. **`kacho-corelib`** — единый репо общих переиспользуемых компонентов
   (`ids`, `operations`, `validate`, `filter`, `db`, `grpcsrv`, `grpcclient`,
   `outbox`, `observability`, `config`, `errors`, `retry`, `shutdown`,
   `migrations/common`). В compute-репо живёт ТОЛЬКО compute-доменная логика;
   утилита, нужная 2+ сервисам, выносится в corelib, а не дублируется.
2. **`kacho-proto`** — единый центральный репо всех `.proto`. **External**
   (verbatim-YC) сервисы обязаны повторять YC Compute API 1-в-1 (proto-форма,
   имена полей, enum, `google.api.http`, `(kacho.cloud.api.operation)` options).
   **Internal** (`Internal*`) — на наше усмотрение (контракт может меняться
   свободно, наружу не публикуется). Compute-домен зафиксирован в
   `kacho-proto/proto/kacho/cloud/compute/v1/*.proto` (vendored от YC,
   переименован пакет `yandex.cloud.compute.v1` → `kacho.cloud.compute.v1`,
   gen/go сгенерирован).
3. **Тесты на КАЖДУЮ функциональность** — Go-unit (`internal/service/*_test.go`,
   `internal/handler/*_test.go`, `internal/repo/*integration_test.go`) и newman
   e2e (`tests/newman/cases/*.py`). Каждый RPC и каждый класс кейсов
   (CRUD/VAL/NEG/BVA/CONF/STATE/...) покрыт обоими уровнями. Критерий приёмки:
   любой newman-кейс для kacho-compute должен зеленеть и против реального YC
   Compute API (verbatim parity).
4. **Откладываем на потом ТОЛЬКО кейсы, которым нужен ещё не реализованный
   сервис.** `Disk.Create` со ссылкой на `kms_key_id` требует `kacho-kms`
   (нет → метка `blocked:kacho-kms`). `Image.Create` через `os_product_ids` →
   `kacho-marketplace`. `AttachFilesystem` → `kacho-filesystem`.
   `snapshot_schedule_ids` / `DiskService.ListSnapshotSchedules` →
   `kacho-snapshot-schedule`. Всё остальное (полный Instance lifecycle,
   attach/detach, NAT) реализуем сразу.
5. **Подробная прописанная архитектура** живёт в `docs/architecture/*.md`
   (этот каталог). By-design расхождения с verbatim YC — в
   `docs/architecture/07-known-divergences.md` (НЕ GitHub Issues).
6. **Для каждого модуля — UI**, встроенный в `kacho-ui` (Vite + React SPA).
   Compute-views в `kacho-ui/src/pages/compute/` (Instances / Disks / Images /
   Snapshots — list + detail + create-wizard). UI ходит в REST api-gateway
   (`/compute/v1/...`). Generic-механизм CRUD-страниц, не копипаста под каждый
   ресурс (memory feedback «Fix systemically»). См. [`08-ui.md`](08-ui.md).

## Clean Architecture (слои)

Строгое dependency rule (Uncle Bob):

```
handler ─┐
         ├─→ service ─→ domain
repo ────┤              ↑
clients ─┘              │
                  (только структуры)
```

```
cmd/compute/main.go       composition root: pgxpool, repo'ы, clients, services,
                          handlers, два gRPC-сервера (:9090 public + :9091 internal),
                          graceful shutdown (operations.Wait(30s)).

internal/
  domain/                 pure Go-структуры, импортируют ТОЛЬКО stdlib и kacho-proto.
                          Disk, Image, Snapshot, Instance, NetworkInterface, AttachedDisk,
                          DiskType, Zone, ...

  service/                use-cases (бизнес-логика):
                            DiskService, ImageService, SnapshotService, InstanceService,
                            DiskTypeService, ZoneService.
                            Внутренние setters: InternalDiskType/Zone service-логика.
                          Port-интерфейсы: DiskRepo, ImageRepo, SnapshotRepo, InstanceRepo,
                            DiskTypeRepo, ZoneRepo (== ZoneRegistry), OperationsRepo,
                            FolderClient, VPCClient. Платформенные таблицы — platforms.go.
                            mapRepoErr / stripSentinel — maperr.go.

  ports/                  leaf-пакет: sentinel-ошибки (ErrNotFound / ErrAlreadyExists /
                          ErrFailedPrecondition / ErrInvalidArg / ErrInternal) + portmock
                          (моки port-интерфейсов для unit-тестов, без import-cycle).

  repo/                   pgx-adapter: реализует port-интерфейсы из service + outbox emit
                          (в той же TX, что и domain-INSERT). По файлу на ресурс.

  clients/                gRPC-adapter: folderClient (resource-manager.FolderService),
                          vpcClient (vpc.{Subnet,SecurityGroup,Address}Service.Get). Retry
                          on Unavailable через kacho-corelib/retry. SkipPeerValidation → no-op.

  handler/                тонкий transport-слой: parse-request → service.Foo() →
                          format-response. Никакой бизнес-логики. Public-handlers (на :9090)
                          и Internal-handlers (на :9091) — отдельные файлы, в одной
                          server-инстанции по портам. internal_watch_handler.go —
                          структурно копия kacho-vpc.

  protoconv/              domain ↔ proto конверсия. Единственное место timestamp-truncate
                          до секунд (verbatim YC).

  migrations/             *.sql, embed.FS (migrations.go), goose-стиль up/down.

  config/                 envconfig-структура (KACHO_COMPUTE_*).
```

**Запрещено** (workspace `CLAUDE.md`): `domain/` или `service/` импортируют
`pgx` / grpc-stubs / sqlc-types; бизнес-логика в `handler/`; глобальные
синглтоны вне `cmd/`; ORM (gorm/ent/bun); каскадное удаление через границу
сервиса; новые единые БД.

## Verbatim-YC parity goal

`kacho.cloud.compute.v1` — побайтовое зеркало `yandex.cloud.compute.v1` (после
переименования пакета). Цель — клиент (`yc compute` CLI / SDK), направленный на
api-gateway вместо `compute.api.cloud.yandex.net`, ведёт себя идентично:
тот же proto-message, те же REST-пути, те же error texts и gRPC-коды, та же
timestamp-precision (секунды), те же regex'ы валидации, та же state-машина
Instance, те же precondition-ошибки. Любой newman-кейс должен зеленеть и против
реального YC. Осознанные текущие расхождения (id-syntax, непроверенные тексты) —
зафиксированы в [`07-known-divergences.md`](07-known-divergences.md), баги —
в GitHub Issues.

## Что НЕ owns kacho-compute

- Org/Cloud/Folder — это `kacho-resource-manager`. Compute только проверяет
  существование folder через `folderClient`.
- Network/Subnet/SecurityGroup/Address/**NetworkInterface** — это `kacho-vpc`.
  Compute создаёт/attach'ит kacho-vpc `NetworkInterface` для Instance-NIC'ов и
  валидирует ссылки через `vpcClient` (не FK); `nic_id` бэкующего NIC хранится в
  `instance_network_interfaces.nic_id`.
- KMS-ключи (`kms_key_id`) — `kacho-kms` (нет → blocked).
- Marketplace-продукты (`os_product_ids`) — `kacho-marketplace` (нет → blocked).
- Filesystem-ресурсы (`AttachFilesystem`) — `kacho-filesystem` (нет → blocked).
- Snapshot-расписания (`snapshot_schedule_ids`) — `kacho-snapshot-schedule` (нет → blocked).
- Operations worker-логика — `kacho-corelib/operations` (таблица `operations`
  копируется через `make sync-migrations`, но включена в `0001_initial.sql`).
- Реальный data-plane (гипервизоры, фактическое forwarding) — другой проект.

## Quick links

- [Resources детально](01-resources.md)
- [Data flows / sequence](02-data-flows.md)
- [Instance lifecycle (state-машина)](03-instance-lifecycle.md)
- [API surface (RPC список)](04-api-surface.md)
- [DB schema + миграции](05-database.md)
- [Conventions + gotchas](06-conventions.md)
- [Known divergences (by-design)](07-known-divergences.md)
- [UI integration](08-ui.md)
- [Go skills applied](09-go-skills-applied.md)
