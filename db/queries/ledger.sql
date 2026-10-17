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

-- name: LedgerBalancesByKind :many
SELECT a.kind,
       COALESCE(SUM(CASE WHEN e.direction = 'credit' THEN e.amount ELSE -e.amount END), 0)::bigint AS balance
FROM ledger_accounts a
LEFT JOIN ledger_entries e ON e.account_id = a.id
GROUP BY a.kind;

-- name: JastiperEscrowHeld :one
SELECT COALESCE(SUM(CASE WHEN e.direction = 'credit' THEN e.amount ELSE -e.amount END), 0)::bigint AS held
FROM ledger_entries e
JOIN ledger_accounts a ON a.id = e.account_id
JOIN orders o ON o.id = e.order_id
WHERE a.kind = 'escrow' AND o.jastiper_id = $1;

-- name: ListUserAccountEntries :many
SELECT e.id, e.direction, e.amount, e.order_id, e.created_at, t.kind AS transaction_kind, t.idempotency_key,
       o.code AS order_code
FROM ledger_entries e
JOIN ledger_accounts a ON a.id = e.account_id
JOIN ledger_transactions t ON t.id = e.transaction_id
LEFT JOIN orders o ON o.id = e.order_id
WHERE a.kind = sqlc.arg(kind)::text AND a.user_id = sqlc.arg(user_id)::uuid AND e.id < sqlc.arg(before_id)::bigint
ORDER BY e.id DESC
LIMIT sqlc.arg(row_limit);
