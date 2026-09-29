-- name: InsertLedgerAccount :exec
INSERT INTO ledger_accounts (id, kind, user_id)
VALUES ($1, $2, $3)
ON CONFLICT (kind, user_id) DO NOTHING;

-- name: GetLedgerAccount :one
SELECT id FROM ledger_accounts
WHERE kind = $1 AND user_id IS NOT DISTINCT FROM $2;

-- name: InsertLedgerTransaction :execrows
INSERT INTO ledger_transactions (id, kind, order_id, idempotency_key, created_by)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (idempotency_key) DO NOTHING;

-- name: InsertLedgerEntry :exec
INSERT INTO ledger_entries (transaction_id, account_id, order_id, direction, amount)
VALUES ($1, $2, $3, $4, $5);

-- name: LedgerAccountBalance :one
SELECT COALESCE(SUM(CASE WHEN e.direction = 'credit' THEN e.amount ELSE -e.amount END), 0)::bigint AS balance
FROM ledger_entries e
JOIN ledger_accounts a ON a.id = e.account_id
WHERE a.kind = $1 AND a.user_id IS NOT DISTINCT FROM $2;

-- name: LedgerOrderBalance :one
SELECT COALESCE(SUM(CASE WHEN e.direction = 'credit' THEN e.amount ELSE -e.amount END), 0)::bigint AS balance
FROM ledger_entries e
JOIN ledger_accounts a ON a.id = e.account_id
WHERE a.kind = $1 AND e.order_id = $2;
