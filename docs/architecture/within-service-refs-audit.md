# Within-service refs audit — DB-уровневое покрытие constraints (KAC-85)

> **Контекст**
>
> Этот документ — полный аудит ссылочных полей и инвариантов всех таблиц схемы
> `kacho_compute` против правила workspace `CLAUDE.md` § «Within-service refs —
> DB-уровень обязателен» (запрет #10): любая ссылочная зависимость **внутри одной
> БД сервиса** и любой инвариант должны быть зафиксированы на уровне Postgres-
> constraint (FK / partial UNIQUE / EXCLUDE / CHECK / atomic conditional UPDATE
> с CAS / `FOR UPDATE SKIP LOCKED`). Software-side `Get → check → Update`
> запрещён — это TOCTOU-prone (см. инцидент 2026-05-14, KAC-52: два конкурентных
> `Compute.Instance.Create` с одним `existing_network_interface_id` оба прошли
> software-guard, second writer wins → два инстанса на одной NIC).
>
> Источник истины:
> - Миграции `internal/migrations/0001_initial.sql` (squashed baseline) +
>   `0002..0005_*.sql` (delta).
> - Service-слой `internal/service/*.go` (software-prechecks как UX-layer).
> - Repo-слой `internal/repo/*.go` (DDL-маппинг ошибок в sentinel-errors,
>   `unique.go::wrapPgErr`).
>
> Парный аудит для kacho-vpc — `kacho-vpc/docs/architecture/within-service-refs-audit.md`
> (KAC-84).
>
> **Cross-service ссылки** (`folder_id` → kacho-resource-manager;
> `instance_network_interfaces.subnet_id` / `security_group_ids` /
> `primary_v4_nat.address_id` → kacho-vpc; `Disk.kms_key.kms_key_id` → kacho-kms
> когда появится) — **out of scope**: для них DB-уровневые FK невозможны
> (`database-per-service` запрет #8), валидация делается в worker'е через peer-API
> (`folderClient.Exists`, `vpcClient.GetSubnet/SecurityGroupExists/GetExternalAddress`)
> + грациозный dangling-ref. Audit касается **только** рёбер графа в пределах
> одной БД `kacho_compute`.
>
> **Историческая правка (KAC-265)**: на момент аудита (2026-05-15) coverage
> включал группу таблиц kube-ovn-эпохи (`0004_hypervisors.sql`) с пометкой
> «pending drop». Эти таблицы и связанный software-слой удалены в KAC-36/79/80
> (миграция `0006_drop_hypervisors.sql`); соответствующие строки coverage и gap'ы
> сняты из этого аудита.

---

## Summary

- **Проверено**: 8 ресурсных/служебных таблиц схемы `kacho_compute`.
- **Покрыто DB-уровнем** (FK / partial UNIQUE / EXCLUDE / CHECK / CAS / SKIP LOCKED): большинство рёбер.
- **Gap'ы выявлены**: **12** (G1–G11, G14; G12/G13 относились к удалённым в KAC-36/79/80 таблицам и сняты).
- **Рекомендуемых миграций**: G1/G3/G4/G5/G9 — DB-уровневые; G6/G7/G8 — by-design (Info, документируются); G2/G14 — code-only / doc-only.

Большинство gap'ов — это **software-side TOCTOU / unenforced uniqueness / unenforced existence**
по тому же шаблону, что вызвал инцидент NIC-attach race KAC-52 в kacho-vpc.
Самый critical из них — **G1 disk-attach race** (точный аналог KAC-52, но в
compute-домене): software-guard в `InstanceService.AttachDisk` / `resolveDiskSource`
+ unconditional INSERT в `attached_disks`. На момент аудита инцидента в проде ещё не было,
но он архитектурно идентичен KAC-52 и обязан быть исправлен.

| #   | Gap | Severity | Тип нарушения |
|-----|-----|----------|----------------|
| G1  | Disk-attach race — `Instance.AttachDisk` / `resolveDiskSource`: `Get → IsAttached → INSERT attached_disks` без CAS; параллельный attach одного диска двум instances оба пройдут software-check и оба сделают INSERT (PK `(instance_id, disk_id)` спасёт *одного и того же* пользователя, но не разных instances) | **High** (parity c KAC-52) | TOCTOU + missing partial UNIQUE on `disk_id` |
| G2  | Instance state-машина (`Start`/`Stop`/`Restart`/`AttachDisk`/`DetachDisk`/`AddOneToOneNat`/Update touchesCompute) — `Get → check status → SetStatus/mutate` без conditional clause; параллельный Start+Stop / Stop+Restart на одной row → second writer wins | **High** | TOCTOU + missing CAS on `status` |
| G3  | `instances.zone_id` → `zones(id)` — within-service ref **без FK** (zone — kacho-compute собственный домен после KAC-15, та же БД) | **High** | Missing within-service FK |
| G4  | `disks.zone_id` → `zones(id)` — within-service ref без FK | **High** | Missing within-service FK |
| G5  | `disks.type_id` → `disk_types(id)` — within-service ref без FK; `Disk.Create` валидирует software'ом через `diskTypeRepo.Get` | Medium | Missing within-service FK |
| G6  | `disks.source_image_id` / `disks.source_snapshot_id` — within-service ref без FK; **by-design** (verbatim YC: source image можно удалить, оставив disk; см. `kacho-compute/CLAUDE.md §2 FK contract`) | Info (by-design) | Documented divergence |
| G7  | `images.source_image_id` / `images.source_snapshot_id` / `images.source_disk_id` — within-service ref без FK; **by-design** (источник можно удалить) | Info (by-design) | Documented divergence |
| G8  | `snapshots.source_disk_id` — within-service ref без FK; **by-design** | Info (by-design) | Documented divergence |
| G9  | Enum-like колонки (`disks.status`, `images.status`, `snapshots.status`, `instances.status`, `zones.status`, `instance_network_interfaces.mode` / `attached_disks.mode`) — нет CHECK | Low | Missing CHECK constraint |
| G10 | `instance_network_interfaces.subnet_id` / `security_group_ids` — cross-service (kacho-vpc), FK невозможен; documented | N/A (cross-service) | N/A |
| G11 | `instances.boot_disk` invariant («ровно один boot disk на instance») — DB-уровень есть (partial UNIQUE `attached_disks_boot_uniq`), software check тоже есть | OK | Closed |
| ~~G14~~ | ~~`instances.folder_id` Move через `SetFolderID`~~ — снято: RPC `Instance.Move` / `Disk.Move` удалены в KAC-266 (контракт-removal), Move-семантики больше нет | — | Closed (removed) |

> G12/G13 (таблицы kube-ovn-эпохи `hypervisors` / `hypervisor_node_index_free`)
> сняты: таблицы удалены в KAC-36/79/80.
> G14 (Move name-conflict semantics) снято: `Move` RPC удалены в KAC-266.

Полные таблицы coverage и детальные рекомендации миграций — ниже.

---

## 1. Полная таблица coverage

Колонки таблицы:
- **Resource.field / invariant** — что проверяем.
- **Что гарантируется** — продуктовый инвариант.
- **DB constraint** — Postgres-механизм (✅ есть / ❌ отсутствует / N/A — cross-service).
- **Software check** — есть ли дублирующий software-precheck (для UX).
- **Решение** — OK / G<n> (отсылка к gap-секции) / N/A.

### 1.1 `zones`

| Resource.field / invariant | Что гарантируется | DB constraint | Software check | Решение |
|---|---|---|---|---|
| `id` PK | уникальный | `zones_pkey` ✅ | n/a | OK |
| `region_id` | существует в `regions` | `zones_region_id_fkey` FK ON DELETE RESTRICT ✅ (0003) | sync (region admin-tooling редкий путь) | OK |
| `name` | произвольный display name | NOT NULL DEFAULT '' ✅ | n/a | OK |
| `status` (TEXT: UP / DOWN / STATUS_UNSPECIFIED) | значение из enum | ❌ нет CHECK | sync mapping в `zoneStatusFromName` | **G9** (minor) |
| `(zone)` deletable если нет dependents (instances/disks/disk_types) | software-precheck в `ZoneService.Delete` (через handler) | ❌ нет FK на zones из instances/disks (G3/G4); FK на disk_types отсутствует тоже | sync precheck в handler/service Delete | **G3/G4** (см. ниже) |

### 1.2 `regions`

| Resource.field / invariant | Что гарантируется | DB constraint | Software check | Решение |
|---|---|---|---|---|
| `id` PK | уникальный | `regions_pkey` ✅ (0003) | n/a | OK |
| `name` | произвольный display name | NOT NULL DEFAULT '' ✅ | n/a | OK |
| `(region)` deletable если нет zones | `zones_region_id_fkey` FK RESTRICT ✅ | sync `CountZones` precheck в `RegionService.Delete` | OK |

### 1.3 `disk_types`

| Resource.field / invariant | Что гарантируется | DB constraint | Software check | Решение |
|---|---|---|---|---|
| `id` PK | уникальный (литерал: `network-ssd`, `network-hdd`, ...) | `disk_types_pkey` ✅ | n/a | OK |
| `zone_ids` (JSONB array) | каждый id существует в `zones(id)` | ❌ нет FK (jsonb array нельзя FK напрямую) | sync: НЕТ проверки в `DiskTypeService.Create/Update` | acceptable (admin-only ресурс; raw INSERT мусора маловероятен; при ошибочном zone_id в zone_ids — disk типа просто не доступен в этой зоне, не fatal) |

### 1.4 `disks`

| Resource.field / invariant | Что гарантируется | DB constraint | Software check | Решение |
|---|---|---|---|---|
| `id` PK | уникальный | `disks_pkey` ✅ | n/a | OK |
| `folder_id` | существует в RM | N/A (cross-service) | `FolderClient.Exists` в `DiskService.checkFolder` | OK (cross-service) |
| `(folder_id, name)` | уникальный non-empty | `disks_folder_name_uniq` partial UNIQUE WHERE `name <> ''` ✅ | n/a | OK |
| `zone_id` | существует, RESTRICT удаления зоны | ❌ **нет FK** | sync `zones.GetZone` в `DiskService.doCreate` | **G4** |
| `type_id` | существует в `disk_types(id)` | ❌ **нет FK** | sync `diskTypeRepo.Get` в `DiskService.doCreate` | **G5** |
| `source_image_id` (nullable, '' = none) | если задан — existed at create time | ❌ нет FK (by-design — verbatim YC, source can be deleted) | sync `imageRepo.Get` в `doCreate` | **G6** (by-design, documented) |
| `source_snapshot_id` (nullable, '' = none) | если задан — existed at create time | ❌ нет FK (by-design) | sync `snapshotRepo.Get` в `doCreate` | **G6** (by-design) |
| `size` ≥ `min_disk_size` source-image / `disk_size` source-snapshot | sync-only при Create | sync в `doCreate` | n/a | OK (immutable после Create — нет race) |
| `status` (TEXT: CREATING/READY/ERROR/DELETING) | значение из enum | ❌ нет CHECK | sync mapping | **G9** (minor) |
| `(disk)` deletable если не attached | `attached_disks.disk_id` FK ON DELETE RESTRICT ✅ (23503→ErrFailedPrecondition в `wrapPgErr`) | sync `IsAttached` precheck в `DiskService.Delete` | OK (DB FK даёт реальную защиту; software-precheck — UX) |
| `Relocate` (zone change) precondition: not attached | software `IsAttached` check; нет CAS на `disks.zone_id` | sync precheck | **G2-like (Disk.Relocate)** — sub-case G2 (см. ниже) |

### 1.5 `images`

| Resource.field / invariant | Что гарантируется | DB constraint | Software check | Решение |
|---|---|---|---|---|
| `id` PK | уникальный | `images_pkey` ✅ | n/a | OK |
| `folder_id` | существует в RM | N/A (cross-service) | `FolderClient.Exists` | OK (cross-service) |
| `(folder_id, name)` | уникальный non-empty | `images_folder_name_uniq` partial UNIQUE WHERE `name <> ''` ✅ | n/a | OK |
| `(folder_id, family, created_at desc)` ordering для GetLatestByFamily | индекс есть | `images_family_idx` ✅ | n/a | OK |
| `family` | regex `^([a-z][-a-z0-9]{1,61}[a-z0-9])?$` | ❌ нет CHECK | sync `validateImageFamily` в `ImageService.Create` | acceptable (immutable после Create; нет raw-INSERT admin-path) |
| `source_image_id` (nullable) | если задан — existed at create time | ❌ нет FK (by-design — YC: source can be deleted) | sync `imageRepo.Get` в `doCreate` | **G7** (by-design) |
| `source_snapshot_id` (nullable) | если задан — existed at create time | ❌ нет FK (by-design) | sync `snapshotRepo.Get` | **G7** (by-design) |
| `source_disk_id` (nullable) | если задан — existed at create time | ❌ нет FK (by-design) | sync `diskRepo.Get` | **G7** (by-design) |
| `status` (TEXT: CREATING/READY/ERROR/DELETING) | значение из enum | ❌ нет CHECK | sync mapping | **G9** (minor) |
| `os_type` (TEXT: LINUX/WINDOWS/TYPE_UNSPECIFIED) | значение из enum | ❌ нет CHECK | sync mapping | **G9** (minor) |

### 1.6 `snapshots`

| Resource.field / invariant | Что гарантируется | DB constraint | Software check | Решение |
|---|---|---|---|---|
| `id` PK | уникальный | `snapshots_pkey` ✅ | n/a | OK |
| `folder_id` | существует в RM | N/A (cross-service) | `FolderClient.Exists` | OK (cross-service) |
| `(folder_id, name)` | уникальный non-empty | `snapshots_folder_name_uniq` partial UNIQUE WHERE `name <> ''` ✅ | n/a | OK |
| `source_disk_id` | existed at create time, Disk был READY | ❌ нет FK (by-design — YC: source disk can be deleted) | sync `diskRepo.Get` + status check в `SnapshotService.doCreate` | **G8** (by-design) |
| `source_disk_idx` для observability | `snapshots_source_disk_idx` partial WHERE `source_disk_id <> ''` ✅ | n/a | OK |
| `status` (TEXT) | значение из enum | ❌ нет CHECK | sync | **G9** (minor) |

### 1.7 `instances`

| Resource.field / invariant | Что гарантируется | DB constraint | Software check | Решение |
|---|---|---|---|---|
| `id` PK | уникальный | `instances_pkey` ✅ | n/a | OK |
| `folder_id` | существует в RM | N/A (cross-service) | `FolderClient.Exists` в `InstanceService.checkFolder` | OK (cross-service) |
| `(folder_id, name)` | уникальный non-empty | `instances_folder_name_uniq` partial UNIQUE WHERE `name <> ''` ✅ | n/a | OK |
| `zone_id` | существует, immutable после Create | ❌ **нет FK** | sync `zones.GetZone` в `doCreate` | **G3** |
| `status` (TEXT: PROVISIONING/RUNNING/STOPPING/STOPPED/STARTING/RESTARTING/UPDATING/ERROR/CRASHED/DELETING) | значение из state-машины (см. CLAUDE.md §8) | ❌ нет CHECK на enum; ❌ переходы делаются `Get → if status != from → SetStatus` без CAS | sync precondition-check в `InstanceService.lifecycle/AttachDisk/DetachDisk/AddOneToOneNat/RemoveOneToOneNat/Update touchesCompute` | **G2** + **G9** (TOCTOU на state + missing CHECK) |
| ~~`Move(dest_folder_id)`~~ | снято — RPC `Instance.Move` удалён в KAC-266 (контракт-removal) | n/a | n/a | Closed (removed) |
| `(instance)` cascade на NIC / attached_disks | `instance_network_interfaces.instance_id` FK CASCADE ✅; `attached_disks.instance_id` FK CASCADE ✅ | n/a | OK |
| `metadata` ≤ 256 KiB | sync-validation | ❌ нет CHECK на `octet_length(metadata::text)` | sync в request-path | acceptable (sync на API-уровне; raw INSERT — admin edge case) |

### 1.8 `instance_network_interfaces`

| Resource.field / invariant | Что гарантируется | DB constraint | Software check | Решение |
|---|---|---|---|---|
| `(instance_id, idx)` PK | уникальный NIC-index в пределах instance | `instance_network_interfaces_pkey` ✅ | n/a | OK |
| `instance_id` → `instances(id)` | существует, CASCADE при delete instance | FK ON DELETE CASCADE ✅ | n/a | OK |
| `subnet_id` | существует subnet в kacho-vpc | N/A (cross-service) | n/a — `Instance.Create` больше не создаёт/валидирует NIC (auto-NIC `materializeNICs` удалён в KAC-266) | OK (cross-service; NIC-привязка — будущая переделка) |
| `subnet_idx` для cascade-check на Subnet.Delete | `instance_nic_subnet_idx` partial WHERE `subnet_id <> ''` ✅ | n/a | OK |
| `security_group_ids` (JSONB array) | каждый id existed in kacho-vpc at attach time | N/A (cross-service) | n/a — NIC не создаётся на Create (auto-NIC удалён в KAC-266) | OK (cross-service) |
| `primary_v4_address_id` (TEXT, '' = none) | если задан — Address-ресурс в kacho-vpc | N/A (cross-service) | (создан в `vpcClient.CreateInternalAddress`, не валидируется на чтение) | OK (cross-service) |
| `nic_id` (TEXT, '' = legacy/skipPeer) | если задан — kacho-vpc NetworkInterface ресурс | N/A (cross-service) | `vpcClient.GetNetworkInterface` в `attachExistingNIC` (с CAS на vpc-side, KAC-52) | OK (cross-service; защита от race — на vpc-стороне через partial UNIQUE + atomic CAS) |
| `mac_address` (TEXT, '' = unset) | unique в пределах cloud (если задан); compute не enforce'ит сам — это vpc-домен | N/A (cross-service NIC owner) | n/a | OK |
| `primary_v4_nat` (JSONB, nullable) | OneToOneNat ref на vpc Address | N/A (cross-service) | sync в `resolveNatAddress` | OK |

### 1.9 `attached_disks`

| Resource.field / invariant | Что гарантируется | DB constraint | Software check | Решение |
|---|---|---|---|---|
| `(instance_id, disk_id)` PK | один disk прикреплён к одному instance максимум 1 раз | `attached_disks_pkey` ✅ | n/a | OK |
| `instance_id` → `instances(id)` | существует, CASCADE при delete instance | FK ON DELETE CASCADE ✅ | n/a | OK |
| `disk_id` → `disks(id)` | существует, RESTRICT при delete disk | FK ON DELETE RESTRICT ✅ | sync `IsAttached` precheck в `Disk.Delete` (UX); 23503→ErrFailedPrecondition backstop | OK |
| `(instance_id) WHERE is_boot` | один boot disk на instance | `attached_disks_boot_uniq` partial UNIQUE ✅ | sync `target.IsBoot` check в `DetachDisk` | OK |
| `(instance_id, device_name) WHERE device_name <> ''` | unique device_name в пределах instance | `attached_disks_device_uniq` partial UNIQUE ✅ | sync check duplicate device_name в `AttachDisk` (UX) | OK |
| `disk_id` invariant «один disk → 0 или 1 instance» (i.e. не может быть в `attached_disks` дважды для разных instance) | ❌ **нет partial UNIQUE on `disk_id`** — двое разных instances могут параллельно вставить `(instA, diskX)` и `(instB, diskX)` если оба прошли software `IsAttached(diskX) == false` (TOCTOU) | sync `IsAttached` в `InstanceService.resolveDiskSource` / `AttachDisk` (через service-слой); 23503-RESTRICT при delete disk не спасает от двойного attach | **G1** (high — parity с KAC-52) |
| `mode` (TEXT: READ_ONLY/READ_WRITE/MODE_UNSPECIFIED) | значение из enum | ❌ нет CHECK | sync mapping в `attachedDiskModeName` | **G9** (minor) |

### 1.10 `operations`, `compute_outbox`, `compute_watch_cursors`

| Table.field / invariant | Что гарантируется | DB constraint | Решение |
|---|---|---|---|
| `operations.id` PK | уникальный | `operations_pkey` ✅ | OK |
| `compute_outbox.sequence_no` PK + BIGSERIAL | строго возрастающий, уникальный | `PRIMARY KEY` + sequence default ✅ | OK |
| `compute_outbox_notify_trg` AFTER INSERT | каждый INSERT → `pg_notify('compute_outbox', sequence_no)` | trigger ✅ | OK |
| outbox row atomicity с ресурс-row | в одной tx | все `emitCompute` вызовы — в той же tx, что INSERT/UPDATE ресурса; review-rule ✅ | OK |
| `compute_watch_cursors.subscriber_id` PK | один cursor на subscriber | PK ✅ | OK |

---

## 2. Детализация gap'ов

### G1 — Disk-attach race: `attached_disks` без partial UNIQUE on `disk_id`

**Severity**: High — точный parity-case c инцидентом KAC-52 (NIC-attach race
2026-05-14), но в compute-домене. На момент аудита инцидента в продe ещё не было,
но архитектура та же.

**Контекст**. `attached_disks(instance_id, disk_id)` PK гарантирует, что **тот же
самый** `(instance, disk)` не будет в таблице дважды. Но **двое разных** instance
могут параллельно вставить `(instA, diskX)` и `(instB, diskX)` — PK не сработает
(составной ключ), и FK `disk_id REFERENCES disks(id)` тоже не сработает (disk
существует один раз). Один disk прикреплён к двум instances одновременно — это
нарушает доменный инвариант («один disk → 0 или 1 instance»).

**Текущая реализация**:

```go
// InstanceService.AttachDisk (instance.go:893-925)
operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
    in, _ := s.repo.Get(ctx, id)                                  // (1) SELECT instance
    if in.Status != Running && in.Status != Stopped { ... }       // (2) status check
    for _, ad := range in.AttachedDisks {                         // (3) same-instance check
        if ad.DiskID == spec.DiskID { return ErrAlreadyExists }
    }
    d, _ := s.diskRepo.Get(ctx, spec.DiskID)                      // (4) SELECT disk
    if d.Status != Ready { ... }
    if d.ZoneID != in.ZoneID { ... }
    attached, _ := s.diskRepo.IsAttached(ctx, spec.DiskID)         // (5) cross-instance check ← TOCTOU window
    if attached { return ErrFailedPrecondition }
    updated, _ := s.repo.AttachDisk(ctx, id, ...)                  // (6) INSERT attached_disks — unconditional
})

// DiskRepo.IsAttached (disk_repo.go:229)
SELECT EXISTS(SELECT 1 FROM attached_disks WHERE disk_id = $1)

// InstanceRepo.AttachDisk (instance_repo.go:223) → insertAttachedDiskTx (instance_repo.go:429)
INSERT INTO attached_disks (instance_id, disk_id, is_boot, mode, device_name, auto_delete, attached_at)
  VALUES ($1, $2, $3, $4, $5, $6, $7)  -- no CAS
```

**Race-сценарий**:
1. Disk `diskX` существует и не attached.
2. `T_A` (Instance.AttachDisk на instA): шаги (1)..(5) → guard `attached=false` пройден.
3. `T_B` (Instance.AttachDisk на instB): шаги (1)..(5) → guard `attached=false` пройден.
4. `T_A` `INSERT (instA, diskX)` — успех. Commit.
5. `T_B` `INSERT (instB, diskX)` — успех (PK `(instance_id, disk_id)` не нарушен — другой instance). Commit.

**Результат**: `diskX` attached к instA И instB. Compute-плейн думает, что disk
доступен в двух VMs — реальный data-plane (когда появится) получит конфликт.
Точный parity к KAC-52.

То же относится к `InstanceService.resolveDiskSource` (Create-path) — там тот же
`Get → IsAttached` паттерн без atomic CAS, плюс к `DiskService.Relocate`
(precondition: not attached) — там точно та же race с возможностью «relocate уже
не свободный disk». (Ссылка на `Instance.materializeNICs` снята — auto-NIC
материализация удалена в KAC-266.)

**Предлагаемая миграция** (новый файл с инкрементным номером):

```sql
-- +goose Up
-- KAC-85 G1: один disk прикреплён максимум к одному instance — invariant
-- на DB-уровне. Workspace CLAUDE.md §«Within-service refs» partial UNIQUE.
-- Parity c kacho-vpc KAC-52 (NIC-attach race).
CREATE UNIQUE INDEX attached_disks_disk_id_uniq
  ON attached_disks (disk_id);

-- +goose Down
DROP INDEX IF EXISTS attached_disks_disk_id_uniq;
```

> **Code-change в repo**: `insertAttachedDiskTx` уже использует unconditional
> INSERT — partial UNIQUE сработает на 23505 → `wrapPgErr` смаппит в
> `service.ErrAlreadyExists`. В service-слое `mapRepoErr` маппит `ErrAlreadyExists`
> в gRPC `AlreadyExists`. **Для verbatim-YC parity** правильнее вернуть
> `FailedPrecondition "Disk is already attached"` — нужно расширить
> `mapRepoErr` или добавить отдельную ветку в `InstanceRepo.AttachDisk` /
> `insertAttachedDiskTx` для маппинга 23505 на disk_id-UNIQUE в
> `ErrFailedPrecondition` (по тексту constraint name).
>
> **Integration test** обязателен (см. CLAUDE.md запрет #11): два goroutine
> параллельно AttachDisk на один disk к разным instances — ровно один winner,
> второй — `FailedPrecondition`. Зеркалит `network_interface_attach_race_integration_test.go`
> в kacho-vpc.
>
> Software-precheck `IsAttached` оставить — он даёт human-friendly error fast-path;
> финальная защита — partial UNIQUE.

---

### G2 — Instance state-машина: `Get → check status → SetStatus` без CAS

**Severity**: High — TOCTOU на state transitions, поражает все lifecycle RPC
(Start / Stop / Restart / AttachDisk / DetachDisk / AddOneToOneNat /
RemoveOneToOneNat / Update touchesCompute / Relocate disk).

**Контекст**. Instance state-машина (CLAUDE.md §8) требует, чтобы каждый
переход shл из ожидаемого `from`-state. Текущая реализация:

```go
// InstanceService.lifecycle (instance.go:849-875) — Start / Stop / Restart
operations.Run(ctx, s.opsRepo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
    in, _ := s.repo.Get(ctx, id)                          // (1) SELECT status
    if in.Status != from {                                 // (2) check
        return ErrFailedPrecondition
    }
    updated, _ := s.repo.SetStatus(ctx, id, to)            // (3) UPDATE status — unconditional!
})

// InstanceRepo.SetStatus (instance_repo.go:209)
UPDATE instances SET status = $2 WHERE id = $1            -- no CAS!
```

**Race-сценарий** (Start + Stop одновременно на RUNNING ВМ):
1. ВМ в RUNNING.
2. `T_A` (Stop): Get → status=RUNNING → guard OK.
3. `T_B` (Start): Get → status=RUNNING → guard `from=STOPPED` НЕ пройден → ErrFailedPrecondition. OK для этого случая.

**Race-сценарий 2** (Stop + Restart одновременно):
1. ВМ в RUNNING.
2. `T_A` (Stop, `from=RUNNING, to=STOPPED`): Get→guard OK.
3. `T_B` (Restart, `from=RUNNING, to=RUNNING`): Get→guard OK.
4. `T_A` `UPDATE status=STOPPED` — успех. Commit.
5. `T_B` `UPDATE status=RUNNING` — **перетирает STOPPED**, second writer wins. Commit.

**Результат**: ВМ должна была быть STOPPED (T_A stop успешен с точки зрения
API), но осталась RUNNING. Operations показывают, что обе операции done=true,
но финальное состояние — RESTART'a, а не STOP'a. Это **классический lost-write
TOCTOU**.

Также поражается:
- `InstanceService.AttachDisk` / `DetachDisk` — precondition `status ∈ {RUNNING, STOPPED}`, потом `repo.AttachDisk/DetachDisk` без статус-CAS.
- `InstanceService.AddOneToOneNat` / `RemoveOneToOneNat` — то же.
- `InstanceService.Update` с `touchesCompute=true` (cores/memory/platform_id) — precondition `status == STOPPED`, потом `repo.Update` без CAS.
- `DiskService.Relocate` — precondition `!attached`, потом `repo.SetZoneID` без CAS (если параллельно AttachDisk прошёл → Relocate перетрёт zone уже-attached диска).

**Предлагаемая миграция** (нет миграции — code-change в repo):

Code-change `internal/repo/instance_repo.go::SetStatus`:

```go
// SetStatus с CAS: переход разрешён только из ожидаемого from-state.
func (r *InstanceRepo) SetStatus(ctx context.Context, id string, from, to domain.InstanceStatus) (*domain.Instance, error) {
    return r.mutateAndReload(ctx, id, "UPDATED", func(ctx context.Context, tx pgx.Tx) error {
        tag, err := tx.Exec(ctx,
            `UPDATE instances SET status = $3 WHERE id = $1 AND status = $2`,
            id, instanceStatusName(from), instanceStatusName(to))
        if err != nil {
            return err
        }
        if tag.RowsAffected() == 0 {
            return fmt.Errorf("%w: state transition not allowed", service.ErrFailedPrecondition)
        }
        return nil
    })
}
```

И обновить InstanceService.lifecycle:

```go
operations.Run(..., func(ctx) (*anypb.Any, error) {
    updated, err := s.repo.SetStatus(ctx, id, from, to)
    if err != nil {
        if errors.Is(err, service.ErrFailedPrecondition) {
            return nil, status.Error(codes.FailedPrecondition, precondMsg)
        }
        return nil, mapRepoErr(err)
    }
    return anypb.New(protoconv.Instance(updated))
})
```

Аналогично для AttachDisk/DetachDisk/Update touchesCompute: `mutateAndReload`
расширяется параметром `expectedStatus`, и в первом SELECT/UPDATE этой
TX-операции делать atomic check.

> **Integration test** обязателен: два goroutine параллельно Stop+Restart на
> RUNNING ВМ — ровно один winner; второй — `FailedPrecondition`.
> Для AttachDisk: параллельно AttachDisk + Stop → consistency либо «диск
> прикрепился к работающей ВМ, потом она остановилась» либо «ВМ остановилась
> до attach, attach получил FailedPrecondition».
>
> **Альтернатива**: добавить CHECK на `status IN (...)` (закрывает G9 partially),
> но не решает TOCTOU — для transitions нужен CAS, не CHECK.

---

### G3 — `instances.zone_id` → `zones(id)` нет FK (within-service ref)

**Severity**: High — within-service ref без DB-уровневой защиты.

**Контекст**. После KAC-15 Geography (Region/Zone) перенесена в kacho-compute,
таблица `zones` живёт в той же БД, что `instances`. `instances.zone_id` — это
within-service ref, обязан быть FK (workspace CLAUDE.md §«Within-service refs»).

```sql
-- 0001_initial.sql:138
zone_id TEXT NOT NULL,                                       -- ← НЕТ FK!
CREATE INDEX instances_zone_idx ON instances (zone_id);      -- индекс есть, FK нет
```

**Risk-сценарий**:
1. Concurrent: `InstanceService.Create(zone_id='ru-central1-d')` worker делает `zones.GetZone(ru-central1-d)` → OK; параллельно admin `Zone.Delete('ru-central1-d')` — software-precheck в `ZoneService.Delete` НЕ проверяет `instances` (только `disks` / `disk_types` через handler-precheck'и, если они есть). 
2. Worker: `INSERT instances (zone_id='ru-central1-d', ...)` — пройдёт, потому что FK нет → instance с dangling zone_id.

**Дополнительный риск**: admin может удалить зону, не зная про существующие
instances в ней — на чтение `Instance.Get` zone_id вернётся как есть, клиент
получит null/inconsistent data.

**Предлагаемая миграция** (`0007_instances_zone_fk.sql` — либо в составе с G4/G5):

```sql
-- +goose Up
-- KAC-85 G3: enforce within-service FK instances.zone_id → zones(id).
-- Workspace CLAUDE.md §«Within-service refs — DB-уровень обязателен».
-- ON DELETE RESTRICT: нельзя удалить зону, в которой ещё есть instances
-- (admin должен сначала удалить/перенести instances).

-- Pre-flight: если есть rows с zone_id, отсутствующим в zones — это уже
-- невалидное состояние. Чинить вручную перед миграцией.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM instances i WHERE NOT EXISTS (SELECT 1 FROM zones z WHERE z.id = i.zone_id)) THEN
    RAISE EXCEPTION 'Cannot add FK: instances.zone_id contains dangling references';
  END IF;
END$$;

ALTER TABLE instances
  ADD CONSTRAINT instances_zone_id_fkey
    FOREIGN KEY (zone_id) REFERENCES zones(id) ON DELETE RESTRICT NOT VALID;
ALTER TABLE instances VALIDATE CONSTRAINT instances_zone_id_fkey;

-- +goose Down
ALTER TABLE instances DROP CONSTRAINT IF EXISTS instances_zone_id_fkey;
```

> **Code-change**: `mapRepoErr` уже маппит 23503 в `ErrFailedPrecondition`
> (через `wrapPgErr`). Сообщение получится «The instance ... is being used» —
> для FK on zone это не очень точно; стоит расширить `wrapPgErr` или
> service-слой для маппинга по constraint name → точный verbatim YC text.
>
> **Integration test**: try Zone.Delete пока есть instance в зоне → FailedPrecondition.

---

### G4 — `disks.zone_id` → `zones(id)` нет FK (within-service ref)

**Severity**: High — точно тот же кейс, что G3, но для `disks`.

**Контекст**. `disks.zone_id TEXT NOT NULL` без FK. `DiskService.doCreate`
делает `zones.GetZone(req.ZoneID)` software'но; admin может удалить зону, на
которую ссылаются диски, без блокировки.

**Предлагаемая миграция** (в составе с G3 / отдельная):

```sql
-- +goose Up
-- KAC-85 G4: enforce within-service FK disks.zone_id → zones(id).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM disks d WHERE NOT EXISTS (SELECT 1 FROM zones z WHERE z.id = d.zone_id)) THEN
    RAISE EXCEPTION 'Cannot add FK: disks.zone_id contains dangling references';
  END IF;
END$$;

ALTER TABLE disks
  ADD CONSTRAINT disks_zone_id_fkey
    FOREIGN KEY (zone_id) REFERENCES zones(id) ON DELETE RESTRICT NOT VALID;
ALTER TABLE disks VALIDATE CONSTRAINT disks_zone_id_fkey;

-- +goose Down
ALTER TABLE disks DROP CONSTRAINT IF EXISTS disks_zone_id_fkey;
```

> **Note**: `Disk.Relocate` (zone change через `SetZoneID`) тоже выиграет — FK
> проверит существование destination zone на DB-уровне, дополнительно к
> software check.

---

### G5 — `disks.type_id` → `disk_types(id)` нет FK

**Severity**: Medium — within-service ref без FK; software precheck в Create,
но Update/Relocate теоретически могли бы повредить (хотя type_id immutable —
см. CLAUDE.md §4.4, поэтому реальный risk низкий).

**Контекст**. `disks.type_id TEXT NOT NULL DEFAULT 'network-ssd'` без FK.
Admin может удалить DiskType, на который ссылаются диски, без блокировки.

**Предлагаемая миграция** (`0008_disks_type_id_fk.sql`):

```sql
-- +goose Up
-- KAC-85 G5: enforce within-service FK disks.type_id → disk_types(id).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM disks d WHERE NOT EXISTS (SELECT 1 FROM disk_types t WHERE t.id = d.type_id)) THEN
    RAISE EXCEPTION 'Cannot add FK: disks.type_id contains dangling references';
  END IF;
END$$;

ALTER TABLE disks
  ADD CONSTRAINT disks_type_id_fkey
    FOREIGN KEY (type_id) REFERENCES disk_types(id) ON DELETE RESTRICT NOT VALID;
ALTER TABLE disks VALIDATE CONSTRAINT disks_type_id_fkey;

-- +goose Down
ALTER TABLE disks DROP CONSTRAINT IF EXISTS disks_type_id_fkey;
```

> **Note**: `type_id` immutable после Create (CLAUDE.md §4.4), поэтому attack
> surface — только admin Delete DiskType. FK защищает от accident'а.

---

### G6 — `disks.source_image_id` / `disks.source_snapshot_id` без FK (by-design)

**Severity**: Info — by-design расхождение с FK-правилом, продиктованное
verbatim-YC семантикой.

**Контекст**. Verbatim YC: можно удалить Image / Snapshot, у которого есть
Disk, созданный из этого источника — Disk просто хранит «откуда создан» в
observability-целях. Соответственно FK нельзя (он бы заблокировал удаление).

**Решение**: НЕ добавлять FK. Документировать в
`docs/architecture/07-known-divergences.md` как осознанный divergence от
правила «within-service ref ⇒ FK».

**Существующая запись** (CLAUDE.md §2 FK contract):
> `disks.source_image_id` / `disks.source_snapshot_id` — **НЕ FK** (Image живёт
> в этой же БД, но YC семантика: можно удалить Image, у которого есть Disk;
> Disk просто хранит «откуда создан»). При Create — existence-check в worker'е.

Это уже задокументировано в CLAUDE.md, дублируется в audit для полноты.
**Action**: добавить отметку в `07-known-divergences.md` со ссылкой на
этот аудит (если ещё нет).

---

### G7 — `images.source_*_id` без FK (by-design)

**Severity**: Info — by-design, аналогично G6.

`images.source_image_id` / `source_snapshot_id` / `source_disk_id` —
observability-поля, не FK. Verbatim YC: source можно удалить.

**Action**: same as G6.

---

### G8 — `snapshots.source_disk_id` без FK (by-design)

**Severity**: Info — by-design.

`snapshots.source_disk_id` — observability. Verbatim YC: source disk можно
удалить.

**Action**: same as G6.

---

### G9 — Enum-like колонки без CHECK constraints

**Severity**: Low — surface area для прямых INSERT-ов «мусора»; в текущем
service-flow не достижимо.

**Затрагивает поля**:
- `disks.status TEXT` (CREATING/READY/ERROR/DELETING)
- `images.status TEXT` (same enum)
- `snapshots.status TEXT` (same enum)
- `images.os_type TEXT` (LINUX/WINDOWS/TYPE_UNSPECIFIED)
- `instances.status TEXT` (PROVISIONING/RUNNING/STOPPING/STOPPED/STARTING/RESTARTING/UPDATING/ERROR/CRASHED/DELETING)
- `instances.network_settings_type TEXT` (STANDARD / SOFTWARE_ACCELERATED / HARDWARE_ACCELERATED)
- `instances.serial_port_ssh_authorization TEXT` (SSH_AUTHORIZATION_UNSPECIFIED / INSTANCE_METADATA / OS_LOGIN)
- `instances.maintenance_policy TEXT`
- `zones.status TEXT` (UP / DOWN / STATUS_UNSPECIFIED)
- `attached_disks.mode TEXT` (READ_ONLY / READ_WRITE / MODE_UNSPECIFIED)

**Предлагаемая миграция** (`0009_enum_checks.sql`):

```sql
-- +goose Up
-- KAC-85 G9: добавить CHECK constraints на enum-like колонки. Workspace
-- CLAUDE.md §«Within-service refs» — инвариант «значение из enum» на DB-уровне.

ALTER TABLE disks
  ADD CONSTRAINT disks_status_check
    CHECK (status IN ('STATUS_UNSPECIFIED','CREATING','READY','ERROR','DELETING'));

ALTER TABLE images
  ADD CONSTRAINT images_status_check
    CHECK (status IN ('STATUS_UNSPECIFIED','CREATING','READY','ERROR','DELETING')),
  ADD CONSTRAINT images_os_type_check
    CHECK (os_type IN ('TYPE_UNSPECIFIED','LINUX','WINDOWS'));

ALTER TABLE snapshots
  ADD CONSTRAINT snapshots_status_check
    CHECK (status IN ('STATUS_UNSPECIFIED','CREATING','READY','ERROR','DELETING'));

ALTER TABLE instances
  ADD CONSTRAINT instances_status_check
    CHECK (status IN ('STATUS_UNSPECIFIED','PROVISIONING','RUNNING','STOPPING','STOPPED',
                      'STARTING','RESTARTING','UPDATING','ERROR','CRASHED','DELETING'));

ALTER TABLE zones
  ADD CONSTRAINT zones_status_check
    CHECK (status IN ('STATUS_UNSPECIFIED','UP','DOWN'));

ALTER TABLE attached_disks
  ADD CONSTRAINT attached_disks_mode_check
    CHECK (mode IN ('MODE_UNSPECIFIED','READ_ONLY','READ_WRITE'));

-- +goose Down
ALTER TABLE attached_disks DROP CONSTRAINT IF EXISTS attached_disks_mode_check;
ALTER TABLE zones DROP CONSTRAINT IF EXISTS zones_status_check;
ALTER TABLE instances DROP CONSTRAINT IF EXISTS instances_status_check;
ALTER TABLE snapshots DROP CONSTRAINT IF EXISTS snapshots_status_check;
ALTER TABLE images DROP CONSTRAINT IF EXISTS images_os_type_check;
ALTER TABLE images DROP CONSTRAINT IF EXISTS images_status_check;
ALTER TABLE disks DROP CONSTRAINT IF EXISTS disks_status_check;
```

> **Pre-flight**: каждый CHECK сначала проверяется через `SELECT … WHERE NOT (…)`
> на live стенде; при существовании невалидных row'ов миграция нагнётся, и нужно
> fill'ить значения вручную до повторного apply.
> **Maintenance**: при добавлении нового enum value в proto — нужно расширить CHECK
> миграцией; есть risk забыть.
> **Note**: `network_settings_type` / `serial_port_ssh_authorization` /
> `maintenance_policy` — оставлены без CHECK на этой итерации, т.к. их
> допустимый set обширнее / возможны расширения; можно добавить позже отдельной
> миграцией если стабилизируется.

---

### G10 — Cross-service refs `instance_network_interfaces.*` — N/A

Все ссылки NIC на kacho-vpc ресурсы (subnet, security group, address, NIC-сам)
— через границу сервиса. FK невозможны (запрет #8). Покрытие — software-validation
+ грациозный dangling-ref. Race-protection — на vpc-стороне (atomic CAS на
`used_by_id`, partial UNIQUE на `mac_address`, KAC-52, KAC-55).

**Action**: ничего; documented как cross-service. Closed.

---

### G11 — `attached_disks_boot_uniq` partial UNIQUE — Closed

`CREATE UNIQUE INDEX attached_disks_boot_uniq ON attached_disks (instance_id)
WHERE is_boot;` (0001:200) уже enforce'ит «ровно один boot disk на instance».
Это **closed** — пример хорошего partial-UNIQUE применения. Включён в audit
для полноты как доказательство того, что not all invariants are gaps.

---

### G12 / G13 — сняты (таблицы kube-ovn-эпохи удалены)

Эти gap'ы относились к таблицам `hypervisors` / `hypervisor_node_index_free`
kube-ovn-эпохи. Таблицы и связанный software-слой удалены в KAC-36/79/80
(миграция `0006_drop_hypervisors.sql`); gap'ы сняты из аудита.

---

### ~~G14 — Move с conflict по `(folder_id, name)` — subtle semantics~~ (снято, KAC-266)

Снято: RPC `Instance.Move` / `Disk.Move` (и `SetFolderID`-worker под ними)
удалены в KAC-266 (контракт-removal). Move-семантики на conflict по
`(folder_id, name)` больше нет — finding закрыт.

---

## 3. Сводная таблица «исправлено / open»

| Категория | Field/invariant | Status |
|---|---|---|
| **Closed (DB-уровневое покрытие — OK)** | | |
| Все PK уникальности | 10 ресурсных таблиц | ✅ |
| `(folder_id, name)` partial UNIQUE по 4 ресурсам | disks/images/snapshots/instances | ✅ |
| `attached_disks (instance_id, disk_id)` PK | один disk не дважды у одной ВМ | ✅ |
| `(instance_id) WHERE is_boot` partial UNIQUE | один boot disk на instance | ✅ |
| `(instance_id, device_name)` partial UNIQUE | device_name unique per instance | ✅ |
| `attached_disks.disk_id → disks(id) ON DELETE RESTRICT` | Disk.Delete blocked when attached | ✅ |
| `attached_disks.instance_id → instances(id) ON DELETE CASCADE` | NIC/disk-row cleanup at Instance.Delete | ✅ |
| `instance_network_interfaces.instance_id → instances(id) ON DELETE CASCADE` | NIC cleanup | ✅ |
| `zones.region_id → regions(id) ON DELETE RESTRICT` | Region.Delete blocked when zones exist | ✅ (0003) |
| ~~`Move/SetFolderID` consistency через UNIQUE 23505~~ | снято — `Move` RPC удалены в KAC-266 | ✅ (G14 closed/removed) |
| `compute_outbox.sequence_no` PK + trigger notify | event sequence | ✅ |
| **Open (gap'ы)** | | |
| Disk-attach race (`attached_disks.disk_id` partial UNIQUE отсутствует) | TOCTOU + missing UNIQUE | **G1** (High) |
| Instance state-машина без CAS | TOCTOU lost-write | **G2** (High) |
| `instances.zone_id` no within-service FK | dangling ref на zones-delete | **G3** (High) |
| `disks.zone_id` no within-service FK | dangling ref | **G4** (High) |
| `disks.type_id` no within-service FK | dangling ref | **G5** (Medium) |
| `disks.source_image_id` / `source_snapshot_id` no FK | by-design (verbatim YC) | **G6** (Info) |
| `images.source_*_id` no FK | by-design | **G7** (Info) |
| `snapshots.source_disk_id` no FK | by-design | **G8** (Info) |
| Enum CHECKs отсутствуют | мусор-INSERT поверхность | **G9** (Low) |
| Cross-service refs N/A | NIC → vpc | **G10** (N/A) |
| Boot disk uniq | already enforced | **G11** (Closed) |
| ~~Move/name conflict semantics~~ | снято — `Move` RPC удалены в KAC-266 | ~~**G14**~~ (closed/removed) |

---

## 4. Рекомендация по follow-up

- Создать **KAC-87 epic** «within-service refs DB-coverage closure (compute)» с subtask'ами:
  - **KAC-87.compute.1** — G1 fix (disk-attach race): миграция
    `attached_disks_disk_id_uniq` partial UNIQUE + adjust `mapRepoErr` / repo
    error mapping → `FailedPrecondition "Disk is already attached"` (verbatim
    YC text). Integration-test зеркалит `network_interface_attach_race_integration_test.go`
    из kacho-vpc. **Newman-кейс** `INST-ATTACH-DISK-RACE` обязателен (запрет #11).
  - **KAC-87.compute.2** — G2 fix (Instance state-машина CAS): code-only change
    в `InstanceRepo.SetStatus` + `mutateAndReload` с `expectedStatus`, без миграции.
    Integration-test: concurrent Stop+Restart на RUNNING → один winner.
    **Newman-кейс** `INST-STATE-RACE` обязателен.
  - **KAC-87.compute.3** — G3+G4+G5 fix (within-service FKs): миграция
    `0007_within_service_fks.sql` объединяющая `instances.zone_id`, `disks.zone_id`,
    `disks.type_id` → `zones(id)/disk_types(id)` ON DELETE RESTRICT с pre-flight
    DO-block. Integration-test: try Zone.Delete пока есть instance/disk в зоне →
    FailedPrecondition; то же для DiskType.Delete. **Newman**: `ZONE-DEL-NEG-INUSE`,
    `DT-DEL-NEG-INUSE`.
  - **KAC-87.compute.4** (defer) — G9 fix (enum CHECKs): миграция
    `0009_enum_checks.sql`. Maintenance burden: при добавлении новых enum
    values нужно расширять CHECK. Low priority.
  - ~~**KAC-87.compute.5** (doc-only) — G14 (Move name-conflict semantics)~~ —
    снято: RPC `Move` удалены в KAC-266 (контракт-removal); finding G14 закрыт.
  - **KAC-87.compute.6** (doc-only) — G6/G7/G8: расширить
    `docs/architecture/07-known-divergences.md` со ссылкой на этот аудит.
  - **KAC-87.compute.7** (closed) — G12/G13 сняты: таблицы kube-ovn-эпохи
    удалены в KAC-36/79/80 (`0006_drop_hypervisors.sql`).

- Каждый non-doc subtask: **integration-test обязателен** (запрет #11);
  newman-кейс с тем же PR (запрет #11 — формулировка «follow-up» не
  принимается). Шаблон integration-test для race-сценариев —
  `kacho-vpc/internal/repo/network_interface_attach_race_integration_test.go`.

- Перед merge каждой миграции — pre-flight на dev-стенде: `SELECT … WHERE …`
  запросом проверить, что existing rows не нарушают новый constraint; при
  найденных нарушениях — backfill / cleanup до повторного apply.

- Порядок применения миграций (новые файлы с инкрементными номерами):
  - within-service FKs (G3+G4+G5)
  - `attached_disks_disk_id_uniq` (G1; независим от G3-G5)
  - enum CHECKs (G9 — defer)

---

## 5. Ссылки

- Workspace CLAUDE.md §«Within-service refs — DB-уровень обязателен» / запрет #10
- kacho-compute CLAUDE.md §2 «FK contract» (формализация by-design без-FK по
  source_*_id полям)
- kacho-compute CLAUDE.md §8 «Instance state-машина»
- kacho-vpc audit (parity): `kacho-vpc/docs/architecture/within-service-refs-audit.md` (KAC-84)
- KAC-52 — NIC-attach race в kacho-vpc, инцидент 2026-05-14 (источник pattern'а для G1/G2)
- KAC-15 — Geography перенесена в kacho-compute (G3/G4 появились как follow-up)
- KAC-36/79/80 — таблицы kube-ovn-эпохи удалены (`0006_drop_hypervisors.sql`); G12/G13 сняты
- `internal/migrations/0001_initial.sql..0005_instance_nic_id.sql`
- `internal/service/instance.go::lifecycle/AttachDisk/AddOneToOneNat` — TOCTOU
  patterns
- `internal/service/disk.go::Relocate` — TOCTOU pattern
- `internal/repo/instance_repo.go::SetStatus/mutateAndReload` — non-CAS UPDATEs
- `internal/repo/unique.go::wrapPgErr` — SQLSTATE → sentinel mapping
