-- name: CreateUpload :exec
INSERT INTO uploads (id, owner_id, purpose, object_key, content_type, size_bytes)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: LockUpload :one
SELECT * FROM uploads WHERE id = $1 FOR UPDATE;

-- name: MarkUploadAttached :exec
UPDATE uploads SET status = 'attached', size_bytes = $2, attached_at = $3 WHERE id = $1;

-- name: ListStalePendingUploads :many
SELECT * FROM uploads
WHERE status = 'pending' AND created_at < sqlc.arg(before)::timestamptz
ORDER BY created_at
LIMIT sqlc.arg(row_limit);

-- name: DeleteUpload :exec
DELETE FROM uploads WHERE id = $1 AND status = 'pending';
