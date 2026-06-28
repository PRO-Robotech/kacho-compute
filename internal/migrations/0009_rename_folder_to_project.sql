-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up
-- +goose StatementBegin
-- =============================================================================
-- KAC-106 (E1) — hard rename folder_id -> project_id on all 4 Compute resources.
-- Strategy: ALTER TABLE RENAME COLUMN + ALTER INDEX RENAME (metadata-only,
-- instant, no data-rewrite; Postgres auto-updates index column references on
-- RENAME COLUMN, so only the index *names* need updating).
-- Affected tables: disks, images, snapshots, instances.
-- (Hypervisors / geography tables — admin-only, no folder_id.)
-- =============================================================================

-- 1) Rename columns (index column references auto-update)
ALTER TABLE disks      RENAME COLUMN folder_id TO project_id;
ALTER TABLE images     RENAME COLUMN folder_id TO project_id;
ALTER TABLE snapshots  RENAME COLUMN folder_id TO project_id;
ALTER TABLE instances  RENAME COLUMN folder_id TO project_id;

-- 2) Rename indexes (just renaming the index identifiers; column references
--    inside are automatically retargeted by Postgres)
ALTER INDEX disks_folder_idx           RENAME TO disks_project_idx;
ALTER INDEX disks_folder_name_uniq     RENAME TO disks_project_name_uniq;

ALTER INDEX images_folder_idx          RENAME TO images_project_idx;
ALTER INDEX images_folder_name_uniq    RENAME TO images_project_name_uniq;
-- images_family_idx contains (folder_id, family, created_at DESC) — column
-- reference auto-updated; name unchanged (semantically "family" index, not
-- "folder" index). Keep name as-is.

ALTER INDEX snapshots_folder_idx       RENAME TO snapshots_project_idx;
ALTER INDEX snapshots_folder_name_uniq RENAME TO snapshots_project_name_uniq;

ALTER INDEX instances_folder_idx       RENAME TO instances_project_idx;
ALTER INDEX instances_folder_name_uniq RENAME TO instances_project_name_uniq;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER INDEX instances_project_name_uniq RENAME TO instances_folder_name_uniq;
ALTER INDEX instances_project_idx       RENAME TO instances_folder_idx;
ALTER INDEX snapshots_project_name_uniq RENAME TO snapshots_folder_name_uniq;
ALTER INDEX snapshots_project_idx       RENAME TO snapshots_folder_idx;
ALTER INDEX images_project_name_uniq    RENAME TO images_folder_name_uniq;
ALTER INDEX images_project_idx          RENAME TO images_folder_idx;
ALTER INDEX disks_project_name_uniq     RENAME TO disks_folder_name_uniq;
ALTER INDEX disks_project_idx           RENAME TO disks_folder_idx;

ALTER TABLE instances  RENAME COLUMN project_id TO folder_id;
ALTER TABLE snapshots  RENAME COLUMN project_id TO folder_id;
ALTER TABLE images     RENAME COLUMN project_id TO folder_id;
ALTER TABLE disks      RENAME COLUMN project_id TO folder_id;
-- +goose StatementEnd
