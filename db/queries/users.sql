-- name: CreateUser :one
INSERT INTO users (id, name, email, password_hash, role)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, name, email, role, created_at;

-- name: GetUserByEmail :one
SELECT id, name, email, password_hash, role, created_at
FROM users
WHERE LOWER(email) = LOWER(sqlc.arg(email));

-- name: GetUserByID :one
SELECT id, name, email, role, created_at
FROM users
WHERE id = $1;

-- name: ListAdminIDs :many
SELECT id FROM users
WHERE role = 'admin'
ORDER BY id;
