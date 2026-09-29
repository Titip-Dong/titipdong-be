-- name: CreateRefreshToken :exec
INSERT INTO refresh_tokens (id, user_id, family_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4, $5);

-- name: GetRefreshTokenByHashForUpdate :one
SELECT id, user_id, family_id, expires_at, revoked_at
FROM refresh_tokens
WHERE token_hash = $1
FOR UPDATE;

-- name: RotateRefreshToken :exec
UPDATE refresh_tokens
SET revoked_at = sqlc.arg(revoked_at)::timestamptz, replaced_by = sqlc.arg(replaced_by)::uuid
WHERE id = sqlc.arg(id);

-- name: RevokeRefreshTokenFamily :exec
UPDATE refresh_tokens
SET revoked_at = sqlc.arg(revoked_at)::timestamptz
WHERE family_id = sqlc.arg(family_id) AND revoked_at IS NULL;

-- name: RefreshTokenFamilyActive :one
SELECT EXISTS (
    SELECT 1 FROM refresh_tokens WHERE family_id = $1 AND revoked_at IS NULL
);
