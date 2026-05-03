-- name: GetSnapshotByUID :one
SELECT uid, folder_id, cloud_id, organization_id, name, labels, annotations,
       creation_timestamp, resource_version, generation, deletion_timestamp,
       finalizers, spec, status
FROM snapshots
WHERE uid = $1 AND deletion_timestamp IS NULL;

-- name: GetSnapshotByFolderAndName :one
SELECT uid, folder_id, cloud_id, organization_id, name, labels, annotations,
       creation_timestamp, resource_version, generation, deletion_timestamp,
       finalizers, spec, status
FROM snapshots
WHERE folder_id = $1 AND name = $2 AND deletion_timestamp IS NULL;

-- name: InsertSnapshot :one
INSERT INTO snapshots (uid, folder_id, cloud_id, organization_id, name, labels, annotations, finalizers, spec, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING uid, folder_id, cloud_id, organization_id, name, labels, annotations,
          creation_timestamp, resource_version, generation, deletion_timestamp,
          finalizers, spec, status;

-- name: UpdateSnapshot :one
UPDATE snapshots
SET labels = $2, annotations = $3, spec = $4, generation = generation + 1
WHERE uid = $1
RETURNING uid, folder_id, cloud_id, organization_id, name, labels, annotations,
          creation_timestamp, resource_version, generation, deletion_timestamp,
          finalizers, spec, status;

-- name: UpdateSnapshotStatus :one
UPDATE snapshots
SET status = $2
WHERE uid = $1
RETURNING uid, folder_id, cloud_id, organization_id, name, labels, annotations,
          creation_timestamp, resource_version, generation, deletion_timestamp,
          finalizers, spec, status;

-- name: SoftDeleteSnapshot :exec
UPDATE snapshots SET deletion_timestamp = now() WHERE uid = $1;

-- name: DeleteSnapshot :exec
DELETE FROM snapshots WHERE uid = $1;

-- name: ListSnapshotsPendingReconcile :many
SELECT uid, folder_id, cloud_id, organization_id, name, labels, annotations,
       creation_timestamp, resource_version, generation, deletion_timestamp,
       finalizers, spec, status
FROM snapshots
WHERE status->>'state' IN ('CREATING','DELETING');
