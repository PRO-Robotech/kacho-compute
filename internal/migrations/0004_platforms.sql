-- +goose Up

CREATE TABLE platforms (
  id          TEXT PRIMARY KEY,
  description TEXT NOT NULL DEFAULT ''
);

INSERT INTO platforms (id, description) VALUES
  ('standard-v1', 'Intel Broadwell (standard-v1)'),
  ('standard-v2', 'Intel Cascade Lake (standard-v2)'),
  ('standard-v3', 'Intel Ice Lake (standard-v3)');

-- +goose Down
DROP TABLE IF EXISTS platforms;
