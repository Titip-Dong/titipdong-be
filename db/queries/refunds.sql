-- name: CreateRefund :exec
INSERT INTO refunds (id, order_id, payment_id, amount, reason)
VALUES ($1, $2, $3, $4, $5);

-- name: MarkRefundSucceeded :exec
UPDATE refunds
SET status = 'succeeded', provider_refund_id = $2, completed_at = sqlc.arg(completed_at)::timestamptz
WHERE id = $1;

-- name: MarkRefundFailed :exec
UPDATE refunds
SET status = 'failed', failure_reason = $2, completed_at = sqlc.arg(completed_at)::timestamptz
WHERE id = $1;
