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
