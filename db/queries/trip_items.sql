-- name: CreateTripItem :exec
INSERT INTO trip_items (id, trip_id, name, description, photo_key, reference_price, position)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ListTripItems :many
SELECT * FROM trip_items
WHERE trip_id = $1
ORDER BY position, created_at, id;

-- name: GetTripItem :one
SELECT * FROM trip_items
WHERE id = $1 AND trip_id = $2;

-- name: CountTripItems :one
SELECT count(*) AS items, COALESCE(MAX(position) + 1, 0)::integer AS next_position
FROM trip_items
WHERE trip_id = $1;

-- name: UpdateTripItem :exec
UPDATE trip_items
SET name = $3, description = $4, photo_key = $5, reference_price = $6, position = $7, updated_at = now()
WHERE id = $1 AND trip_id = $2;

-- name: DeleteTripItem :execrows
DELETE FROM trip_items
WHERE id = $1 AND trip_id = $2;
