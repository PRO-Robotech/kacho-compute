-- +goose Up

CREATE TABLE zones (
  id          TEXT PRIMARY KEY,
  description TEXT NOT NULL DEFAULT ''
);

INSERT INTO zones (id, description) VALUES
  ('kacho-zone-a', 'Availability zone A'),
  ('kacho-zone-b', 'Availability zone B');

-- +goose Down
DROP TABLE IF EXISTS zones;
