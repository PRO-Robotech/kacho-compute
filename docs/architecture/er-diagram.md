# ER-diagram — `kacho_compute` schema (KAC-98)

> **Источник**: `internal/migrations/0001_initial.sql` (squashed baseline) + delta-migrations
> `0002…0006`. Делается под Skill `evgeniy §5 E.6` (ER-диаграмма обязательна для каждого
> сервиса). Парная документация — `within-service-refs-audit.md` (KAC-85), которая аудитит,
> что каждая ссылка / инвариант покрыты DB-уровнем (FK / UNIQUE / CHECK / CAS).
>
> Schema-naming: таблицы живут в `public` (исторически, как и kacho-vpc). Все id-колонки —
> `TEXT` (3-char crockford-base32 prefix + 17 chars; `epd` для Instance/Disk/Operation,
> `fd8` для Image/Snapshot — см. `kacho-compute/CLAUDE.md §3`).
>
> **Hypervisors удалены** (миграция 0006, KAC-80/KAC-36 — post-kube-ovn): inventory нод
> переходит на k8s Node objects, наша таблица `hypervisors` больше не нужна. В ER ниже её
> нет — соответствует текущему состоянию схемы.
>
> См. также: `kacho-compute/CLAUDE.md §2` (Доменная модель и связи), `01-resources.md`
> (поля ресурсов), `03-instance-lifecycle.md` (state-машина Instance), `05-database.md`
> (миграции / индексы прочие).

---

## Mermaid ER

```mermaid
erDiagram
  REGIONS {
    text id PK
    text name
    timestamptz created_at
  }

  ZONES {
    text id PK
    text region_id "FK → regions.id ON DELETE RESTRICT (mig 0003)"
    text name
    text status "UP | DOWN | STATUS_UNSPECIFIED"
    timestamptz created_at
  }

  DISK_TYPES {
    text id PK "literal: network-hdd / network-ssd / network-ssd-nonreplicated / network-ssd-io-m3"
    text description
    jsonb zone_ids "denormalised []string of zone ids"
    timestamptz created_at
  }

  DISKS {
    text id PK
    text folder_id "cross-service → resource-manager.folders.id (no FK)"
    text name "partial UNIQUE (folder_id, name) WHERE name<>''"
    text description
    jsonb labels
    text type_id "soft-ref → disk_types.id (no FK; existence-check in worker)"
    text zone_id "soft-ref → zones.id (no FK; existence-check in worker)"
    bigint size
    bigint block_size
    jsonb product_ids
    text status "CREATING | READY | ERROR | DELETING"
    text source_image_id "no FK (YC: image can be deleted)"
    text source_snapshot_id "no FK (YC: snapshot can be deleted)"
    jsonb disk_placement_policy
    jsonb hardware_generation
    jsonb kms_key "blocked:kacho-kms"
    timestamptz created_at
  }

  IMAGES {
    text id PK
    text folder_id
    text name "partial UNIQUE (folder_id, name) WHERE name<>''"
    text description
    jsonb labels
    text family "index (folder_id, family, created_at DESC) WHERE family<>''"
    bigint storage_size
    bigint min_disk_size
    jsonb product_ids
    text status
    text os_type "LINUX | WINDOWS | TYPE_UNSPECIFIED"
    text os_nvidia_driver
    bool pooled
    jsonb hardware_generation
    jsonb kms_key
    text source_image_id "no FK"
    text source_snapshot_id "no FK"
    text source_disk_id "no FK"
    text source_uri
    timestamptz created_at
  }

  SNAPSHOTS {
    text id PK
    text folder_id
    text name "partial UNIQUE (folder_id, name) WHERE name<>''"
    text description
    jsonb labels
    bigint storage_size
    bigint disk_size
    jsonb product_ids
    text status
    text source_disk_id "no FK (YC: source disk can be deleted)"
    jsonb hardware_generation
    jsonb kms_key
    timestamptz created_at
  }

  INSTANCES {
    text id PK
    text folder_id
    text name "partial UNIQUE (folder_id, name) WHERE name<>''"
    text description
    jsonb labels
    text zone_id "soft-ref → zones.id"
    text platform_id
    bigint cores
    bigint memory
    bigint core_fraction
    bigint gpus
    text status "Instance.Status state-machine (see CLAUDE.md §8)"
    jsonb metadata
    jsonb metadata_options
    text service_account_id
    text hostname
    text fqdn "output-only"
    text network_settings_type
    bool scheduling_preemptible
    jsonb placement_policy
    text serial_port_ssh_authorization
    text gpu_cluster_id
    jsonb hardware_generation
    text maintenance_policy
    bigint maintenance_grace_period_seconds
    text reserved_instance_pool_id
    text host_group_id
    text host_id
    jsonb application
    timestamptz created_at
  }

  INSTANCE_NETWORK_INTERFACES {
    text instance_id PK_compound "FK → instances.id ON DELETE CASCADE"
    text idx PK_compound "PRIMARY KEY (instance_id, idx)"
    text mac_address
    text subnet_id "cross-service → vpc.subnets (no FK)"
    text primary_v4_address
    text primary_v4_address_id "VPC Address id for ephemeral auto-allocated internal IPv4 (mig 0002)"
    jsonb primary_v4_nat "OneToOneNat | null"
    jsonb primary_v4_dns_records
    text primary_v6_address
    jsonb primary_v6_nat
    jsonb primary_v6_dns_records
    jsonb security_group_ids "cross-service → vpc.security_groups (no FK)"
    text nic_id "VPC NetworkInterface id (mig 0005, epic KAC-2); '' = legacy / SKIP_PEER"
  }

  ATTACHED_DISKS {
    text instance_id PK_compound "FK → instances.id ON DELETE CASCADE"
    text disk_id PK_compound "FK → disks.id ON DELETE RESTRICT; PRIMARY KEY (instance_id, disk_id)"
    bool is_boot "partial UNIQUE (instance_id) WHERE is_boot — exactly one boot disk per instance"
    text mode "READ_ONLY | READ_WRITE | MODE_UNSPECIFIED"
    text device_name "partial UNIQUE (instance_id, device_name) WHERE device_name<>''"
    bool auto_delete
    timestamptz attached_at
  }

  OPERATIONS {
    text id PK
    text description
    text created_by
    bool done
    text metadata_type
    bytea metadata_data
    text resource_id
    int error_code
    text error_message
    bytea error_details
    text response_type
    bytea response_data
    timestamptz created_at
    timestamptz modified_at
  }

  COMPUTE_OUTBOX {
    bigint sequence_no PK "BIGSERIAL"
    text resource_kind "Instance | Disk | Image | Snapshot"
    text resource_id
    text event_type "CREATED | UPDATED | DELETED"
    jsonb payload
    timestamptz created_at
    timestamptz processed_at
  }

  COMPUTE_WATCH_CURSORS {
    text subscriber_id PK
    bigint last_sequence_no
    timestamptz updated_at
  }

  REGIONS ||--o{ ZONES : "zones.region_id (RESTRICT, mig 0003)"
  INSTANCES ||--o{ INSTANCE_NETWORK_INTERFACES : "instance_id (CASCADE)"
  INSTANCES ||--o{ ATTACHED_DISKS : "instance_id (CASCADE)"
  DISKS     ||--o{ ATTACHED_DISKS : "disk_id (RESTRICT)"
  ZONES     }o--o{ DISK_TYPES : "disk_types.zone_ids[] (denormalised, no FK)"
  ZONES     }o..o{ INSTANCES : "instances.zone_id (soft-ref, no FK)"
  ZONES     }o..o{ DISKS : "disks.zone_id (soft-ref, no FK)"
  DISK_TYPES }o..o{ DISKS : "disks.type_id (soft-ref, no FK)"
  IMAGES    }o..o{ DISKS : "source_image_id (soft-ref, no FK — YC: source can be deleted)"
  SNAPSHOTS }o..o{ DISKS : "source_snapshot_id (soft-ref, no FK)"
  DISKS     }o..o{ SNAPSHOTS : "snapshots.source_disk_id (soft-ref, no FK)"
  DISKS     }o..o{ IMAGES : "images.source_disk_id (soft-ref, no FK)"
  IMAGES    }o..o{ IMAGES : "images.source_image_id (soft-ref, no FK)"
  SNAPSHOTS }o..o{ IMAGES : "images.source_snapshot_id (soft-ref, no FK)"
```

---

## Таблицы — описание и DB-level гарантии

### Geography (admin-only public reads, kacho-compute — owner с KAC-15)

#### `regions`
Read-only справочник регионов (admin CRUD через `InternalRegionService`). PK `id` (`ru-central1`). Создан миграцией 0003, seed `ru-central1` в той же миграции. После переноса Geography из kacho-vpc (эпик KAC-15) **kacho-compute — owner**; kacho-vpc и другие сервисы валидируют `zone_id` вызовом `compute.v1.ZoneService.Get` через cross-service gRPC.

#### `zones`
Read-only справочник зон. PK `id` (`ru-central1-a`). FK `region_id → regions(id) ON DELETE RESTRICT` (миграция 0003 — раньше FK не было: миграция 0001 baseline создавала `zones` без `regions`-таблицы, потому что kacho-vpc держал Region). Index `zones_region_idx` на `region_id`. Seed `ru-central1-{a,b,d}` — в миграции 0001 baseline (UP).

Колонка `name` добавлена в 0003 (раньше zone was id-only).

#### `disk_types`
Read-only справочник типов дисков. PK `id` (literal — `network-ssd` / `network-hdd` / `network-ssd-nonreplicated` / `network-ssd-io-m3`). `zone_ids` — jsonb-массив id зон (denormalised mirror, не FK; используется UI для фильтрации «какие типы доступны в зоне X»). Admin CRUD — `InternalDiskTypeService`. Seed (4 типа) — в 0001.

---

### Public ресурсы (folder-scoped, под Operation LRO)

#### `disks`
Disk. PK `id` (`epd…`). UNIQUE partial `(folder_id, name) WHERE name<>''`. Index `disks_folder_idx`, `disks_created_at_idx`. Cross-service: `folder_id`, `zone_id`, `type_id`, `source_image_id`, `source_snapshot_id` — **no FK**. `source_image_id` / `source_snapshot_id` указывают на ресурсы той же БД (`images`, `snapshots`), но FK не ставится — verbatim YC семантика: после Disk.Create source-resource можно удалить, Disk остаётся живым (просто хранит «откуда был создан»).

#### `images`
Image. PK `id` (`fd8…`). UNIQUE partial `(folder_id, name) WHERE name<>''`. Index `images_family_idx (folder_id, family, created_at DESC) WHERE family <> ''` — для `GetLatestByFamily`. Sources (`source_image_id`, `source_snapshot_id`, `source_disk_id`, `source_uri`) — no FK.

#### `snapshots`
Snapshot. PK `id` (`fd8…`). UNIQUE partial `(folder_id, name) WHERE name<>''`. Index `snapshots_source_disk_idx (source_disk_id) WHERE source_disk_id <> ''`. `source_disk_id` — no FK (YC: source disk можно удалить, snapshot остаётся).

#### `instances`
Instance. PK `id` (`epd…`). UNIQUE partial `(folder_id, name) WHERE name<>''`. Index `instances_folder_idx`, `instances_created_at_idx`, `instances_zone_idx`. Cross-service: `folder_id`, `zone_id`, `service_account_id`, `gpu_cluster_id`, `host_group_id`, `host_id`, `reserved_instance_pool_id` — все no FK.

#### `instance_network_interfaces`
Same-table children Instance (NIC spec на инстансе). PK `(instance_id, idx)`. FK `instance_id → instances(id) ON DELETE CASCADE` — same-table CASCADE — единственный case, где cascade применимо (NIC-rows инстанса очищаются при его удалении). Index `instance_nic_subnet_idx (subnet_id) WHERE subnet_id <> ''`.

Cross-service soft-refs (no FK):
- `subnet_id` → kacho-vpc `subnets` (existence-check в worker'е).
- `security_group_ids[]` → kacho-vpc `security_groups`.
- `primary_v4_address_id` (миграция 0002) — kacho-vpc `addresses.id` для ephemeral auto-allocated IPv4; на Instance.Delete compute удаляет этот Address через vpc API. `''` = manual IP / synthetic (`KACHO_COMPUTE_SKIP_PEER_VALIDATION=true`).
- `nic_id` (миграция 0005, эпик KAC-2) — kacho-vpc `network_interfaces.id`; compute создаёт (или attach'ит существующий) kacho-vpc NIC per spec и хранит id. На Instance.Delete compute detach+delete'ит NIC. `''` = legacy / synthetic.

#### `attached_disks`
M:N Instance↔Disk. PK `(instance_id, disk_id)`. FK `instance_id → instances(id) ON DELETE CASCADE` (Instance.Delete worker сам решает судьбу дисков по `auto_delete`, потом CASCADE чистит attached-row). FK `disk_id → disks(id) ON DELETE RESTRICT` — нельзя удалить Disk пока attached → verbatim YC `"The disk <id> is being used"`.

**Partial UNIQUE** (DB-level гарантии):
- `attached_disks_boot_uniq (instance_id) WHERE is_boot` — ровно один boot disk на инстанс.
- `attached_disks_device_uniq (instance_id, device_name) WHERE device_name <> ''` — `device_name` уникален в пределах инстанса.

---

### Operations / Outbox (corelib-стиль, per-service)

#### `operations`
Long-running async operations (corelib schema; включена в baseline 0001). PK `id` (`epd…`, `PrefixOperationCompute == PrefixInstance` — все compute-ops в одном backend по prefix-routing api-gateway opsproxy). Индексы по `done`, `created_at`, `resource_id`.

#### `compute_outbox`
Транзакционный outbox. PK `sequence_no BIGSERIAL`. Trigger `compute_outbox_notify_trg` AFTER INSERT → `pg_notify('compute_outbox', NEW.sequence_no::text)`. `InternalWatchService` использует dedicated pgx-conn вне pool с `LISTEN compute_outbox` (структурно копия VPC).

#### `compute_watch_cursors`
Per-subscriber cursor для LISTEN/NOTIFY restart-сценария. PK `subscriber_id`.

---

## Связи через границу сервиса (cross-service, **software-validated, no FK**)

> Workspace `CLAUDE.md` §«Кросс-доменные ссылки на ресурсы» / запрет #8 — database-per-service запрещает cross-DB FK. Ссылки в этом списке хранятся как `TEXT` / jsonb колонки и валидируются gRPC-вызовом owner-сервиса в worker'е Create/Update; на чтении переживается dangling-ref.

| Колонка                                          | Owner-сервис             | Owner-метод                                     | ON DELETE-симуляция         |
|--------------------------------------------------|--------------------------|-------------------------------------------------|------------------------------|
| `disks.folder_id` / `instances.folder_id` / etc. | `kacho-resource-manager` | `FolderService.Exists`                          | n/a (validate-on-write)      |
| `disks.zone_id` / `instances.zone_id`            | **kacho-compute self**   | local `zones`-table — **same-DB ref, no FK** by choice (admin справочник; service-level existence-check `ZoneRegistry`) | n/a |
| `disks.type_id`                                  | **kacho-compute self**   | local `disk_types`-table — same-DB ref, no FK   | n/a                          |
| `disks.source_image_id`                          | **kacho-compute self**   | local `images`-table — **same-DB but no FK** (YC семантика: source можно удалить) | n/a (graceful dangling) |
| `disks.source_snapshot_id`                       | **kacho-compute self**   | local `snapshots`-table — same-DB no FK         | n/a (graceful dangling)      |
| `snapshots.source_disk_id`                       | **kacho-compute self**   | local `disks`-table — same-DB no FK             | n/a (graceful dangling)      |
| `images.source_*`                                | **kacho-compute self**   | local same-DB no FK                             | n/a                          |
| `instance_network_interfaces.subnet_id`          | `kacho-vpc`              | `SubnetService.Get`                             | n/a (delete blocked by NIC)  |
| `instance_network_interfaces.security_group_ids[]` | `kacho-vpc`            | `SecurityGroupService.Get`                      | n/a                          |
| `instance_network_interfaces.primary_v4_address_id` | `kacho-vpc`           | `AddressService.Get` (ephemeral allocated by compute) | compute releases on Instance.Delete |
| `instance_network_interfaces.nic_id`             | `kacho-vpc`              | `NetworkInterfaceService.Get` (epic KAC-2)      | compute deletes NIC on Instance.Delete |
| `instances.service_account_id` / `gpu_cluster_id` / `host_group_id` / `host_id` / `reserved_instance_pool_id` | various (blocked:kacho-iam / blocked:* ) | future peer-call | n/a |

**Note**: `regions` / `zones` / `disk_types` живут в **этой же БД** (kacho-compute — owner Geography с KAC-15). FK `zones.region_id → regions(id)` (миграция 0003) — это **within-service** FK. Software-side: `Disk.zone_id`, `Instance.zone_id`, `disk_types.zone_ids[]` — soft-refs без FK (admin-справочник, namespace-level — переживают rename/dangling).

---

## Удалённые ресурсы

- **`hypervisors` / `hypervisor_node_index_free` / `hypervisor_node_index_seq`** — добавлены миграцией 0004, удалены миграцией 0006 (KAC-80/KAC-36, post-kube-ovn). InternalHypervisorService удалён из proto. Inventory нод теперь — k8s Node objects (управляются kube-ovn). В ER выше отсутствуют — это финальное состояние схемы. Down-миграция в 0006 best-effort пересоздаёт минимальную таблицу без backfill — она НЕ источник истины и не для production rollback.

---

## Ссылки

- `within-service-refs-audit.md` (KAC-85) — построчный аудит ссылок против запрета workspace #10.
- `01-resources.md` — описание ресурсов с проекциями proto-полей.
- `03-instance-lifecycle.md` — Instance state-machine.
- `05-database.md` — миграционная история, индексы.
- `06-conventions.md` — соглашения по error-mapping, timestamp, name-policy.
- `07-known-divergences.md` — by-design расхождения с verbatim YC.
- `internal/migrations/0001_initial.sql` … `0006_drop_hypervisors.sql` — источник истины.
- `kacho-compute/CLAUDE.md §2` (Доменная модель и связи), §10 (Cross-service clients), §12 (Migrations).
- Workspace `CLAUDE.md` — §«Within-service refs — DB-уровень обязателен» (запрет #10), §«Кросс-доменные ссылки на ресурсы», §E.6 (skill `evgeniy`).
