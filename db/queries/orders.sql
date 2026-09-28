-- name: CreateOrder :exec
INSERT INTO orders (id, code, trip_id, buyer_id, jastiper_id, product_name, product_url, quantity, notes)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: GetOrder :one
SELECT sqlc.embed(o), t.slug AS trip_slug, t.title AS trip_title, b.name AS buyer_name, j.name AS jastiper_name
FROM orders o
JOIN trips t ON t.id = o.trip_id
JOIN users b ON b.id = o.buyer_id
JOIN users j ON j.id = o.jastiper_id
WHERE o.id = $1;

-- name: LockOrder :one
SELECT sqlc.embed(o), t.slug AS trip_slug, t.title AS trip_title, b.name AS buyer_name, j.name AS jastiper_name
FROM orders o
JOIN trips t ON t.id = o.trip_id
JOIN users b ON b.id = o.buyer_id
JOIN users j ON j.id = o.jastiper_id
WHERE o.id = $1
FOR UPDATE OF o;

-- name: ListOrders :many
SELECT sqlc.embed(o), t.slug AS trip_slug, t.title AS trip_title, b.name AS buyer_name, j.name AS jastiper_name
FROM orders o
JOIN trips t ON t.id = o.trip_id
JOIN users b ON b.id = o.buyer_id
JOIN users j ON j.id = o.jastiper_id
WHERE (o.buyer_id = sqlc.arg(user_id)::uuid OR o.jastiper_id = sqlc.arg(user_id)::uuid)
  AND (sqlc.arg(status)::text = '' OR o.status = sqlc.arg(status)::text)
  AND (sqlc.narg(trip_id)::uuid IS NULL OR o.trip_id = sqlc.narg(trip_id)::uuid)
  AND (o.created_at, o.id) < (sqlc.arg(before_created_at)::timestamptz, sqlc.arg(before_id)::uuid)
ORDER BY o.created_at DESC, o.id DESC
LIMIT sqlc.arg(row_limit);

-- name: UpdateOrderStatus :exec
UPDATE orders
SET status = $2, status_reason = $3, item_price = $4, jastip_fee = $5, total = $6,
    payment_deadline = $7, updated_at = now()
WHERE id = $1;

-- name: InsertOrderEvent :exec
INSERT INTO order_events (order_id, from_status, to_status, actor_id, actor_type, reason)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListOrderEvents :many
SELECT from_status, to_status, actor_id, actor_type, reason, created_at
FROM order_events
WHERE order_id = $1
ORDER BY id;

-- name: ExpireUnpaidOrders :exec
WITH expired AS (
    UPDATE orders
    SET status = 'cancelled', status_reason = 'Payment deadline passed.', updated_at = now()
    WHERE status = 'approved' AND payment_deadline <= sqlc.arg(now)::timestamptz
    RETURNING id
)
INSERT INTO order_events (order_id, from_status, to_status, actor_type, reason)
SELECT id, 'approved', 'cancelled', 'system', 'Payment deadline passed.'
FROM expired;
