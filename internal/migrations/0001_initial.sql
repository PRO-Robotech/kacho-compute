-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up
-- kacho-compute squashed baseline (sub-phase 0.4).
-- Flat resources (без K8s envelope) + Operations (corelib) + outbox + LISTEN/NOTIFY.
-- Все id-колонки — TEXT (YC-style "<prefix><17 base32>", не UUID).

-- ---------------------------------------------------------------------------
-- operations — long-running async operations (схема идентична corelib
-- migrations/common/0001_operations.sql; здесь включена в baseline).
-- ---------------------------------------------------------------------------
CREATE TABLE operations (
  id            TEXT         PRIMARY KEY,
  description   TEXT         NOT NULL,
  created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
  created_by    TEXT         NOT NULL DEFAULT 'anonymous',
  modified_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
  done          BOOLEAN      NOT NULL DEFAULT false,
  metadata_type TEXT,
  metadata_data BYTEA,
  resource_id   TEXT,
  error_code    INT,
  error_message TEXT,
  error_details BYTEA,
  response_type TEXT,
  response_data BYTEA
);
CREATE INDEX operations_resource_idx   ON operations (resource_id);
CREATE INDEX operations_done_idx       ON operations (done);
CREATE INDEX operations_created_at_idx ON operations (created_at);

-- ---------------------------------------------------------------------------
-- zones / disk_types — read-only справочники (admin CRUD через Internal* RPC).
-- ---------------------------------------------------------------------------
CREATE TABLE zones (
  id         TEXT         PRIMARY KEY,         -- "ru-central1-a"
  region_id  TEXT         NOT NULL,            -- "ru-central1"
  status     TEXT         NOT NULL DEFAULT 'UP', -- UP | DOWN | STATUS_UNSPECIFIED
  created_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX zones_region_idx ON zones (region_id);

CREATE TABLE disk_types (
  id          TEXT         PRIMARY KEY,        -- "network-ssd"
  description TEXT         NOT NULL DEFAULT '',
  zone_ids    JSONB        NOT NULL DEFAULT '[]'::jsonb,  -- []string of zone ids
  created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- disks
-- ---------------------------------------------------------------------------
CREATE TABLE disks (
  id                    TEXT         PRIMARY KEY,
  folder_id             TEXT         NOT NULL,
  created_at            TIMESTAMPTZ  NOT NULL DEFAULT now(),
  name                  TEXT         NOT NULL DEFAULT '',
  description           TEXT         NOT NULL DEFAULT '',
  labels                JSONB        NOT NULL DEFAULT '{}'::jsonb,
  type_id               TEXT         NOT NULL DEFAULT 'network-ssd',
  zone_id               TEXT         NOT NULL,
  size                  BIGINT       NOT NULL,
  block_size            BIGINT       NOT NULL DEFAULT 4096,
  product_ids           JSONB        NOT NULL DEFAULT '[]'::jsonb,
  status                TEXT         NOT NULL DEFAULT 'READY',  -- CREATING|READY|ERROR|DELETING
  source_image_id       TEXT         NOT NULL DEFAULT '',
  source_snapshot_id    TEXT         NOT NULL DEFAULT '',
  disk_placement_policy JSONB,                                -- {placement_group_id, placement_group_partition} | null
  hardware_generation   JSONB,                                -- HardwareGeneration | null
  kms_key               JSONB                                 -- KMSKey | null
);
CREATE INDEX disks_folder_idx     ON disks (folder_id);
CREATE INDEX disks_created_at_idx ON disks (created_at);
CREATE UNIQUE INDEX disks_folder_name_uniq ON disks (folder_id, name) WHERE name <> '';

-- ---------------------------------------------------------------------------
-- images
-- ---------------------------------------------------------------------------
CREATE TABLE images (
  id                  TEXT         PRIMARY KEY,
  folder_id           TEXT         NOT NULL,
  created_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),
  name                TEXT         NOT NULL DEFAULT '',
  description         TEXT         NOT NULL DEFAULT '',
  labels              JSONB        NOT NULL DEFAULT '{}'::jsonb,
  family              TEXT         NOT NULL DEFAULT '',
  storage_size        BIGINT       NOT NULL DEFAULT 0,
  min_disk_size       BIGINT       NOT NULL DEFAULT 0,
  product_ids         JSONB        NOT NULL DEFAULT '[]'::jsonb,
  status              TEXT         NOT NULL DEFAULT 'READY',   -- CREATING|READY|ERROR|DELETING
  os_type             TEXT         NOT NULL DEFAULT 'LINUX',   -- LINUX|WINDOWS|TYPE_UNSPECIFIED
  os_nvidia_driver    TEXT         NOT NULL DEFAULT '',
  pooled              BOOLEAN      NOT NULL DEFAULT false,
  hardware_generation JSONB,
  kms_key             JSONB,
  -- source (откуда создан) — для observability; не FK (YC: source можно удалить).
  source_image_id     TEXT         NOT NULL DEFAULT '',
  source_snapshot_id  TEXT         NOT NULL DEFAULT '',
  source_disk_id      TEXT         NOT NULL DEFAULT '',
  source_uri          TEXT         NOT NULL DEFAULT ''
);
CREATE INDEX images_folder_idx     ON images (folder_id);
CREATE INDEX images_created_at_idx ON images (created_at);
CREATE INDEX images_family_idx     ON images (folder_id, family, created_at DESC) WHERE family <> '';
CREATE UNIQUE INDEX images_folder_name_uniq ON images (folder_id, name) WHERE name <> '';

-- ---------------------------------------------------------------------------
-- snapshots
-- ---------------------------------------------------------------------------
CREATE TABLE snapshots (
  id                  TEXT         PRIMARY KEY,
  folder_id           TEXT         NOT NULL,
  created_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),
  name                TEXT         NOT NULL DEFAULT '',
  description         TEXT         NOT NULL DEFAULT '',
  labels              JSONB        NOT NULL DEFAULT '{}'::jsonb,
  storage_size        BIGINT       NOT NULL DEFAULT 0,
  disk_size           BIGINT       NOT NULL DEFAULT 0,
  product_ids         JSONB        NOT NULL DEFAULT '[]'::jsonb,
  status              TEXT         NOT NULL DEFAULT 'READY',   -- CREATING|READY|ERROR|DELETING
  source_disk_id      TEXT         NOT NULL DEFAULT '',        -- не FK (YC: source disk можно удалить)
  hardware_generation JSONB,
  kms_key             JSONB
);
CREATE INDEX snapshots_folder_idx     ON snapshots (folder_id);
CREATE INDEX snapshots_created_at_idx ON snapshots (created_at);
CREATE INDEX snapshots_source_disk_idx ON snapshots (source_disk_id) WHERE source_disk_id <> '';
CREATE UNIQUE INDEX snapshots_folder_name_uniq ON snapshots (folder_id, name) WHERE name <> '';

-- ---------------------------------------------------------------------------
-- instances
-- ---------------------------------------------------------------------------
CREATE TABLE instances (
  id                              TEXT         PRIMARY KEY,
  folder_id                       TEXT         NOT NULL,
  created_at                      TIMESTAMPTZ  NOT NULL DEFAULT now(),
  name                            TEXT         NOT NULL DEFAULT '',
  description                     TEXT         NOT NULL DEFAULT '',
  labels                          JSONB        NOT NULL DEFAULT '{}'::jsonb,
  zone_id                         TEXT         NOT NULL,
  platform_id                     TEXT         NOT NULL,
  cores                           BIGINT       NOT NULL DEFAULT 2,
  memory                          BIGINT       NOT NULL DEFAULT 2147483648,
  core_fraction                   BIGINT       NOT NULL DEFAULT 100,
  gpus                            BIGINT       NOT NULL DEFAULT 0,
  status                          TEXT         NOT NULL DEFAULT 'PROVISIONING', -- см. Instance.Status enum
  metadata                        JSONB        NOT NULL DEFAULT '{}'::jsonb,    -- map<string,string>
  metadata_options                JSONB,                                       -- MetadataOptions | null
  service_account_id              TEXT         NOT NULL DEFAULT '',
  hostname                        TEXT         NOT NULL DEFAULT '',  -- из CreateInstanceRequest; для вычисления fqdn
  fqdn                            TEXT         NOT NULL DEFAULT '',  -- output-only, вычисляется при Create
  network_settings_type           TEXT         NOT NULL DEFAULT 'STANDARD',
  scheduling_preemptible          BOOLEAN      NOT NULL DEFAULT false,
  placement_policy                JSONB,                                       -- PlacementPolicy | null
  serial_port_ssh_authorization   TEXT         NOT NULL DEFAULT 'SSH_AUTHORIZATION_UNSPECIFIED',
  gpu_cluster_id                  TEXT         NOT NULL DEFAULT '',
  hardware_generation             JSONB,
  maintenance_policy              TEXT         NOT NULL DEFAULT '',  -- maintenance.proto MaintenancePolicy enum-name
  maintenance_grace_period_seconds BIGINT      NOT NULL DEFAULT 0,
  reserved_instance_pool_id       TEXT         NOT NULL DEFAULT '',
  host_group_id                   TEXT         NOT NULL DEFAULT '',
  host_id                         TEXT         NOT NULL DEFAULT '',
  application                     JSONB                                        -- Application | null
);
CREATE INDEX instances_folder_idx     ON instances (folder_id);
CREATE INDEX instances_created_at_idx ON instances (created_at);
CREATE INDEX instances_zone_idx       ON instances (zone_id);
CREATE UNIQUE INDEX instances_folder_name_uniq ON instances (folder_id, name) WHERE name <> '';

-- network interfaces (same-table children of instance; cascade)
CREATE TABLE instance_network_interfaces (
  instance_id           TEXT         NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  idx                   TEXT         NOT NULL,                  -- "0", "1", ...
  mac_address           TEXT         NOT NULL DEFAULT '',
  subnet_id             TEXT         NOT NULL DEFAULT '',       -- VPC subnet ref (не FK — cross-service)
  primary_v4_address    TEXT         NOT NULL DEFAULT '',
  primary_v4_nat        JSONB,                                  -- OneToOneNat {address, ip_version, dns_records} | null
  primary_v4_dns_records JSONB        NOT NULL DEFAULT '[]'::jsonb,
  primary_v6_address    TEXT         NOT NULL DEFAULT '',
  primary_v6_nat        JSONB,
  primary_v6_dns_records JSONB        NOT NULL DEFAULT '[]'::jsonb,
  security_group_ids    JSONB        NOT NULL DEFAULT '[]'::jsonb,  -- []string VPC SG refs (не FK)
  PRIMARY KEY (instance_id, idx)
);
CREATE INDEX instance_nic_subnet_idx ON instance_network_interfaces (subnet_id) WHERE subnet_id <> '';

-- attached disks (instance ↔ disk M:N). FK on instance_id CASCADE (Instance.Delete
-- worker сам решает судьбу дисков по auto_delete, потом DELETE instance → CASCADE
-- чистит строки тут). FK on disk_id RESTRICT (нельзя удалить Disk пока attached).
CREATE TABLE attached_disks (
  instance_id TEXT         NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  disk_id     TEXT         NOT NULL REFERENCES disks(id) ON DELETE RESTRICT,
  is_boot     BOOLEAN      NOT NULL DEFAULT false,
  mode        TEXT         NOT NULL DEFAULT 'READ_WRITE',  -- READ_ONLY | READ_WRITE | MODE_UNSPECIFIED
  device_name TEXT         NOT NULL DEFAULT '',
  auto_delete BOOLEAN      NOT NULL DEFAULT false,
  attached_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
  PRIMARY KEY (instance_id, disk_id)
);
CREATE INDEX attached_disks_disk_idx ON attached_disks (disk_id);
-- ровно один boot disk на instance
CREATE UNIQUE INDEX attached_disks_boot_uniq ON attached_disks (instance_id) WHERE is_boot;
-- device_name уникален в пределах instance (если задан)
CREATE UNIQUE INDEX attached_disks_device_uniq ON attached_disks (instance_id, device_name) WHERE device_name <> '';

-- ---------------------------------------------------------------------------
-- outbox + LISTEN/NOTIFY (для InternalWatchService / observability)
-- ---------------------------------------------------------------------------
CREATE TABLE compute_outbox (
  sequence_no   BIGSERIAL    PRIMARY KEY,
  resource_kind TEXT         NOT NULL,        -- Instance | Disk | Image | Snapshot
  resource_id   TEXT         NOT NULL,
  event_type    TEXT         NOT NULL,        -- CREATED | UPDATED | DELETED
  payload       JSONB        NOT NULL DEFAULT '{}'::jsonb,
  created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
  processed_at  TIMESTAMPTZ
);
CREATE INDEX compute_outbox_seq_idx  ON compute_outbox (sequence_no);
CREATE INDEX compute_outbox_kind_idx ON compute_outbox (resource_kind, sequence_no);

CREATE TABLE compute_watch_cursors (
  subscriber_id    TEXT         PRIMARY KEY,
  last_sequence_no BIGINT       NOT NULL DEFAULT 0,
  updated_at       TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- +goose StatementBegin
CREATE FUNCTION compute_outbox_notify() RETURNS trigger
  LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_notify('compute_outbox', NEW.sequence_no::text);
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER compute_outbox_notify_trg AFTER INSERT ON compute_outbox
  FOR EACH ROW EXECUTE FUNCTION compute_outbox_notify();

-- ---------------------------------------------------------------------------
-- seed: zones + disk_types (зеркалит kacho-vpc geography seed)
-- ---------------------------------------------------------------------------
INSERT INTO zones (id, region_id, status) VALUES
  ('ru-central1-a', 'ru-central1', 'UP'),
  ('ru-central1-b', 'ru-central1', 'UP'),
  ('ru-central1-d', 'ru-central1', 'UP');

INSERT INTO disk_types (id, description, zone_ids) VALUES
  ('network-hdd',                'Standard network HDD',                 '["ru-central1-a","ru-central1-b","ru-central1-d"]'::jsonb),
  ('network-ssd',                'Fast network SSD (replicated)',        '["ru-central1-a","ru-central1-b","ru-central1-d"]'::jsonb),
  ('network-ssd-nonreplicated',  'High-performance non-replicated SSD',  '["ru-central1-a","ru-central1-b","ru-central1-d"]'::jsonb),
  ('network-ssd-io-m3',          'Ultra high-performance SSD (io-m3)',   '["ru-central1-a","ru-central1-b","ru-central1-d"]'::jsonb);

-- +goose Down
DROP TRIGGER IF EXISTS compute_outbox_notify_trg ON compute_outbox;
DROP FUNCTION IF EXISTS compute_outbox_notify();
DROP TABLE IF EXISTS compute_watch_cursors;
DROP TABLE IF EXISTS compute_outbox;
DROP TABLE IF EXISTS attached_disks;
DROP TABLE IF EXISTS instance_network_interfaces;
DROP TABLE IF EXISTS instances;
DROP TABLE IF EXISTS snapshots;
DROP TABLE IF EXISTS images;
DROP TABLE IF EXISTS disks;
DROP TABLE IF EXISTS disk_types;
DROP TABLE IF EXISTS zones;
DROP TABLE IF EXISTS operations;
