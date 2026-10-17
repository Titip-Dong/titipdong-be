// Package notification implements F07 Notification: in-app notifications written on the caller's transaction.
package notification

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/platform/pagination"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	TypeOrderRequested  = "order.requested"
	TypeOrderApproved   = "order.approved"
	TypeOrderRejected   = "order.rejected"
	TypeOrderCancelled  = "order.cancelled"
	TypePaymentReceived = "payment.received"
	TypeOrderShipped    = "order.shipped"
	TypeOrderReleased   = "order.released"
	TypeOrderRefunded   = "order.refunded"
	TypeDisputeOpened   = "dispute.opened"
	TypeDisputeMessage  = "dispute.message"
	TypeDisputeResolved = "dispute.resolved"
	TypePayoutPaid      = "payout.paid"
	TypePayoutRejected  = "payout.rejected"
)

var ErrNotFound = errors.New("notification not found")

type Event struct {
	Type       string
	Recipients []uuid.UUID
	Data       map[string]any
}

type Notification struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Type      string
	Data      map[string]any
	ReadAt    *time.Time
	CreatedAt time.Time
}

type ListParams struct {
	UnreadOnly bool
	Limit      int
	Cursor     string
}

type Page struct {
	Items      []Notification
	NextCursor string
}

type Service interface {
	// Notify inserts one row per distinct recipient on the transaction in ctx.
	Notify(ctx context.Context, e Event) error
	AdminIDs(ctx context.Context) ([]uuid.UUID, error)
	List(ctx context.Context, userID uuid.UUID, p ListParams) (Page, error)
	UnreadCount(ctx context.Context, userID uuid.UUID) (int64, error)
	MarkRead(ctx context.Context, userID, id uuid.UUID) error
	MarkAllRead(ctx context.Context, userID uuid.UUID) error
}

func NewService(pool *pgxpool.Pool) Service {
	return newService(newRepository(pool), time.Now)
}

type service struct {
	repo repository
	now  func() time.Time
}

func newService(repo repository, now func() time.Time) *service {
	return &service{repo: repo, now: now}
}

func (s *service) Notify(ctx context.Context, e Event) error {
	if e.Type == "" {
		return errors.New("notification type is required")
	}
	if e.Data == nil {
		e.Data = map[string]any{}
	}
	var seen []uuid.UUID
	for _, userID := range e.Recipients {
		if userID == uuid.Nil || slices.Contains(seen, userID) {
			continue
		}
		seen = append(seen, userID)
		err := s.repo.Insert(ctx, Notification{
			ID:     uuid.Must(uuid.NewV7()),
			UserID: userID,
			Type:   e.Type,
			Data:   e.Data,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *service) AdminIDs(ctx context.Context) ([]uuid.UUID, error) {
	return s.repo.AdminIDs(ctx)
}

func (s *service) List(ctx context.Context, userID uuid.UUID, p ListParams) (Page, error) {
	before, err := pagination.Decode(p.Cursor, pagination.Newest)
	if err != nil {
		return Page{}, err
	}
	rows, err := s.repo.List(ctx, userID, p.UnreadOnly, before, p.Limit+1)
	if err != nil {
		return Page{}, err
	}
	items, next := pagination.Page(rows, p.Limit, func(n Notification) pagination.Cursor {
		return pagination.Cursor{At: n.CreatedAt, ID: n.ID}
	})
	return Page{Items: items, NextCursor: next}, nil
}

func (s *service) UnreadCount(ctx context.Context, userID uuid.UUID) (int64, error) {
	return s.repo.UnreadCount(ctx, userID)
}

func (s *service) MarkRead(ctx context.Context, userID, id uuid.UUID) error {
	found, err := s.repo.MarkRead(ctx, userID, id, s.now())
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	return nil
}

func (s *service) MarkAllRead(ctx context.Context, userID uuid.UUID) error {
	return s.repo.MarkAllRead(ctx, userID, s.now())
}
