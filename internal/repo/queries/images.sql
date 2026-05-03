-- name: GetImageByUID :one
SELECT uid, name, labels, creation_timestamp, resource_version, generation, spec, state
FROM images_catalog
WHERE uid = $1;

-- name: ListImages :many
SELECT uid, name, labels, creation_timestamp, resource_version, generation, spec, state
FROM images_catalog
ORDER BY resource_version ASC, uid ASC;
