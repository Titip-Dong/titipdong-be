package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/db"
	"github.com/Titip-Dong/titipdong-be/internal/platform/database"
	"github.com/Titip-Dong/titipdong-be/internal/platform/pagination"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type repository interface {
	Insert(ctx context.Context, n Notification) error
	AdminIDs(ctx context.Context) ([]uuid.UUID, error)
	List(ctx context.Context, userID uuid.UUID, unreadOnly bool, before pagination.Cursor, limit int) ([]Notification, error)
	UnreadCount(ctx context.Context, userID uuid.UUID) (int64, error)
	// MarkRead reports false when the notification does not exist or belongs to someone else.
	MarkRead(ctx context.Context, userID, id uuid.UUID, at time.Time) (bool, error)
	MarkAllRead(ctx context.Context, userID uuid.UUID, at time.Time) error
}

type pgRepository struct {
	pool *pgxpool.Pool
}

func newRepository(pool *pgxpool.Pool) *pgRepository {
	return &pgRepository{pool: pool}
}

func (r *pgRepository) q(ctx context.Context) *db.Queries {
	return db.New(database.Conn(ctx, r.pool))
}

func (r *pgRepository) Insert(ctx context.Context, n Notification) error {
	data, err := json.Marshal(n.Data)
	if err != nil {
		return fmt.Errorf("encode notification data: %w", err)
	}
	return r.q(ctx).InsertNotification(ctx, db.InsertNotificationParams{
		ID:     n.ID,
		UserID: n.UserID,
		Type:   n.Type,
		Data:   data,
	})
}

func (r *pgRepository) AdminIDs(ctx context.Context) ([]uuid.UUID, error) {
	return r.q(ctx).ListAdminIDs(ctx)
}

func (r *pgRepository) List(ctx context.Context, userID uuid.UUID, unreadOnly bool, before pagination.Cursor, limit int) ([]Notification, error) {
	rows, err := r.q(ctx).ListNotifications(ctx, db.ListNotificationsParams{
		UserID:          userID,
		UnreadOnly:      unreadOnly,
		BeforeCreatedAt: before.At,
		BeforeID:        before.ID,
		RowLimit:        int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Notification, len(rows))
	for i, row := range rows {
		if out[i], err = toNotification(row); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r *pgRepository) UnreadCount(ctx context.Context, userID uuid.UUID) (int64, error) {
	return r.q(ctx).CountUnreadNotifications(ctx, userID)
}

func (r *pgRepository) MarkRead(ctx context.Context, userID, id uuid.UUID, at time.Time) (bool, error) {
	n, err := r.q(ctx).MarkNotificationRead(ctx, db.MarkNotificationReadParams{ReadAt: at, ID: id, UserID: userID})
	return n == 1, err
}

func (r *pgRepository) MarkAllRead(ctx context.Context, userID uuid.UUID, at time.Time) error {
	return r.q(ctx).MarkAllNotificationsRead(ctx, db.MarkAllNotificationsReadParams{ReadAt: at, UserID: userID})
}

func toNotification(n db.Notification) (Notification, error) {
	var data map[string]any
	if err := json.Unmarshal(n.Data, &data); err != nil {
		return Notification{}, fmt.Errorf("decode notification data: %w", err)
	}
	return Notification{
		ID:        n.ID,
		UserID:    n.UserID,
		Type:      n.Type,
		Data:      data,
		ReadAt:    n.ReadAt,
		CreatedAt: n.CreatedAt,
	}, nil
}
