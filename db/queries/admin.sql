-- name: CountUsersByRoleAndStatus :many
SELECT role, status, count(*) AS users
FROM users
GROUP BY role, status
ORDER BY role, status;

-- name: OrderTotalsByStatus :many
SELECT status, count(*) AS orders, COALESCE(SUM(total), 0)::bigint AS total
FROM orders
GROUP BY status
ORDER BY status;

-- name: CountOpenDisputes :one
SELECT count(*) FROM disputes
WHERE status = 'open';

-- name: RequestedPayoutsSummary :one
SELECT count(*) AS payouts, COALESCE(SUM(amount), 0)::bigint AS amount
FROM payouts
WHERE status = 'requested';

-- name: ListAdminOrders :many
SELECT sqlc.embed(o), t.slug AS trip_slug, t.title AS trip_title, b.name AS buyer_name, j.name AS jastiper_name
FROM orders o
JOIN trips t ON t.id = o.trip_id
JOIN users b ON b.id = o.buyer_id
JOIN users j ON j.id = o.jastiper_id
WHERE (sqlc.arg(status)::text = '' OR o.status = sqlc.arg(status)::text)
  AND (sqlc.arg(code)::text = '' OR o.code ILIKE '%' || sqlc.arg(code)::text || '%')
  AND (o.created_at, o.id) < (sqlc.arg(before_created_at)::timestamptz, sqlc.arg(before_id)::uuid)
ORDER BY o.created_at DESC, o.id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListLedgerTransactions :many
SELECT t.*, o.code AS order_code
FROM ledger_transactions t
LEFT JOIN orders o ON o.id = t.order_id
WHERE (sqlc.arg(kind)::text = '' OR t.kind = sqlc.arg(kind)::text)
  AND (t.created_at, t.id) < (sqlc.arg(before_created_at)::timestamptz, sqlc.arg(before_id)::uuid)
ORDER BY t.created_at DESC, t.id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListLedgerEntriesForTransactions :many
SELECT e.transaction_id, e.direction, e.amount, a.kind AS account_kind, a.user_id AS account_user_id
FROM ledger_entries e
JOIN ledger_accounts a ON a.id = e.account_id
WHERE e.transaction_id = ANY(sqlc.arg(transaction_ids)::uuid[])
ORDER BY e.id;

-- name: ListAdminUsers :many
SELECT id, name, email, role, status, phone, created_at
FROM users
WHERE (sqlc.arg(role)::text = '' OR role = sqlc.arg(role)::text)
  AND (sqlc.arg(status)::text = '' OR status = sqlc.arg(status)::text)
  AND (sqlc.arg(search)::text = '' OR name ILIKE '%' || sqlc.arg(search)::text || '%' OR email ILIKE '%' || sqlc.arg(search)::text || '%')
  AND (created_at, id) < (sqlc.arg(before_created_at)::timestamptz, sqlc.arg(before_id)::uuid)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListAuditLogs :many
SELECT l.*, u.name AS actor_name
FROM audit_logs l
LEFT JOIN users u ON u.id = l.actor_id
WHERE (sqlc.arg(target_type)::text = '' OR l.target_type = sqlc.arg(target_type)::text)
  AND (sqlc.arg(target_id)::text = '' OR l.target_id = sqlc.arg(target_id)::text)
  AND (sqlc.narg(actor_id)::uuid IS NULL OR l.actor_id = sqlc.narg(actor_id)::uuid)
  AND l.id < sqlc.arg(before_id)::bigint
ORDER BY l.id DESC
LIMIT sqlc.arg(row_limit);
