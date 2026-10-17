-- name: CreateShipment :exec
INSERT INTO shipments (order_id, method, courier_code, tracking_number, handover_note, shipped_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: GetShipment :one
SELECT * FROM shipments
WHERE order_id = $1;

-- name: MarkShipmentDelivered :exec
UPDATE shipments
SET delivered_at = sqlc.arg(delivered_at)::timestamptz, updated_at = now()
WHERE order_id = $1;

-- name: ListShipmentsDueForRelease :many
SELECT s.order_id
FROM shipments s
JOIN orders o ON o.id = s.order_id
WHERE s.delivered_at IS NULL
  AND s.shipped_at <= sqlc.arg(cutoff)::timestamptz
  AND o.status = 'shipped'
ORDER BY s.shipped_at, s.order_id
LIMIT sqlc.arg(row_limit);
