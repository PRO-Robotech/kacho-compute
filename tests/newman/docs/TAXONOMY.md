# Taxonomy кейсов newman — kacho-compute

## Naming convention

```
<DOMAIN>-<METHOD>-<CLASS>-<DETAIL>
```

| Часть | Значения |
|---|---|
| DOMAIN | `DISK` (Disk), `IMG` (Image), `SNAP` (Snapshot), `INST` (Instance), `DT` (DiskType), `ZONE` (Zone), `OP` (Operation) |
| METHOD | `CR` (Create), `GET`, `LST` (List), `UPD` (Update), `DEL` (Delete), `MV` (Move), `REL` (Relocate), `LOP` (ListOperations), `GLF` (GetLatestByFamily — Image), `START`/`STOP`/`RESTART` (Instance state), `AD` (AttachDisk), `DD` (DetachDisk), `NAT` (Add/RemoveOneToOneNat), `UNI` (UpdateNetworkInterface), `ANI` (AttachNetworkInterface), `UMETA` (UpdateMetadata), `SPO` (GetSerialPortOutput), `SME` (SimulateMaintenanceEvent), `STATE` (state-машина), `CANCEL` (Operation.Cancel), `LIFECYCLE` (полный CRUD-цикл) |
| CLASS | `CRUD`, `VAL`, `NEG`, `BVA`, `IDM`, `CONC`, `CONF`, `STATE`, `AUTHZ`, `PAGE`, `FILTER`, `SEC` |
| DETAIL | свободное краткое описание |

Примеры: `DISK-CR-CRUD-OK`, `DISK-CR-BVA-SIZE-BELOW-MIN`, `IMG-GLF-CRUD-OK`,
`INST-STATE-START-FROM-RUNNING`, `INST-DISK-DEL-WHILE-ATTACHED`, `OP-CANCEL-NEG-ALREADY-DONE`.

## Классы

| Класс | Назначение | Техника источника (testing-product-coach) |
|---|---|---|
| `CRUD` | Happy path: создать → прочитать → обновить → удалить; lifecycle-инварианты | §3.7 Use-case |
| `VAL` | Sync-валидация полей: required, format, regex, length, oneof, UpdateMask | §3.1 ECP + §3.2 BVA по полю |
| `NEG` | Async/sync ошибки: NotFound, AlreadyExists, FailedPrecondition (state-машина), InvalidArgument | §3.3 Decision tables |
| `BVA` | Boundary values: disk size [4 MiB, 26 TiB / 4 TiB], name len 63/64, page_size 0/1/1000/1001, labels 64/65, cores set, core_fraction set | §3.2 BVA |
| `IDM` | Идемпотентность retry-safe операций | §3.10 Property-based |
| `CONC` | Concurrency invariants (parallel Create same name → ALREADY_EXISTS) | Stress + invariant |
| `CONF` | Verbatim YC parity: id-prefix (`epd`/`fd8`), created_at до секунд, error text, Operation.response shape, BASIC-view metadata omission | §4.3 Conformance / approval |
| `STATE` | State transition / immutable fields / Instance state-машина (Start/Stop/Restart preconditions, AttachDisk/DetachDisk/NAT) | §3.4 State transition testing |
| `AUTHZ` | Cross-tenant / folder isolation; sync-NF на mutate несуществующего | Permission matrix |
| `PAGE` | Pagination boundary + token roundtrip | §3.2 BVA + §3.10 property |
| `FILTER` | Filter syntax + supported fields | §3.1 ECP по filter expression |
| `SEC` | Security probes: SQL/cmd/XSS/path-traversal в name/filter → не 500, без pgx/stack leak | §4.10 Security |

## Применение по методам (обязательные классы)

| Метод RPC | Обязательные классы | Доп. |
|---|---|---|
| `Get<Resource>` | NEG (NotFound), CONF (NF-text) | — |
| `List<Resource>s` | CRUD, VAL (folder req для folder-scoped), PAGE (4 BVA + token), FILTER | — |
| `Create<Resource>` | CRUD, VAL (all required + format), NEG (parent NotFound, dup name), CONC | BVA (size, name len, labels), IDM, SEC |
| `Update<Resource>` | CRUD (mutable fields), VAL (mask unknown), STATE (immutable fields reject; full-PATCH silent ignore), NEG (sync-NF) | — |
| `Delete<Resource>` | CRUD, NEG (sync-NF), CONF (response=Empty) | STATE (Disk.Delete-while-attached) |
| `Move<Resource>` | CRUD-OK, NEG (dest-NF, sync-NF), VAL (no-dest) | — |
| `Disk.Relocate` | CRUD-OK, NEG (dest-zone-unknown / in-use) | STATE |
| `Image.GetLatestByFamily` | CRUD-OK (newest wins), NEG (family-NF), VAL (folder req) | — |
| `Instance.Start/Stop/Restart` | STATE (precondition: Start←STOPPED, Stop/Restart←RUNNING), CRUD-OK happy, NEG (sync-NF) | — |
| `Instance.Update {resources_spec/platform_id}` | STATE (требует STOPPED → FailedPrecondition; после Stop → OK) | — |
| `Instance.AttachDisk` | CRUD-OK, NEG (wrong-zone, already-attached, not-READY) | — |
| `Instance.DetachDisk` | CRUD-OK, NEG (boot disk → FailedPrecondition, not-attached) | — |
| `Instance.AddOneToOneNat / RemoveOneToOneNat` | CRUD-OK, NEG (NAT already / bad index) | — |
| `Instance.UpdateNetworkInterface` | CRUD-OK, NEG (bad index) | — |
| `Instance.UpdateMetadata` | CRUD-OK (upsert/delete; FULL-view round-trip) | — |
| `Instance.GetSerialPortOutput` | CRUD-OK (contents string), NEG (NotFound) | — |
| `<Resource>.ListOperations` | CRUD-OK (≥1 op), NEG (parent-NF) | — |
| `DiskType.Get/List` / `Zone.Get/List` | CRUD (seeded fixtures), NEG (Get garbage→404), BVA (page_size), CONF (NF-text) | — |
| `OperationService.Get/Cancel` | CRUD-OK (done op), NEG (NotFound, unknown-prefix, Cancel-already-done) | CONF (NF-text) |

## Priority уровни

| Priority | Применение |
|---|---|
| P0 | Security (SEC), data-integrity (FK attached_disks RESTRICT), Instance state-machine preconditions, required-field validation, Disk.Delete-while-attached |
| P1 | CRUD happy, validation P0-полей, conformance с YC, BVA-границы size/cores, NEG dup-name / NotFound |
| P2 | BVA pagination, ECP полей с низким impact, filter, Relocate, GetSerialPortOutput |
| P3 | Cosmetic (labels, description over-max), редкие transitions, SimulateMaintenanceEvent, HTTP-method semantics |

В Newman tag'и для filtering (в `description` каждого case-folder): `class:CRUD`, `priority:P0`, ...

## Что НЕ покрываем в newman (явный scope-cut)

| Зона | Причина | Альтернатива |
|---|---|---|
| Internal RPC (`:9091`) — `InternalWatchService`, `InternalDiskType/ZoneService` | Не публичный API | unit/integration; отдельная suite (вне scope newman v1) |
| `kms_key_id` в Disk/Image Create | Нет `kacho-kms` | `blocked:kacho-kms` issue; покрыть после реализации |
| `os_product_ids` | Нет `kacho-marketplace` | `blocked:kacho-marketplace` |
| `Disk.ListSnapshotSchedules` | Нет `SnapshotSchedule`-ресурса | `blocked:kacho-snapshot-schedule` |
| `Instance.AttachFilesystem/DetachFilesystem` | Нет `Filesystem`-ресурса | `blocked:kacho-filesystem` |
| `Instance.Relocate` | Нужен cross-zone disk move + cross-service оркестрация | `blocked` |
| `Instance.AttachNetworkInterface/DetachNetworkInterface` happy path | Нужен 2-й subnet из kacho-vpc | `enhancement` (есть только NEG sync-NF) |
| access-bindings RPC (`:setAccessBindings` и т.п.) | No-op skeleton под AAA | покрыть после AAA |
| Performance / load | Не функциональная проверка | k6 (`tests/k6/`) |
| Migration up/down | Operational, не product | `kacho-deploy` smoke |

## Cross-service зависимости

- **Instance** NIC.subnet_id / security_group_ids → `kacho-vpc` (нужен поднятый kacho-vpc + seeded subnet/SG в e2e-стенде; кейсы помечены `# requires kacho-vpc subnet {{existingSubnetId}}`).
- **все Create/Move** folder_id → `kacho-resource-manager` (NEG-FOLDER-NOTFOUND-кейсы).
- При `KACHO_COMPUTE_SKIP_PEER_VALIDATION=true` (test-config без поднятого VPC/RM) cross-service existence-checks становятся no-op → кейсы `*-NEG-SUBNET-NOTFOUND` / `*-NEG-FOLDER-NOTFOUND` / `OP-GET-CRUD-FAILED-OP` не сработают — помечены `# requires peer-validation enabled`.

## Test data lifecycle

| Уровень | Подход |
|---|---|
| Per-run | `runId` = `r` + base36(Date.now()) + base36(random), 11 chars (начинается с буквы → проходит compute name regex) |
| Per-suite | `_suiteFolderId` / `_suiteFolderCrossId` из env (pre-allocated в стенде) |
| Per-case | Folder cleanup полагается на `Delete<Resource>` в кейсе; `run-incremental.js` делает periodic + final cleanup тест-папок (FK-safe порядок: instances → snapshots → images → disks) |
| Read-only фикстуры | `existingZoneId`/`existingZoneAltId`/`existingDiskTypeId`/`existingPlatformId`/`existingNetworkId`/`existingSubnetId`/`existingSgId` — НЕ трогаются |
