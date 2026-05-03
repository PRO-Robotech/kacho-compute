-- +goose Up

CREATE TABLE images_catalog (
  uid                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  name               TEXT        NOT NULL UNIQUE,
  labels             JSONB       NOT NULL DEFAULT '{}'::jsonb,
  creation_timestamp TIMESTAMPTZ NOT NULL DEFAULT now(),
  resource_version   BIGINT      NOT NULL DEFAULT nextval('resource_version_seq'),
  generation         BIGINT      NOT NULL DEFAULT 1,
  spec               JSONB       NOT NULL DEFAULT '{}'::jsonb,
  -- status: всегда READY
  state              TEXT        NOT NULL DEFAULT 'READY'
);
CREATE INDEX images_catalog_labels_gin ON images_catalog USING GIN (labels jsonb_path_ops);

CREATE TABLE disks (
  uid                   UUID        PRIMARY KEY,
  folder_id             UUID        NOT NULL,
  cloud_id              UUID        NOT NULL,
  organization_id       UUID        NOT NULL,
  name                  TEXT        NOT NULL,
  labels                JSONB       NOT NULL DEFAULT '{}'::jsonb,
  annotations           JSONB       NOT NULL DEFAULT '{}'::jsonb,
  creation_timestamp    TIMESTAMPTZ NOT NULL DEFAULT now(),
  resource_version      BIGINT      NOT NULL DEFAULT nextval('resource_version_seq'),
  generation            BIGINT      NOT NULL DEFAULT 1,
  deletion_timestamp    TIMESTAMPTZ,
  finalizers            TEXT[]      NOT NULL DEFAULT '{}',
  spec                  JSONB       NOT NULL DEFAULT '{}'::jsonb,
  status                JSONB       NOT NULL DEFAULT '{}'::jsonb,
  UNIQUE (folder_id, name)
);
CREATE INDEX disks_labels_gin       ON disks USING GIN (labels jsonb_path_ops);
CREATE INDEX disks_folder_idx       ON disks (folder_id);
CREATE INDEX disks_status_state_idx ON disks ((status->>'state'));
CREATE TRIGGER disks_bump_rv BEFORE UPDATE ON disks FOR EACH ROW EXECUTE FUNCTION bump_resource_version();

CREATE TABLE instances (
  uid                   UUID        PRIMARY KEY,
  folder_id             UUID        NOT NULL,
  cloud_id              UUID        NOT NULL,
  organization_id       UUID        NOT NULL,
  name                  TEXT        NOT NULL,
  labels                JSONB       NOT NULL DEFAULT '{}'::jsonb,
  annotations           JSONB       NOT NULL DEFAULT '{}'::jsonb,
  creation_timestamp    TIMESTAMPTZ NOT NULL DEFAULT now(),
  resource_version      BIGINT      NOT NULL DEFAULT nextval('resource_version_seq'),
  generation            BIGINT      NOT NULL DEFAULT 1,
  deletion_timestamp    TIMESTAMPTZ,
  finalizers            TEXT[]      NOT NULL DEFAULT '{}',
  restarted_at          TIMESTAMPTZ,
  spec                  JSONB       NOT NULL DEFAULT '{}'::jsonb,
  status                JSONB       NOT NULL DEFAULT '{}'::jsonb,
  UNIQUE (folder_id, name)
);
CREATE INDEX instances_labels_gin       ON instances USING GIN (labels jsonb_path_ops);
CREATE INDEX instances_folder_idx       ON instances (folder_id);
CREATE INDEX instances_status_state_idx ON instances ((status->>'state'));
CREATE TRIGGER instances_bump_rv BEFORE UPDATE ON instances FOR EACH ROW EXECUTE FUNCTION bump_resource_version();

CREATE TABLE snapshots (
  uid                   UUID        PRIMARY KEY,
  folder_id             UUID        NOT NULL,
  cloud_id              UUID        NOT NULL,
  organization_id       UUID        NOT NULL,
  name                  TEXT        NOT NULL,
  labels                JSONB       NOT NULL DEFAULT '{}'::jsonb,
  annotations           JSONB       NOT NULL DEFAULT '{}'::jsonb,
  creation_timestamp    TIMESTAMPTZ NOT NULL DEFAULT now(),
  resource_version      BIGINT      NOT NULL DEFAULT nextval('resource_version_seq'),
  generation            BIGINT      NOT NULL DEFAULT 1,
  deletion_timestamp    TIMESTAMPTZ,
  finalizers            TEXT[]      NOT NULL DEFAULT '{}',
  spec                  JSONB       NOT NULL DEFAULT '{}'::jsonb,
  status                JSONB       NOT NULL DEFAULT '{}'::jsonb,
  UNIQUE (folder_id, name)
);
CREATE INDEX snapshots_labels_gin       ON snapshots USING GIN (labels jsonb_path_ops);
CREATE INDEX snapshots_folder_idx       ON snapshots (folder_id);
CREATE INDEX snapshots_status_state_idx ON snapshots ((status->>'state'));
CREATE TRIGGER snapshots_bump_rv BEFORE UPDATE ON snapshots FOR EACH ROW EXECUTE FUNCTION bump_resource_version();

-- zones — справочник зон доступности
CREATE TABLE zones (
  id          TEXT PRIMARY KEY,
  description TEXT NOT NULL DEFAULT ''
);

-- disk_types — справочник типов дисков
CREATE TABLE disk_types (
  id          TEXT PRIMARY KEY,
  description TEXT NOT NULL DEFAULT ''
);

-- platforms — справочник платформ (типов хостов)
CREATE TABLE platforms (
  id          TEXT PRIMARY KEY,
  description TEXT NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE IF EXISTS snapshots;
DROP TABLE IF EXISTS instances;
DROP TABLE IF EXISTS disks;
DROP TABLE IF EXISTS images_catalog;
DROP TABLE IF EXISTS zones;
DROP TABLE IF EXISTS disk_types;
DROP TABLE IF EXISTS platforms;
