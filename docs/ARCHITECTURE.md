# kacho-compute — итоговый архитектурный документ

5-минутный обзор сервиса `kacho-compute` сверху вниз: системный контекст →
контейнеры → компоненты → поведенческие паттерны → доменная модель → БД →
API → операционные аспекты → тестирование → шаги воспроизведения. Детализация
по темам — `docs/architecture/00..09` (ссылки по тексту). Кода в документе нет.

> Происхождение: сервис написан заново на проверенных паттернах `kacho-vpc`
> (flat resources + Operations LRO + Clean Architecture + verbatim YC parity).
> Где видишь «как в VPC» — буквально смотри одноимённый файл в `../kacho-vpc/`.

---

## Часть I. Системный контекст

`kacho-compute` — control-plane сервис управления вычислительными ресурсами
платформы Kachō. Владеет жизненным циклом четырёх публичных folder-scoped
ресурсов — **Instance** (виртуальные машины), **Disk** (тома), **Image**
(образы), **Snapshot** (снимки дисков) — и read-only справочников **DiskType**,
**Region**, **Zone** (Geography — owner kacho-compute с эпика `KAC-15`).
Compute-NIC бэкуется ресурсом kacho-vpc `NetworkInterface` (`nic_id`, эпик
`KAC-9`). ⚠️ `Instance.Create` **не** создаёт и не привязывает NIC автоматически
(auto-NIC материализация удалена в `KAC-266`; инстанс создаётся без сетевых
интерфейсов, правильная сетевая модель — будущая переделка). Сервис **не**
управляет реальным data plane: он только хранит
конфигурацию, валидирует её, имитирует жизненный цикл (детерминированная
state-машина Instance), эмитит события об изменениях.
Внешний контракт повторяет Yandex Cloud Compute API (`kacho.cloud.compute.v1`
== зеркало `yandex.cloud.compute.v1`) по форме и семантике.

**Место в Kachō (polyrepo).** Внешние клиенты ходят через `kacho-api-gateway`
(gRPC-proxy + grpc-gateway REST). Сервисы общаются по gRPC; у каждого — своя
Postgres-БД, шаринг через прямой SQL запрещён.

```
                  kacho-ui (SPA, REST/JSON)
                          |
                          v
                  kacho-api-gateway
            /             |               \
           v public       v public          v internal :9091
   kacho-iam          kacho-vpc          kacho-compute (этот сервис)
   (Account/Project)  (Subnet/SG/Address)  ┌──────────────────────┐
        ^         ^      ^                  │  service layer       │
        │         │      └── vpcClient ─────┤  (Subnet/SG/Address  │
        │         └────────── projectClient ┤   .Get; Project.Exists)│
        │                                   └─┬────────────────────┘
        │                                     v
        └─────────────────────────────  pg-compute (своя БД kacho_compute)
```

**Соседи и контракты.**

| Сосед | Канал | Что делает |
|---|---|---|
| `kacho-api-gateway` | gRPC `:9090` → REST `/compute/v1/...` + opsproxy `/operations/{id}` | Маршрутизирует публичные RPC, преобразует ошибки в HTTP; internal mux на cluster-internal listener для `/compute/v1/diskTypes`,`/compute/v1/regions`,`/compute/v1/zones` (`Internal*` admin) — НЕ на external TLS endpoint |
| `kacho-iam` | gRPC client | `projectClient.Exists(projectID)` — existence-check владельца-проекта в Create (`ProjectService.Get`; колонка-владелец в схеме — legacy-имя `folder_id`) |
| `kacho-vpc` | gRPC client | `vpcClient.{GetSubnet, GetSecurityGroup, GetAddress, NetworkInterfaceService.*, InternalAddressService}` — валидация ссылок Instance + delete kacho-vpc `NetworkInterface` при `Instance.Delete` (если у NIC есть `nic_id`) + IPAM эфемерных Address. ⚠️ авто-создание/привязка NIC при `Instance.Create` удалены в `KAC-266` (инстанс создаётся без сетевых интерфейсов; правильная сетевая модель — будущая переделка) |
| Postgres (`kacho_compute`) | pgx + LISTEN/NOTIFY | Источник истины |
| Внутренние подписчики на изменения | gRPC server-stream `:9091` | `InternalWatchService.Watch` — events из `compute_outbox` |
| Admin-инструменты (UI / curl на api-gateway internal mux) | gRPC `:9091` через internal listener | `InternalDiskTypeService` / `InternalRegionService` / `InternalZoneService` — seed справочников |

**Внешний контракт.** Все мутации (`Create/Update/Delete/Start/Stop/Restart/
Relocate/AttachDisk/DetachDisk/AddOneToOneNat/RemoveOneToOneNat/
UpdateNetworkInterface/UpdateMetadata/SimulateMaintenanceEvent`) возвращают
`Operation` (long-running async); клиент полит `OperationService.Get(id)` до
`done=true`. Все чтения (`Get/List/GetLatestByFamily/GetSerialPortOutput`) —
синхронные. Ошибки — стандартные gRPC-коды с verbatim-YC текстами (`NOT_FOUND
"Disk <id> not found"`, `FAILED_PRECONDITION "The disk <id> is being used"`,
`INVALID_ARGUMENT "Disk size can only be increased"`, ...). Подробно — [00-overview.md](architecture/00-overview.md), [04-api-surface.md](architecture/04-api-surface.md).

**Нефункциональные.** Чтения идемпотентны; мутации идемпотентны по `Operation.id`;
изоляция БД — Read Committed (критичные участки на FK/UNIQUE/`xmin`); цель —
verbatim YC parity (regex, status codes, error texts, timestamp precision,
state-машина); graceful shutdown — до 30 с на drain LRO-worker'ов
(`operations.Wait(30s)`); concurrent watch streams — лимит
`KACHO_COMPUTE_WATCH_MAX_STREAMS` (default 32). **Control plane only**: реального
data plane нет — Instance.status переходит детерминированной state-машиной без
таймеров; disk data не существует; serial-port output синтетический; image
download (uri-source) мгновенный (см. [07-known-divergences.md](architecture/07-known-divergences.md) §5).

---

## Часть II. Контейнерный уровень

Два независимых бинаря в одном образе:
- **`kacho-compute serve`** — поднимает pgxpool, repo'ы, clients, services, handlers,
  два gRPC-сервера: `:9090` public (`KACHO_COMPUTE_GRPC_PORT`) и `:9091`
  internal (`KACHO_COMPUTE_INTERNAL_PORT`); graceful shutdown. Миграции не несёт.
- **`kacho-migrator {up|down|status}`** — отдельный least-privilege binary,
  прокатывает миграции (`internal/migrations/*.sql` через embed.FS, goose dialect
  `postgres`, `cfg.MigrateDSN()` без `pool_max_conns`). В кластере —
  init-container перед основным процессом. serve-образ `kacho-compute` не может
  менять схему (нет embed-миграций и деструктивного `down`).

**Порты.**

| Порт | Сервисы | Кто использует |
|---|---|---|
| `:9090` | `InstanceService`, `DiskService`, `ImageService`, `SnapshotService`, `DiskTypeService`, `RegionService`, `ZoneService`, `OperationService` | api-gateway (external + UI). Geography (Region/Zone) — owner kacho-compute (эпик `KAC-15`) |
| `:9091` | `InternalWatchService`, `InternalDiskTypeService`, `InternalRegionService`, `InternalZoneService` | admin-tooling / UI (через api-gateway internal mux) — **НЕ** на external TLS endpoint (`api.kacho.local:443`) |

**Хранилище.** `kacho_compute` (`pg-compute` StatefulSet в helm umbrella).
Database-per-service. Подробно — [05-database.md](architecture/05-database.md).

**Конфигурация** (`internal/config/config.go`, envconfig): `KACHO_COMPUTE_DB_*`
(host/port/user/password/name/sslmode/max_conns), `KACHO_COMPUTE_GRPC_PORT`
(9090), `KACHO_COMPUTE_INTERNAL_PORT` (9091), `KACHO_COMPUTE_WATCH_MAX_STREAMS`
(32), `KACHO_COMPUTE_IAM_GRPC_ADDR` / `_TLS`,
`KACHO_COMPUTE_VPC_GRPC_ADDR` / `_TLS`, `KACHO_COMPUTE_SKIP_PEER_VALIDATION`
(no-op cross-service checks для тестов), `KACHO_COMPUTE_AUTH_MODE`
(`dev`/`production`/`production-strict`).

---

## Часть III. Компонентный уровень (Clean Architecture)

Строгое dependency rule (`handler/repo/clients → service → domain`). Структура
`internal/`:

- **`domain/`** — pure Go-структуры (импортируют только stdlib и kacho-proto):
  Disk, Image, Snapshot, Instance, NetworkInterface (`nic_id`), AttachedDisk,
  DiskType, Region, Zone.
- **`service/`** — use-cases (бизнес-логика): `DiskService`, `ImageService`,
  `SnapshotService`, `InstanceService`, `DiskTypeService`, `RegionService`,
  `ZoneService` + internal service-логика. Port-интерфейсы: `DiskRepo`,
  `ImageRepo`, `SnapshotRepo`, `InstanceRepo`, `DiskTypeRepo`,
  `ZoneRepo`(=`ZoneRegistry`), `RegionRepo`, `OperationsRepo`, `ProjectClient`,
  `VPCClient`. `platforms.go` — per-platform валидация resources. `maperr.go` —
  `mapRepoErr` / `stripSentinel`.
- **`ports/`** — leaf-пакет: sentinel-ошибки (`ErrNotFound` / `ErrAlreadyExists` /
  `ErrFailedPrecondition` / `ErrInvalidArg` / `ErrInternal`) + `portmock` (моки
  без import-cycle).
- **`repo/`** — pgx-adapter: реализует port-интерфейсы из service + outbox emit
  (в той же TX, что domain-write). По файлу на ресурс.
- **`clients/`** — gRPC-adapter: `projectClient` (iam.ProjectService.Get,
  `internal/clients/iam_client.go`),
  `vpcClient` (vpc.{Subnet,SecurityGroup,Address}Service.Get + `NetworkInterfaceService`
  delete + `InternalAddressService` IPAM). Авто-создание/привязка NIC при
  `Instance.Create` удалены в `KAC-266` (инстанс создаётся без сетевых
  интерфейсов; правильная сетевая модель — будущая переделка). Retry
  on `Unavailable` через `kacho-corelib/retry`; `SkipPeerValidation` → no-op.
- **`handler/`** — тонкий transport: parse-request → service.Foo() →
  format-response. Public-handlers (`:9090`) и Internal-handlers (`:9091`) —
  отдельные файлы. `internal_watch_handler.go` — структурно копия kacho-vpc.
- **`protoconv/`** — domain ↔ proto; единственное место timestamp-truncate до
  секунд.
- **`migrations/`** — `*.sql` + `migrations.go` (embed.FS).
- **`config/`** — envconfig-структура.

`cmd/compute/main.go` — единственное место wiring (composition root): pgxpool →
repo'ы → clients → services → handlers → два gRPC-сервера → graceful shutdown
(`operations.Wait(30s)`). Подробно — [00-overview.md](architecture/00-overview.md) §Clean Architecture.

---

## Часть IV. Поведенческие паттерны

**Operations (LRO).** Каждая мутация: (1) SYNC-валидация (required, `NameCompute`,
size/cores, UpdateMask, oneof-checks), (2) `resID = ids.NewID(...)`,
`operations.New(ids.PrefixOperationCompute, "Create xxx <name>", &CreateXxxMetadata{...})`,
`opsRepo.Create(op)`, (3) `operations.Run(ctx, opsRepo, op.ID, fn doCreate)` →
возврат `&Operation` клиенту. Worker внутри `doCreate`: existence-checks (project
через `projectClient`; zone/type/source — локально; Instance NIC — через
`vpcClient`) → `BEGIN; INSERT; INSERT compute_outbox; COMMIT` → `anypb.New(
protoconv.Xxx(created))`; при ошибке — `error` через `mapRepoErr` → `google.rpc.
Status`. Delete/Stop/Restart/SimulateMaintenanceEvent → response
`google.protobuf.Empty`. Подробно — [02-data-flows.md](architecture/02-data-flows.md) §1.

**ID format.** 3-char prefix + 17-char crockford-base32. Instance/Disk → `epd`;
Image/Snapshot → `fd8`; Operation (Compute) → `epd` (`PrefixOperationCompute ==
PrefixInstance` — api-gateway opsproxy роутит по первым 3 символам, все
compute-операции в один backend). DiskType/Region/Zone — литералы. id-колонки —
`TEXT`.
**Не валидировать id-формат sync** (`(length) = "<=50"` — max-длина, не format) —
расхождение с реальным YC, см. [07-known-divergences.md](architecture/07-known-divergences.md) §1.

**UpdateMask discipline.** unknown поле → `InvalidArgument`; immutable поле →
`InvalidArgument "<field> is immutable after <Resource>.Create"`; пустой mask →
full-PATCH мутабельных (immutable из тела — silent ignore). Immutable: Disk —
`type_id`/`zone_id`/`block_size`/`source`; Image — `family`/`os`/`product_ids`/
`pooled`/`hardware_generation`; Snapshot — `source_disk_id`/`disk_size`/
`storage_size`; Instance — `zone_id`/`boot_disk` (+ `resources_spec`/`platform_id`
только при STOPPED). [06-conventions.md](architecture/06-conventions.md) §UpdateMask.

**Instance state-машина (control-plane имитация).** `Status` enum: `PROVISIONING/
RUNNING/STOPPING/STOPPED/STARTING/RESTARTING/UPDATING/ERROR/CRASHED/DELETING`.
Переходы детерминированы и происходят синхронно внутри TX worker'а (без
таймеров). Стабильные состояния — `RUNNING`/`STOPPED`. Transition table: Create→
RUNNING; Start (STOPPED→)RUNNING; Stop (RUNNING→)STOPPED; Restart (RUNNING→)
RUNNING; Update resources/platform (STOPPED→)STOPPED; AttachDisk/DetachDisk/
AddNat/RemoveNat (RUNNING|STOPPED, status unchanged); AttachNIC/DetachNIC
(STOPPED, status unchanged); Delete (any→deleted). Precondition-нарушение →
`FailedPrecondition` (verbatim текст — probe). Подробно —
[03-instance-lifecycle.md](architecture/03-instance-lifecycle.md).

**Outbox + LISTEN/NOTIFY.** Каждая успешная мутация пишет событие в
`compute_outbox` (`resource_kind` ∈ {Instance, Disk, Image, Snapshot},
`event_type` ∈ {CREATED, UPDATED, DELETED}, `payload` JSONB) в той же TX, что
domain-write. Триггер `compute_outbox_notify_trg` → `pg_notify('compute_outbox',
sequence_no::text)`. `InternalWatchHandler.Watch` — dedicated pgx.Conn вне пула,
`LISTEN compute_outbox`, catchup batch 100, `WaitForNotification` timeout 30s,
per-stream semaphore. Подробно — [02-data-flows.md](architecture/02-data-flows.md) §8.

**Pagination & filter.** cursor `(created_at, id)` ASC,ASC; `page_token` opaque
base64; `page_size` (0→50, max 1000); garbage token → `InvalidArgument`. Filter —
`name="<v>"` (whitelist). `order_by` — пока частично.

**Error mapping.** `internal/service/maperr.go::mapRepoErr`: `ErrNotFound`→
`NOT_FOUND`; `ErrAlreadyExists`→`ALREADY_EXISTS`; `ErrFailedPrecondition`→
`FAILED_PRECONDITION`; `ErrInvalidArg`→`INVALID_ARGUMENT`; `ErrInternal`→
`INTERNAL "internal database error"` (no pgx-text leak). `stripSentinel` убирает
internal-обёртку.

**Timestamp precision.** Все `created_at` truncate до секунд в proto-ответе
(`internal/protoconv/protoconv.go`). БД хранит микросекунды.

---

## Часть V. Доменная модель

```
Instance (1) ──┬─ boot_disk / secondary_disks (через attached_disks, M:N) ──→ Disk (N)
   │           │                                                                │
   │           └─ network_interfaces[] (через instance_network_interfaces, N):   │ source = image|snapshot
   │              {nic_id ──→ kacho-vpc NetworkInterface (source of truth);       ▼
   │               subnet_id/primary_v4_address/security_group_ids — denorm}    Snapshot (N)
   │           └─ filesystem_specs[] → blocked:kacho-filesystem                  source_disk_id
   └─ status: state-машина

Image (folder-level): family (GetLatestByFamily); source = image|snapshot|disk|uri
Disk  (zone-level): type_id → DiskType; source = image|snapshot; instance_ids derived
DiskType — read-only справочник (id — литерал)
Region / Zone — публичный read-only справочник Geography (owner kacho-compute, эпик KAC-15)
```

Все мутируемые ресурсы — folder-level (`folder_id` в Create). Все таблицы flat
(без K8s envelope). `cloud_id`/`organization_id` отсутствуют — фильтрация только
по `folder_id`. Cross-resource links: boot/secondary disk → `attached_disks` →
`disks` (FK `disk_id` RESTRICT, `instance_id` CASCADE); `boot_disk` = строка
`attached_disks` с `is_boot=true`; NIC `nic_id` → kacho-vpc `NetworkInterface`
(НЕ FK; denorm subnet/SG/NAT-address). ⚠️ **`Instance.Create` больше не создаёт и
не привязывает NIC автоматически** (auto-NIC материализация `materializeNICs`
удалена в `KAC-266`): инстанс создаётся без сетевых интерфейсов, правильная
сетевая модель (явная привязка NIC) — будущая переделка. NIC subnet/SG/NAT-address → VPC (НЕ FK, denorm);
disk source / snapshot source / image source → локальные ресурсы (НЕ FK,
existence-check на Create); `zones.region_id` → `regions.id` (FK RESTRICT, same-DB).
Подробно — [01-resources.md](architecture/01-resources.md).

---

## Часть VI. БД-схема

`kacho_compute`. Миграции (`internal/migrations/`): `0001_initial.sql` (squashed
baseline: `operations`/`zones`/`disk_types`/`disks`/`images`/`snapshots`/`instances`/
`instance_network_interfaces`/`attached_disks`/`compute_outbox`/`compute_watch_cursors`,
seed `disk_types`); `0002_nic_ephemeral_address.sql`; `0003_geography_owner.sql`
(kacho-compute становится owner Geography — таблица `regions`, колонка `zones.name`,
FK `zones.region_id → regions.id` RESTRICT, seed `ru-central1` + `ru-central1-{a,b,d}`
здесь; эпик `KAC-15`); `0005_instance_nic_id.sql` (`instance_network_interfaces.nic_id`
— id бэкующего kacho-vpc `NetworkInterface`, эпик `KAC-9`). Особенности: flat
resources; TEXT id-колонки; hard-delete; partial UNIQUE `(folder_id, name) WHERE
name <> ''` (disks/images/snapshots/instances); FK `attached_disks.disk_id` →
disks RESTRICT, `.instance_id` → instances CASCADE; FK
`instance_network_interfaces.instance_id` → instances CASCADE; partial UNIQUE
`attached_disks_boot_uniq` / `attached_disks_device_uniq`; outbox trigger
`compute_outbox_notify_trg`; индекс `images_family_idx` для GetLatestByFamily;
`xmin::text` OCC для UpdateNetworkInterface. Запрет: не редактировать
применённые миграции; новая = новый файл с инкрементным номером. Подробно —
[05-database.md](architecture/05-database.md).

---

## Часть VII. API-поверхность

~60 публичных RPC в 7 публичных сервисах (`InstanceService` — крупнейший:
Get/List/Create/Update/Delete/UpdateMetadata/GetSerialPortOutput/Stop/Start/
Restart/AttachDisk/DetachDisk/AttachNetworkInterface/DetachNetworkInterface/
AddOneToOneNat/RemoveOneToOneNat/UpdateNetworkInterface/ListOperations/
Relocate(blocked)/SimulateMaintenanceEvent(no-op)/access-bindings(no-op);
`DiskService` — CRUD/ListOperations/Relocate(частично)/ListSnapshotSchedules
(blocked)/access-bindings; `ImageService` — CRUD/GetLatestByFamily/ListOperations/
access-bindings; `SnapshotService` — CRUD/ListOperations/access-bindings;
`DiskTypeService`/`RegionService`/`ZoneService` — Get/List) + `OperationService`
(Get/Cancel, `/operations/{id}` через opsproxy). Geography (Region/Zone) — owner
kacho-compute (эпик `KAC-15`: перенесено из kacho-vpc; компьют читает зоны/регионы
из своих таблиц, нет proxy / `skipPeer`-fallback; другие сервисы валидируют
`zone_id` вызовом `ZoneService.Get`). `Instance.Create` NIC spec — exactly one of
{`subnet_id`, `nic_id`} (эпик `KAC-9`; `subnet_id` больше не безусловно `(required)`).
Internal (`:9091`, НЕ на external TLS): `InternalWatchService.Watch` (outbox stream);
`InternalDiskTypeService` / `InternalRegionService` / `InternalZoneService` (admin
CRUD справочников) → `/compute/v1/diskTypes`,`/compute/v1/regions`,`/compute/v1/zones`
— только cluster-internal listener. Подробно —
[04-api-surface.md](architecture/04-api-surface.md).

---

## Часть VIII. Sequence-диаграммы (ключевые)

См. [02-data-flows.md](architecture/02-data-flows.md): Operations LRO worker
(общий шаблон); Disk.Create (folder/zone/type/source checks → INSERT READY →
outbox); Image.Create (source oneof resolve: image|snapshot|disk|uri, uri —
мгновенный download-заглушка); Snapshot.Create (из Disk READY); Instance.Create
(boot-disk resolve/inline-create → INSERT instance+attached_disks в одной TX →
outbox; status PROVISIONING→RUNNING внутри TX; **без авто-NIC** — auto-NIC
материализация удалена в `KAC-266`, инстанс создаётся без сетевых интерфейсов,
правильная сетевая модель — будущая переделка); Instance.AttachDisk/DetachDisk (precondition
status ∈ {RUNNING,STOPPED}; disk READY & same zone & not attached / not boot);
Instance.Delete (auto_delete: true→DELETE disk, false→строка attached_disks
CASCADE; DELETE instance → CASCADE NIC+attached_disks; best-effort NAT-address
release); outbox + LISTEN/NOTIFY + InternalWatchService (catchup + WaitForNotification).

---

## Часть IX. Операционные аспекты

- **Запуск** (kacho-deploy): `make dev-up` (kind + helm + Postgres + все
  сервисы), `make reload-svc SVC=compute`, `make logs-svc SVC=compute`,
  `make psql SVC=compute`. Миграции вне kind: `KACHO_COMPUTE_DB_PASSWORD=secret
  bin/kacho-migrator up`.
- **Graceful shutdown** — `cmd/compute/main.go` ловит сигнал → закрывает gRPC-
  серверы → `operations.Wait(30s)` (drain in-flight worker'ов) → закрывает
  pgxpool. При краше pod'а операция остаётся `done=false` навсегда (известное
  ограничение `operations.Run` без heartbeat/cleanup).
- **Observability** — slog (JSON) стандарт; Prometheus/OTel/pprof — gap (GitHub
  Issue).
- **Логи / диагностика** — `goose_db_version` (миграции), `operations` (LRO
  state), `compute_outbox` (последние события). `KACHO_COMPUTE_SKIP_PEER_VALIDATION
  =true` — отключить cross-service checks для dev/test без поднятых peer-сервисов.

---

## Часть X. Безопасность

TLS terminate в api-gateway. `KACHO_COMPUTE_DB_SSLMODE` (default `disable` для
dev; production — `verify-full`), `KACHO_COMPUTE_*_TLS` для cross-service gRPC.
`KACHO_COMPUTE_AUTH_MODE` (`dev`/`production`/`production-strict`) — fail-closed
гейт перед IAM merge. `Internal*` сервисы — только cluster-internal listener
`:9091`, **не** на external TLS endpoint (workspace `CLAUDE.md` §запрет 6); список
admin paths для будущего TLS-фильтра — `/compute/v1/diskTypes`,
`/compute/v1/regions`, `/compute/v1/zones` (POST/PATCH/DELETE — GET публичен).
**Gap** (как у VPC): полноценный IAM
(claims-extraction / folder-membership через RM), mTLS на `:9091`, NetworkPolicy
для internal-port — scope.

---

## Часть XI. Тестирование

Три уровня (как kacho-vpc): **unit** (`internal/service/*_test.go`,
`internal/handler/*_test.go` — моки port-интерфейсов из `internal/ports/portmock`;
worker'ы дожидаются через `portmock.AwaitOpDone`/`AwaitAllOpsDone`, не
`time.Sleep`; `make test-short`); **integration** (`internal/repo/*integration_test.go`
— testcontainers Postgres 16, локально `make test` + CI job `integration`:
Repo CRUD, partial UNIQUE `(folder_id, name)`, FK `attached_disks` (attach/detach
+ delete-blocked + Instance.Delete cascade), outbox emit транзакционность +
LISTEN/NOTIFY, Instance NIC cascade delete, xmin OCC); **e2e/newman**
(`tests/newman/` — black-box через api-gateway `localhost:18080`; декларативные
`cases/*.py` (`disk.py`/`image.py`/`snapshot.py`/`instance.py`/`disk-type.py`/
`region-zone.py`/`zone.py`/`operation.py`) → `gen.py` → Postman-коллекции
по сервису; новые кейсы: Region/Zone (list/get/admin-CRUD/del-restrict/not-in-vpc),
Instance с `nic_id`. case-id `<DOMAIN>-<ACTION>-<DETAIL>`, напр. `DISK-CR-CRUD-OK`,
`INST-START-NEG-NOT-STOPPED`;
каждый case в своём `runId` внутри pre-allocated `existingFolderId`). **Критерий
приёмки:** любой newman-кейс должен зеленеть и против реального YC Compute API.
Найденные баги → GitHub Issues; by-design расхождения → [07-known-divergences.md](architecture/07-known-divergences.md).

---

## Часть XII. Зависимости от `kacho-corelib`

`ids` (NewID, prefix-константы), `operations` (Operation table + Worker `Run` +
Repo + `Wait`), `validate` (`NameCompute`, `UpdateMask`, `PageSize`, ...),
`filter` (Parse), `db` (pgxpool + transactor), `grpcsrv` (server bootstrap +
interceptors), `grpcclient` (client factory), `outbox`, `observability` (slog),
`config` (envconfig Load), `errors` (sentinel helpers), `retry` (gRPC retry на
Unavailable), `shutdown` (graceful), `migrations/common` (`0001_operations.sql`,
синхронизируется `make sync-migrations`). В compute-репо — ТОЛЬКО compute-доменная
логика; утилита, нужная 2+ сервисам, выносится в corelib.

---

## Часть XIII. Пошаговое воспроизведение проекта

1. **`kacho-proto`** — compute-домен уже зафиксирован
   (`proto/kacho/cloud/compute/v1/*.proto`, vendored от YC, пакет переименован,
   `gen/go` сгенерирован). Новый internal `.proto` — в тот же каталог; `buf
   lint`/`breaking` зелёные.
2. **`kacho-corelib`** — `ids.PrefixInstance/Disk/Image/Snapshot/OperationCompute`,
   `validate.NameCompute`. Если меняются общие пакеты — отдельный PR.
3. **`kacho-compute`**: `internal/migrations/0001_initial.sql` (есть);
   `internal/config/config.go` (есть); затем `internal/domain/` → `internal/ports/`
   (sentinel + portmock) → `internal/service/` (use-cases + port-интерфейсы +
   `platforms.go` + `maperr.go`) → `internal/repo/` (pgx + outbox) →
   `internal/clients/` (projectClient + vpcClient) → `internal/handler/` (public +
   internal + watch) → `internal/protoconv/` → `cmd/compute/main.go`. Тесты — на
   каждую функциональность (unit + integration + newman).
4. **`kacho-api-gateway`** — регистрация публичных RPC (public mux:
   `InstanceService`/`DiskService`/`ImageService`/`SnapshotService`/`DiskTypeService`/
   `RegionService`/`ZoneService`/`OperationService`) + internal mux
   (`computeInternalAddr` блок: `InternalDiskTypeService`/`InternalRegionService`/
   `InternalZoneService` → `/compute/v1/diskTypes`,
   `/compute/v1/regions`, `/compute/v1/zones`
   — только cluster-internal listener, НЕ на external TLS endpoint).
5. **`kacho-deploy`** — helm chart для `pg-compute` + `kacho-compute` deployment
   (init-container `kacho-migrator up`, env-vars).
6. **`kacho-ui`** — compute-views (`src/pages/compute/{instances,disks,images,
   snapshots}/`) на generic CRUD-механизме; polling Operation; см.
   [08-ui.md](architecture/08-ui.md).
7. **`kacho-workspace`** — docs/specs (acceptance-документ
   `sub-phase-0.4-compute-acceptance.md`).

Порядок merge — топологическая сортировка graph'а (proto → corelib → compute →
api-gateway → deploy → workspace); пока вышестоящие изменения не в `main` —
нижестоящий CI временно пиннит siblings к feature-веткам (`ref:`-строки).

---

## Приложения / ссылки

- `CLAUDE.md` — operational правила для AI-агентов (sub-phase 0.4, компактнее).
- `docs/architecture/00..09` — детализация по темам (см. [README.md](architecture/README.md)).
- GitHub Issues — `github.com/PRO-Robotech/kacho-compute/issues` (баги, tech-debt,
  blocked:*). `TODO.md` упразднён.
- `tests/newman/` — e2e regression suite.
- `../kacho-vpc/` — эталон-сервис (compute написан на тех же паттернах).
- `../kacho-proto/proto/kacho/cloud/compute/v1/` — proto-определения.
