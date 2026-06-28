-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up
-- Geography (Region/Zone) is now owned by kacho-geo (epic kacho-workspace#82, Stage
-- S7). compute no longer SERVES Region/Zone: the public read RPCs, Internal* admin
-- CRUD and zone_id validation have all moved to kacho-geo (zone_id validation switched
-- to the geo client in Stage S4). The local `zones`/`regions` tables are therefore
-- dead and dropped here. Data was already migrated to kacho-geo, so dropping is safe.
-- DiskType (disk_types) is unaffected — it shares the catalog schema but stays.
ALTER TABLE zones DROP CONSTRAINT zones_region_id_fkey;
DROP TABLE zones;
DROP TABLE regions;

-- +goose Down
-- Recreate the geography tables in their pre-S7 shape (final state of 0001 + 0003):
-- regions(id,name,created_at), zones(id,region_id,name,status,created_at) with
-- FK zones.region_id → regions(id) ON DELETE RESTRICT, then reseed ru-central1
-- + ru-central1-{a,b,d}.
CREATE TABLE regions (
  id         TEXT         PRIMARY KEY,         -- "ru-central1"
  name       TEXT         NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE zones (
  id         TEXT         PRIMARY KEY,         -- "ru-central1-a"
  region_id  TEXT         NOT NULL,            -- "ru-central1"
  status     TEXT         NOT NULL DEFAULT 'UP', -- UP | DOWN | STATUS_UNSPECIFIED
  name       TEXT         NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX zones_region_idx ON zones (region_id);

ALTER TABLE zones
  ADD CONSTRAINT zones_region_id_fkey FOREIGN KEY (region_id) REFERENCES regions (id) ON DELETE RESTRICT;

INSERT INTO regions (id, name) VALUES ('ru-central1', 'Russia Central 1');

INSERT INTO zones (id, region_id, status, name) VALUES
  ('ru-central1-a', 'ru-central1', 'UP', 'Russia Central 1 A'),
  ('ru-central1-b', 'ru-central1', 'UP', 'Russia Central 1 B'),
  ('ru-central1-d', 'ru-central1', 'UP', 'Russia Central 1 D');
