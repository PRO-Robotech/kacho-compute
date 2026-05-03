-- +goose Up

CREATE TABLE snapshots (
  id                  UUID PRIMARY KEY,
  folder_id           TEXT NOT NULL,
  name                TEXT NOT NULL,
  description         TEXT NOT NULL DEFAULT '',
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  labels              JSONB NOT NULL DEFAULT '{}',

  disk_id             TEXT NOT NULL,
  size                BIGINT NOT NULL DEFAULT 0,

  status              INT NOT NULL DEFAULT 0,
  progress_percent    INT NOT NULL DEFAULT 0,

  generation          BIGINT NOT NULL DEFAULT 1,
  resource_version    TEXT NOT NULL DEFAULT '',
  observed_generation BIGINT NOT NULL DEFAULT 0,
  deleted_at          TIMESTAMPTZ
);

CREATE INDEX snapshots_folder_idx ON snapshots (folder_id);
CREATE INDEX snapshots_disk_idx ON snapshots (disk_id);
CREATE INDEX snapshots_status_idx ON snapshots (status) WHERE deleted_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS snapshots;
