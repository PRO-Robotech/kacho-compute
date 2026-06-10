# kacho-compute — CLAUDE.md

Compute-специфичный файл, дополняющий workspace `CLAUDE.md` + `.claude/rules/*`
(parent-walkup). Здесь — **только compute-специфика**; общие правила (API-конвенции,
clean-arch, data-integrity, security, git/YouTrack, testing, vault, AI-оснастка) —
в workspace rules, не дублируются. Глубокий reference — `docs/architecture/`.

## 1. Что это за сервис

gRPC control-plane вычислительных ресурсов: **Instance, Disk, Image, Snapshot** +
read-only справочник **DiskType** + домен **Geography (Region / Zone)**. Control-plane
only (нет реального data plane: status переходит детерминированной state-машиной без
гипервизоров; disk data не существует; serial-port output синтетический; image download
мгновенный).

Конвенции API — общие для всего Kachō (`@.claude/rules/api-conventions.md`): flat-resource
+ async `Operation` на мутациях, camelCase JSON, error-format, update_mask discipline
(§4 — compute-specific immutable-поля), timestamp-to-seconds.

В скоупе:
- **Disk** — Get/List/Create/Update/Delete/ListOperations/Move/Relocate +
  ListSnapshotSchedules (`blocked:kacho-snapshot-schedule`).
- **Image** — Get/GetLatestByFamily/List/Create/Update/Delete/ListOperations. Create-источники:
  `image_id`/`snapshot_id`/`disk_id`/`uri` (download — заглушка, статус сразу `READY`);
  `os_product_ids` → `blocked:kacho-marketplace`.
- **Snapshot** — Get/List/Create (из Disk)/Update/Delete/ListOperations.
- **Instance** — Get/List/Create/Update/Delete/Start/Stop/Restart/Move/ListOperations/
  AttachDisk/DetachDisk/AddOneToOneNat/RemoveOneToOneNat/UpdateNetworkInterface/
  UpdateMetadata/GetSerialPortOutput; AttachFilesystem/DetachFilesystem
  (`blocked:kacho-filesystem`); SimulateMaintenanceEvent (no-op). Cross-service refs —
  §6.
- **DiskType** — Get/List (read-only; seed: `network-hdd`, `network-ssd`,
  `network-ssd-nonreplicated`, `network-ssd-io-m3`).
- **Region / Zone** — Get/List (read-only). ⚠️ **kacho-compute — owner Geography** (эпик
  `KAC-15`: перенесено из kacho-vpc). Схема `kacho_compute` держит `regions`(`id,name,created_at`)
  и `zones`(`id,region_id,name,status,created_at`), seed (`ru-central1` + зоны) — в миграции.
  `disk_types.zone_ids`/`Disk.zone_id`/`Instance.zone_id` валидируются локально; другие
  сервисы валидируют свой `zone_id` вызовом нашего `ZoneService.Get` (edge vpc→compute, §6).
- **OperationService** — Get/Cancel (per-сервисная таблица `operations`, prefix `epd`).

Internal endpoints (:9091, не на external TLS):
- `InternalWatchService` — outbox stream через LISTEN/NOTIFY (`compute_outbox`).
- `InternalDiskTypeService` / `InternalRegionService` / `InternalZoneService` — admin CRUD
  справочников (admin-only — §9).

Вне скоупа (proto есть, handler `Unimplemented` / `blocked:*`): реальный data plane;
`InstanceGroupService` (`enhancement`); `DiskPlacementGroup`/`PlacementGroup`/`HostGroup`/
`HostType`/`GpuCluster`/`Filesystem`/`SnapshotSchedule`/`ReservedInstancePool`/`Maintenance`.

## 2. Доменная модель и FK contract

```
Image ◄── (source) ── Disk ──► DiskType (type_id)
  ▲                     ▲
  │ (source)            │ (source: image|snapshot)
Instance (1) ─┬─ attached_disks (M:N, auto_delete flag, is_boot) ──► Disk (N)
              ├─ instance_network_interfaces[] (N): subnet_id, primary_v4_address,
              │     {one_to_one_nat: address_id?}, security_group_ids[]
              └─ status: state-машина (§5)
Snapshot ── (source) ── Disk (source_disk_id)
Region (1) ──► (N) Zone   |   DiskType / Region / Zone — read-only справочники
```

Все мутируемые ресурсы (Instance/Disk/Image/Snapshot) — **project-level** (id проекта
обязателен в Create; DB-колонка-владелец — `project_id`). Все таблицы **flat** (без
K8s-envelope). Полная ER-схема — `docs/architecture/er-diagram.md`.

**FK (same-DB only — workspace `@.claude/rules/data-integrity.md`; cross-service refs — §6):**
- `disks.source_image_id` / `disks.source_snapshot_id` — **НЕ FK** (Image можно удалить,
  Disk хранит «откуда создан»). Existence-check в worker'е Create.
- `snapshots.source_disk_id` — **НЕ FK** (Disk можно удалить, оставив Snapshot).
  Existence-check только на Snapshot.Create.
- `attached_disks.disk_id` → disks **ON DELETE RESTRICT** (нельзя удалить attached Disk →
  `"The disk is being used"`); `attached_disks.instance_id` → instances **ON DELETE CASCADE**
  (Instance.Delete worker решает судьбу дисков по `auto_delete`, затем CASCADE чистит строки).
- `instance_network_interfaces.instance_id` → instances **ON DELETE CASCADE** (same-table children).
- `instances.boot_disk_id` — диск из `attached_disks` c `is_boot=true`, не отдельный FK.
- `zones.region_id` → regions **ON DELETE RESTRICT** (`Region.Delete` блокируется при наличии зон).
- partial UNIQUE `(project_id, name) WHERE name <> ''` для disks/images/snapshots/instances.

## 3. Resource ID prefixes (`kacho-corelib/ids`)

3-char prefix + 17-char crockford-base32. Источник истины — `kacho-corelib/ids/ids.go`:

| Ресурс | const | Prefix |
|---|---|---|
| Instance | `ids.PrefixInstance` | `epd` |
| Disk | `ids.PrefixDisk` | `epd` |
| Image | `ids.PrefixImage` | `fd8` |
| Snapshot | `ids.PrefixSnapshot` | `fd8` |
| Operation (compute) | `ids.PrefixOperationCompute` (== `PrefixInstance`) | `epd` |
| DiskType / Zone / Region | литерал-строка (`network-ssd`, `ru-central1-a`, …) | — |

⚠️ Instance/Disk делят `epd`; Image/Snapshot делят `fd8` — умышленно (по образцу
kacho-vpc, где ресурсы группируются по доменному префиксу). **Все compute-операции** независимо
от ресурса получают prefix `epd` — api-gateway opsproxy маршрутизирует `OperationService.Get(id)`
по первым 3 символам, поэтому все операции домена идут в один backend. `ImageService.Create`
вернёт operation `epd…`, внутри которого `response` = Image с id `fd8…`. Колонки `id` — `TEXT`.

Не валидировать id-формат sync на входе RPC (`(length) = "<=50"` из proto — max-длина, не
format): well-formed-но-несуществующий id → async `NotFound` (ловится на DB-уровне).
By-design нюанс malformed/wrong-prefix id зафиксирован в `docs/architecture/07-known-divergences.md`.

## 4. Compute-specific update_mask immutable-поля

Дисциплина update_mask — общая (`@.claude/rules/api-conventions.md §update_mask`). Per-ресурс:
- **Disk**: immutable `type_id`, `zone_id`, `block_size`, `source` (image_id/snapshot_id);
  mutable `name`, `description`, `labels`, `size` (только увеличение — иначе `InvalidArgument
  "Disk size can only be increased"`), `disk_placement_policy`. ⚠️ верхняя граница `size` в
  Update меньше, чем в Create (Update `4194304..4398046511104` vs Create `4194304..28587302322176`).
- **Image**: immutable `family`, `min_disk_size`, `os`, `product_ids`, `pooled`; mutable
  `name`, `description`, `labels`.
- **Snapshot**: immutable `source_disk_id`, `disk_size`, `storage_size`; mutable `name`,
  `description`, `labels`.
- **Instance**: immutable `zone_id`, `boot_disk` (меняются через AttachDisk/Relocate); mutable
  `name`, `description`, `labels`, `service_account_id`, `network_settings`, `placement_policy`,
  `scheduling_policy`. `metadata` — через `UpdateMetadata` RPC. `resources_spec` (cores/memory)
  и `platform_id` — только когда Instance STOPPED (иначе `FailedPrecondition "Instance must be stopped"`).

## 5. Instance state-машина (control-plane имитация)

`Instance.Status`: `PROVISIONING, RUNNING, STOPPING, STOPPED, STARTING, RESTARTING, UPDATING,
ERROR, CRASHED, DELETING`. Нет гипервизора → переходы детерминированы **внутри worker'а
операции** (меняет status → конечное состояние синхронно в той же TX, без таймеров).

| RPC | precondition (иначе `FailedPrecondition`) | end status | response |
|---|---|---|---|
| Create | — | RUNNING | Instance |
| Start | status ∈ {STOPPED} | RUNNING | Instance |
| Stop | status ∈ {RUNNING} | STOPPED | Instance |
| Restart | status ∈ {RUNNING} | RUNNING | Instance |
| Update (resources_spec/platform_id) | status ∈ {STOPPED} | STOPPED | Instance |
| Update (name/labels/…) / UpdateMetadata | any | unchanged | Instance |
| AttachDisk | status ∈ {RUNNING, STOPPED}; disk READY & same zone & not attached | unchanged | Instance |
| DetachDisk | status ∈ {RUNNING, STOPPED}; disk attached & not boot | unchanged | Instance |
| Add/RemoveOneToOneNat / UpdateNetworkInterface | status ∈ {RUNNING, STOPPED}; NIC index valid | unchanged | Instance |
| Move | any | unchanged | Instance |
| GetSerialPortOutput | any → синтетический текст (sync, не операция) | — | GetSerialPortOutputResponse |
| Delete | any (отвязывает диски по auto_delete) | (deleted) | Empty |

Precondition error-тексты — **канонический Kachō-контракт** (зафиксирован в
`docs/architecture/` и проверяется newman-ассертами): `"Instance must be stopped"`,
`"Instance is not running"`, `"The disk is being used"`. Меняются только осознанно через
тикет. `status_message` в control-plane всегда пусто.

**Disk lifecycle**: `Status` = `CREATING, READY, ERROR, DELETING`. Create → insert READY
(мгновенно). Delete пока attached (строка в `attached_disks`) → `FailedPrecondition "The disk
<id> is being used"` (FK RESTRICT). Relocate меняет `zone_id`; precondition — disk не attached.
`instance_ids` в Disk вычисляется из `attached_disks`.

**Hard-delete** (не soft): `DELETE FROM <table> WHERE id=$1`, без tombstones. Instance.Delete:
worker обрабатывает attached disks (auto_delete=true → DELETE disk; иначе строка
attached_disks уйдёт CASCADE), затем DELETE instance (CASCADE чистит NIC + attached_disks),
затем освобождает one_to_one_nat addresses через `vpcClient` (best-effort: VPC недоступен →
log warning, не fail операции).

## 6. Cross-service refs (`internal/clients/`)

Регламент cross-domain ссылок — `@.claude/rules/data-integrity.md`. Compute-edges (НЕ FK,
валидируются gRPC-вызовом peer-сервиса в worker'е Create/Update; peer недоступен → `Unavailable`):
- **owner-проект** → `kacho-iam.ProjectService.Get` (`internal/clients/iam_client.go`, порт
  `ProjectClient`). Existence-check на каждом Create/Move; not-found → `NotFound "Project with
  id <X> not found"`; недоступен → `Unavailable "project check: <err>"`. Колонка-владелец — `project_id`.
- **VPC** → `vpc.v1.{SubnetService.Get, SecurityGroupService.Get, AddressService.Get}` для
  Instance NIC: subnet existence + `subnet.zone_id == instance.zone_id`, security_group_ids,
  one_to_one_nat.address. not-found → `NotFound "<Resource> <X> not found"`.
- **Входящее ребро vpc→compute** (compute — owner Geography): kacho-vpc валидирует
  `Subnet/AddressPool/Address.zone_id` вызовом нашего `ZoneService.Get`.
- `imageRepo`/`snapshotRepo`/`diskTypeRepo`/`zoneRepo` — **НЕ clients**, локальные repo (та же
  БД), existence-check source-ресурсов и zone/type_id.

Конфиг: `KACHO_COMPUTE_IAM_GRPC_ADDR`, `KACHO_COMPUTE_VPC_GRPC_ADDR` + `*_TLS`. Retry on
`Unavailable` — `kacho-corelib/retry`. `KACHO_COMPUTE_SKIP_PEER_VALIDATION=true` переводит
cross-service existence-check в no-op (unit/newman без поднятого peer).

## 7. Compute-specific error-mapping

`internal/service/maperr.go::mapRepoErr` — единая точка трансляции sentinel→gRPC. Тексты —
канонический Kachō-контракт (часть API, стабильны):
- `ErrNotFound` → `NOT_FOUND` `"<Resource> <id> not found"` (Disk/Image/Snapshot/Instance/
  Disk type/Zone/Region).
- `ErrAlreadyExists` (partial UNIQUE `(project_id, name)`) → `ALREADY_EXISTS`.
- `ErrFailedPrecondition` (FK `attached_disks` RESTRICT, state-машина) → `FAILED_PRECONDITION`.
- `ErrInvalidArg` (size/block_size/cores/zone…) → `INVALID_ARGUMENT`.
- `ErrInternal` → `INTERNAL` `"internal database error"` (без leak pgx/SQL).

`stripSentinel` удаляет sentinel-префикс. Sentinel-ы живут в leaf-пакете `internal/ports`
(чтобы `portmock` возвращал их без import-cycle).

**Compute validation specifics** (sync, до Operation): name policy — **lowercase-only**
`corevalidate.NameCompute` (regex `^([a-z]([-_a-z0-9]{0,61}[a-z0-9])?)?$`; ⚠️ НЕ `NameVPC`,
там uppercase разрешён). Disk `size` range (разный max Create/Update — §4). Instance
`resources_spec` per-platform (`cores`/`memory`/`core_fraction`∈{0,5,20,50,100}/`gpus`) —
`internal/service/platforms.go` (таблица `standard-v1/v2/v3`, `highfreq-v3`, `gpu-*`).
`metadata` ≤256 KiB. `secondary_disk_specs`/`boot_disk_spec` — ровно один из {`disk_id`,`disk_spec`}.

## 8. Migrations (`internal/migrations/*.sql`, goose, embed.FS)

Схема — `kacho_compute`. `0001_initial.sql` — squashed baseline: `operations`, `disk_types`,
`regions`, `zones`, `disks`, `images`, `snapshots`, `instances`, `instance_network_interfaces`,
`attached_disks`, `compute_outbox`, `compute_watch_cursors`; индексы; partial UNIQUE
`(project_id,name) WHERE name<>''`; FK (§2); outbox trigger `compute_outbox_notify_trg`;
id-колонки `TEXT`; seed `disk_types`/`regions`/`zones`. Следующая миграция — `0002_*`.

`migrations/` (корень репо) — staging для `make sync-migrations` (только `0001_operations.sql`
от corelib; в `0001_initial.sql` схема `operations` уже включена). Source of truth —
`internal/migrations/`. Goose dialect `postgres`, `cfg.MigrateDSN()` (без `pool_max_conns` —
иначе `database/sql` шлёт unknown PG-param → FATAL); pgxpool — `cfg.DSN()`. НЕ редактировать
применённую миграцию. OCC для read-modify-write — Postgres `xmin::text`.

## 9. Внутренние (admin-only) ресурсы

`DiskType` / `Region` / `Zone` — read-only в публичном API (Get/List). **Admin-управление**
(seed/CRUD) — через `Internal*` на :9091: `InternalDiskTypeService`/`InternalRegionService`/
`InternalZoneService.{Create,Update,Delete}`, проброшены через api-gateway internal mux
(`/compute/v1/diskTypes`, `/compute/v1/regions`, `/compute/v1/zones`) — только cluster-internal
listener, НЕ на external TLS (`@.claude/rules/security.md`). Любой новый admin-RPC, которого
нет в публичном API — добавлять ТОЛЬКО в `Internal*` сервис и регистрировать через
`computeInternalAddr`-блок в `kacho-api-gateway/internal/restmux/mux.go` (ответственность
`api-gateway-registrar`). `Region.Delete` — FK RESTRICT при наличии зон; `Zone.Delete` проверяет
своих dependents (instances/disks/disk_types); cross-сервисных dependents (vpc-подсети) НЕ
проверяет — admin-ответственность.

## 10. Тесты (специфика; общие правила — `@.claude/rules/testing.md`)

- unit: `internal/service/*_test.go`, `internal/handler/*_test.go` — моки port-интерфейсов из
  `internal/ports/portmock`; worker `operations.Run` дожидается детерминированно
  (`portmock.AwaitOpDone`/`AwaitAllOpsDone`, не `time.Sleep`). Запуск `make test-short`.
- integration: `internal/repo/*integration_test.go` (testcontainers PG16) — Repo CRUD, partial
  UNIQUE `(project_id,name)`, FK `attached_disks` (attach/detach + delete-blocked + Instance.Delete
  cascade), outbox emit + LISTEN/NOTIFY, NIC cascade delete, xmin OCC. Запуск `make test`.
- e2e: `tests/newman/` — главная regression-инфра, black-box через api-gateway
  (`localhost:18080`). Декларативные `cases/*.py` (источник истины) → `gen.py` → коллекции
  по сервису (`collections/*.postman_collection.json`, не править руками). Запуск
  `tests/newman/scripts/run.sh` (`--service disk` для одного). Cases: `disk.py`, `image.py`,
  `snapshot.py`, `instance.py`, `disk-type.py`, `zone.py`, `operation.py`. Изоляция кейса —
  свой `runId`, работает в pre-allocated `existingProjectId` (из env), имена суффиксуются
  `{{runId}}`. case-id: `<DOMAIN>-<ACTION>-<DETAIL>` (`DISK-CR-CRUD-OK`,
  `INST-START-NEG-NOT-STOPPED`). Cross-service Instance NIC-кейсы — `# requires kacho-vpc subnet`.
  Агент `compute-newman-author`.
- финал перед merge: `go test ./... -race` + `golangci-lint run` + `govulncheck` + newman зелёные.

## 11. Local dev

```bash
cd ../kacho-deploy && make dev-up                       # стенд (kind + helm + Postgres)
cd ../kacho-deploy && make reload-svc SVC=compute       # перезапустить только compute
cd ../kacho-deploy && make logs-svc SVC=compute · make psql SVC=compute
make test-short   # unit (-short)        |   make test   # unit + integration (testcontainers)
python3 tests/newman/scripts/gen.py      # перегенерить коллекции из cases/*.py
tests/newman/scripts/run.sh              # newman (нужен port-forward api-gateway → localhost:18080)
```

## 12. Compute subagents (`.claude/agents/`) и skills (`.claude/skills/`)

Domain-specific (общие 13 — из workspace, parent-walkup):
- `compute-instance-lifecycle-specialist` — state-машина Instance (preconditions, переходы,
  AttachDisk/DetachDisk/NAT инварианты).
- `compute-disk-image-specialist` — Disk/Image/Snapshot инварианты (size constraints,
  source-resolve image↔snapshot↔disk, family/GetLatestByFamily, block_size).
- `compute-outbox-watch-engineer` — outbox + LISTEN/NOTIFY + InternalWatchService.
- `compute-newman-author` — newman regression (`cases/*.py` → `gen.py`).
- `compute-conventions-auditor` — аудит конвенций Kachō на внутреннюю согласованность
  (error-format/regex/status-mapping/timestamp/update_mask/sync-vs-async, state-машина Instance),
  НЕ сравнение с чужими облаками. Запускать после `rpc-implementer` перед merge.
- `compute-load-testing` (skill `.claude/skills/compute-load-testing/`) — нагрузка (k6 + ghz);
  defers generic-методологию workspace `load-testing-coach`.

> Напоминание: `Internal.*` методы не должны попадать в api-gateway external TLS endpoint —
> ответственность `api-gateway-registrar`.

## 13. Ссылки

- Workspace rules: `../../CLAUDE.md` + `../../.claude/rules/*`
- Acceptance: `../../docs/specs/sub-phase-0.4-compute-acceptance.md` · Proto:
  `../kacho-proto/proto/kacho/cloud/compute/v1/`
- Глубокий reference: `docs/architecture/` (00-overview, 01-resources, 02-data-flows,
  03-instance-lifecycle, 04-api-surface, 05-database, 06-conventions, 07-known-divergences, er-diagram)
- Spec data model: `../../docs/specs/02-data-model-and-conventions.md`
- Баги/tech-debt: GitHub Issues `PRO-Robotech/kacho-compute/issues` (`TODO.md` упразднён)
