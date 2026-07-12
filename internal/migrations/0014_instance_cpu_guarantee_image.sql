-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up
-- S5-03/05 compute→storage cutover: sizing gains a guaranteed CPU baseline and the
-- Instance gains an OCI image reference (kacho-registry) the rootfs is delivered
-- from. cpu_guarantee_percent — guaranteed baseline per vCPU in percent, 0 =
-- best-effort/burstable, 1..100 = guaranteed (DB-CHECK 0..100; sizing is mutable
-- only while STOPPED, enforced by the resize CAS). image — the OCI reference on the
-- input path; image_digest — the resolved immutable content digest image was pinned
-- to (output-only; registry-resolve is a later slice, so it stays '' for now).
ALTER TABLE instances
  ADD COLUMN cpu_guarantee_percent INTEGER NOT NULL DEFAULT 0
    CHECK (cpu_guarantee_percent BETWEEN 0 AND 100),
  ADD COLUMN image TEXT NOT NULL DEFAULT '',
  ADD COLUMN image_digest TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE instances
  DROP COLUMN cpu_guarantee_percent,
  DROP COLUMN image,
  DROP COLUMN image_digest;
