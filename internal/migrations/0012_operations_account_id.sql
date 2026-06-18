-- +goose Up

-- Additive, nullable account_id denormalization on the compute operations
-- table to match the corelib operations.Repo.CreateWithPrincipal INSERT, which
-- now lists account_id unconditionally (corelib migrations/common/0003 +
-- repo.go INSERT column-list). Without this column EVERY compute async mutation
-- (Disk/Image/Snapshot/Instance Create/Update/Delete → Operation row) fails
-- with SQLSTATE 42703 undefined_column.
--
-- Mirrors kacho-iam migration 0016_operations_account_id.sql. The compute
-- operations table is declared UNQUALIFIED in 0001_initial.sql (relies on the
-- session schema / search_path), exactly like 0008_operations_principal.sql —
-- so this ALTER/CREATE INDEX is unqualified too, targeting the same table.
--
-- ADDITIVE / BACK-COMPAT: nullable, no DEFAULT, no NOT NULL. account_id stays
-- NULL for compute — it is an IAM-only denormalization (set by IAM when the
-- writing use-case passes metadata with the exact-name account_id field via
-- corelib extractAccountID). Compute op-producers leave it NULL.
--
-- partial index (account_id, created_at, id) WHERE account_id IS NOT NULL —
-- covers account-scoped cursor pagination and does NOT index the (all-NULL for
-- compute) rows, so there is no index bloat.

ALTER TABLE operations
  ADD COLUMN account_id text NULL;

CREATE INDEX operations_account_id_idx
  ON operations (account_id, created_at, id)
  WHERE account_id IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS operations_account_id_idx;

ALTER TABLE operations
  DROP COLUMN account_id;
