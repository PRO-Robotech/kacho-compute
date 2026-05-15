-- +goose Up
-- KAC-90 (G1 из compute within-service refs audit KAC-85):
-- attached_disks.disk_id UNIQUE — предотвращение двойного attach одного и того
-- же диска двум разным Instance (parity с NIC-attach race инцидентом KAC-52).
--
-- До этой миграции PK (instance_id, disk_id) НЕ запрещал ситуацию
-- (instance_A, disk_X) + (instance_B, disk_X) — две concurrent AttachDisk-операции
-- могли обе пройти software-side guard (IsAttached / cycle по AttachedDisks)
-- и обе вставить строку: second-writer wins, диск «привязан» к двум ВМ.
--
-- DB-уровень (workspace CLAUDE.md §«Within-service refs — DB-уровень обязателен»):
-- атомарный UNIQUE INDEX на disk_id даёт ровно одну строку attached_disks
-- per disk_id — concurrent INSERT-ы получают SQLSTATE 23505, service-слой
-- маппит на FailedPrecondition "disk already attached to another instance".

-- +goose StatementBegin
DO $$
DECLARE
  dup_count BIGINT;
BEGIN
  SELECT COUNT(*) INTO dup_count
  FROM (
    SELECT disk_id
      FROM attached_disks
     GROUP BY disk_id
    HAVING COUNT(*) > 1
  ) d;
  IF dup_count > 0 THEN
    RAISE EXCEPTION 'attached_disks has % disk_id with multiple instance_id rows; resolve before adding UNIQUE index (KAC-90)', dup_count
      USING ERRCODE = 'P0001';
  END IF;
END;
$$;
-- +goose StatementEnd

CREATE UNIQUE INDEX IF NOT EXISTS attached_disks_disk_id_uniq
  ON attached_disks (disk_id);

-- +goose Down
DROP INDEX IF EXISTS attached_disks_disk_id_uniq;
