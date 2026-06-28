-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up
-- SEC-D (epic §3.1 Variant A): transactional outbox for FGA owner-tuple
-- register/unregister intents. kacho-compute no longer writes to OpenFGA directly
-- (epic requirement #6; GitHub Issue N5 — best-effort post-commit dual-write that
-- silently lost the owner-tuple on an FGA hiccup → per-resource Check fail-closed
-- DENY forever). Instead every resource Create/Delete records an INTENT row in
-- THIS table IN THE SAME writer-tx as the resource Insert/Delete (one commit, no
-- dual-write). A separate register-drainer (corelib outbox/drainer) replays each
-- intent by calling kacho-iam InternalIAMService.RegisterResource /
-- UnregisterResource over mTLS — idempotent, at-least-once, IAM-Unavailable →
-- retry, the owner-tuple is never lost.
--
-- This is a SEPARATE table from compute_outbox (OQ-SEC-D-1): the FGA-relay
-- drainer and the domain Watch-drainer have different appliers and different
-- failure modes, so isolating them avoids one poisoned FGA intent blocking the
-- Watch stream (and vice-versa).
--
-- Column shape mirrors kacho-iam.fga_outbox (the W1.1 drainer contract): id
-- BIGSERIAL PK, event_type, payload JSONB, sent_at, last_error, attempt_count.
-- The drainer claims with `UPDATE … WHERE sent_at IS NULL AND attempt_count <
-- MaxAttempts … FOR UPDATE SKIP LOCKED` (CAS-claim → exactly-once across
-- replicas, data-integrity.md ban #10), so no extra UNIQUE backstop is needed.
CREATE TABLE compute_fga_register_outbox (
  id            BIGSERIAL    PRIMARY KEY,
  event_type    TEXT         NOT NULL,        -- fga.register | fga.unregister
  resource_kind TEXT         NOT NULL,        -- Instance | Disk | Image | Snapshot
  resource_id   TEXT         NOT NULL,
  payload       JSONB        NOT NULL DEFAULT '{}'::jsonb,
  created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
  sent_at       TIMESTAMPTZ,
  last_error    TEXT,
  attempt_count INTEGER      NOT NULL DEFAULT 0,
  CONSTRAINT compute_fga_register_outbox_event_type_check
    CHECK (event_type IN ('fga.register', 'fga.unregister'))
);

-- Pending-rows partial index — the drainer's hot claim path filters on
-- (sent_at IS NULL): keeps the catch-up SELECT cheap as the table grows.
CREATE INDEX compute_fga_register_outbox_pending_idx
  ON compute_fga_register_outbox (id) WHERE sent_at IS NULL;

-- LISTEN/NOTIFY wake-up: on every INSERT notify the register-drainer so it picks
-- up the new intent without waiting for the next catch-up poll. Channel name
-- matches the drainer Config.Channel ("compute_fga_register_outbox").
-- +goose StatementBegin
CREATE FUNCTION compute_fga_register_outbox_notify() RETURNS trigger
  LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_notify('compute_fga_register_outbox', NEW.id::text);
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER compute_fga_register_outbox_notify_trg
  AFTER INSERT ON compute_fga_register_outbox
  FOR EACH ROW EXECUTE FUNCTION compute_fga_register_outbox_notify();

-- +goose Down
DROP TRIGGER IF EXISTS compute_fga_register_outbox_notify_trg ON compute_fga_register_outbox;
DROP FUNCTION IF EXISTS compute_fga_register_outbox_notify();
DROP TABLE IF EXISTS compute_fga_register_outbox;
