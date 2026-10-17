-- name: InsertNotification :exec
INSERT INTO notifications (id, user_id, type, data)
VALUES ($1, $2, $3, $4);

-- name: ListNotifications :many
SELECT * FROM notifications
WHERE user_id = sqlc.arg(user_id)
  AND (NOT sqlc.arg(unread_only)::bool OR read_at IS NULL)
  AND (created_at, id) < (sqlc.arg(before_created_at)::timestamptz, sqlc.arg(before_id)::uuid)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: CountUnreadNotifications :one
SELECT count(*) FROM notifications
WHERE user_id = $1 AND read_at IS NULL;

-- name: MarkNotificationRead :execrows
UPDATE notifications
SET read_at = COALESCE(read_at, sqlc.arg(read_at)::timestamptz)
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id);

-- name: MarkAllNotificationsRead :exec
UPDATE notifications
SET read_at = sqlc.arg(read_at)::timestamptz
WHERE user_id = sqlc.arg(user_id) AND read_at IS NULL;
