-- name: GetSystemMetadataValue :one
SELECT metadata_value
FROM system_metadata
WHERE metadata_key = $1;

-- name: SetSystemMetadataValue :exec
INSERT INTO system_metadata (
    metadata_key,
    metadata_value
) VALUES (
    $1,
    $2
)
ON CONFLICT (metadata_key) DO UPDATE
SET
    metadata_value = EXCLUDED.metadata_value,
    updated_at = now();

-- name: DeleteSystemMetadata :exec
DELETE FROM system_metadata
WHERE metadata_key = $1;
