-- +goose Up

CREATE TABLE disks (
  id                      UUID PRIMARY KEY,
  folder_id               TEXT NOT NULL,
  name                    TEXT NOT NULL,
  description             TEXT NOT NULL DEFAULT '',
  created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
  labels                  JSONB NOT NULL DEFAULT '{}',

  disk_type_id            TEXT NOT NULL DEFAULT 'network-ssd',
  zone_id                 TEXT NOT NULL DEFAULT '',
  size                    TEXT NOT NULL DEFAULT '10Gi',
  image_id                TEXT,

  status                  INT NOT NULL DEFAULT 0,
  attached_to_instance_id TEXT,

  generation              BIGINT NOT NULL DEFAULT 1,
  resource_version        TEXT NOT NULL DEFAULT '',
  observed_generation     BIGINT NOT NULL DEFAULT 0,
  status_last_transition_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at              TIMESTAMPTZ
);

CREATE INDEX disks_folder_idx ON disks (folder_id);
CREATE INDEX disks_status_idx ON disks (status) WHERE deleted_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS disks;
