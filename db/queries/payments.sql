-- name: CreatePayment :exec
INSERT INTO payments (id, order_id, amount, provider, expires_at)
VALUES ($1, $2, $3, $4, $5);

-- name: SetPaymentCheckout :exec
UPDATE payments
SET checkout_token = $2, checkout_url = $3, updated_at = now()
WHERE id = $1;

-- name: GetPendingPaymentByOrder :one
SELECT * FROM payments
WHERE order_id = $1 AND status = 'pending';

-- name: LockPayment :one
SELECT * FROM payments
WHERE id = $1
FOR UPDATE;

-- name: MarkPaymentPaid :exec
UPDATE payments
SET status = 'paid', provider_transaction_id = $2, paid_at = sqlc.arg(paid_at)::timestamptz, updated_at = now()
WHERE id = $1;

-- name: MarkPaymentExpired :exec
UPDATE payments
SET status = 'expired', updated_at = now()
WHERE id = $1;

-- name: GetPaidPaymentByOrder :one
SELECT * FROM payments
WHERE order_id = $1 AND status = 'paid';
