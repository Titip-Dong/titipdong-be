-- name: CreateUser :one
INSERT INTO users (id, name, email, password_hash, role)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, name, email, role, status, phone, created_at;

-- name: GetUserByEmail :one
SELECT id, name, email, password_hash, role, status, phone, created_at
FROM users
WHERE LOWER(email) = LOWER(sqlc.arg(email));

-- name: GetUserByID :one
SELECT id, name, email, role, status, phone, created_at
FROM users
WHERE id = $1;

-- name: ListAdminIDs :many
SELECT id FROM users
WHERE role = 'admin'
ORDER BY id;

-- name: SetUserStatus :one
UPDATE users
SET status = sqlc.arg(status)::text, updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, name, email, role, status, phone, created_at;
