# Регламент продуктовых требований — kacho-compute (от QA)

Нормативный список **продуктовых требований** к публичному API `kacho-compute`, выведенный из
каталога тест-кейсов (`CASES-INDEX.md`) и контракта (verbatim-YC Compute API + acceptance-spec).
Это **регламент**, на соответствие которому агент `compute-yc-parity-auditor` проверяет любое
изменение кода / proto / миграций / тестов: для каждого затронутого `REQ-*` — соблюдён ли он.
Нарушение → блокирующее замечание.

**Кто ведёт.** Тестировщики добавляют сюда новые `REQ-*` по мере выявления требований (из ревью,
прогонов, probe'ов реального YC). Формат — ниже. Не путать с:
- `REQUIREMENTS.md` — бэклог *улучшений* (testability/contract-clarification asks), не нормативный.
- `docs/architecture/07-known-divergences.md` — намеренные расхождения с YC (исключения из регламента,
  помечаются в REQ как «Divergence: …»).
- баги/задачи — GitHub Issues (`kacho-compute/CLAUDE.md` §14.4).

**Формат REQ.**
```
### REQ-<AREA>-<NN> — <короткий заголовок>           [P0|P1|P2|P3]
<нормативная формулировка: продукт ДОЛЖЕН / НЕ ДОЛЖЕН ...>
- Validated-by: <case-id-паттерны из CASES-INDEX, через запятую> (или «gap — нет кейса»)
- Agent-check: <где смотреть, чтобы проверить соответствие: файл/слой/proto/миграция>
- Divergence: <если намеренное отклонение от verbatim-YC — ссылка>
```

**Области (`<AREA>`):** `RES` (модель/lifecycle), `VAL` (валидация), `NAME`, `SIZE` (disk size),
`UPD` (UpdateMask), `LIST`, `DEL`, `OPS` (Operations LRO), `STATE` (Instance state-машина),
`ATTACH` (disk attach/detach), `NAT`, `IMG` (Image source/family), `SNAP`, `REF` (cross-service refs),
`AUTHZ`, `SEC`, `CONF` (verbatim-YC parity), `CAT` (DiskType/Zone справочники).

---

## A. Модель ресурсов и lifecycle (`RES`)

### REQ-RES-01 — публичные ресурсы Disk / Image / Snapshot / Instance + read-only DiskType / Zone     [P1]
Продукт ДОЛЖЕН предоставлять ресурсы Disk, Image, Snapshot, Instance (folder-scoped, `folder_id`
обязателен в Create; поддерживают Get/List/Create/Update/Delete + ListOperations; Disk/Instance — ещё Move)
и read-only справочники DiskType, Zone (только Get/List).
- Validated-by: `*-LIFECYCLE-CONF`, `*-CR-CRUD-OK`, `*-GET-*`, `*-LST-CRUD-OK`, `DT-*`, `ZONE-*`
- Agent-check: `internal/service/{disk,image,snapshot,instance,disk_type,zone}.go`, `cmd/compute/main.go`, `kacho-proto/.../compute/v1/*_service.proto`.

### REQ-RES-02 — все мутации возвращают Operation (async LRO)                               [P0]
`Create`/`Update`/`Delete`/`Move`/`Relocate`/`Start`/`Stop`/`Restart`/`AttachDisk`/`DetachDisk`/
`AddOneToOneNat`/`RemoveOneToOneNat`/`UpdateNetworkInterface`/`UpdateMetadata` ДОЛЖНЫ возвращать
`operation.Operation`; реальная работа — в worker-горутине; клиент поллит `OperationService.Get(id)`
до `done=true`. Возвращать сам ресурс синхронно из мутации — ЗАПРЕЩЕНО. `GetSerialPortOutput` —
sync (не мутация). DiskType/Zone Create/Update/Delete не существуют (read-only).
- Validated-by: `OP-GET-CRUD-OK`, все `*-CR/*-UPD/*-DEL` (poll-паттерн), `INST-STATE-*`, `INST-AD/DD/NAT/UMETA-*`
- Agent-check: сигнатуры RPC в `.proto` (`returns (operation.Operation)`); `internal/service/*.go` — `operations.New`+`operations.Run`; workspace `CLAUDE.md` запрет #9.

### REQ-RES-03 — Delete-операция: response = Empty, metadata = Delete<Resource>Metadata{<res>_id}   [P1]
В завершённой Delete-`Operation`: `response` = `google.protobuf.Empty`, `metadata` = `Delete<Resource>Metadata` с id ресурса.
- Validated-by: `DISK-DEL-CONF-RESPONSE-EMPTY`, `INST-DEL-CONF-RESPONSE-EMPTY`
- Agent-check: worker всех `Delete` в `internal/service/*.go` (`return anypb.New(&emptypb.Empty{})`); proto-option `response`/`metadata`.

### REQ-RES-04 — operation prefix всегда `epd` независимо от ресурса                           [P0]
ID любой compute-операции начинается с `epd` (`PrefixOperationCompute == PrefixInstance`).
ID ресурсов: Instance/Disk — `epd`; Image/Snapshot — `fd8`. api-gateway OpsProxy маршрутизирует
`/operations/{id}` по первым 3 символам id → backend `compute`.
- Validated-by: `*-CR-CONF-ID-PREFIX-*`, `*-CR-CRUD-OK` (assert id regex), `OP-GET-CRUD-OK`, `OP-GET-NEG-UNKNOWN-PREFIX`
- Agent-check: `kacho-corelib/ids/ids.go` (`PrefixOperationCompute`, `PrefixDisk`/`PrefixInstance`=`epd`, `PrefixImage`/`PrefixSnapshot`=`fd8`); `operations.New(ids.PrefixOperationCompute, ...)`; `kacho-api-gateway/internal/opsproxy/proxy.go` (`"epd": "compute"`).

### REQ-RES-05 — hard-delete, без soft-delete/tombstone                                        [P2]
`Delete` физически удаляет строку (`DELETE FROM`); в схеме нет `deletion_timestamp`/`finalizers`/envelope.
- Validated-by: косвенно `*-DEL-CRUD-OK` + `*-GET-NEG-NOTFOUND` после Delete; `*-LIFECYCLE-CONF`
- Agent-check: `internal/migrations/0001_initial.sql` (нет envelope-колонок); `internal/repo/*.go` (`DELETE FROM`).

### REQ-RES-06 — created_at в proto-ответе truncate до секунд                                  [P1]
Поле `created_at` всех ресурсов в proto-ответе НЕ содержит дробной секунды (verbatim YC).
- Validated-by: `*-CR-CRUD-OK` (assert `assert_created_at_seconds`)
- Agent-check: `internal/protoconv/protoconv.go` — `timestamppb.New(t.Truncate(time.Second))` (единственное место конверсии).
- Divergence: нет (паритет YC).

### REQ-RES-07 — Move в другой folder: ресурс перемещается, остальное (включая Instance.status) сохраняется   [P1]
`Move` Disk/Instance в `destinationFolderId` ДОЛЖЕН успешно завершиться; `folder_id` обновляется;
прочие поля (для Instance — `status`) не меняются. Move несуществующего → sync `NotFound`. Move без `destinationFolderId` → `InvalidArgument`.
- Validated-by: `DISK-MV-CRUD-OK`, `INST-MV-CRUD-OK`, `*-MV-AUTHZ-NF-SYNC`, `DISK-MV-NEG-DEST-NOTFOUND`, `DISK-MV-VAL-NO-DEST`
- Agent-check: `internal/service/{disk,instance}.go` doMove.

---

## B. Валидация (`VAL` / `NAME` / `SIZE`)

### REQ-NAME-01 — name compute-ресурсов: lowercase-only regex `\|[a-z]([-_a-z0-9]{0,61}[a-z0-9])?`   [P1]
Имя Disk/Image/Snapshot/Instance (и `hostname`, `disk_spec.name`) ДОЛЖНО проходить proto-pattern
`|[a-z]([-_a-z0-9]{0,61}[a-z0-9])?` — пустая строка ОК; иначе начинается с lowercase-буквы, далее
lowercase/digits/`-`/`_`, длина ≤63. UPPERCASE / digit-start / hyphen-start / спец-символы → `InvalidArgument`.
⚠️ Это НЕ `NameVPC` (там uppercase разрешён) — нужен `corevalidate.NameCompute`.
- Validated-by: `*-CR-VAL-NAME-{EMPTY-OK,UPPERCASE,DIGIT-START,HYPHEN-START,SPECIAL-CHARS}`, `*-CR-BVA-NAME-{MAX-63,OVER-64}`, `INST-CR-VAL-NAME-*`
- Agent-check: `kacho-corelib/validate/validate.go` (`NameCompute`); вызов в начале каждого `Create`/`Update` (`internal/service/*.go`).
- Divergence: точный YC-контракт для empty/edge — `# probe-needed`; см. `docs/architecture/07-known-divergences.md`.

### REQ-VAL-01 — required-поля Create — sync `InvalidArgument`                                  [P0]
До создания Operation проверяются required: `folder_id` (все), `zone_id` (Disk/Instance),
`size` (Disk), `disk_id` (Snapshot), `platform_id`/`resources_spec`/`boot_disk_spec`/`≥1 network_interface_spec`/`zone_id` (Instance). Отсутствие → `InvalidArgument`.
- Validated-by: `*-CR-VAL-*-REQUIRED`, `INST-CR-VAL-MISSING-*`, `SNAP-CR-VAL-NO-DISK`, `IMG-CR-VAL-FOLDER-REQUIRED`, `*-CR-VAL-EMPTY-BODY`
- Agent-check: начало каждого `Create` в `internal/service/*.go` (sync-checks до `operations.New`).

### REQ-VAL-02 — `description` ≤256, `labels` ≤64 пар (key regex `[a-z][-_./\@0-9a-z]*`)         [P2]
Превышение → `InvalidArgument`.
- Validated-by: `*-CR-BVA-DESC-{MAX-256,OVER-257}`, `*-CR-{VAL-LABELS-UPPERCASE-KEY,VAL-LABELS-INVALID-KEY-CHAR,BVA-LABELS-MAX-64,BVA-LABELS-OVER-65}`
- Agent-check: `corevalidate.Description` / `corevalidate.Labels` в `Create`/`Update`.

### REQ-SIZE-01 — Disk size: Create ∈ [4194304, 28587302322176]; Update ∈ [4194304, 4398046511104]   [P1]
Из proto `(value)`. Вне диапазона → `InvalidArgument`. Update size — только увеличение
(`InvalidArgument` при уменьшении). Верхняя граница Update (4 TiB) меньше Create (≈26 TiB).
- Validated-by: `DISK-CR-BVA-SIZE-{MIN-OK,BELOW-MIN,CREATE-MAX-OK,ABOVE-CREATE-MAX}`, `DISK-UPD-SIZE-{INCREASE-OK,DECREASE-REJECT}`
- Agent-check: `internal/service/disk.go` `validateDiskSize` (разные max для Create/Update); proto `disk_service.proto` `(value)`.

### REQ-VAL-03 — Instance resources: cores ∈ proto-set {2,4,6,...,80}; core_fraction ∈ {0,5,20,50,100}; memory ≤ 274877906944   [P1]
Per-platform валидация. Невалидные → `InvalidArgument`.
- Validated-by: `INST-CR-VAL-CORE-FRACTION-INVALID`, `INST-CR-VAL-CORES-ODD-INVALID`
- Agent-check: `internal/service/platforms.go` + sync-валидация в `InstanceService.Create`.

### REQ-VAL-04 — `boot_disk_spec` / `secondary_disk_specs[i]`: exactly one of {disk_id, disk_spec}   [P1]
И `disk_id`, и `disk_spec` одновременно → `InvalidArgument` (proto `(exactly_one)`).
- Validated-by: `INST-CR-VAL-BOOTDISK-EXACTLY-ONE`
- Agent-check: sync-валидация `AttachedDiskSpec` в `InstanceService.Create`/`AttachDisk`.

---

## C. UpdateMask discipline (`UPD`)

### REQ-UPD-01 — mask с unknown полем → `InvalidArgument`                                       [P1]
- Validated-by: `*-UPD-MASK-UNKNOWN-FIELD`
- Agent-check: `corevalidate.UpdateMask` с known-set в каждом `Update`.

### REQ-UPD-02 — mask с immutable полем → `InvalidArgument` («<field> is immutable after <Resource>.Create»)   [P1]
Immutable: Disk — `type_id`, `zone_id`, `block_size`, `source`; Image — `family`, `min_disk_size`, `os`, `product_ids`, `pooled`;
Snapshot — `source_disk_id`, `disk_size`, `storage_size`; Instance — `zone_id`, `boot_disk`.
- Validated-by: `DISK-UPD-MASK-IMMUTABLE-{TYPE,ZONE}`, `IMG-UPD-MASK-IMMUTABLE-FAMILY`, `SNAP-UPD-MASK-IMMUTABLE-SOURCE-DISK`, `INST-UPD-MASK-IMMUTABLE-ZONE`
- Agent-check: switch в начале каждого `Update` в `internal/service/*.go`.
- Divergence: точный текст — `# probe-needed`.

### REQ-UPD-03 — пустой mask → full-object PATCH; immutable из body silently игнорируются          [P2]
Verbatim YC behaviour.
- Validated-by: `DISK-UPD-MASK-EMPTY-FULL-PATCH`
- Agent-check: ветка empty-mask в `Update`.

### REQ-UPD-04 — Instance.Update {resources_spec / platform_id} требует STOPPED → `FailedPrecondition`   [P0]
Изменение cores/memory/platform на RUNNING-инстансе отвергается `FailedPrecondition`; после Stop → OK.
`metadata` обновляется через `UpdateMetadata` RPC, не через Update.
- Validated-by: `INST-UPD-RESOURCES-REQUIRES-STOPPED`, `INST-UMETA-CRUD-OK`
- Agent-check: `internal/service/instance.go` doUpdate — state-check перед применением resources/platform.
- Divergence: точный текст («Instance must be stopped») — `# probe-needed`.

---

## D. List / Pagination / Filter (`LIST`)

### REQ-LIST-01 — folder-scoped List требует `folder_id` → `InvalidArgument` без него              [P0]
Disk/Image/Snapshot/Instance. DiskType/Zone List — без folder_id.
- Validated-by: `*-LST-VAL-FOLDER-REQUIRED`
- Agent-check: sync-check в `List` хендлерах folder-scoped ресурсов.

### REQ-LIST-02 — `page_size`: 0 → default (≤1000); >1000 → `InvalidArgument`; garbage `page_token` → `InvalidArgument`   [P1]
Pagination cursor `(created_at, id)` ASC,ASC; `page_token` opaque base64.
- Validated-by: `*-LST-BVA-PAGESIZE-{ZERO,1,MAX-1000,OVER-1001}`, `*-LST-PAGE-TOKEN-GARBAGE`, `ZONE-LST-PAGE-ROUNDTRIP`
- Agent-check: `corevalidate.PageSize` + `internal/repo/paging.go`.
- Divergence: справочники (DiskType/Zone) могут игнорировать pageToken — `# probe-needed`.

### REQ-LIST-03 — filter `name="<v>"` поддерживается; не-`name`/garbage syntax — `InvalidArgument` или игнор   [P2]
- Validated-by: `*-LST-FILTER-{NAME-OK,GARBAGE,UNKNOWN-FIELD,MATCH}`
- Agent-check: `kacho-corelib/filter.Parse` с whitelist + использование в `List`.

### REQ-LIST-04 — Instance.List view=BASIC (default) → metadata не возвращается                   [P2]
Verbatim YC: BASIC опускает `Instance.metadata`; FULL — включает.
- Validated-by: `INST-LST-VIEW-BASIC-NO-METADATA`, `INST-UMETA-CRUD-OK` (Get?view=FULL)
- Agent-check: `internal/handler/instance_handler.go` / `protoconv` — обнуление metadata при BASIC view в List.

---

## E. Sync vs Async ошибки + error mapping (`AUTHZ` / общее)

### REQ-AUTHZ-01 — Get/Update/Delete/Move/Start/Stop/... несуществующего ресурса → sync `NotFound`   [P1]
Well-formed-но-отсутствующий id → `NotFound` (Get — sync; mutate — sync через AuthZ-Get-guard).
malformed/wrong-prefix id у реального YC → `InvalidArgument "invalid <res> id '<X>'"`, у нас пока `NotFound` — divergence.
- Validated-by: `*-GET-NEG-NOTFOUND`, `*-UPD-AUTHZ-NF-SYNC`, `*-DEL-NEG-NOTFOUND`, `*-MV-AUTHZ-NF-SYNC`, `INST-{START,STOP}-AUTHZ-NF-SYNC`, `INST-{SPO,ANI}-*-NF-SYNC`
- Agent-check: `internal/service/*.go` mutate-методы — Get перед Operation; `internal/repo/*.go` `ErrNotFound`.
- Divergence: malformed-id → NotFound вместо InvalidArgument; `docs/architecture/07-known-divergences.md` (паритет kacho-vpc gotcha #1).

### REQ-AUTHZ-02 — duplicate name `(folder_id, name)` в Create → async `ALREADY_EXISTS`            [P1]
Partial UNIQUE `(folder_id, name) WHERE name <> ''` для disks/images/snapshots/instances.
- Validated-by: `*-CR-NEG-DUP-NAME`
- Agent-check: `internal/migrations/0001_initial.sql` partial UNIQUE; `mapRepoErr` 23505 → `ALREADY_EXISTS`.
- Divergence: точный YC text — `# probe-needed`.

### REQ-AUTHZ-03 — error mapping: ErrNotFound→NOT_FOUND, ErrAlreadyExists→ALREADY_EXISTS, ErrFailedPrecondition→FAILED_PRECONDITION, ErrInvalidArg→INVALID_ARGUMENT, ErrInternal→INTERNAL (без leak)   [P0]
- Validated-by: все NEG-кейсы (проверка grpc code); `*-SEC-*` (нет pgx/stack leak в INTERNAL)
- Agent-check: `internal/service/maperr.go` `mapRepoErr` + `stripSentinel`.

### REQ-CONF-01 — NotFound текст формата «<Resource> <id> not found» (verbatim YC)                [P1]
Disk / Image / Snapshot / Instance / Disk type / Zone / Operation.
- Validated-by: `*-GET-CONF-NF-TEXT`, `OP-GET-CONF-NF-TEXT`
- Agent-check: error-text в `internal/repo`/`internal/service` (`fmt.Errorf("%s %s not found", ...)`).
- Divergence: точная формулировка — `# probe-needed`; пока проверяем substr "not found".

---

## F. Cross-service refs (`REF`)

### REQ-REF-01 — `folder_id` в Create/Move валидируется gRPC-вызовом к resource-manager → `NotFound` если нет   [P0]
worker каждого Create/Move: `folderClient.Exists(folder_id)`; error → `Unavailable "folder check: ..."`; false → `NotFound "Folder with id <X> not found"`.
- Validated-by: `*-CR-NEG-FOLDER-NOTFOUND`, `DISK-MV-NEG-DEST-NOTFOUND`, `OP-GET-CRUD-FAILED-OP`
- Agent-check: `internal/clients/folder_client.go`; вызов в `do*` worker'ах. ⚠️ `KACHO_COMPUTE_SKIP_PEER_VALIDATION=true` → no-op (test-config).

### REQ-REF-02 — Instance NIC.subnet_id / security_group_ids / one_to_one_nat.address → kacho-vpc; subnet.zone_id == instance.zone_id   [P1]
worker `Instance.Create`: `vpcClient.GetSubnet` (NotFound "Subnet <X> not found"), subnet zone match (иначе InvalidArgument), `GetSecurityGroup`, `GetAddress`.
- Validated-by: `INST-CR-NEG-SUBNET-NOTFOUND`, `INST-CR-CRUD-OK` (NIC.subnetId совпадает)
- Agent-check: `internal/clients/vpc_client.go`; NIC-валидация в `InstanceService.doCreate`. ⚠️ требует поднятого kacho-vpc + `KACHO_COMPUTE_SKIP_PEER_VALIDATION!=true`.

### REQ-REF-03 — Disk source image/snapshot, Snapshot source disk, Image source — existence-check в worker'е (не FK)   [P1]
Та же БД, но НЕ FK (verbatim YC: можно удалить Image/Disk оставив зависимый ресурс). Отсутствие source → `NotFound`. Disk size ≥ image.min_disk_size / snapshot.disk_size, иначе `InvalidArgument`.
- Validated-by: `DISK-CR-{CRUD-FROM-IMAGE-OK,NEG-SOURCE-IMAGE-NOTFOUND,NEG-SOURCE-SNAPSHOT-NOTFOUND}`, `IMG-CR-{CRUD-FROM-*,NEG-SOURCE-DISK-NOTFOUND}`, `SNAP-CR-{CRUD-OK,NEG-DISK-NOTFOUND}`, `SNAP-DEL-STATE-DISK-DELETABLE-AFTER`
- Agent-check: `internal/migrations/0001_initial.sql` (source-колонки НЕ FK); existence-check в `do*Create`.

---

## G. Instance state-машина (`STATE`)

### REQ-STATE-01 — Create→RUNNING; Start←{STOPPED}→RUNNING; Stop←{RUNNING}→STOPPED; Restart←{RUNNING}→RUNNING; иначе `FailedPrecondition`   [P0]
Control-plane: переходы детерминированы внутри worker'а соответствующей операции (без таймеров).
- Validated-by: `INST-CR-CRUD-OK` (RUNNING), `INST-STATE-{STOP-OK,START-FROM-STOPPED-OK,RESTART-OK}` (happy), `INST-STATE-{START-FROM-RUNNING,STOP-FROM-STOPPED,RESTART-FROM-STOPPED}` (FailedPrec)
- Agent-check: `internal/service/instance.go` doStart/doStop/doRestart — precondition-проверки; `internal/domain/instance.go` Status enum.
- Divergence: точные precondition-тексты («Instance is not running» и т.п.) — `# probe-needed`.

---

## H. Disk attach/detach (`ATTACH`)

### REQ-ATTACH-01 — AttachDisk: disk должен быть READY, в той же zone, не attached → иначе `InvalidArgument`/`FailedPrecondition`; успех → secondary_disks обновлён   [P0]
- Validated-by: `INST-AD-{CRUD-OK,NEG-WRONG-ZONE,NEG-ALREADY-ATTACHED}`
- Agent-check: `internal/service/instance.go` doAttachDisk — проверки disk.status / disk.zone_id / уже-attached.

### REQ-ATTACH-02 — DetachDisk boot disk → `FailedPrecondition` («Cannot detach boot disk»); detach не-attached → ошибка   [P0]
- Validated-by: `INST-DD-{CRUD-OK,NEG-BOOT,NEG-NOT-ATTACHED}`
- Agent-check: `internal/service/instance.go` doDetachDisk — `is_boot` check.
- Divergence: точный текст — `# probe-needed`.

### REQ-ATTACH-03 — Disk.Delete пока attached к Instance → `FailedPrecondition` («The disk ... is being used»); Detach → Delete OK   [P0]
FK `attached_disks.disk_id` → disks ON DELETE RESTRICT.
- Validated-by: `INST-DISK-DEL-WHILE-ATTACHED`
- Agent-check: `internal/migrations/0001_initial.sql` FK RESTRICT; `mapRepoErr` 23503 → `FailedPrecondition`.
- Divergence: точный текст — `# probe-needed`.

### REQ-ATTACH-04 — Instance.Delete: auto_delete=true диск удаляется; auto_delete=false — остаётся; one_to_one_nat addresses освобождаются (best-effort)   [P1]
worker сначала обрабатывает attached disks по `auto_delete`, затем DELETE instance (cascade чистит instance_network_interfaces + attached_disks).
- Validated-by: `INST-DEL-STATE-AUTODELETE-BOOT-GONE`, `INST-DEL-STATE-NONAUTODELETE-DISK-REMAINS`
- Agent-check: `internal/service/instance.go` doDelete; `attached_disks.instance_id` FK CASCADE.

---

## I. One-to-one NAT / NetworkInterface / Metadata (`NAT`)

### REQ-NAT-01 — AddOneToOneNat на NIC index → NIC.primary_v4_address.one_to_one_nat появляется; повторный Add → `FailedPrecondition`; RemoveOneToOneNat → исчезает   [P1]
- Validated-by: `INST-NAT-{ADD-CRUD-OK,ADD-NEG-ALREADY,REMOVE-CRUD-OK}`
- Agent-check: `internal/service/instance.go` doAddOneToOneNat / doRemoveOneToOneNat.

### REQ-NAT-02 — UpdateNetworkInterface: mask-based update (subnet/SG/primary_v4); bad index → ошибка   [P2]
- Validated-by: `INST-UNI-{CRUD-OK,NEG-BAD-INDEX}`
- Agent-check: `internal/service/instance.go` doUpdateNetworkInterface.

### REQ-NAT-03 — UpdateMetadata: upsert/delete; FULL-view round-trip отражает изменения; total ≤512 KB   [P1]
- Validated-by: `INST-UMETA-CRUD-OK`
- Agent-check: `internal/service/instance.go` doUpdateMetadata.

---

## J. Image family / GetLatestByFamily (`IMG`)

### REQ-IMG-01 — Create: exactly one of source {image_id, snapshot_id, disk_id, uri}; нет/несколько → `InvalidArgument`   [P0]
Control-plane: uri-download мгновенный → status сразу `READY`. family pattern `|[a-z][-a-z0-9]{1,61}[a-z0-9]`.
- Validated-by: `IMG-CR-{VAL-NO-SOURCE,VAL-MULTIPLE-SOURCE,VAL-FAMILY-INVALID,CRUD-FROM-URI-OK,CRUD-FROM-IMAGE-OK,CRUD-FROM-SNAPSHOT-OK,CRUD-OK}`
- Agent-check: sync-валидация oneof в `ImageService.Create`; `internal/service/image.go` — uri→READY.

### REQ-IMG-02 — GetLatestByFamily: возвращает самый новый Image из family; family без images → `NotFound`; без folder_id → `InvalidArgument`   [P1]
- Validated-by: `IMG-GLF-{CRUD-OK,NEG-NOTFOUND,VAL-FOLDER-REQUIRED}`
- Agent-check: `internal/service/image.go` GetLatestByFamily (ORDER BY created_at DESC LIMIT 1 в family).

---

## K. Snapshot (`SNAP`)

### REQ-SNAP-01 — Create требует disk_id; snapshot.disk_size == исходный disk.size; status READY; source_disk_id сохраняется   [P1]
Disk можно удалить, оставив Snapshot (source_disk_id не FK).
- Validated-by: `SNAP-CR-{CRUD-OK,VAL-NO-DISK,NEG-DISK-NOTFOUND}`, `SNAP-DEL-STATE-DISK-DELETABLE-AFTER`
- Agent-check: `internal/service/snapshot.go` doCreate.

---

## L. DiskType / Zone справочники (`CAT`)

### REQ-CAT-01 — DiskType.List ≥4 seeded (network-hdd / network-ssd / network-ssd-nonreplicated / network-ssd-io-m3); каждый с непустым zone_ids; Get garbage → `NotFound`   [P1]
- Validated-by: `DT-{LST-CRUD-OK,GET-CRUD-OK,GET-CRUD-HDD-OK,GET-NEG-NOTFOUND,GET-CONF-NF-TEXT}`
- Agent-check: `internal/migrations/0001_initial.sql` seed `disk_types`; `internal/service/disk_type.go`.

### REQ-CAT-02 — Zone.List ≥3 seeded (ru-central1-a / -b / -d), status UP, region_id set; Get garbage → `NotFound`   [P1]
Seed зеркалит kacho-vpc geography.
- Validated-by: `ZONE-{LST-CRUD-OK,GET-CRUD-OK,GET-CRUD-ALT-OK,GET-NEG-NOTFOUND,GET-CONF-NF-TEXT}`
- Agent-check: `internal/migrations/0001_initial.sql` seed `zones`; `internal/service/zone.go`.

### REQ-CAT-03 — DiskType/Zone — read-only (нет Create/Update/Delete на публичном API)            [P2]
POST на `/compute/v1/diskTypes` / `/compute/v1/zones` → 404/405/501. Admin-CRUD — только через `Internal*` сервисы (порт 9091).
- Validated-by: `DT-CR-NEG-NOT-ALLOWED`, `ZONE-CR-NEG-NOT-ALLOWED`
- Agent-check: `disk_type_service.proto` / `zone_service.proto` (только Get/List); workspace `CLAUDE.md` запрет #6.

### REQ-OPS-01 — Disk.Create с unknown type_id (или без type_id) → `NotFound`/default network-ssd   [P1]
Пустой type_id → default `network-ssd`; garbage type_id → `NotFound "Disk type ... not found"`.
- Validated-by: `DISK-CR-CRUD-OK` (typeId set в Get), `DISK-CR-CRUD-TYPE-EXPLICIT`, `DISK-CR-NEG-TYPE-UNKNOWN`
- Agent-check: `internal/service/disk.go` doCreate — type-resolve.
- Divergence: точный текст — `# probe-needed`.

---

## M. Security (`SEC`)

### REQ-SEC-01 — injection в name / filter (SQLi/cmd/XSS/path) → не `INTERNAL` (500), без pgx/sqlstate/stack-trace leak в ответе   [P0]
- Validated-by: `*-CR-SEC-{SQLI,UNION,XSS,CMD,PATH,LONGPAYLOAD}`, `*-LST-SEC-FILTER-SQLI`
- Agent-check: параметризованные запросы (sqlc + pgx) `internal/repo/*.go`; `mapRepoErr` не протекает SQLSTATE; нет `fmt.Errorf("%v", pgErr)` в публичных текстах.

### REQ-SEC-02 — HTTP method semantics: PUT/DELETE на коллекционный endpoint → 404/405/501; malformed JSON → 400/415   [P3]
- Validated-by: `*-METHOD-{PUT-NOT-ALLOWED,DELETE-LIST}`, `*-CR-VAL-{MALFORMED-JSON,EMPTY-BODY}`
- Agent-check: grpc-gateway routing (`google.api.http` annotations) — других методов нет.
