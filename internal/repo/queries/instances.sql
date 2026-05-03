-- name: GetInstanceByUID :one
SELECT uid, folder_id, cloud_id, organization_id, name, labels, annotations,
       creation_timestamp, resource_version, generation, deletion_timestamp,
       finalizers, restarted_at, spec, status
FROM instances
WHERE uid = $1 AND deletion_timestamp IS NULL;

-- name: GetInstanceByUIDIncludeDeleted :one
SELECT uid, folder_id, cloud_id, organization_id, name, labels, annotations,
       creation_timestamp, resource_version, generation, deletion_timestamp,
       finalizers, restarted_at, spec, status
FROM instances
WHERE uid = $1;

-- name: GetInstanceByFolderAndName :one
SELECT uid, folder_id, cloud_id, organization_id, name, labels, annotations,
       creation_timestamp, resource_version, generation, deletion_timestamp,
       finalizers, restarted_at, spec, status
FROM instances
WHERE folder_id = $1 AND name = $2 AND deletion_timestamp IS NULL;

-- name: InsertInstance :one
INSERT INTO instances (uid, folder_id, cloud_id, organization_id, name, labels, annotations, finalizers, spec, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING uid, folder_id, cloud_id, organization_id, name, labels, annotations,
          creation_timestamp, resource_version, generation, deletion_timestamp,
          finalizers, restarted_at, spec, status;

-- name: UpdateInstance :one
UPDATE instances
SET labels = $2, annotations = $3, spec = $4, generation = generation + 1
WHERE uid = $1
RETURNING uid, folder_id, cloud_id, organization_id, name, labels, annotations,
          creation_timestamp, resource_version, generation, deletion_timestamp,
          finalizers, restarted_at, spec, status;

-- name: UpdateInstanceStatus :one
UPDATE instances
SET status = $2
WHERE uid = $1
RETURNING uid, folder_id, cloud_id, organization_id, name, labels, annotations,
          creation_timestamp, resource_version, generation, deletion_timestamp,
          finalizers, restarted_at, spec, status;

-- name: UpdateInstanceFinalizers :one
UPDATE instances
SET finalizers = $2
WHERE uid = $1
RETURNING uid, folder_id, cloud_id, organization_id, name, labels, annotations,
          creation_timestamp, resource_version, generation, deletion_timestamp,
          finalizers, restarted_at, spec, status;

-- name: UpdateInstanceRestartedAt :one
UPDATE instances
SET restarted_at = $2::timestamptz
WHERE uid = $1
RETURNING uid, folder_id, cloud_id, organization_id, name, labels, annotations,
          creation_timestamp, resource_version, generation, deletion_timestamp,
          finalizers, restarted_at, spec, status;

-- name: SetInstanceRestart :one
UPDATE instances
SET restarted_at = now()
WHERE uid = $1
RETURNING uid, folder_id, cloud_id, organization_id, name, labels, annotations,
          creation_timestamp, resource_version, generation, deletion_timestamp,
          finalizers, restarted_at, spec, status;

-- name: SoftDeleteInstance :exec
UPDATE instances SET deletion_timestamp = now() WHERE uid = $1;

-- name: DeleteInstance :exec
DELETE FROM instances WHERE uid = $1;

-- name: ListInstancesPendingReconcile :many
SELECT uid, folder_id, cloud_id, organization_id, name, labels, annotations,
       creation_timestamp, resource_version, generation, deletion_timestamp,
       finalizers, restarted_at, spec, status
FROM instances
WHERE (
  (status->>'state' IN ('PROVISIONING','STOPPING','STARTING','DELETING'))
  OR (deletion_timestamp IS NOT NULL AND finalizers @> ARRAY['compute.kacho.io/disk-detach'])
  OR (restarted_at IS NOT NULL AND (
      status->>'last_restart_completed_at' IS NULL
      OR restarted_at > (status->>'last_restart_completed_at')::timestamptz
  ))
);

-- name: SnapshotResourceVersion :one
SELECT nextval('resource_version_seq') AS resource_version;
