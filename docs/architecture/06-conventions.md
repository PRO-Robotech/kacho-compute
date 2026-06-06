# 06 — Conventions & Gotchas

Compute-specific правила, error mapping, top-10 gotchas. Workspace-уровень
(naming, polyrepo, git, Issues) — в `kacho-workspace/CLAUDE.md`. Эталон-паттерны —
`../kacho-vpc/docs/architecture/06-conventions.md` (compute написан на них).

## Naming

| Контекст | Значение |
|---|---|
| Бренд / README / UI | **Kachō** |
| Технические идентификаторы (ASCII) | `kacho` |
| Proto package | `kacho.cloud.compute.v1` |
| Имя репо | `kacho-compute` |
| Postgres database | `kacho_compute` |
| Env-переменные | `KACHO_COMPUTE_<NAME>` (`KACHO_COMPUTE_DB_HOST`, `KACHO_COMPUTE_GRPC_PORT`, `KACHO_COMPUTE_INTERNAL_PORT`, `KACHO_COMPUTE_VPC_GRPC_ADDR`, `KACHO_COMPUTE_IAM_GRPC_ADDR`, `KACHO_COMPUTE_SKIP_PEER_VALIDATION`, `KACHO_COMPUTE_WATCH_MAX_STREAMS`, `KACHO_COMPUTE_AUTH_MODE`, ...) |
| Коммиты | Conventional Commits (`feat:`, `fix:`, `chore:`, `docs:`, `test:`, `ci:`, `refactor:`); подпись — git-config репозитория; **НЕ** добавлять `Co-Authored-By` (локальный проект); коммит, закрывающий issue — trailer `Closes #N` |

**НЕ упоминать «yandex»** в handwritten-коде, README, комментариях, env-name,
именах функций (workspace `CLAUDE.md` §запрет 2). Proto-mirror naming
(`IpVersion`, `SetXxxId`, `OneToOneNat` и т.п.) сохраняется — переименование
сломало бы proto-API.

## Resource ID format

3-char prefix + 17-char crockford-base32 (всего 20). Источник истины —
`kacho-corelib/ids/ids.go`:

| Resource | Prefix const | Значение |
|---|---|---|
| Instance | `ids.PrefixInstance` | `epd` |
| Disk | `ids.PrefixDisk` | `epd` |
| Image | `ids.PrefixImage` | `fd8` |
| Snapshot | `ids.PrefixSnapshot` | `fd8` |
| Operation (Compute) | `ids.PrefixOperationCompute` (== `ids.PrefixInstance`) | `epd` |
| DiskType / Region / Zone | литерал-строка (`network-ssd`, `ru-central1`, `ru-central1-a`) | — (не prefix-id) |

⚠️ **Operation prefix всегда `epd`** независимо от ресурса (`PrefixOperationCompute
== PrefixInstance`) — api-gateway opsproxy маршрутизирует `OperationService.Get(id)`
по первым 3 символам, все compute-операции должны идти в один backend.
`ImageService.Create` → op id `epd...`, внутри `response` — Image с id `fd8...`.
Колонки `id` — `TEXT`, не UUID. **Не валидировать id-формат sync** (`(length) =
"<=50"` из proto — max-длина, не format) — см. gotcha #1 ниже.

## Validation layering

**Sync (до создания Operation):**
- Required: `folder_id` (Create всех 4); `zone_id` (Disk/Instance Create);
  `name` где proto помечает; `Snapshot.Create` требует `disk_id`; `Image.Create`
  — ровно один из `source` (`image_id`/`snapshot_id`/`disk_id`/`uri`,
  `(exactly_one)`); `Instance.Create` требует `zone_id`, `platform_id`,
  `resources_spec`, `boot_disk_spec` (⚠️ NIC-spec больше не требуется — авто-NIC
  материализация удалена в `KAC-266`; инстанс создаётся без сетевых интерфейсов,
  правильная сетевая модель — будущая переделка).
- Format: `corevalidate.NameCompute` для Disk/Image/Snapshot/Instance — proto
  `(pattern) = "|[a-z]([-_a-z0-9]{0,61}[a-z0-9])?"` (**lowercase**-only + digits
  + hyphens + underscore, empty allowed, start с буквы; regex
  `^([a-z]([-_a-z0-9]{0,61}[a-z0-9])?)?$`). ⚠️ это НЕ `NameVPC` (там uppercase
  разрешён). Точный контракт против реального YC — probe (см.
  [`07-known-divergences.md`](07-known-divergences.md) §2). `family` —
  `(pattern) = "|[a-z][-a-z0-9]{1,61}[a-z0-9]"`. `device_name` —
  `(pattern) = "[a-z][a-z0-9-_]{,19}"`. `hostname` — как `name`.
- `Description` ≤256; `Labels` ≤64 пар (key regex `[a-z][-_./\@0-9a-z]*`, value
  regex `[-_./\@0-9a-z]*`, value ≤63).
- `zone_id` — required + existence через `ZoneRegistry` — **локальная таблица
  `zones`** (kacho-compute owns Geography, эпик `KAC-15`; больше нет proxy в
  kacho-vpc и `skipPeer`-fallback). Неизвестная зона → `InvalidArgument "Zone
  <id> not found"` (probe — YC может давать `NotFound`).
- Disk `size` — `[4194304 .. 28587302322176]` на Create, `[4194304 ..
  4398046511104]` на Update (из proto `(value)`). `AttachedDiskSpec.DiskSpec.size`
  — `[4194304 .. 4398046511104]`. `block_size` — default 4096, whitelist
  {4096, ...} (probe YC точный set).
- Image `min_disk_size` — `[4194304 .. 4398046511104]`.
- Instance `resources_spec`: `memory ≤ 274877906944` и per-platform range/multiple;
  `cores ∈ {2,4,6,...,80}` и per-platform set; `core_fraction ∈ {0,5,20,50,100}`;
  `gpus ∈ {0,1,2,4}` и per-platform. Сложная per-platform валидация —
  `internal/service/platforms.go` (таблица платформ `standard-v1/v2/v3`,
  `highfreq-v3`, `gpu-*`). `metadata` — суммарно ≤ 256 KiB (proto: суммарно
  ключей+значений < 512 KB; каждое значение ≤ 256 KB).
  `maintenance_grace_period` — `[1s .. 24h]`.
- `boot_disk_spec` / `secondary_disk_specs[]` / `AttachInstanceDiskRequest.
  attached_disk_spec`: ровно один из `{disk_id, disk_spec}` (proto
  `(exactly_one)`). `DetachInstanceDiskRequest`: ровно один из
  `{disk_id, device_name}`.
- UpdateMask: known-set + immutable check (см. ниже).
- DiskType/Zone `Get/List`: `disk_type_id`/`zone_id` required (`(required) = true`).

**Async (внутри Operation worker):**
- project (владелец) existence (`projectClient.Exists` через
  `kacho-iam.ProjectService.Get` → `NotFound "Folder with id <X> not
  found"` — error-text — legacy-формулировка, колонка-владелец в схеме хранит
  projectId под именем `folder_id`; error → `Unavailable "folder check: <err>"`;
  retry on `Unavailable` через `kacho-corelib/retry`).
- zone / type_id / source image|snapshot|disk existence → `NotFound` /
  `InvalidArgument`.
- Instance.Create: ⚠️ **без авто-NIC** — auto-NIC материализация `materializeNICs`
  удалена в `KAC-266`. Worker больше **не** валидирует per-NIC ссылки, не создаёт
  kacho-vpc `Address` / `NetworkInterface` и не делает attach; инстанс создаётся
  без сетевых интерфейсов. NIC-RPC `AttachToInstance` / `DetachFromInstance` на
  стороне kacho-vpc также сняты в `KAC-266`. Правильная сетевая модель (явная
  привязка NIC) — будущая переделка.
  `Instance.Delete` → для каждого NIC с непустым `nic_id` (если есть от прежних
  данных) delete kacho-vpc NIC (best-effort).
- Repo Insert/Update — UNIQUE violation `(folder_id, name)` partial
  `WHERE name <> ''` для всех 4 ресурсов → `ALREADY_EXISTS`; FK violation
  (`attached_disks.disk_id` RESTRICT) → `FailedPrecondition`; boot/device UNIQUE
  → `InvalidArgument`/`ALREADY_EXISTS`.
- Все ошибки маппятся через `mapRepoErr` в gRPC-status (см. ниже).

## Error mapping (sentinel → gRPC code)

`internal/service/maperr.go::mapRepoErr` — единая точка трансляции (копия VPC):

| Sentinel error (`internal/ports`) | gRPC code | Verbatim YC text source |
|---|---|---|
| `ErrNotFound` | `NOT_FOUND` | `"<Resource> <id> not found"` (`Disk`, `Image`, `Snapshot`, `Instance`, `Disk type`, `Zone`) / `"Folder with id <X> not found"` / `"Subnet <X> not found"` |
| `ErrAlreadyExists` | `ALREADY_EXISTS` | `"<resource> with name '<n>' already exists ..."` (probe verbatim YC text) |
| `ErrFailedPrecondition` | `FAILED_PRECONDITION` | varies (`"The disk <id> is being used"`, `"Instance must be stopped"`, `"Instance is not running"`, ... — probe verbatim) |
| `ErrInvalidArg` | `INVALID_ARGUMENT` | varies (size, block_size, cores, `"Disk size can only be increased"`, ...) |
| `ErrInternal` | `INTERNAL` | `"internal database error"` (no leak — pgx-текст не утекает наружу) |

`stripSentinel` удаляет sentinel-префикс из текста, чтобы клиент видел verbatim
YC сообщение без internal-обёртки. Файлы `internal/service/errors.go` +
`internal/ports/errors.go` — type-alias'ы как в VPC (sentinel-ы живут в
leaf-пакете `internal/ports`, чтобы `portmock` мог их возвращать без
import-cycle). `status.FromError + code != Unknown` guard — не маппить повторно
уже-обёрнутые grpc-статусы.

## Timestamp truncation

Все `created_at` truncate до **seconds** в proto-ответе (verbatim YC):
`CreatedAt: timestamppb.New(s.CreatedAt.Truncate(time.Second))` — единственное
место конверсии: `internal/protoconv/protoconv.go` (как в VPC). БД хранит
микросекунды (`TIMESTAMPTZ DEFAULT now()`), клиент видит секунды.

## UpdateMask discipline

Каждый Update RPC (`UpdateDiskRequest`, `UpdateImageRequest`,
`UpdateSnapshotRequest`, `UpdateInstanceRequest`,
`UpdateInstanceNetworkInterfaceRequest`) принимает `google.protobuf.FieldMask`.
Decision table (как в VPC):
- mask содержит **unknown** поле → `InvalidArgument` (через `corevalidate.UpdateMask`
  с known-set).
- mask содержит **immutable** поле → `InvalidArgument` (`"<field> is immutable
  after <Resource>.Create"`).
- mask **пустой** → full-object PATCH: применяются все mutable-поля; immutable из
  тела **silently игнорируются** (verbatim YC).
- mask содержит mutable-поле → применяется; валидируется по тем же правилам, что
  Create.

**Immutable fields:**
- Disk: `type_id`, `zone_id`, `block_size`, `source` (image_id/snapshot_id).
  mutable: `name`, `description`, `labels`, `size` (только увеличение —
  `InvalidArgument "Disk size can only be increased"` при уменьшении; верхняя
  граница в Update меньше, чем в Create — 4 TiB vs 28 TiB), `disk_placement_policy`.
- Image: `family`, `os`, `product_ids`, `pooled`, `hardware_generation`.
  mutable: `name`, `description`, `labels`, `min_disk_size`.
- Snapshot: `source_disk_id`, `disk_size`, `storage_size`. mutable: `name`,
  `description`, `labels`.
- Instance: `zone_id`, `boot_disk`. mutable: `name`, `description`, `labels`,
  `service_account_id`, `network_settings`, `placement_policy`,
  `scheduling_policy`, `maintenance_policy`, `maintenance_grace_period`,
  `serial_port_settings`. `metadata` — через `UpdateMetadata` RPC, не через
  `Update`. `resources_spec` (cores/memory/core_fraction/gpus) и `platform_id` —
  изменяются только когда Instance `STOPPED` (verbatim YC `FailedPrecondition
  "Instance must be stopped"` — text probe).

## Pagination & Filter

- Pagination — cursor `(created_at, id)` ORDER BY ASC, ASC. `page_token` — opaque
  base64 структуры `{created_at, id}`. `page_size` через `corevalidate.PageSize`
  (0 → DefaultPageSize=50, max 1000). Garbage `page_token` → `InvalidArgument`.
  Зеркаль `../kacho-vpc/internal/repo/paging.go`.
- Filter — `List*` RPC принимают `filter` строку YC-syntax (`name="<v>"`; для
  Instance proto документирует `id/name/created_at/status/zone_id/platform_id/
  host_id`, но текущая фаза — только `name=`). Парсится через
  `kacho-corelib/filter.Parse` с whitelist полей. `order_by` (`"createdAt desc"`,
  default `"id asc"`) — пока игнорируется/частично.

## Hard-delete

`DELETE FROM <table> WHERE id = $1`. Никаких tombstones (паритет VPC 1.0).
`Instance.Delete`: worker сначала обрабатывает attached disks (`auto_delete=true`
→ DELETE disk; `auto_delete=false` → строка `attached_disks` удалится CASCADE при
DELETE instance), затем DELETE instance (FK cascade чистит
`instance_network_interfaces` и `attached_disks`), затем освобождает
one_to_one_nat addresses (best-effort `vpcClient` — не fail операцию если VPC
недоступен → log warning). `Disk.Delete`: attached → `FailedPrecondition "The
disk <id> is being used"` (FK RESTRICT); иначе DELETE.

## Cross-service refs

Все межсервисные ссылки — **НЕ FK** (database-per-service; workspace `CLAUDE.md`
§запрет 4). Валидируются gRPC-вызовом к peer-сервису в worker'е Create/Update:
- `projectClient` → `kacho-iam.ProjectService.Get` (existence-check владельца-проекта
  в worker'е каждого Create; колонка-владелец в схеме — legacy-имя `folder_id`).
- `vpcClient` → `kacho.cloud.vpc.v1.{SubnetService.Get, SecurityGroupService.Get,
  AddressService.Get, NetworkInterfaceService.*}` + `InternalAddressService` на
  kacho-vpc — IPAM-аллокация эфемерных Address + delete kacho-vpc `NetworkInterface`
  при `Instance.Delete` (`kacho-compute → kacho-vpc` runtime-edge). ⚠️ авто-создание/
  привязка NIC при `Instance.Create` удалены в `KAC-266` (инстанс создаётся без
  сетевых интерфейсов; правильная сетевая модель — будущая переделка).
- `imageRepo` / `snapshotRepo` / `diskTypeRepo` / `zoneRepo` / `regionRepo` —
  **НЕ clients**, а локальные repo (та же БД): existence-check source-ресурсов;
  Geography (`zones`/`regions`) — kacho-compute owns (эпик `KAC-15`, нет proxy в
  kacho-vpc).

Конфиг адресов: `KACHO_COMPUTE_IAM_GRPC_ADDR`,
`KACHO_COMPUTE_VPC_GRPC_ADDR` + `*_TLS` флаги. `KACHO_COMPUTE_SKIP_PEER_VALIDATION=true`
переводит cross-service existence-check / NIC-materialization в no-op (синтетический
NIC, `nic_id=''`) — для unit/newman/load-тестов без поднятых peer-сервисов. Retry on
`Unavailable` — `kacho-corelib/retry`.

## Admin boundary

⚠️ **Внутренние служебные сущности не публиковать наружу** (workspace `CLAUDE.md`
§запрет 6; CLAUDE.md compute §16):
- `InternalWatchService` / `InternalDiskTypeService` / `InternalRegionService` /
  `InternalZoneService` — на cluster-internal порту
  `:9091`; `InternalDiskType/Region/ZoneService` проброшены через
  api-gateway internal mux на `/compute/v1/diskTypes`, `/compute/v1/regions`,
  `/compute/v1/zones` — только cluster-internal listener.
- На external TLS endpoint (`api.kacho.local:443`, advertised для внешних клиентов)
  эти paths **не должны** быть доступны. Список admin paths (для будущего
  TLS-middleware фильтра):
  - `/compute/v1/diskTypes` (POST/PATCH/DELETE — kacho-only; GET публичный через `DiskTypeService`)
  - `/compute/v1/regions` (POST/PATCH/DELETE — kacho-only; GET публичный через `RegionService`)
  - `/compute/v1/zones` (POST/PATCH/DELETE — kacho-only; GET публичный через `ZoneService`)
- **Правило для новых admin-RPC**: добавлять **только** в `Internal*` сервис на
  `:9091`, регистрировать через `computeInternalAddr` блок в
  `kacho-api-gateway/internal/restmux/mux.go`. **НЕ** расширять публичные
  `InstanceService`/`DiskService`/etc. для admin-нужд — это сломает verbatim-YC
  parity и засветит admin-функции на TLS endpoint.

## Optimistic concurrency

Без отдельной колонки. Postgres `xmin::text`:

```sql
SELECT field, xmin::text FROM instance_network_interfaces WHERE instance_id=$1 AND idx=$2;
UPDATE instance_network_interfaces SET field=$3 WHERE instance_id=$1 AND idx=$2 AND xmin::text=$4 RETURNING ...;
```

Zero-overhead, миграция не нужна. Используется в `UpdateNetworkInterface`
(read-modify-write над одной NIC-строкой).

## Top-10 gotchas (наследие kacho-vpc + compute-specific)

1. **id sync-валидация** — well-formed-но-несуществующий id → `NotFound`;
   malformed/wrong-prefix id: реальный YC → `InvalidArgument "invalid <res> id
   '<X>'"` (probe 2026-05-11), у нас пока `NotFound` через `repo.Get` —
   расхождение, `docs/architecture/07-known-divergences.md` §1 (паритет с
   поведением kacho-vpc, gotcha #1). Не использовать UUID/format-валидацию на
   входе RPC.
2. **Name policy compute = lowercase-only** (proto `(pattern) = "|[a-z]([-_a-z0-9]
   {0,61}[a-z0-9])?"`) — `corevalidate.NameCompute` (НЕ переиспользовать
   `NameVPC` — там uppercase). Точный контракт против YC — probe.
3. **Disk size: разный max в Create vs Update** (28 TiB vs 4 TiB — из proto
   `(value)`). `AttachedDiskSpec.DiskSpec.size` — 4 TiB max.
4. **Disk.Delete пока attached** → `FailedPrecondition` (FK `attached_disks`
   RESTRICT на `disk_id`), verbatim text `"The disk <id> is being used"` (probe).
5. **Instance.Update {cores/memory/platform}** требует `STOPPED` →
   `FailedPrecondition "Instance must be stopped"` (probe).
6. **Hard-delete, не soft** (паритет VPC).
7. **Operation prefix всегда `epd`** (`PrefixOperationCompute == PrefixInstance`)
   независимо от ресурса; resource id может быть `fd8` (Image/Snapshot) или `epd`
   (Instance/Disk).
8. **Timestamp truncate to seconds** в proto-ответе.
9. **Cross-service refs не FK** — subnet/SG/address (VPC), folder (RM), source
   image/snapshot/disk — validated gRPC-call'ом / existence-check в worker'е, не
   FK cascade. `KACHO_COMPUTE_SKIP_PEER_VALIDATION=true` отключает для тестов.
10. **`secondary_disk_specs` / `boot_disk_spec` / `attached_disk_spec`: exactly
    one of {disk_id, disk_spec}** (proto `(exactly_one)`); `DetachDisk`: exactly
    one of {disk_id, device_name}; `Image.Create source`: exactly one of
    {image_id, disk_id, snapshot_id, uri} — sync-валидация; нарушение →
    `InvalidArgument`.

## Compute-specific gotchas

11. **`metadata` омитится из `Instance` в ответе `List`** (verbatim YC — proto
    явно документирует). `Get` с `view=FULL` включает metadata; `view=BASIC`
    (default) — нет.
12. **`status_message` всегда пусто** — control-plane не использует.
13. **Instance status переходы мгновенны** — нет реального гипервизора;
    промежуточные статусы (`PROVISIONING`/`STARTING`/etc.) видны только внутри TX
    worker'а. См. `07-known-divergences.md` §5 и `03-instance-lifecycle.md`.
14. **`GetSerialPortOutput` — sync, не операция** — возвращает синтетический текст
    напрямую (`GetInstanceSerialPortOutputResponse{contents}`), без LRO.
15. **`uri`-source у `Image.Create` — мгновенный download** (control-plane
    заглушка), статус сразу `READY`, `storage_size` синтетический.
16. **`SimulateMaintenanceEvent` — no-op** (operation сразу `done`, status не
    меняется).
17. **boot disk нельзя `DetachDisk`** (`is_boot=true`) — только удалив Instance.
18. **`Instance.Create` создаётся без авто-NIC** (`materializeNICs` удалён,
    `KAC-266`): инстанс создаётся **без сетевых интерфейсов**, NIC больше не
    создаётся/привязывается на Create; NIC-RPC `AttachToInstance`/`DetachFromInstance`
    на стороне kacho-vpc также сняты. Правильная сетевая модель (явная привязка NIC)
    — будущая переделка. Колонка `instance_network_interfaces.nic_id` (миграция
    `0005_instance_nic_id.sql`) сохраняется в схеме, но при Create не заполняется;
    `Instance.Delete` чистит NIC'и в kacho-vpc, если `nic_id` непустой (от прежних
    данных). Для истории: backed-NIC модель — эпик `KAC-9`.
19. **Geography (Region/Zone) — owner kacho-compute** (эпик `KAC-15`): читается из
    локальных таблиц `regions`/`zones`; нет proxy в kacho-vpc и `skipPeer`-fallback;
    другие сервисы валидируют `zone_id` вызовом `ZoneService.Get`. Миграция
    `0003_geography_owner.sql`.

## Что нельзя делать

- НЕ менять public proto без обновления verbatim-YC parity (kacho-proto + buf
  lint/breaking).
- НЕ редактировать применённые миграции — только новые.
- НЕ добавлять admin-нужное в публичный сервис — только в `Internal*` на `:9091`.
- НЕ возвращать ресурс синхронно из мутирующих RPC — все мутации через `Operation`.
- НЕ делать каскадное удаление через границу сервиса — только same-DB FK.
- НЕ использовать ORM (gorm/ent/bun) — только sqlc + handwritten pgx.
- НЕ упоминать «yandex» в handwritten-коде/README/комментариях/env-name.

## Где фиксировать находки

- **Баг / расхождение с verbatim YC / observability-gap / доп-задача** → GitHub
  Issue в `PRO-Robotech/kacho-compute` (если не compute-specific — в
  `PRO-Robotech/kacho-workspace`). Метки: `bug` / `tech-debt` / `enhancement`;
  заблокировано ещё-не-реализованным сервисом → `blocked:kacho-kms` /
  `blocked:kacho-filesystem` / `blocked:kacho-snapshot-schedule` /
  `blocked:kacho-marketplace` + в теле «при каких условиях браться». Кросс-репо
  эпик → tracking-issue в `kacho-workspace` (метка `epic`). Коммит, закрывающий
  issue — trailer `Closes #N`. `TODO.md` упразднён.
- **Не баг** (by-design / documented divergence) → `docs/architecture/07-known-divergences.md`.
- **Новое продуктовое требование** → новый `REQ-*` в
  `tests/newman/docs/PRODUCT-REQUIREMENTS.md`.
