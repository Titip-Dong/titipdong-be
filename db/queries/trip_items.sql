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

-- name: ListTripPreviewItems :many
-- The first items of each trip in display order, with the trip's item count; feeds the trip lists.
SELECT trip_id, name, photo_key, item_count::integer AS item_count
FROM (
    SELECT ti.trip_id, ti.name, ti.photo_key,
           row_number() OVER (PARTITION BY ti.trip_id ORDER BY ti.position, ti.created_at, ti.id) AS rn,
           count(*) OVER (PARTITION BY ti.trip_id) AS item_count
    FROM trip_items ti
    WHERE ti.trip_id = ANY(sqlc.arg(trip_ids)::uuid[])
) ranked
WHERE rn <= sqlc.arg(per_trip)::bigint
ORDER BY trip_id, rn;
