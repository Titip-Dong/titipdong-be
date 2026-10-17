-- name: CreateTrip :exec
INSERT INTO trips (
    id, jastiper_id, slug, title, description, destination_city, destination_country,
    departs_on, returns_on, request_deadline, capacity
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

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
  AND t.reserved_capacity < t.capacity
  AND t.destination_city ILIKE sqlc.arg(city)::text || '%'
  AND (sqlc.narg(departs_from)::date IS NULL OR t.departs_on >= sqlc.narg(departs_from)::date)
  AND (sqlc.narg(departs_to)::date IS NULL OR t.departs_on <= sqlc.narg(departs_to)::date)
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

-- name: LockTrip :one
SELECT sqlc.embed(t), u.name AS jastiper_name
FROM trips t
JOIN users u ON u.id = t.jastiper_id
WHERE t.id = $1
FOR UPDATE OF t;

-- name: AdjustTripReservedCapacity :exec
UPDATE trips
SET reserved_capacity = reserved_capacity + sqlc.arg(units)::integer, updated_at = now()
WHERE id = sqlc.arg(id);

-- name: CloseExpiredTrips :execrows
UPDATE trips
SET status = 'closed', updated_at = now()
WHERE status = 'open' AND request_deadline <= sqlc.arg(now)::timestamptz;

-- name: CompleteReturnedTrips :execrows
UPDATE trips t
SET status = 'completed', updated_at = now()
WHERE t.status = 'closed'
  AND t.returns_on < sqlc.arg(today)::date
  AND NOT EXISTS (
      SELECT 1 FROM orders o
      WHERE o.trip_id = t.id
        AND o.status IN ('requested', 'approved', 'paid', 'shipped', 'delivered', 'disputed')
  );
