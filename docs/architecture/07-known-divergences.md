# Намеренные поведенческие решения (и где они расходятся с verbatim-YC)

Это **не баги** и **не задачи** — осознанные решения, которые могут удивить
ревьюера: либо мы **расходимся** с reference Yandex Cloud Compute API (с
обоснованием), либо **deliberately не делаем** того, что напрашивается. Цель
файла — чтобы это не «фиксили» по второму разу.

**Сюда НЕ пишем** то, что просто корректно реализует verbatim-YC контракт — это
спека (см. `00-overview.md`, `01-resources.md`, `04-api-surface.md`). Например:
Compute-ресурсы folder-scoped без `cloud_id`/`organization_id`; `metadata`
омитится из `Instance` в `List`; Disk size max в Update меньше, чем в Create —
всё это **и есть** YC, расхождения тут нет.

Баги / подтверждённые probe-расхождения, которые решили выровнять — GitHub Issues
(`PRO-Robotech/kacho-compute` / `kacho-api-gateway`), см. `06-conventions.md`
→ «Где фиксировать находки» и workspace `CLAUDE.md` §14.4.

---

## 1. Malformed / wrong-prefix resource id → мы `NotFound`, YC `InvalidArgument`

Proto-поля `*_id` помечены только `(length) = "<=50"` — это max-длина, не
format-regex. На входе RPC мы **не валидируем синтаксис** id (нет prefix-check,
нет base32-check, нет UUID-проверки) — идём в `repo.Get` → если строки нет,
получаем sentinel `ErrNotFound` → `NOT_FOUND "<Resource> <id> not found"`.

Поведение реального YC (probe 2026-05-11, по аналогии с kacho-vpc#7):
- well-formed-но-несуществующий id (20 симв., известный prefix `epd...`/`fd8...`,
  но ресурса нет) → `NotFound "<Resource> <X> not found"` — **совпадаем**.
- malformed / wrong-prefix id (`not-a-real-disk-id`, `xyz`, чужой prefix) → YC
  даёт `InvalidArgument "invalid <res> id '<X>'"` — **расходимся** (мы → `NotFound`).

Выравнивание затрагивает ~все RPC, берущие resource-id, + newman-кейсы,
ассертящие «garbage id → InvalidArgument». Паритет с поведением kacho-vpc (тот же
паттерн). **Что нужно для закрытия:** добавить prefix/format-проверку id в начале
каждого handler'а (или общий decorator), вернуть `InvalidArgument` с verbatim
текстом → завести GitHub Issue `PRO-Robotech/kacho-compute`, мигрировать
newman-кейсы. Низкоприоритетно (реальные клиенты в это редко упираются).

## 2. Name validation contract не probe-verified против реального YC

Используем proto-pattern через `corevalidate.NameCompute` —
`^([a-z]([-_a-z0-9]{0,61}[a-z0-9])?)?$` (lowercase + digits + hyphens +
underscore, **empty allowed**, start с буквы, длина 1-63). Реальный YC может:
- запрещать пустое имя на Create некоторых ресурсов (хотя proto pattern его
  допускает `|...`);
- иметь иные граничные правила (двойной дефис, trailing-underscore и т.п.).

**Что нужно для закрытия:** probe реального YC Compute (`yc compute disk create
--name '<edge>'` для набора edge-case'ов: empty, uppercase, leading/trailing
hyphen, double-underscore, 64 chars, ...) → если расхождение — выровнять
`corevalidate.NameCompute` или ввести отдельную проверку, завести Issue.

## 3. Instance precondition error texts не probe-verified

State-машина (см. `03-instance-lifecycle.md`) определена корректно по семантике,
но **тексты** `FailedPrecondition`-ошибок при нарушении precondition —
placeholder'ы до probe реального YC. Текущие формулировки (могут отличаться):
- `Start` при не-`STOPPED` → ожидаем `"Instance is not stopped"` / `"Cannot
  start instance in state <X>"`;
- `Stop` при не-`RUNNING` → `"Instance is not running"` / `"Cannot stop instance
  in state <X>"`;
- `Restart` при не-`RUNNING` → `"Instance is not running"`;
- `Update` resources_spec/platform_id при не-`STOPPED` → `"Instance must be
  stopped"`;
- `AttachDisk`/`DetachDisk`/`AddNat`/`RemoveNat` при не-`{RUNNING,STOPPED}` →
  precondition-текст probe;
- `AttachNetworkInterface`/`DetachNetworkInterface`/`Move` при не-`STOPPED` →
  proto-комментарий говорит «must have STOPPED status» — текст ошибки probe;
- `DetachDisk` boot disk → `"Boot disk cannot be detached"` / similar;
- `AttachDisk` disk не READY / wrong zone / уже attached → `"The disk is being
  used"` / `"Disk and instance must be in the same zone"` — probe.

**Что нужно для закрытия:** probe реального YC (намеренно нарушить каждый
precondition, записать verbatim text+code) → выровнять, завести Issue если
расхождение. До probe — фиксируется здесь.

## 4. Disk size «only increase» / `Image.min_disk_size` constraint texts не probe-verified

- `Disk.Update` с уменьшением `size` → ожидаем `InvalidArgument "Disk size can
  only be increased"` (точный verbatim текст probe).
- `Disk.Create` с `image_id`, где `size < image.min_disk_size` → ожидаем
  `InvalidArgument "Disk size <X> is less than minimum disk size <Y> for image
  <id>"` (текст probe).
- `Disk.Create` с `snapshot_id`, где `size < snapshot.disk_size` → аналогично
  (текст probe).
- `block_size` whitelist — точный допустимый set (4096, 8192, ...) probe;
  невалидный → `InvalidArgument` (текст probe).

**Что нужно для закрытия:** probe реального YC, выровнять тексты, завести Issue
если код/текст расходится.

## 5. Control-plane simulation — Instance/Disk lifecycle мгновенный, данных нет

Самое крупное by-design расхождение. Kachō — control plane only:
- **Instance status transitions мгновенны** — нет реального гипервизора → переходы
  происходят синхронно внутри TX worker'а соответствующей операции (без таймеров,
  без задержки provisioning). Реальный YC: provisioning занимает секунды-минуты,
  `Instance.status` реально проходит `STARTING`/`STOPPING`/`PROVISIONING`/etc.
- **`ERROR` / `CRASHED` статусы не достигаются штатно** — нет реального VM, нечему
  крашиться (зарезервированы в enum для parity, но в Kachō не выставляются).
- **`GetSerialPortOutput` — синтетический текст** (стабильный per-instance
  плейсхолдер вида `[ OK ] Reached target Multi-User System.`), не реальный
  console-вывод.
- **`Image.Create` через `uri`-source — мгновенный «download»** (control-plane
  заглушка), статус сразу `READY`, `storage_size` синтетический. Реальный YC:
  скачивает образ из Object Storage по signed URL, статус `CREATING` → `READY`.
- **disk data не существует** — Disk/Snapshot/Image — только метаданные. Snapshot
  «делается» мгновенно из Disk `READY`.
- **`SimulateMaintenanceEvent` — no-op** (operation сразу `done`, Instance не
  переселяется по `maintenance_policy`).
- **`reserved_instance_pool_id` / `host_group_id` / `host_id` / `gpu_cluster_id`
  / `placement_policy.placement_group_id`** — хранятся как переданные значения,
  но реальных ReservedInstancePool / HostGroup / Host / GpuCluster / PlacementGroup
  нет (proto vendored, реализация отложена) → existence-check этих ссылок **не
  делается** (в отличие от subnet/SG/address, которые валидируются через vpcClient).
- При краше pod'а compute операция остаётся `done=false` навсегда (общее
  ограничение `operations.Run` без heartbeat/cleanup; `operations.Wait(30s)` на
  graceful shutdown спасает только от in-flight worker'ов при штатном завершении).

**Это не «фиксится»** — это архитектурное решение Kachō (control plane only,
весь проект). Если когда-нибудь появится data-plane проект — он отдельный (как
`kacho-vpc-implement` для VPC).

## 6. DiskType / Zone admin CRUD через `Internal*` сервисы — kacho-only расширение

В verbatim YC Compute API есть только `DiskTypeService.{Get,List}` /
`ZoneService.{Get,List}` (статический discovery, без Create/Update/Delete). Мы
добавили `InternalDiskTypeService.{Create,Update,Delete}` /
`InternalZoneService.{Create,Update,Delete}` на cluster-internal порту `:9091`,
проброшено через api-gateway internal mux на `/compute/v1/diskTypes`,
`/compute/v1/zones` — для admin-tooling / UI seed'ить справочники.

Это **сознательное kacho-only расширение** (зеркалит kacho-vpc, где
Region/Zone/AddressPool управляются через `Internal*` сервисы). На external TLS
endpoint (advertised для `yc` CLI) эти POST/PATCH/DELETE paths **не должны** быть
доступны (workspace `CLAUDE.md` §запрет 6) — публичными остаются только
`DiskTypeService.Get/List` / `ZoneService.Get/List`. Выравнивать не с чем (в YC
аналога нет). Proto-комментарии (`internal_catalog_service.proto`) это отражают.

## 7. Blocked-on-missing-service — отложено до появления зависимого сервиса

Не расхождение «по решению», а пробел из-за нереализованного peer-сервиса
(workspace `CLAUDE.md` §запрет 4 / принцип 4 — откладываем только то, чему нужен
ещё не существующий сервис). Помечается `blocked:*`-меткой.

| Что | Зависит от | Текущее поведение | Что нужно для закрытия |
|---|---|---|---|
| `Disk.Create` / `AttachedDiskSpec.DiskSpec` поле `kms_key_id`; `Disk/Image/Snapshot.kms_key` | `kacho-kms` | поле принимается синтаксически, но шифрование не реализовано; `kms_key` в ответе пуст. Попытка использовать → `blocked:kacho-kms` (либо игнор, либо `Unimplemented`) | реализовать `kacho-kms` → валидировать `kms_key_id` через kms-client, проставлять `kms_key` |
| `Image.Create` поле `os_product_ids` (marketplace product IDs) | `kacho-marketplace` | `product_ids` хранятся как переданы (license IDs), но marketplace-семантика не реализована | реализовать `kacho-marketplace` → валидировать product IDs |
| `Instance.AttachFilesystem` / `DetachFilesystem`; `Instance.filesystems[]` / `filesystem_specs[]` | `kacho-filesystem` (ресурса Filesystem нет) | `Unimplemented` / `blocked:kacho-filesystem`; `filesystem_specs[]` в `Instance.Create` отвергается | реализовать `kacho-filesystem` + ресурс Filesystem → реализовать attach/detach |
| `Disk.ListSnapshotSchedules`; `Disk.Create` поле `snapshot_schedule_ids` | `kacho-snapshot-schedule` | `ListSnapshotSchedules` → пустой list / `Unimplemented`; `snapshot_schedule_ids` игнорируется | реализовать `kacho-snapshot-schedule` + ресурс SnapshotSchedule |
| `Disk.Relocate` (cross-zone disk move) | — (частично; нужен реальный cross-zone disk relocation pipeline) | меняет `zone_id` с проверкой «disk не attached»; cross-zone semantics simplified (нет реального переноса данных — control-plane) | по сути закрыто на control-plane уровне; «полное» закрытие требует data-plane (не делается) |
| `Instance.Relocate` (cross-zone instance move) | `Disk.Relocate` + restart-семантика | `Unimplemented` / частично | реализовать cross-zone disk move для всех attached disks + restart-логику |
| `Instance.SimulateMaintenanceEvent` | — (control-plane: нечего симулировать) | no-op (operation сразу done, Empty) | по сути закрыто на control-plane уровне; «реальное» поведение требует data-plane |
| Ресурсы `InstanceGroup`, `DiskPlacementGroup`, `PlacementGroup`, `HostGroup`, `HostType`, `GpuCluster`, `Filesystem`, `SnapshotSchedule`, `ReservedInstancePool`, `Maintenance` | каждый — отдельный store/домен | proto vendored, сервисы не реализованы (`enhancement` / `blocked:*`); связанные поля в Instance/Disk хранятся, но не интерпретируются | реализовать соответствующие домены (отдельные acceptance-документы) |

> Каждый `blocked:*` пункт также имеет (или должен иметь) GitHub Issue в
> `PRO-Robotech/kacho-compute` с меткой `blocked:<service>` и описанием «при
> каких условиях браться». Этот файл — карта by-design состояния; Issues —
> трекинг работы.

---

## Подтверждённые расхождения, вынесенные в issues (здесь — указатель)

- **Malformed / wrong-prefix resource id → мы `NotFound`, YC `InvalidArgument`**
  — см. §1 выше. Паритет с поведением kacho-vpc#7. → GitHub Issue
  `PRO-Robotech/kacho-compute` (создать при выравнивании).
- **`OperationService.Get`/`Cancel` с bad id** — api-gateway opsproxy парсит
  первые 3 символа id, на любой нероутящийся id возвращает `400 INVALID_ARGUMENT
  "operation_id has unknown prefix"`; реальный YC для well-formed-но-unroutable
  id даёт `404 NotFound "Operation <X> not found"` — расхождение по коду. Общий
  для всех kacho-* (issue в `kacho-api-gateway`, см. `../kacho-vpc/docs/
  architecture/07-known-divergences.md` §2).
