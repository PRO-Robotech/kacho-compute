-- +goose Up

CREATE TABLE instances (
  id                       UUID PRIMARY KEY,
  folder_id                TEXT NOT NULL,
  created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
  name                     TEXT NOT NULL,
  description              TEXT NOT NULL DEFAULT '',
  labels                   JSONB NOT NULL DEFAULT '{}',

  zone_id                  TEXT NOT NULL DEFAULT '',
  platform_id              TEXT NOT NULL DEFAULT 'standard-v1',

  resources_cores          BIGINT NOT NULL DEFAULT 2,
  resources_memory         TEXT NOT NULL DEFAULT '4Gi',
  resources_core_fraction  INT NOT NULL DEFAULT 100,
  resources_gpus           BIGINT NOT NULL DEFAULT 0,

  status                   INT NOT NULL DEFAULT 0,
  fqdn                     TEXT NOT NULL DEFAULT '',
  metadata                 JSONB NOT NULL DEFAULT '{}',

  boot_disk_id             TEXT NOT NULL DEFAULT '',
  boot_disk_device_name    TEXT NOT NULL DEFAULT '',
  boot_disk_auto_delete    BOOLEAN NOT NULL DEFAULT true,

  secondary_disks          JSONB NOT NULL DEFAULT '[]',
  network_interfaces       JSONB NOT NULL DEFAULT '[]',

  service_account_id       TEXT NOT NULL DEFAULT '',
  scheduling_preemptible   BOOLEAN NOT NULL DEFAULT false,
  desired_power_state      INT NOT NULL DEFAULT 0,

  generation               BIGINT NOT NULL DEFAULT 1,
  resource_version         TEXT NOT NULL DEFAULT '',
  observed_generation      BIGINT NOT NULL DEFAULT 0,
  status_last_transition_at TIMESTAMPTZ NOT NULL DEFAULT now(),

  ips_internal             JSONB NOT NULL DEFAULT '[]',
  ips_external             JSONB NOT NULL DEFAULT '[]',

  last_restart_completed_at TIMESTAMPTZ,
  deleted_at               TIMESTAMPTZ
);

CREATE INDEX instances_folder_idx ON instances (folder_id);
CREATE INDEX instances_status_idx ON instances (status) WHERE deleted_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS instances;
