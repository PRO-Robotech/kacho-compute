# 05 — Database

`kacho_compute` (`pg-compute` StatefulSet в helm umbrella). **Database-per-service**
— никаких JOIN'ов с БД resource-manager / vpc или внешними источниками
(workspace `CLAUDE.md` §запрет 8). Все cross-service refs (folder_id, subnet_id,
SG, address, source image/snapshot/disk) — НЕ FK, валидируются gRPC-вызовом /
existence-check в БД в worker'е (см. [`02-data-flows.md`](02-data-flows.md)).

## Особенности схемы

- **Flat resources** — без K8s envelope (`resource_version` / `generation` /
  `deletion_timestamp` / `finalizers` / `spec` / `status` как JSONB). Только
  domain-specific колонки + `id` / `folder_id` / `name` / `description` /
  `labels` / `created_at`. Зеркалит kacho-vpc 1.0.
- **TEXT id columns** — YC-style `<prefix><17 base32>` (`epd...` / `fd8...`), не
  UUID. DiskType/Zone — литералы (`network-ssd`, `ru-central1-a`).
- **Hard-delete, не soft-delete** — `DELETE FROM <table> WHERE id = $1`. Никаких
  tombstones.
- **Partial UNIQUE** `(folder_id, name) WHERE name <> ''` для всех 4 мутируемых
  ресурсов — дубль непустого `name` в folder → `23505` → `ALREADY_EXISTS`;
  пустой `name` допускает несколько ресурсов (verbatim YC permissive).
- **Optimistic concurrency** для read-modify-write (`UpdateNetworkInterface`-style)
  — Postgres system column `xmin::text` (txid версия row), без отдельной колонки
  (zero-overhead, миграция не нужна) — как `SecurityGroup.UpdateRules` в VPC.
- **Outbox + LISTEN/NOTIFY** — `compute_outbox` + триггер
  `compute_outbox_notify_trg` → `pg_notify('compute_outbox', sequence_no::text)`.
- **JSONB** для structured-полей, не имеющих собственного запроса
  (`labels`, `disk_placement_policy`, `hardware_generation`, `kms_key`,
  `metadata`, `metadata_options`, `placement_policy`, `primary_v4_nat`,
  `security_group_ids`, `application`, `disk_types.zone_ids`).

## Миграции

`internal/migrations/*.sql`, embedded через `embed.FS` (`migrations.go`),
goose-стиль up/down. Goose dialect `postgres`, `cfg.MigrateDSN()` (без
`pool_max_conns` — иначе `database/sql` шлёт серверу unknown PG-param → FATAL,
VPC FINDING-007); pgxpool — `cfg.DSN()` (с `pool_max_conns` если
`KACHO_COMPUTE_DB_MAX_CONNS > 0`).

| # | Файл | Что |
|---|---|---|
| 0001 | `0001_initial.sql` | **squashed baseline** — `operations` (схема как у corelib `0001_operations.sql`), `zones`, `disk_types`, `disks`, `images`, `snapshots`, `instances`, `instance_network_interfaces`, `attached_disks`, `compute_outbox`, `compute_watch_cursors`; индексы; partial UNIQUE `(folder_id, name) WHERE name <> ''`; FK `attached_disks.disk_id` → disks RESTRICT, `.instance_id` → instances CASCADE; FK `instance_network_interfaces.instance_id` → instances CASCADE; outbox trigger `compute_outbox_notify_trg`; seed `disk_types` + `zones`. Id-колонки — `TEXT` |
| 0002 | `0002_nic_ephemeral_address.sql` | поддержка эфемерных Address-аллокаций для NIC |
| 0003 | `0003_geography_owner.sql` | **kacho-compute становится owner Geography** (эпик `KAC-15`): таблица `regions`(`id,name,created_at`), колонка `zones.name`, FK `zones.region_id → regions.id` RESTRICT, seed `ru-central1` + `ru-central1-{a,b,d}` здесь (больше не зеркалится из kacho-vpc) |
| 0004 | `0004_hypervisors.sql` | таблица `hypervisors` (internal-only ресурс — placement/HW инвентарь) + `hypervisor_node_index_seq` (`MINVALUE 0`, 0-based) + `hypervisor_node_index_free` (free-list возвращённых `node_index`) |
| 0005 | `0005_instance_nic_id.sql` | `ALTER TABLE instance_network_interfaces ADD COLUMN nic_id TEXT NOT NULL DEFAULT ''` — id бэкующего kacho-vpc `NetworkInterface` (эпик `KAC-9`); `''` = legacy NIC / `SKIP_PEER_VALIDATION` синтетика |

`migrations/` (корень репо) — staging для `make sync-migrations` (только
`0001_operations.sql` от corelib; в `0001_initial.sql` схема `operations` уже
включена). Source of truth — `internal/migrations/`.

⚠️ Запреты (workspace `CLAUDE.md` §5):
- НЕ редактировать применённую миграцию. Только новая.
- НЕ модифицировать `0001_operations.sql` (staging-копия corelib).
- Новая миграция = новый файл с инкрементным номером (следующий — `0006_*`).

## Таблицы

### `operations`

Long-running async operations (схема идентична corelib
`migrations/common/0001_operations.sql`; включена в baseline). prefix `epd` для
всех compute-операций.

```
id            TEXT         PRIMARY KEY      -- "epd..."
description   TEXT         NOT NULL         -- "Create disk <name>" и т.п.
created_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
created_by    TEXT         NOT NULL DEFAULT 'anonymous'
modified_at   TIMESTAMPTZ  NOT NULL DEFAULT now()
done          BOOLEAN      NOT NULL DEFAULT false
metadata_type TEXT                          -- "kacho.cloud.compute.v1.CreateDiskMetadata" и т.п.
metadata_data BYTEA                         -- сериализованный Any
resource_id   TEXT                          -- id затрагиваемого ресурса
error_code    INT                           -- grpc-код при failure
error_message TEXT
error_details BYTEA
response_type TEXT                          -- "kacho.cloud.compute.v1.Disk" / "google.protobuf.Empty" и т.п.
response_data BYTEA                          -- сериализованный Any

INDEX operations_resource_idx   (resource_id)
INDEX operations_done_idx       (done)
INDEX operations_created_at_idx (created_at)
```

### `regions`

Read-only справочник регионов (admin CRUD через `InternalRegionService`).
PK — литерал. Добавлена в `0003_geography_owner.sql` (эпик `KAC-15`).

```
id         TEXT         PRIMARY KEY          -- "ru-central1"
name       TEXT         NOT NULL DEFAULT ''
created_at TIMESTAMPTZ  NOT NULL DEFAULT now()
```

### `zones`

Read-only справочник зон (admin CRUD через `InternalZoneService`). PK — литерал.
`zones.name` + FK `zones.region_id → regions.id` RESTRICT добавлены в
`0003_geography_owner.sql`.

```
id         TEXT         PRIMARY KEY          -- "ru-central1-a"
region_id  TEXT         NOT NULL REFERENCES regions(id) ON DELETE RESTRICT   -- "ru-central1"
name       TEXT         NOT NULL DEFAULT ''
status     TEXT         NOT NULL DEFAULT 'UP'  -- UP | DOWN | STATUS_UNSPECIFIED (имя enum-значения Zone.Status)
created_at TIMESTAMPTZ  NOT NULL DEFAULT now()

INDEX zones_region_idx (region_id)
```

Источник данных для `ZoneService.Get/List` / `RegionService.Get/List` и для
`ZoneRegistry` (existence-check `zone_id` в `Disk.Create` / `Instance.Create` /
`Disk.Relocate`, и `disk_types.zone_ids`) — **эти таблицы** (kacho-compute owns
Geography, эпик `KAC-15`). Больше нет proxy в kacho-vpc и `skipPeer`-fallback.
Другие сервисы (kacho-vpc) валидируют `zone_id` вызовом нашего `ZoneService.Get`.
Seed `ru-central1` + `ru-central1-{a,b,d}` (`status = UP`) — в `0003_geography_owner.sql`.

### `disk_types`

Read-only справочник типов дисков (admin CRUD через `InternalDiskTypeService`).
PK — литерал.

```
id          TEXT         PRIMARY KEY          -- "network-ssd"
description TEXT         NOT NULL DEFAULT ''
zone_ids    JSONB        NOT NULL DEFAULT '[]'::jsonb  -- []string id зон, где тип доступен
created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
```

### `disks`

```
id                    TEXT         PRIMARY KEY      -- "epd..."
folder_id             TEXT         NOT NULL
created_at            TIMESTAMPTZ  NOT NULL DEFAULT now()
name                  TEXT         NOT NULL DEFAULT ''
description           TEXT         NOT NULL DEFAULT ''
labels                JSONB        NOT NULL DEFAULT '{}'::jsonb
type_id               TEXT         NOT NULL DEFAULT 'network-ssd'   -- не FK на disk_types (resilient к удалению типа)
zone_id               TEXT         NOT NULL
size                  BIGINT       NOT NULL
block_size            BIGINT       NOT NULL DEFAULT 4096
product_ids           JSONB        NOT NULL DEFAULT '[]'::jsonb
status                TEXT         NOT NULL DEFAULT 'READY'    -- CREATING | READY | ERROR | DELETING
source_image_id       TEXT         NOT NULL DEFAULT ''         -- НЕ FK (Image можно удалить)
source_snapshot_id    TEXT         NOT NULL DEFAULT ''         -- НЕ FK (Snapshot можно удалить)
disk_placement_policy JSONB                                    -- {placement_group_id, placement_group_partition} | null
hardware_generation   JSONB                                    -- HardwareGeneration | null
kms_key               JSONB                                    -- KMSKey | null (blocked:kacho-kms)

INDEX disks_folder_idx     (folder_id)
INDEX disks_created_at_idx (created_at)
UNIQUE INDEX disks_folder_name_uniq (folder_id, name) WHERE name <> ''
```

### `images`

```
id                  TEXT         PRIMARY KEY      -- "fd8..."
folder_id           TEXT         NOT NULL
created_at          TIMESTAMPTZ  NOT NULL DEFAULT now()
name                TEXT         NOT NULL DEFAULT ''
description         TEXT         NOT NULL DEFAULT ''
labels              JSONB        NOT NULL DEFAULT '{}'::jsonb
family              TEXT         NOT NULL DEFAULT ''
storage_size        BIGINT       NOT NULL DEFAULT 0
min_disk_size       BIGINT       NOT NULL DEFAULT 0
product_ids         JSONB        NOT NULL DEFAULT '[]'::jsonb
status              TEXT         NOT NULL DEFAULT 'READY'    -- CREATING | READY | ERROR | DELETING
os_type             TEXT         NOT NULL DEFAULT 'LINUX'    -- LINUX | WINDOWS | TYPE_UNSPECIFIED (Os.Type имя)
os_nvidia_driver    TEXT         NOT NULL DEFAULT ''
pooled              BOOLEAN      NOT NULL DEFAULT false
hardware_generation JSONB                                    -- HardwareGeneration | null
kms_key             JSONB                                    -- KMSKey | null (blocked:kacho-kms)
-- происхождение (для observability; НЕ FK — source можно удалить):
source_image_id     TEXT         NOT NULL DEFAULT ''
source_snapshot_id  TEXT         NOT NULL DEFAULT ''
source_disk_id      TEXT         NOT NULL DEFAULT ''
source_uri          TEXT         NOT NULL DEFAULT ''

INDEX images_folder_idx     (folder_id)
INDEX images_created_at_idx (created_at)
INDEX images_family_idx     (folder_id, family, created_at DESC) WHERE family <> ''   -- для GetLatestByFamily
UNIQUE INDEX images_folder_name_uniq (folder_id, name) WHERE name <> ''
```

### `snapshots`

```
id                  TEXT         PRIMARY KEY      -- "fd8..."
folder_id           TEXT         NOT NULL
created_at          TIMESTAMPTZ  NOT NULL DEFAULT now()
name                TEXT         NOT NULL DEFAULT ''
description         TEXT         NOT NULL DEFAULT ''
labels              JSONB        NOT NULL DEFAULT '{}'::jsonb
storage_size        BIGINT       NOT NULL DEFAULT 0           -- delta от предыдущего снимка того же диска
disk_size           BIGINT       NOT NULL DEFAULT 0           -- размер диска в момент создания снимка
product_ids         JSONB        NOT NULL DEFAULT '[]'::jsonb
status              TEXT         NOT NULL DEFAULT 'READY'     -- CREATING | READY | ERROR | DELETING
source_disk_id      TEXT         NOT NULL DEFAULT ''          -- НЕ FK (source disk можно удалить)
hardware_generation JSONB                                     -- HardwareGeneration | null
kms_key             JSONB                                     -- KMSKey | null (blocked:kacho-kms)

INDEX snapshots_folder_idx      (folder_id)
INDEX snapshots_created_at_idx  (created_at)
INDEX snapshots_source_disk_idx (source_disk_id) WHERE source_disk_id <> ''
UNIQUE INDEX snapshots_folder_name_uniq (folder_id, name) WHERE name <> ''
```

### `instances`

Плоская: все Instance-поля на верхнем уровне; structured-поля (`metadata`,
`metadata_options`, `placement_policy`, `hardware_generation`, `application`) —
JSONB; дети (`network_interfaces`, `boot_disk`/`secondary_disks`) — отдельные
таблицы.

```
id                              TEXT         PRIMARY KEY      -- "epd..."
folder_id                       TEXT         NOT NULL
created_at                      TIMESTAMPTZ  NOT NULL DEFAULT now()
name                            TEXT         NOT NULL DEFAULT ''
description                     TEXT         NOT NULL DEFAULT ''
labels                          JSONB        NOT NULL DEFAULT '{}'::jsonb
zone_id                         TEXT         NOT NULL
platform_id                     TEXT         NOT NULL
cores                           BIGINT       NOT NULL DEFAULT 2
memory                          BIGINT       NOT NULL DEFAULT 2147483648
core_fraction                   BIGINT       NOT NULL DEFAULT 100
gpus                            BIGINT       NOT NULL DEFAULT 0
status                          TEXT         NOT NULL DEFAULT 'PROVISIONING'  -- см. Instance.Status enum (03-instance-lifecycle.md)
metadata                        JSONB        NOT NULL DEFAULT '{}'::jsonb     -- map<string,string>
metadata_options                JSONB                                        -- MetadataOptions | null
service_account_id              TEXT         NOT NULL DEFAULT ''
hostname                        TEXT         NOT NULL DEFAULT ''   -- из CreateInstanceRequest; для вычисления fqdn
fqdn                            TEXT         NOT NULL DEFAULT ''   -- output-only, вычисляется при Create
network_settings_type           TEXT         NOT NULL DEFAULT 'STANDARD'
scheduling_preemptible          BOOLEAN      NOT NULL DEFAULT false
placement_policy                JSONB                                        -- PlacementPolicy | null
serial_port_ssh_authorization   TEXT         NOT NULL DEFAULT 'SSH_AUTHORIZATION_UNSPECIFIED'
gpu_cluster_id                  TEXT         NOT NULL DEFAULT ''
hardware_generation             JSONB                                        -- HardwareGeneration | null
maintenance_policy              TEXT         NOT NULL DEFAULT ''              -- MaintenancePolicy enum-name
maintenance_grace_period_seconds BIGINT      NOT NULL DEFAULT 0
reserved_instance_pool_id       TEXT         NOT NULL DEFAULT ''
host_group_id                   TEXT         NOT NULL DEFAULT ''
host_id                         TEXT         NOT NULL DEFAULT ''
application                     JSONB                                        -- Application | null

INDEX instances_folder_idx     (folder_id)
INDEX instances_created_at_idx (created_at)
INDEX instances_zone_idx       (zone_id)
UNIQUE INDEX instances_folder_name_uniq (folder_id, name) WHERE name <> ''
```

### `instance_network_interfaces`

Same-table children of Instance (cascade). PK `(instance_id, idx)`.

```
instance_id           TEXT         NOT NULL REFERENCES instances(id) ON DELETE CASCADE
idx                   TEXT         NOT NULL                  -- "0", "1", ... (NetworkInterface.index)
nic_id                TEXT         NOT NULL DEFAULT ''        -- id бэкующего kacho-vpc NetworkInterface (эпик KAC-9, миграция 0005); '' = legacy NIC / SKIP_PEER_VALIDATION синтетика. Остальные колонки ниже — denorm-зеркало kacho-vpc NIC
mac_address           TEXT         NOT NULL DEFAULT ''
subnet_id             TEXT         NOT NULL DEFAULT ''        -- VPC subnet ref (НЕ FK — cross-service; denorm-зеркало)
primary_v4_address    TEXT         NOT NULL DEFAULT ''
primary_v4_nat        JSONB                                  -- OneToOneNat {address, ip_version, dns_records} | null
primary_v4_dns_records JSONB       NOT NULL DEFAULT '[]'::jsonb
primary_v6_address    TEXT         NOT NULL DEFAULT ''
primary_v6_nat        JSONB
primary_v6_dns_records JSONB       NOT NULL DEFAULT '[]'::jsonb
security_group_ids    JSONB        NOT NULL DEFAULT '[]'::jsonb  -- []string VPC SG refs (НЕ FK; denorm-зеркало)
PRIMARY KEY (instance_id, idx)

INDEX instance_nic_subnet_idx (subnet_id) WHERE subnet_id <> ''
```

На `Instance.Create` compute создаёт (или attach'ит существующий) kacho-vpc
`NetworkInterface` на каждый NIC spec и кладёт его id в `nic_id`; на
`Instance.Delete` detach'ит + удаляет этот NIC (что освобождает его Address-ресурсы).
`subnet_id` / `primary_v4_address` / `security_group_ids` — read-only denorm
от kacho-vpc NIC, source of truth — kacho-vpc.

### `attached_disks`

M:N связь instance ↔ disk. FK на `instance_id` CASCADE (Instance.Delete worker
сам решает судьбу дисков по `auto_delete`, потом DELETE instance → CASCADE чистит
строки тут). FK на `disk_id` RESTRICT (нельзя удалить Disk пока attached →
verbatim YC `"The disk is being used"`).

```
instance_id TEXT         NOT NULL REFERENCES instances(id) ON DELETE CASCADE
disk_id     TEXT         NOT NULL REFERENCES disks(id) ON DELETE RESTRICT
is_boot     BOOLEAN      NOT NULL DEFAULT false
mode        TEXT         NOT NULL DEFAULT 'READ_WRITE'   -- READ_ONLY | READ_WRITE | MODE_UNSPECIFIED
device_name TEXT         NOT NULL DEFAULT ''
auto_delete BOOLEAN      NOT NULL DEFAULT false
attached_at TIMESTAMPTZ  NOT NULL DEFAULT now()
PRIMARY KEY (instance_id, disk_id)

INDEX attached_disks_disk_idx (disk_id)
UNIQUE INDEX attached_disks_boot_uniq   (instance_id) WHERE is_boot                       -- ровно один boot disk на instance
UNIQUE INDEX attached_disks_device_uniq (instance_id, device_name) WHERE device_name <> ''  -- device_name уникален в пределах instance
```

`instances.boot_disk_id` — НЕ отдельный FK; boot disk = строка `attached_disks`
с `is_boot=true`. `Disk.instance_ids` — derived из `attached_disks` (output-only).

### `hypervisors` (internal-only)

Реестр физических хостов (placement / HW инвентарь — **internal-only ресурс**;
не появляется на публичной поверхности, см. workspace `CLAUDE.md`
§«Инфра-чувствительные данные»). Миграция `0004_hypervisors.sql`.

```
id                    TEXT        PRIMARY KEY                 -- "hyp..." (или явный id от admin)
zone_id               TEXT        NOT NULL                    -- зона, где стоит хост
node_index            INTEGER     NOT NULL UNIQUE             -- 0-based стабильный индекс узла; основа /48-SRv6-локатора в kacho-vpc-implement
fqdn                  TEXT        NOT NULL DEFAULT ''
state                 TEXT        NOT NULL DEFAULT 'READY'    -- READY | CORDONED | DRAINING | DOWN (имя Hypervisor.State)
capacity_vcpus        BIGINT      NOT NULL DEFAULT 0
capacity_memory_bytes BIGINT      NOT NULL DEFAULT 0
capacity_instances    BIGINT      NOT NULL DEFAULT 0
created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()

INDEX hypervisors_zone_idx (zone_id)
```

Аллокация `node_index`: `SEQUENCE hypervisor_node_index_seq` (`MINVALUE 0 START
WITH 0 MAXVALUE 65535`) + `TABLE hypervisor_node_index_free (id INTEGER PRIMARY
KEY)` — при `RegisterHypervisor` берётся минимальный из free-list (если есть),
иначе `nextval`; при `DeregisterHypervisor` `node_index` возвращается во
free-list. `DeregisterHypervisor` fails (`FailedPrecondition`), если на хосте ещё
размещены инстансы. Управляется только через `InternalHypervisorService`
(синхронные RPC, не Operation). Потребители — kacho-compute reconciler (placement),
kacho-vpc-implement (читает `node_index`), admin-UI/tooling.

### `compute_outbox`

Таблица событий для `InternalWatchService` / observability.

```
sequence_no   BIGSERIAL    PRIMARY KEY
resource_kind TEXT         NOT NULL          -- Instance | Disk | Image | Snapshot
resource_id   TEXT         NOT NULL
event_type    TEXT         NOT NULL          -- CREATED | UPDATED | DELETED
payload       JSONB        NOT NULL DEFAULT '{}'::jsonb   -- полное состояние ресурса (для CREATED/UPDATED) или tombstone (DELETED)
created_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
processed_at  TIMESTAMPTZ                    -- зарезервировано под будущий durable-consumer

INDEX compute_outbox_seq_idx  (sequence_no)
INDEX compute_outbox_kind_idx (resource_kind, sequence_no)

TRIGGER compute_outbox_notify_trg AFTER INSERT ON compute_outbox
  FOR EACH ROW EXECUTE FUNCTION compute_outbox_notify()   -- PERFORM pg_notify('compute_outbox', NEW.sequence_no::text)
```

Каждая успешная мутация в `service/*.go` (через worker) пишет событие в
`compute_outbox` **в той же транзакции**, что и сам ресурс — транзакционная
гарантия consistency outbox-паттерна.

### `compute_watch_cursors`

```
subscriber_id    TEXT         PRIMARY KEY
last_sequence_no BIGINT       NOT NULL DEFAULT 0
updated_at       TIMESTAMPTZ  NOT NULL DEFAULT now()
```

Зарезервировано для durable-consumer'ов (persistence курсора). Сейчас
`InternalWatchService.Watch` принимает `from_sequence_no` от клиента — таблица не
обязательна для текущей фазы.

## Seed data

`disk_types` seed — в `0001_initial.sql`:

```sql
INSERT INTO disk_types (id, description, zone_ids) VALUES
  ('network-hdd',               'Standard network HDD',                '["ru-central1-a","ru-central1-b","ru-central1-d"]'),
  ('network-ssd',               'Fast network SSD (replicated)',       '["ru-central1-a","ru-central1-b","ru-central1-d"]'),
  ('network-ssd-nonreplicated', 'High-performance non-replicated SSD', '["ru-central1-a","ru-central1-b","ru-central1-d"]'),
  ('network-ssd-io-m3',         'Ultra high-performance SSD (io-m3)',  '["ru-central1-a","ru-central1-b","ru-central1-d"]');
```

Geography seed (`regions` + `zones`) — в `0003_geography_owner.sql` (kacho-compute
owns Geography, эпик `KAC-15`; больше не зеркало kacho-vpc):

```sql
INSERT INTO regions (id, name) VALUES ('ru-central1', 'Russia Central 1') ON CONFLICT DO NOTHING;
INSERT INTO zones (id, region_id, status, name) VALUES
  ('ru-central1-a', 'ru-central1', 'UP', 'Russia Central 1 A'),
  ('ru-central1-b', 'ru-central1', 'UP', 'Russia Central 1 B'),
  ('ru-central1-d', 'ru-central1', 'UP', 'Russia Central 1 D');
```

## FK contract (резюме)

| FK | ON DELETE | смысл |
|---|---|---|
| `attached_disks.instance_id` → `instances.id` | CASCADE | при `Instance.Delete` строки `attached_disks` чистятся; сам disk обрабатывается worker'ом по `auto_delete` ДО `DELETE instances` |
| `attached_disks.disk_id` → `disks.id` | RESTRICT | нельзя удалить Disk пока attached → `FailedPrecondition "The disk <id> is being used"` |
| `instance_network_interfaces.instance_id` → `instances.id` | CASCADE | same-table children; чистятся при `Instance.Delete` |
| `disks.source_image_id` / `disks.source_snapshot_id` | **НЕ FK** | можно удалить Image/Snapshot, оставив Disk; existence-check только на `Disk.Create` |
| `snapshots.source_disk_id` | **НЕ FK** | можно удалить Disk, оставив Snapshot; existence-check только на `Snapshot.Create` |
| `images.source_*` | **НЕ FK** | происхождение для observability |
| `instances.boot_disk_id` | **не отдельный FK** | boot disk = строка `attached_disks` с `is_boot=true` |
| `disks.type_id` → `disk_types.id` | **НЕ FK** | resilient к удалению типа; existence-check на `Disk.Create` |
| `zones.region_id` → `regions.id` | **FK RESTRICT** (same-DB; эпик `KAC-15`) | нельзя удалить регион пока есть зоны |
| `instances.zone_id` / `disks.zone_id` → `zones.id` | **НЕ FK** | existence-check через `ZoneRegistry` на Create — локальная таблица `zones` (kacho-compute owns Geography, эпик `KAC-15`) |
| `instance_network_interfaces.nic_id` → VPC `NetworkInterface` | **НЕ FK** (другая БД) | source of truth для интерфейса; на `Instance.Create` — attach/inline-create kacho-vpc NIC, на `Instance.Delete` — detach+delete (best-effort vpcClient) |
| cross-service: `instance_network_interfaces.subnet_id` → VPC Subnet; `.security_group_ids[]` → VPC SG; NAT `address` → VPC Address; `instances.folder_id` / `disks.folder_id` / ... → RM Folder | **НЕ FK** (другая БД) | валидируются gRPC-вызовом к peer-сервису в worker'е (workspace `CLAUDE.md` §запрет 4: нельзя каскадить через границу сервиса) |

## Connection / pooling

- `kacho-corelib/db.NewPool(cfg)` — pgxpool с retry + lifecycle, DSN из `cfg.DSN()`
  (с `pool_max_conns` если `KACHO_COMPUTE_DB_MAX_CONNS > 0`).
- `migrate up` / dedicated Watch-conn — `cfg.MigrateDSN()` (без `pool_max_conns`).
- Init container `migrate up` прокатывает миграции до старта основного процесса.
- `KACHO_COMPUTE_DB_SSLMODE` — `disable` для dev-стенда; production обязан
  `verify-full`.

## psql быстрый доступ

```bash
cd ../kacho-deploy && make psql SVC=compute       # psql kacho_compute
KACHO_COMPUTE_DB_PASSWORD=secret bin/kacho-compute migrate up   # миграции вне kind
```

Полезные команды:

```sql
SELECT * FROM goose_db_version ORDER BY version_id DESC LIMIT 10;   -- список миграций
\d disks
\d attached_disks
SELECT i.id, i.name, i.status, count(ad.*) AS disks
  FROM instances i LEFT JOIN attached_disks ad ON ad.instance_id = i.id GROUP BY i.id;   -- ВМ и кол-во дисков
SELECT * FROM compute_outbox ORDER BY sequence_no DESC LIMIT 20;    -- последние события
SELECT id, done, error_code, error_message FROM operations ORDER BY created_at DESC LIMIT 20;  -- последние операции
```
