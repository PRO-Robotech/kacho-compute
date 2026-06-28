-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up
-- kacho-compute becomes the owner of Geography (Region/Zone), moved from kacho-vpc
-- (epic KAC-15). Add a regions table, add a name column to zones, FK zones→regions,
-- and seed ru-central1 + ru-central1-{a,b,d} here (no longer mirrored from kacho-vpc).
CREATE TABLE regions (
  id         TEXT         PRIMARY KEY,         -- "ru-central1"
  name       TEXT         NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);

ALTER TABLE zones ADD COLUMN name TEXT NOT NULL DEFAULT '';

-- seed regions (idempotent) and backfill region/zone names for any pre-existing rows
INSERT INTO regions (id, name) VALUES ('ru-central1', 'Russia Central 1')
  ON CONFLICT (id) DO NOTHING;

-- ensure the seed zones exist (compute previously mirrored them from kacho-vpc)
INSERT INTO zones (id, region_id, status, name) VALUES
  ('ru-central1-a', 'ru-central1', 'UP', 'Russia Central 1 A'),
  ('ru-central1-b', 'ru-central1', 'UP', 'Russia Central 1 B'),
  ('ru-central1-d', 'ru-central1', 'UP', 'Russia Central 1 D')
  ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name;

-- any other pre-existing zone gets a placeholder name derived from its id
UPDATE zones SET name = id WHERE name = '';

-- referential integrity zones → regions (RESTRICT: a region with zones cannot be deleted).
-- Any zone referencing a not-yet-known region would fail here; seed the region first.
INSERT INTO regions (id, name)
  SELECT DISTINCT region_id, region_id FROM zones
  WHERE region_id <> '' AND region_id NOT IN (SELECT id FROM regions)
  ON CONFLICT (id) DO NOTHING;

ALTER TABLE zones
  ADD CONSTRAINT zones_region_id_fkey FOREIGN KEY (region_id) REFERENCES regions (id) ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE zones DROP CONSTRAINT zones_region_id_fkey;
ALTER TABLE zones DROP COLUMN name;
DROP TABLE regions;
