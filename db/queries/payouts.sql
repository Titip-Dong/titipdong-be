-- name: UpsertPayoutAccount :one
INSERT INTO payout_accounts (user_id, bank_name, account_number, account_name)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id) DO UPDATE
SET bank_name = EXCLUDED.bank_name, account_number = EXCLUDED.account_number,
    account_name = EXCLUDED.account_name, updated_at = now()
RETURNING *;

-- name: GetPayoutAccount :one
SELECT * FROM payout_accounts
WHERE user_id = $1;

-- name: LockPayoutAccount :one
SELECT * FROM payout_accounts
WHERE user_id = $1
FOR UPDATE;

-- name: SumRequestedPayouts :one
SELECT COALESCE(SUM(amount), 0)::bigint AS total
FROM payouts
WHERE jastiper_id = $1 AND status = 'requested';

-- name: CreatePayout :exec
INSERT INTO payouts (id, jastiper_id, amount, bank_name, account_number, account_name)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: GetPayout :one
SELECT sqlc.embed(p), u.name AS jastiper_name
FROM payouts p
JOIN users u ON u.id = p.jastiper_id
WHERE p.id = $1;

-- name: LockPayout :one
SELECT sqlc.embed(p), u.name AS jastiper_name
FROM payouts p
JOIN users u ON u.id = p.jastiper_id
WHERE p.id = $1
FOR UPDATE OF p;

-- name: ListPayoutsByJastiper :many
SELECT sqlc.embed(p), u.name AS jastiper_name
FROM payouts p
JOIN users u ON u.id = p.jastiper_id
WHERE p.jastiper_id = sqlc.arg(jastiper_id)
  AND (p.requested_at, p.id) < (sqlc.arg(before_requested_at)::timestamptz, sqlc.arg(before_id)::uuid)
ORDER BY p.requested_at DESC, p.id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListPayoutQueue :many
SELECT sqlc.embed(p), u.name AS jastiper_name
FROM payouts p
JOIN users u ON u.id = p.jastiper_id
WHERE (sqlc.arg(status)::text = '' OR p.status = sqlc.arg(status)::text)
  AND (p.requested_at, p.id) > (sqlc.arg(after_requested_at)::timestamptz, sqlc.arg(after_id)::uuid)
ORDER BY p.requested_at, p.id
LIMIT sqlc.arg(row_limit);

-- name: DecidePayout :execrows
UPDATE payouts
SET status = sqlc.arg(status)::text, reference = sqlc.narg(reference), rejection_reason = sqlc.narg(rejection_reason),
    decided_by = sqlc.arg(decided_by)::uuid, decided_at = sqlc.arg(decided_at)::timestamptz
WHERE id = sqlc.arg(id) AND status = 'requested';
