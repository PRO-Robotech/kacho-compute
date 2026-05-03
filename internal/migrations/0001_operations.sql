-- +goose Up
-- synced from kacho-corelib/migrations/common/0001_operations.sql

CREATE TABLE operations (
  id            UUID         PRIMARY KEY,
  description   TEXT         NOT NULL,
  created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
  created_by    TEXT         NOT NULL DEFAULT 'anonymous',
  modified_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
  done          BOOLEAN      NOT NULL DEFAULT false,
  metadata_type TEXT,
  metadata_data BYTEA,
  resource_id   TEXT,
  error_code    INT,
  error_message TEXT,
  error_details BYTEA,
  response_type TEXT,
  response_data BYTEA
);

CREATE INDEX operations_resource_idx ON operations (resource_id);
CREATE INDEX operations_done_idx ON operations (done);
CREATE INDEX operations_created_at_idx ON operations (created_at);

-- +goose Down
DROP TABLE IF EXISTS operations;
