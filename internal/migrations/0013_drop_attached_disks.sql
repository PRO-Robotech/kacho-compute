-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up
-- Storage-split cutover (compute→storage decomposition, slice 2): the volume↔
-- Instance attach-state moves out of compute entirely. The join table
-- `attached_disks` (instance ↔ compute-local Disk M:N) is retired — attachment is
-- now owned by kacho-storage (`volume_attachments` in the storage DB, coordinated
-- via `storage.v1.InternalVolumeService.Attach/Detach/ListAttachments`). Compute
-- holds ZERO local attach-state: `Instance.boot_volume`/`secondary_volumes` become
-- read-only mirrors recomputed on read from storage (source of truth), with
-- graceful-degrade when storage is unreachable.
--
-- STRANGLER boundary: the compute `disks`/`images`/`snapshots` tables and the
-- compute.v1 Disk/Image/Snapshot serving stay untouched — ONLY the attach-state
-- (`attached_disks`) is dropped here. The within-service invariants that used to
-- live on `attached_disks` (single-attach UNIQUE disk_id / per-instance device
-- UNIQUE / single boot EXCLUDE / delete-in-use FK RESTRICT) now live on the storage
-- side as real DB-constraints on `volume_attachments` (tested in kacho-storage S2).
--
-- Dropping the table also drops the FK `attached_disks.disk_id → disks RESTRICT`
-- and its indexes (attached_disks_disk_idx / _boot_uniq / _device_uniq /
-- _disk_id_uniq from migration 0007) via CASCADE — a compute Disk is no longer
-- directly attachable, so its "in use by instance" gate dissolves with the table.

DROP TABLE IF EXISTS attached_disks;

-- +goose Down
-- Recreate the retired join table (schema identical to 0001 baseline + the
-- 0007 disk_id UNIQUE backstop) so a rollback restores the pre-cutover shape.
CREATE TABLE attached_disks (
  instance_id TEXT         NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  disk_id     TEXT         NOT NULL REFERENCES disks(id) ON DELETE RESTRICT,
  is_boot     BOOLEAN      NOT NULL DEFAULT false,
  mode        TEXT         NOT NULL DEFAULT 'READ_WRITE',
  device_name TEXT         NOT NULL DEFAULT '',
  auto_delete BOOLEAN      NOT NULL DEFAULT false,
  attached_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
  PRIMARY KEY (instance_id, disk_id)
);
CREATE INDEX attached_disks_disk_idx ON attached_disks (disk_id);
CREATE UNIQUE INDEX attached_disks_boot_uniq ON attached_disks (instance_id) WHERE is_boot;
CREATE UNIQUE INDEX attached_disks_device_uniq ON attached_disks (instance_id, device_name) WHERE device_name <> '';
CREATE UNIQUE INDEX attached_disks_disk_id_uniq ON attached_disks (disk_id);
