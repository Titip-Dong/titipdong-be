-- name: CreateDispute :exec
INSERT INTO disputes (id, order_id, opened_by, category, description)
VALUES ($1, $2, $3, $4, $5);

-- name: GetDispute :one
SELECT sqlc.embed(d), o.code AS order_code, o.buyer_id, o.jastiper_id
FROM disputes d
JOIN orders o ON o.id = d.order_id
WHERE d.id = $1;

-- name: GetDisputeByOrder :one
SELECT sqlc.embed(d), o.code AS order_code, o.buyer_id, o.jastiper_id
FROM disputes d
JOIN orders o ON o.id = d.order_id
WHERE d.order_id = $1;

-- name: LockDispute :one
SELECT sqlc.embed(d), o.code AS order_code, o.buyer_id, o.jastiper_id
FROM disputes d
JOIN orders o ON o.id = d.order_id
WHERE d.id = $1
FOR UPDATE OF d;

-- name: ResolveDispute :execrows
UPDATE disputes
SET status = 'resolved', decision = sqlc.arg(decision)::text, decision_note = sqlc.arg(decision_note)::text,
    decided_by = sqlc.arg(decided_by)::uuid, decided_at = sqlc.arg(decided_at)::timestamptz, updated_at = now()
WHERE id = sqlc.arg(id) AND status = 'open';

-- name: ListDisputes :many
SELECT sqlc.embed(d), o.code AS order_code, o.buyer_id, o.jastiper_id
FROM disputes d
JOIN orders o ON o.id = d.order_id
WHERE d.status = sqlc.arg(status)::text
  AND (d.created_at, d.id) > (sqlc.arg(after_created_at)::timestamptz, sqlc.arg(after_id)::uuid)
ORDER BY d.created_at, d.id
LIMIT sqlc.arg(row_limit);

-- name: InsertDisputeEvidence :exec
INSERT INTO dispute_evidence (id, dispute_id, uploaded_by, file_key, note)
VALUES ($1, $2, $3, $4, $5);

-- name: ListDisputeEvidence :many
SELECT e.*, u.name AS uploader_name, u.role AS uploader_role
FROM dispute_evidence e
JOIN users u ON u.id = e.uploaded_by
WHERE e.dispute_id = $1
ORDER BY e.created_at, e.id;

-- name: InsertDisputeMessage :exec
INSERT INTO dispute_messages (dispute_id, author_id, body)
VALUES ($1, $2, $3);

-- name: ListDisputeMessages :many
SELECT m.*, u.name AS author_name, u.role AS author_role
FROM dispute_messages m
JOIN users u ON u.id = m.author_id
WHERE m.dispute_id = $1
ORDER BY m.id;
