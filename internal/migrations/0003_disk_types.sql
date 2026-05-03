-- +goose Up

CREATE TABLE disk_types (
  id          TEXT PRIMARY KEY,
  description TEXT NOT NULL DEFAULT ''
);

INSERT INTO disk_types (id, description) VALUES
  ('network-ssd', 'Network SSD'),
  ('network-hdd', 'Network HDD');

-- +goose Down
DROP TABLE IF EXISTS disk_types;
