-- name: InsertAuditLog :exec
INSERT INTO audit_logs (actor_id, actor_type, action, target_type, target_id, metadata, request_id)
VALUES ($1, $2, $3, $4, $5, $6, $7);
