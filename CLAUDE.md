# kacho-compute — CLAUDE.md

Compute-специфичный CLAUDE.md, дополняющий общий workspace `CLAUDE.md` (лежит в
корне `kacho-workspace/`, подцепляется автоматически через parent-walkup
discovery Claude Code). Этот файл — обязательный контекст при работе из
`project/kacho-compute/` и любых его подпапок.

> **Происхождение:** сервис написан заново на проверенных паттернах `kacho-vpc`
> (flat resources + Operations LRO + Clean Architecture + verbatim YC parity).
> Где видишь «как в VPC» — буквально смотри одноимённый файл в `../kacho-vpc/`.

## 0. Шесть базовых принципов проекта (обязательно)

1. **`kacho-corelib`** — единый репо общих переиспользуемых компонентов
   (`ids`, `operations`, `validate`, `filter`, `db`, `grpcsrv`, `grpcclient`,
   `outbox`, `observability`, `config`, `errors`, `retry`, `shutdown`,
   `migrations/common`). Перед написанием новой утилиты в `kacho-compute` —
   проверь, нет ли её в corelib; если нужна 2+ сервисам — выноси в corelib, не
   дублируй per-service. В compute-репо живёт ТОЛЬКО compute-доменная логика.
2. **`kacho-proto`** — единый центральный репо всех `.proto`. Для **external**
   (verbatim-YC) сервисов proto обязан повторять Yandex Cloud Compute API
   1-в-1 (proto-форма, имена полей, enum-значения, `google.api.http`
   annotations, `(kacho.cloud.api.operation)` options). Для **internal**
   (`Internal*`) сервисов — на наше усмотрение (контракт может меняться
   свободно, наружу не публикуется). Compute-домен уже зафиксирован в
   `kacho-proto/proto/kacho/cloud/compute/v1/*.proto` (vendored от YC,
   переименован пакет `yandex.cloud.compute.v1` → `kacho.cloud.compute.v1`,
   gen/go сгенерирован). Новый internal `.proto` — кладём в тот же каталог.
3. **Тесты на КАЖДУЮ функциональность** — и Go-unit (`internal/service/*_test.go`,
   `internal/handler/*_test.go`, `internal/repo/*integration_test.go`), и
   newman e2e (`tests/newman/cases/*.py`). Каждый RPC и каждый класс кейсов
   (CRUD/VAL/NEG/BVA/CONF/STATE/...) покрыт обоими уровнями. Критерий приёмки:
   **любой newman-кейс для kacho-compute должен зеленеть и против реального YC
   Compute API** (verbatim parity).
4. **Откладываем на потом ТОЛЬКО кейсы, которым нужен ещё не реализованный
   сервис.** Например `Disk.Create` со ссылкой на `kms_key_id` требует
   `kacho-kms` (нет → метка `blocked:kacho-kms`, в теле issue «при каких
   условиях браться»). `Image.Create` через `os_product_ids` ссылается на
   marketplace — тоже blocked. Всё остальное (включая полный Instance
   lifecycle, attach/detach, NAT) реализуем сразу.
5. **Подробная прописанная архитектура** живёт в `docs/architecture/*.md`
   (см. §17). Туда же — `docs/architecture/07-known-divergences.md` для
   by-design расхождений с verbatim YC (НЕ issues).
6. **Для каждого модуля — UI**, встроенный в `kacho-ui` (Vite + React SPA).
   Compute-views в `kacho-ui/src/pages/compute/` (Instances / Disks / Images /
   Snapshots — list + detail + create-wizard). UI ходит в REST api-gateway
   (`/compute/v1/...`). Generic-механизм CRUD-страниц, не копипаста под каждый
   ресурс (memory feedback «Fix systemically»).

## 1. Что это за сервис

Sub-phase 0.4 продукта Kachō. gRPC-сервис управления вычислительными ресурсами:
**Instance, Disk, Image, Snapshot** + read-only справочники **DiskType, Zone**.
Цель — verbatim parity с Yandex Cloud Compute API (`kacho.cloud.compute.v1`
== зеркало `yandex.cloud.compute.v1`): proto-форма, error texts, status codes,
timestamp precision, regex'ы, behavioural semantics, state-машина Instance.

В скоупе (public, verbatim-YC):
- **Disk** — Get / List / Create / Update / Delete / ListOperations / Move /
  Relocate / ListSnapshotSchedules (`blocked:kacho-snapshot-schedule`) +
  access-bindings RPC (no-op скелет под AAA).
- **Image** — Get / GetLatestByFamily / List / Create / Update / Delete /
  ListOperations + access-bindings. Create-источники: `image_id`,
  `snapshot_id`, `disk_id`, `uri` (downloads via signed URL — для control-plane
  заглушка: статус сразу `READY`); `os_product_ids` → blocked (marketplace).
- **Snapshot** — Get / List / Create (из Disk) / Update / Delete /
  ListOperations + access-bindings.
- **Instance** — Get / List / Create / Update / Delete / Start / Stop / Restart /
  Move / ListOperations / AttachDisk / DetachDisk / AddOneToOneNat /
  RemoveOneToOneNat / UpdateNetworkInterface / UpdateMetadata /
  GetSerialPortOutput / AttachFilesystem / DetachFilesystem
  (`blocked:kacho-filesystem` — нет ресурса Filesystem) / Relocate
  (`blocked` — нужен cross-zone disk move) / SimulateMaintenanceEvent (no-op) +
  access-bindings. **Cross-service refs** валидируются через peer-сервисы
  (VPC: subnet_id / security_group_id / address; resource-manager: folder_id).
- **DiskType** — Get / List (read-only справочник; seed: `network-hdd`,
  `network-ssd`, `network-ssd-nonreplicated`, `network-ssd-io-m3`).
- **Zone** — Get / List (read-only; seed зеркалит kacho-vpc zones:
  `ru-central1-a`, `ru-central1-b`, `ru-central1-d`).
- **OperationService** — Get / Cancel (per-сервисная таблица `operations`,
  prefix `epd`).
- Internal endpoints (порт 9091, не выставляется на external TLS endpoint):
  - `InternalWatchService` — outbox stream через LISTEN/NOTIFY (`compute_outbox`),
    для будущих consumer'ов и observability.
  - `InternalDiskTypeService` / `InternalZoneService` — admin CRUD справочников
    (kacho-only, нет в verbatim-YC; проброшено через api-gateway internal mux на
    `/compute/v1/diskTypes`, `/compute/v1/zones` — см. workspace CLAUDE.md §запрет 6).

Вне скоупа (не реализуем сейчас; proto есть, но handler возвращает
`Unimplemented` / `blocked:*`):
- Реальный data plane (control plane only, как и весь Kachō) — Instance.status
  переходит детерминированной state-машиной без реальных гипервизоров; disk
  data не существует; serial-port output — синтетический; image download — мгновенный.
- `InstanceGroupService` (`kacho.cloud.compute.v1.instancegroup`) — отдельный
  крупный домен, отложен (метка `enhancement`, не `blocked`).
- `DiskPlacementGroupService` / `PlacementGroupService` / `HostGroupService` /
  `HostTypeService` / `GpuClusterService` / `FilesystemService` /
  `SnapshotScheduleService` / `ReservedInstancePoolService` /
  `MaintenanceService` — proto vendored, реализация отложена (`blocked:*` либо
  `enhancement` в зависимости от того, нужен ли отдельный store).

## 2. Доменная модель и связи

```
                ┌──────────── Image ◄─────┐ (source)
Instance (1) ───┤                          │
   │            └─ boot_disk / secondary_disks (N) ──→ Disk (N)
   │                                                     │
   │  attached_disk: instance ↔ disk (M:N через          │ (source)
   │  attached_disks таблицу; auto_delete flag)           ▼
   ├─ network_interfaces[] (N): subnet_id, primary_v4_address
   │     {one_to_one_nat: address_id?}, security_group_ids[]   Snapshot (N)
   ├─ filesystem_specs[] → blocked:kacho-filesystem
   └─ status: state-машина (см. §8)

Disk      — zone-level, type_id → DiskType, может иметь source = image|snapshot
Image     — folder-level, family (GetLatestByFamily), source = image|snapshot|disk|uri
Snapshot  — folder-level, source_disk_id (обязателен в Create)
DiskType  — глобальный read-only справочник (id = "network-ssd" и т.п.)
Zone      — глобальный read-only справочник (id = "ru-central1-a" и т.п.)
```

Все мутируемые ресурсы (Instance/Disk/Image/Snapshot) — **folder-level**
(`folder_id` обязателен в Create). Все таблицы **flat** (без K8s envelope
`resource_version`/`generation`/`deletion_timestamp`/`finalizers`/`spec`/`status`
как JSONB) — как в kacho-vpc 1.0. `cloud_id`/`organization_id` в схеме
отсутствуют — фильтрация только по `folder_id` (как в VPC).

**FK contract (same-DB only — запрет workspace CLAUDE.md §4: НЕ каскадить
через границу сервиса):**
- `disks.source_image_id` / `disks.source_snapshot_id` — **НЕ FK** (Image живёт
  в этой же БД, но YC семантика: можно удалить Image, у которого есть Disk; Disk
  просто хранит «откуда создан»). При Create — existence-check в worker'е.
- `snapshots.source_disk_id` — НЕ FK по той же причине (можно удалить Disk,
  оставив Snapshot). Existence-check только на Snapshot.Create.
- `attached_disks (instance_id, disk_id)` — FK ON DELETE RESTRICT на disk_id
  (нельзя удалить Disk пока attached → verbatim YC `"The disk is being used"`);
  на instance_id — ON DELETE CASCADE (Instance.Delete worker сам решает судьбу
  дисков по `auto_delete`: true → DELETE disk; false → остаётся; затем CASCADE
  чистит attached_disks-строки).
- `instance_network_interfaces (instance_id, index)` — FK ON DELETE CASCADE
  (same-table children, не cross-service).
- `instances.boot_disk_id` — диск из `attached_disks` с `is_boot=true`; не отдельный FK.
- Cross-service refs (`network_interfaces[].subnet_id` → VPC subnet,
  `.security_group_ids[]` → VPC SG, `.one_to_one_nat.address_id` → VPC address,
  `instances.folder_id` → RM folder) — **НЕ FK**, валидируются gRPC-вызовом к
  peer-сервису в worker'е Create/Update (через `internal/clients`).

## 3. Resource ID format

Все ресурсы получают ID через `kacho-corelib/ids.NewID(<prefix>)`. Префиксы —
3 символа + 17-char crockford-base32 (всего 20). **Источник истины — `kacho-corelib/ids/ids.go`**:

| Ресурс           | Prefix const                | Значение | Пример                |
|------------------|-----------------------------|----------|-----------------------|
| Instance         | `ids.PrefixInstance`        | `epd`    | `epd + 17 base32`     |
| Disk             | `ids.PrefixDisk`            | `epd`    | `epd + ...`           |
| Image            | `ids.PrefixImage`           | `fd8`    | `fd8 + ...`           |
| Snapshot         | `ids.PrefixSnapshot`        | `fd8`    | `fd8 + ...`           |
| Operation (CMP)  | `ids.PrefixOperationCompute` (== `ids.PrefixInstance`) | `epd` | `epd + ...` |
| DiskType         | литерал-строка (`network-ssd` и т.п.) | — | не prefix-id |
| Zone             | литерал-строка (`ru-central1-a` и т.п.) | — | не prefix-id |

⚠️ Instance/Disk **делят `epd`**; Image/Snapshot **делят `fd8`** — умышленно
(зеркалит VPC где Network/RT/SG/GW/PE делят `enp`, Subnet/Address делят `e9b`).
**Все compute-операции** независимо от ресурса получают prefix `epd`
(`PrefixOperationCompute == PrefixInstance`) — api-gateway opsproxy
маршрутизирует `OperationService.Get(id)` по первым 3 символам id, поэтому все
операции домена должны идти в один backend. `ImageService.Create` вернёт
operation с id `epd...`, внутри которого `response` = Image с id `fd8...`
(так же как в VPC `SubnetService.Create` → op `enp...`, внутри Subnet `e9b...`).

Колонки `id` — `TEXT` (как в VPC после squash; не UUID).
**Не валидировать id-формат sync** на входе RPC (`(length) = "<=50"` из proto —
это max-длина, не format) — verbatim YC: well-formed-но-несуществующий id даёт
**async `NotFound`**; malformed/wrong-prefix id у реального YC → sync
`InvalidArgument "invalid <res> id '<X>'"` (probe 2026-05-11), у нас пока
ловится на DB-уровне → `NotFound` — расхождение, см. `docs/architecture/07-known-divergences.md`
(паритет с поведением kacho-vpc, gotcha #1).

## 4. Архитектурные паттерны (compute-специфичные)

### 4.1 Operations (Long Running Operations)

Все мутации (`Create/Update/Delete/Start/Stop/Restart/Move/AttachDisk/...`)
возвращают `*operation.Operation`, реальная работа — в worker-горутине через
`operations.Run(ctx, opsRepo, opID, fn)`. Шаблон идентичен VPC (см.
`../kacho-vpc/internal/service/route_table.go`):

```go
func (s *DiskService) Create(ctx context.Context, req CreateDiskReq) (*operations.Operation, error) {
    // 1. SYNC: required-поля + format-валидация + sanitization
    if req.FolderID == "" { return nil, status.Error(codes.InvalidArgument, "folder_id required") }
    if req.ZoneID == "" { return nil, status.Error(codes.InvalidArgument, "zone_id required") }
    if err := corevalidate.NameCompute("name", req.Name); err != nil { return nil, err }
    if err := validateDiskSize("size", req.Size); err != nil { return nil, err }
    // ...

    // 2. Создать Operation (prefix всегда PrefixOperationCompute)
    diskID := ids.NewID(ids.PrefixDisk)
    op, err := operations.New(ids.PrefixOperationCompute, fmt.Sprintf("Create disk %s", req.Name),
        &computev1.CreateDiskMetadata{DiskId: diskID})
    if err != nil { return nil, err }
    if err := s.opsRepo.Create(ctx, op); err != nil { return nil, err }

    // 3. ASYNC: Worker
    operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
        return s.doCreate(ctx, diskID, req)
    })
    return &op, nil
}
```

`doCreate` внутри:
- folder existence через `folderClient.Exists` → `NotFound "Folder with id <X> not found"`
- zone existence sync через `ZoneRegistry` (таблица `zones`) → `InvalidArgument`
  (паритет с VPC; TODO probe — YC может давать `NotFound "Zone <X> not found"`).
- type_id existence через `diskTypeRepo.Get` → unknown → `NotFound "Disk type <X> not found"`
  (если type_id пуст — default `network-ssd`).
- если есть `image_id` / `snapshot_id` — existence-check (та же БД) → `NotFound`;
  size >= image.min_disk_size / snapshot.disk_size, иначе `InvalidArgument`.
- repo.Insert (status сразу `READY` — control plane only)
- `return anypb.New(protoconv.Disk(created))` — успех; outbox-event `Disk CREATED`.

### 4.2 Operation Delete-response = Empty

Согласно proto-options всех Delete RPC: `metadata: "DeleteXxxMetadata", response:
"google.protobuf.Empty"`. Worker возвращает `anypb.New(&emptypb.Empty{})`,
metadata уже в `Operation.metadata`. То же для DetachDisk / RemoveOneToOneNat и
других lifecycle-операций — смотри proto-option каждого RPC (`response: "Instance"`
→ возвращаем Instance; `response: "google.protobuf.Empty"` → Empty).

### 4.3 Outbox + LISTEN/NOTIFY

Каждая успешная мутация в `service/*.go` (через worker) пишет событие в
`compute_outbox` в той же транзакции, что и сам ресурс. Триггер
`compute_outbox_notify_trg` шлёт `pg_notify('compute_outbox', sequence_no::text)`.
`InternalWatchHandler.Watch` — копия VPC-логики (dedicated pgx.Conn вне пула,
`LISTEN compute_outbox`, catchup batch 100, `WaitForNotification` timeout 30s,
per-stream semaphore `KACHO_COMPUTE_WATCH_MAX_STREAMS` default 32). Файл
`internal/handler/internal_watch_handler.go` — структурно идентичен
`../kacho-vpc/internal/handler/internal_watch_handler.go`.

### 4.4 UpdateMask discipline

YC verbatim: каждый Update RPC принимает `google.protobuf.FieldMask`.
**Decision table** (как в VPC §4.4):
- mask содержит unknown поле → `InvalidArgument` (через `corevalidate.UpdateMask` с known-set).
- mask содержит **immutable** поле → `InvalidArgument` (`"<field> is immutable after <Resource>.Create"`).
- mask пустой → full-object PATCH: применяются все mutable поля; immutable из тела
  **silently игнорируются** (verbatim YC).
- mask содержит mutable поле → применяется; валидируется по тем же правилам, что Create.

**Immutable fields:**
- Disk: `type_id`, `zone_id`, `block_size`, `source` (image_id/snapshot_id) —
  immutable; mutable: `name`, `description`, `labels`, `size` (только увеличение —
  `InvalidArgument "Disk size can only be increased"` при уменьшении),
  `disk_placement_policy`. ⚠️ верхняя граница `size` в Update меньше чем в Create
  (`4194304-4398046511104` vs `4194304-28587302322176`) — из proto `(value)`.
- Image: `family`, `min_disk_size`, `os`, `product_ids`, `pooled` — immutable;
  mutable: `name`, `description`, `labels`.
- Snapshot: `source_disk_id`, `disk_size`, `storage_size` — immutable; mutable:
  `name`, `description`, `labels`.
- Instance: `zone_id`, `boot_disk` — immutable (изменяются через AttachDisk/Relocate);
  mutable: `name`, `description`, `labels`, `service_account_id`, `network_settings`,
  `placement_policy`, `scheduling_policy`. `metadata` — через `UpdateMetadata` RPC,
  не через Update. `resources_spec` (cores/memory) и `platform_id` — изменяются
  только когда Instance STOPPED (verbatim YC `FailedPrecondition "Instance must be stopped"`).

### 4.5 Filter parsing & Pagination

`List*` RPC принимают `filter` строку YC-syntax (`name="<v>"`; для текущей фазы
только `name=`), парсится через `kacho-corelib/filter.Parse` с whitelist полей.
`order_by` (`"createdAt desc"`, default `"id asc"`) — пока игнорируется/частично.
Pagination — cursor `(created_at, id)` ASC,ASC; `page_token` opaque base64;
`page_size` через `corevalidate.PageSize` (0→50, max 1000); garbage token →
`InvalidArgument`. Зеркаль `../kacho-vpc/internal/repo/paging.go`.

### 4.6 Instance state-машина — см. §8

## 5. Validation layering

**Sync (до создания Operation):**
- Required: `folder_id` (Create), `zone_id` (Disk/Instance Create), `name` где
  proto помечает; `Snapshot.Create` требует `disk_id`; `Image.Create` — один из
  source (`image_id`/`snapshot_id`/`disk_id`/`uri`); `Instance.Create` требует
  `zone_id`, `platform_id`, `resources_spec`, `boot_disk_spec`, минимум 1
  `network_interface_spec`.
- Format: `corevalidate.NameCompute` для Disk/Image/Snapshot/Instance — proto
  `(pattern) = "|[a-z]([-_a-z0-9]{0,61}[a-z0-9])?"` (**lowercase**-only +
  digits + hyphens + underscore, empty allowed, start с буквы). ⚠️ это НЕ
  `NameVPC` (там uppercase разрешён) — нужен отдельный `corevalidate.NameCompute`
  в corelib `validate/validate.go` (regex `^([a-z]([-_a-z0-9]{0,61}[a-z0-9])?)?$`).
  Probe реального YC для точного контракта (`docs/architecture/07-known-divergences.md`).
- `Description` ≤256, `Labels` ≤64 пар (key regex `[a-z][-_./\\@0-9a-z]*`).
- `zone_id` — required + existence через `ZoneRegistry`.
- Disk `size` — `[4194304 .. 28587302322176]` на Create, `[4194304 .. 4398046511104]`
  на Update (из proto `(value)`).
- `block_size` — default 4096; whitelist {4096, 8192, ...} (probe YC точный set).
- Instance `resources_spec`: `cores` per-platform set, `memory` кратно GB и
  в range, `core_fraction` ∈ {0,5,20,50,100}, `gpus` per-platform. Сложная
  per-platform валидация — `internal/service/platforms.go` (таблица платформ
  `standard-v1/v2/v3`, `highfreq-v3`, `gpu-*`).
- `metadata` — суммарно ≤256 KiB.
- UpdateMask: known-set + immutable check.
- `secondary_disk_specs` / `boot_disk_spec`: ровно один из {`disk_id`, `disk_spec`}.

**Async (внутри Operation worker):**
- folder existence (`folderClient.Exists` → `NotFound`).
- zone / type_id / source image|snapshot|disk existence → `NotFound` / `InvalidArgument`.
- Instance.Create: для каждого `network_interface_spec` — subnet existence
  (`vpcClient.GetSubnet` → `NotFound "Subnet <X> not found"`), subnet.zone_id ==
  instance.zone_id (иначе `InvalidArgument`), security_group_ids
  (`vpcClient.GetSecurityGroup`), one_to_one_nat.address (`vpcClient.GetAddress`).
- Repo Insert/Update — UNIQUE violation `(folder_id, name)` partial
  `WHERE name <> ''` для всех 4 ресурсов → `ALREADY_EXISTS`; FK violation
  (`attached_disks`) → `FailedPrecondition`.
- Все ошибки маппятся через `mapRepoErr` в gRPC-status (см. §6).

## 6. Error mapping

`internal/service/maperr.go::mapRepoErr` — единая точка трансляции (копия VPC):

| Sentinel error           | gRPC code             | Verbatim YC text source           |
|--------------------------|-----------------------|------------------------------------|
| `ErrNotFound`            | `NOT_FOUND`           | `"<Resource> <id> not found"` (`Disk`, `Image`, `Snapshot`, `Instance`, `Disk type`, `Zone`) |
| `ErrAlreadyExists`       | `ALREADY_EXISTS`      | `"<resource> with name '<n>' already exists ..."` (probe verbatim YC text) |
| `ErrFailedPrecondition`  | `FAILED_PRECONDITION` | varies (`"The disk is being used"`, `"Instance must be stopped"` — probe verbatim) |
| `ErrInvalidArg`          | `INVALID_ARGUMENT`    | varies (size, block_size, cores...) |
| `ErrInternal`            | `INTERNAL`            | `"internal database error"` (no leak) |

`stripSentinel` удаляет sentinel-префикс. Файлы `internal/service/errors.go` +
`internal/ports/errors.go` — type-alias'ы как в VPC (sentinel-ы живут в
leaf-пакете `internal/ports` чтобы `portmock` мог их возвращать без import-cycle).

## 7. Hard-delete (не soft-delete)

`DELETE FROM <table> WHERE id = $1`. Никаких tombstones (паритет с VPC 1.0).
Instance.Delete: worker сначала обрабатывает attached disks (auto_delete=true →
DELETE disk; auto_delete=false → строка attached_disks удалится CASCADE при
DELETE instance), затем DELETE instance (FK cascade чистит
`instance_network_interfaces` и `attached_disks`), затем освобождает
one_to_one_nat addresses (вызов `vpcClient` — best-effort: не fail операцию если
VPC недоступен → log warning).

## 8. Instance state-машина (control-plane имитация)

YC `Instance.Status` enum: `STATUS_UNSPECIFIED, PROVISIONING, RUNNING, STOPPING,
STOPPED, STARTING, RESTARTING, UPDATING, ERROR, CRASHED, DELETING`.

В control-plane-only Kachō нет реального гипервизора → переходы детерминированы и
происходят **внутри worker'а соответствующей операции** (worker «имитирует»
короткую асинхронную работу: меняет status → конечное состояние синхронно в той
же TX, без таймеров).

| RPC          | preconditions (иначе `FailedPrecondition`)     | end status | response |
|--------------|------------------------------------------------|------------|----------|
| Create       | —                                              | RUNNING    | Instance |
| Start        | status ∈ {STOPPED}                             | RUNNING    | Instance |
| Stop         | status ∈ {RUNNING}                             | STOPPED    | Instance |
| Restart      | status ∈ {RUNNING}                             | RUNNING    | Instance |
| Update (resources_spec / platform_id) | status ∈ {STOPPED}    | STOPPED    | Instance |
| Update (name/labels/...)              | any                    | unchanged  | Instance |
| UpdateMetadata | any                                          | unchanged  | Instance |
| AttachDisk   | status ∈ {RUNNING, STOPPED}; disk READY & same zone & not attached | unchanged | Instance |
| DetachDisk   | status ∈ {RUNNING, STOPPED}; disk attached & not boot | unchanged | Instance |
| AddOneToOneNat / RemoveOneToOneNat | status ∈ {RUNNING, STOPPED}; NIC index valid | unchanged | Instance |
| UpdateNetworkInterface | any (SG/NAT) — probe YC                | unchanged  | Instance |
| Move         | any (status сохраняется)                        | unchanged  | Instance |
| GetSerialPortOutput | any → синтетический текст (не операция)    | —          | (sync GetSerialPortOutputResponse) |
| Delete       | any (Instance с диском — отвязывает по auto_delete) | (deleted) | Empty |

⚠️ verbatim YC текстов precondition-ошибок — **probe реального YC** при написании
acceptance/newman (примеры: `"Instance is not running"`, `"Instance is already running"`,
`"Cannot stop instance in state STOPPED"`). До probe — фиксируй текущую формулировку
в `docs/architecture/07-known-divergences.md`. `status_message` поле — всегда пусто
(в control-plane не используется).

## 9. Disk lifecycle

`Disk.Status` enum: `STATUS_UNSPECIFIED, CREATING, READY, ERROR, DELETING`.
Control-plane: Create → insert READY (мгновенно). Update → остаётся READY.
Delete → если есть строка в `attached_disks` (attached) → `FailedPrecondition`
`"The disk <id> is being used"` (probe verbatim YC); иначе DELETE.
Relocate — меняет `zone_id`; precondition: disk не attached
(`FailedPrecondition "Disk is in use"`). `instance_ids` в proto Disk — вычисляется
из `attached_disks`.

## 10. Cross-service clients (`internal/clients/`)

Реализуют port-интерфейсы из `internal/service/ports.go`:
- `folderClient` → `kacho-resource-manager.FolderService.Exists` (как в VPC).
  Используется в worker'е каждого Create/Move. error → `Unavailable "folder check: <err>"`;
  `false` → `NotFound "Folder with id <X> not found"`. Retry on `Unavailable` —
  `kacho-corelib/retry`.
- `vpcClient` → `kacho.cloud.vpc.v1.{SubnetService.Get, SecurityGroupService.Get,
  AddressService.Get}` (для Instance NIC validation). error → `Unavailable`;
  not-found → `NotFound "Subnet <X> not found"` и т.п. Retry on `Unavailable`.
- `imageRepo` / `snapshotRepo` / `diskTypeRepo` / `zoneRepo` — **НЕ clients**,
  это локальные repo (та же БД); используются для existence-check source-ресурсов.

Конфиг адресов: `KACHO_COMPUTE_RESOURCE_MANAGER_GRPC_ADDR`,
`KACHO_COMPUTE_VPC_GRPC_ADDR` + `*_TLS` флаги (см. `internal/config/config.go`).
В dev/test peer-сервисы могут быть недоступны — `KACHO_COMPUTE_SKIP_PEER_VALIDATION=true`
переводит cross-service existence-check в no-op (для unit/newman без поднятого VPC).

## 11. Timestamp precision

Все `created_at` truncate до **seconds** в proto-ответе (verbatim YC):
`CreatedAt: timestamppb.New(s.CreatedAt.Truncate(time.Second))` — единственное
место конверсии: `internal/protoconv/protoconv.go` (как в VPC). БД хранит
микросекунды, клиент видит секунды.

## 12. Migrations

**Боевые миграции** — `internal/migrations/*.sql`, embedded через `embed.FS`
(`migrations.go`). Стартовый baseline:
- `0001_initial.sql` — squashed baseline: таблицы `operations` (схема как у
  corelib `0001_operations.sql`), `disk_types`, `zones`, `disks`, `images`,
  `snapshots`, `instances`, `instance_network_interfaces`, `attached_disks`,
  `compute_outbox`, `compute_watch_cursors`; индексы; partial UNIQUE
  `(folder_id, name) WHERE name <> ''` для disks/images/snapshots/instances; FK
  `attached_disks.disk_id` → disks RESTRICT, `.instance_id` → instances CASCADE;
  FK `instance_network_interfaces.instance_id` → instances CASCADE; outbox trigger
  `compute_outbox_notify_trg`; id-колонки `TEXT`. Seed `disk_types` и `zones`
  делается **в этой же миграции** (как VPC seed'ит regions/zones).

`migrations/` (корень репо) — staging для `make sync-migrations` (только
`0001_operations.sql` от corelib; в `0001_initial.sql` схема `operations` уже включена).
Source of truth — `internal/migrations/`. Goose dialect `postgres`, `cfg.MigrateDSN()`
(без `pool_max_conns` — иначе `database/sql` шлёт unknown PG-param → FATAL, VPC FINDING-007);
pgxpool — `cfg.DSN()` (с `pool_max_conns` если `KACHO_COMPUTE_DB_MAX_CONNS > 0`).

**Запреты:** НЕ редактировать применённые миграции; НЕ модифицировать
`0001_operations.sql` (staging-копия corelib); новая миграция = новый файл с
инкрементным номером (следующий — `0002_*`).

**Schema flat** — без K8s envelope (см. §2). Optimistic concurrency для
read-modify-write (UpdateNetworkInterface-style) — Postgres `xmin::text` (как VPC).

## 13. Local dev

```bash
cd ../kacho-deploy && make dev-up                       # стенд (kind + helm + Postgres)
cd ../kacho-deploy && make reload-svc SVC=compute       # перезапустить только compute
cd ../kacho-deploy && make logs-svc SVC=compute
cd ../kacho-deploy && make psql SVC=compute             # psql kacho_compute
KACHO_COMPUTE_DB_PASSWORD=secret bin/kacho-compute migrate up   # миграции вне kind
make test-short                                         # unit (-short)
make test                                               # unit + integration (testcontainers)
# Newman regression (нужен port-forward api-gateway → localhost:18080)
python3 tests/newman/scripts/gen.py                     # перегенерить коллекции из cases/*.py
tests/newman/scripts/run.sh                             # все ресурсы; --service disk для одного
```

## 14. Тесты

Три уровня, как в kacho-vpc (см. `../kacho-vpc/CLAUDE.md` §14 и
`.claude/agents/TESTING.md` / `TESTING-PRODUCT.md`):

### 14.1 Unit (`internal/service/*_test.go`, `internal/handler/*_test.go`)
Моки port-интерфейсов — из `internal/ports/portmock`. Worker-горутины
`operations.Run` дожидаются детерминированно через `portmock.AwaitOpDone` /
`AwaitAllOpsDone` (poll до `Operation.Done`, дедлайн 2s — не `time.Sleep`).
Запуск: `make test-short`. Service-тест, требующий Postgres → утечка adapter в use-case.

### 14.2 Integration (`internal/repo/*integration_test.go`)
Testcontainers Postgres 16; локально (`make test`) + в CI (job `integration`).
Покрывает: Repo CRUD против реальной БД; partial UNIQUE `(folder_id, name)`;
FK `attached_disks` (attach/detach + delete-blocked + Instance.Delete cascade);
outbox emit транзакционность + LISTEN/NOTIFY; Instance NIC cascade delete; xmin OCC.

### 14.3 E2E / Postman (`tests/newman/`)
**Главная regression-инфраструктура** — black-box покрытие всех публичных RPC.
HTTP через api-gateway (`localhost:18080`). Декларативный генератор: case-файлы
Python (`cases/*.py`, ИСТОЧНИК ИСТИНЫ) → `gen.py` → Postman-коллекции по сервису
(`collections/*.postman_collection.json` — НЕ править руками). Структура — копия
`../kacho-vpc/tests/newman/` (scripts/{gen.py,run.sh,run-incremental.{sh,js}},
environments/local.postman_environment.json, docs/{TAXONOMY,TEST-PLAN,CASES-INDEX,
PRODUCT-REQUIREMENTS,REQUIREMENTS,RESULTS}.md, out/).

Cases по ресурсам: `disk.py`, `image.py`, `snapshot.py`, `instance.py`,
`disk-type.py`, `zone.py`, `operation.py`. Контракт изоляции кейса — как в VPC:
каждый case в своём `runId`, работает внутри pre-allocated
`existingFolderId`/`existingFolderCrossId` (из env), Org/Cloud/Folder НЕ создаёт;
имена суффиксуются `{{runId}}`. Полный case-id: `<DOMAIN>-<ACTION>-<DETAIL>`,
например `DISK-CR-CRUD-OK`, `INST-START-NEG-NOT-STOPPED`, `IMG-GLF-CRUD-OK`.
Cross-service кейсы Instance (NIC → реальный subnet/SG из kacho-vpc) требуют
поднятого kacho-vpc — помечать `# requires kacho-vpc subnet {{subnetId}}`.

### 14.4 Где фиксировать найденные баги и задачи (ОБЯЗАТЕЛЬНО)
**Любой баг / расхождение с verbatim YC / observability-gap / доп-задача →
GitHub Issue в `PRO-Robotech/kacho-compute`** (если не compute-specific, а
общий — в `PRO-Robotech/kacho-workspace`). `TODO.md` упразднён (stub со ссылкой
на Issues). Метки: `bug` / `tech-debt` / `enhancement`; заблокировано
ещё-не-реализованным сервисом → `blocked:kacho-kms` / `blocked:kacho-filesystem` /
`blocked:kacho-snapshot-schedule` / `blocked:kacho-marketplace` + в теле issue
«при каких условиях браться». Кросс-репо эпик → tracking-issue в `kacho-workspace`
(метка `epic`). Коммит, закрывающий issue — trailer `Closes #N`.
**Не баг** (by-design / documented divergence) → `docs/architecture/07-known-divergences.md`,
не issue. **Новое продуктовое требование** → новый `REQ-*` в
`tests/newman/docs/PRODUCT-REQUIREMENTS.md`.

## 15. Top-10 gotchas (наследие kacho-vpc + compute-specific)

1. **id sync-валидация** — well-formed-но-несуществующий id → `NotFound`;
   malformed/wrong-prefix id: реальный YC → `InvalidArgument "invalid <res> id '<X>'"`,
   у нас пока `NotFound` (расхождение — `docs/architecture/07-known-divergences.md`).
2. **Name policy compute = lowercase-only** (proto `(pattern) = "|[a-z]([-_a-z0-9]{0,61}[a-z0-9])?"`)
   — `corevalidate.NameCompute` (НЕ переиспользовать `NameVPC` — там uppercase).
3. **Disk size: разный max в Create vs Update** (28 TiB vs 4 TiB — из proto `(value)`).
4. **Disk.Delete пока attached** → `FailedPrecondition` (FK `attached_disks` RESTRICT на disk_id),
   verbatim text `"The disk <id> is being used"` (probe).
5. **Instance.Update {cores/memory/platform}** требует STOPPED → `FailedPrecondition`.
6. **Hard-delete, не soft** (паритет VPC).
7. **Operation prefix всегда `epd`** (`PrefixOperationCompute == PrefixInstance`)
   независимо от ресурса; resource id может быть `fd8` (Image/Snapshot) или `epd` (Instance/Disk).
8. **Timestamp truncate to seconds** в proto-ответе.
9. **Cross-service refs не FK** — subnet/SG/address (VPC), folder (RM), source
   image/snapshot/disk — validated gRPC-call'ом / existence-check в worker'е, не FK cascade.
10. **`secondary_disk_specs` / `boot_disk_spec`: exactly one of {disk_id, disk_spec}**
    — sync-валидация; нарушение → `InvalidArgument`.

## 16. Внутренние (admin-only) ресурсы

`DiskType` и `Zone` — read-only справочники в публичном API (verbatim YC:
`DiskTypeService`/`ZoneService` есть, но только Get/List — без Create/Update/Delete).
Их **admin-управление** (seed/добавление новых типов) — через `Internal*` сервисы
на порту 9091: `InternalDiskTypeService.{Create,Update,Delete}` /
`InternalZoneService.{Create,Update,Delete}`, проброшено через api-gateway
internal mux на `/compute/v1/diskTypes`, `/compute/v1/zones` (только cluster-internal
listener — НЕ на external TLS endpoint, см. workspace CLAUDE.md §запрет 6 и §16.x).
Любой новый admin-RPC, которого нет в verbatim-YC — добавлять ТОЛЬКО в `Internal*`
сервис, регистрировать через `computeInternalAddr` блок в
`kacho-api-gateway/internal/restmux/mux.go`.

## 17. Ссылки

- Workspace правила: `../../CLAUDE.md`
- Acceptance документ: `../../docs/specs/sub-phase-0.4-compute-acceptance.md` (создаётся `acceptance-author`)
- Proto: `../kacho-proto/proto/kacho/cloud/compute/v1/` (vendored YC, переименован)
- Эталон-сервис (паттерны): `../kacho-vpc/` — буквально смотри одноимённые файлы
- Архитектура: `docs/architecture/` — 00-overview, 01-resources, 02-data-flows,
  03-instance-lifecycle, 04-api-surface, 05-database, 06-conventions,
  07-known-divergences, 08-ui, 09-go-skills-applied, README
- Открытые задачи / баги: GitHub Issues — github.com/PRO-Robotech/kacho-compute/issues
  (`TODO.md` упразднён)
- Spec data model: `../../docs/specs/02-data-model-and-conventions.md`

## 18. Subagents (`.claude/agents/`)

Помимо общих 13 workspace-агентов (acceptance-author/reviewer, proto-sync,
service-scaffolder, rpc-implementer, migration-writer, api-gateway-registrar,
integration-tester, system-design-reviewer, db-architect-reviewer,
go-style-reviewer, proto-api-reviewer, qa-test-engineer) — compute-специализированные:

**Domain-experts:**
- `compute-yc-parity-auditor` — аудит verbatim YC Compute parity (regex, error
  texts, status codes, timestamp, state-машина Instance) — после rpc-implementer перед merge.
- `compute-instance-lifecycle-specialist` — state-машина Instance (precondition-проверки,
  переходы статусов, AttachDisk/DetachDisk/NAT инварианты) — при работе над Instance lifecycle.
- `compute-disk-image-specialist` — Disk/Image/Snapshot инварианты (size constraints,
  source-resolve image↔snapshot↔disk, family/GetLatestByFamily, block_size) — при работе над storage-ресурсами.
- `compute-outbox-watch-engineer` — outbox + LISTEN/NOTIFY + InternalWatchService
  (структурно копия VPC) — при изменении outbox/Watch logic.
- `compute-newman-author` — newman regression suites (декларативные `cases/*.py` → `gen.py`)
  — при добавлении нового RPC в e2e-coverage.

**Testing coaches / load (skills в `.claude/skills/`):**
- `testing-code-coach` (`.claude/agents/TESTING.md`), `testing-product-coach`
  (`.claude/agents/TESTING-PRODUCT.md`) — эталонные практики тестирования кода/продукта.
- `compute-load-testing` (skill) — нагрузочные сценарии Compute (k6 + ghz Jobs);
  defers generic-методологию workspace `load-testing-coach`.

Использовать после соответствующих этапов: yc-parity-auditor — после rpc-implementer
перед merge; instance-lifecycle-specialist — при работе над Instance state-машиной;
disk-image-specialist — при работе над Disk/Image/Snapshot; outbox-watch-engineer —
при изменении outbox/Watch; newman-author — при добавлении RPC в e2e; testing-*-coach —
при review/дизайне тестов.

**Использовать готовые (не создавать заново):** `Explore`, `Plan`, `general-purpose`,
`superpowers:code-reviewer`, `superpowers:brainstorming`, `superpowers:writing-plans`,
`superpowers:test-driven-development`, `superpowers:systematic-debugging`,
`superpowers:requesting-code-review`.

> Напоминание: `Internal.*` методы сервиса не должны попадать в api-gateway
> external TLS endpoint. Это ответственность `api-gateway-registrar`.

## 19. Permissions

`.claude/settings.json` использует `bypassPermissions` для локальной dev-машины.
