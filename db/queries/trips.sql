-- name: CreateTrip :exec
INSERT INTO trips (
    id, jastiper_id, slug, title, description, destination_city, destination_country,
    departs_on, returns_on, request_deadline
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: GetTripByID :one
SELECT sqlc.embed(t), u.name AS jastiper_name
FROM trips t
JOIN users u ON u.id = t.jastiper_id
WHERE t.id = $1;

-- name: GetTripBySlug :one
SELECT sqlc.embed(t), u.name AS jastiper_name
FROM trips t
JOIN users u ON u.id = t.jastiper_id
WHERE t.slug = $1;

-- name: ListOpenTrips :many
SELECT sqlc.embed(t), u.name AS jastiper_name
FROM trips t
JOIN users u ON u.id = t.jastiper_id
WHERE t.status = 'open'
  AND t.request_deadline > sqlc.arg(now)::timestamptz
  AND t.destination_city ILIKE sqlc.arg(city)::text || '%'
  AND (t.departs_on, t.id) > (sqlc.arg(after_departs_on)::date, sqlc.arg(after_id)::uuid)
ORDER BY t.departs_on, t.id
LIMIT sqlc.arg(row_limit);

-- name: ListTripsByJastiper :many
SELECT sqlc.embed(t), u.name AS jastiper_name
FROM trips t
JOIN users u ON u.id = t.jastiper_id
WHERE t.jastiper_id = sqlc.arg(jastiper_id)
  AND (t.created_at, t.id) < (sqlc.arg(before_created_at)::timestamptz, sqlc.arg(before_id)::uuid)
ORDER BY t.created_at DESC, t.id DESC
LIMIT sqlc.arg(row_limit);

-- name: CloseTrip :execrows
UPDATE trips
SET status = 'closed', updated_at = now()
WHERE id = $1 AND status = 'open';
