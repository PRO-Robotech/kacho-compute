-- +goose Up

-- Seed zones
INSERT INTO zones (id, description) VALUES
  ('kacho-zone-a', 'Zone A — primary availability zone'),
  ('kacho-zone-b', 'Zone B — secondary availability zone')
ON CONFLICT (id) DO NOTHING;

-- Seed disk_types
INSERT INTO disk_types (id, description) VALUES
  ('network-ssd',        'Network SSD — high-performance block storage'),
  ('network-hdd',        'Network HDD — cost-effective block storage'),
  ('network-ssd-nonreplicated', 'Non-replicated SSD — ultra-high IOPS')
ON CONFLICT (id) DO NOTHING;

-- Seed platforms
INSERT INTO platforms (id, description) VALUES
  ('standard-v1', 'Standard platform v1 (Intel Broadwell)'),
  ('standard-v2', 'Standard platform v2 (Intel Cascade Lake)'),
  ('standard-v3', 'Standard platform v3 (Intel Ice Lake)')
ON CONFLICT (id) DO NOTHING;

-- Seed images catalog (базовые образы)
INSERT INTO images_catalog (name, spec) VALUES
  ('ubuntu-22-04-lts', '{"display_name":"Ubuntu 22.04 LTS","description":"Ubuntu 22.04 LTS base image","family":"ubuntu-2204-lts","zone_id":"","size":"8Gi"}'),
  ('ubuntu-20-04-lts', '{"display_name":"Ubuntu 20.04 LTS","description":"Ubuntu 20.04 LTS base image","family":"ubuntu-2004-lts","zone_id":"","size":"8Gi"}'),
  ('debian-11',        '{"display_name":"Debian 11 Bullseye","description":"Debian 11 base image","family":"debian-11","zone_id":"","size":"8Gi"}')
ON CONFLICT (name) DO NOTHING;

-- +goose Down
DELETE FROM images_catalog WHERE name IN ('ubuntu-22-04-lts','ubuntu-20-04-lts','debian-11');
DELETE FROM platforms WHERE id IN ('standard-v1','standard-v2','standard-v3');
DELETE FROM disk_types WHERE id IN ('network-ssd','network-hdd','network-ssd-nonreplicated');
DELETE FROM zones WHERE id IN ('kacho-zone-a','kacho-zone-b');
