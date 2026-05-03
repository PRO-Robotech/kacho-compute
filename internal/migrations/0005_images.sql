-- +goose Up

CREATE TABLE images (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  family      TEXT NOT NULL DEFAULT '',
  os_type     TEXT NOT NULL DEFAULT 'linux',
  size        BIGINT NOT NULL DEFAULT 0,
  status      INT NOT NULL DEFAULT 1  -- IMAGE_STATUS_READY
);

CREATE INDEX images_family_idx ON images (family);

INSERT INTO images (id, name, description, family, os_type, size) VALUES
  (gen_random_uuid(), 'ubuntu-22-04-lts', 'Ubuntu 22.04 LTS', 'ubuntu-2204-lts', 'linux', 10737418240),
  (gen_random_uuid(), 'ubuntu-20-04-lts', 'Ubuntu 20.04 LTS', 'ubuntu-2004-lts', 'linux', 10737418240),
  (gen_random_uuid(), 'debian-11',        'Debian 11 Bullseye', 'debian-11', 'linux', 8589934592);

-- +goose Down
DROP TABLE IF EXISTS images;
