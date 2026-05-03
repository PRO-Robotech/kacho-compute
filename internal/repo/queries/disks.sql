-- name: GetDiskByUID :one
SELECT uid, folder_id, cloud_id, organization_id, name, labels, annotations,
       creation_timestamp, resource_version, generation, deletion_timestamp,
       finalizers, spec, status
FROM disks
WHERE uid = $1 AND deletion_timestamp IS NULL;

-- name: GetDiskByFolderAndName :one
SELECT uid, folder_id, cloud_id, organization_id, name, labels, annotations,
       creation_timestamp, resource_version, generation, deletion_timestamp,
       finalizers, spec, status
FROM disks
WHERE folder_id = $1 AND name = $2 AND deletion_timestamp IS NULL;

-- name: InsertDisk :one
INSERT INTO disks (uid, folder_id, cloud_id, organization_id, name, labels, annotations, finalizers, spec, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING uid, folder_id, cloud_id, organization_id, name, labels, annotations,
          creation_timestamp, resource_version, generation, deletion_timestamp,
          finalizers, spec, status;

-- name: UpdateDisk :one
UPDATE disks
SET labels = $2, annotations = $3, spec = $4, generation = generation + 1
WHERE uid = $1
RETURNING uid, folder_id, cloud_id, organization_id, name, labels, annotations,
          creation_timestamp, resource_version, generation, deletion_timestamp,
          finalizers, spec, status;

-- name: UpdateDiskStatus :one
UPDATE disks
SET status = $2
WHERE uid = $1
RETURNING uid, folder_id, cloud_id, organization_id, name, labels, annotations,
          creation_timestamp, resource_version, generation, deletion_timestamp,
          finalizers, spec, status;

-- name: SoftDeleteDisk :exec
UPDATE disks SET deletion_timestamp = now() WHERE uid = $1;

-- name: DeleteDisk :exec
DELETE FROM disks WHERE uid = $1;

-- name: DiskHasSnapshots :one
SELECT EXISTS (
  SELECT 1 FROM snapshots WHERE spec->>'disk_id' = $1::text AND deletion_timestamp IS NULL
) AS has_snapshots;

-- name: ListDisksPendingReconcile :many
SELECT uid, folder_id, cloud_id, organization_id, name, labels, annotations,
       creation_timestamp, resource_version, generation, deletion_timestamp,
       finalizers, spec, status
FROM disks
WHERE status->>'state' IN ('CREATING','ATTACHING','DETACHING','DELETING');

-- name: ListDisksAttachedToInstance :many
SELECT uid, folder_id, cloud_id, organization_id, name, labels, annotations,
       creation_timestamp, resource_version, generation, deletion_timestamp,
       finalizers, spec, status
FROM disks
WHERE status->>'attached_to_instance_id' = $1::text AND deletion_timestamp IS NULL;
