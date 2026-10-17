// Package admin implements F08 Admin: dashboard figures and back-office lists. It reads with its own queries and
// writes only through other modules' services.
package admin

import (
	"context"
	"strings"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/payment"
	"github.com/Titip-Dong/titipdong-be/internal/platform/audit"
	"github.com/Titip-Dong/titipdong-be/internal/platform/pagination"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// gmvStatuses are the orders that were paid and not refunded.
var gmvStatuses = map[string]bool{"paid": true, "shipped": true, "delivered": true, "disputed": true, "released": true}

type UserCount struct {
	Role   string
	Status string
	Count  int64
}

type OrderCount struct {
	Status string
	Count  int64
	Total  int64
}

type Stats struct {
	Users               []UserCount
	Orders              []OrderCount
	GMV                 int64
	Ledger              payment.Totals
	OpenDisputes        int64
	PendingPayouts      int64
	PendingPayoutAmount int64
}

type Order struct {
	ID           uuid.UUID
	Code         string
	Status       string
	ProductName  string
	Quantity     int
	Total        *int64
	TripID       uuid.UUID
	TripSlug     string
	TripTitle    string
	BuyerID      uuid.UUID
	BuyerName    string
	JastiperID   uuid.UUID
	JastiperName string
	CreatedAt    time.Time
}

type Entry struct {
	Account   string
	UserID    *uuid.UUID
	Direction string
	Amount    int64
}

type Transaction struct {
	ID        uuid.UUID
	Kind      string
	OrderID   *uuid.UUID
	OrderCode string
	CreatedBy *uuid.UUID
	CreatedAt time.Time
	Entries   []Entry
}

type User struct {
	ID        uuid.UUID
	Name      string
	Email     string
	Role      string
	Status    string
	Phone     string
	CreatedAt time.Time
}

type AuditLog struct {
	ID         int64
	ActorID    *uuid.UUID
	ActorName  string
	ActorType  string
	Action     string
	TargetType string
	TargetID   string
	Metadata   map[string]any
	RequestID  string
	CreatedAt  time.Time
}

type Page[T any] struct {
	Items      []T
	NextCursor string
}

type OrderQuery struct {
	Status string
	Code   string
	Limit  int
	Cursor string
}

type UserQuery struct {
	Role   string
	Status string
	Search string
	Limit  int
	Cursor string
}

type AuditQuery struct {
	TargetType string
	TargetID   string
	ActorID    *uuid.UUID
	Limit      int
	Cursor     string
}

type Service interface {
	Stats(ctx context.Context) (Stats, error)
	Orders(ctx context.Context, q OrderQuery) (Page[Order], error)
	Transactions(ctx context.Context, kind string, limit int, cursor string) (Page[Transaction], error)
	Users(ctx context.Context, q UserQuery) (Page[User], error)
	// SetUserStatus suspends or reactivates a user through auth and records it in the audit log.
	SetUserStatus(ctx context.Context, adminID, userID uuid.UUID, status auth.Status) (User, error)
	AuditLogs(ctx context.Context, q AuditQuery) (Page[AuditLog], error)
}

type auditWriter interface {
	Write(ctx context.Context, e audit.Entry) error
}

func NewService(pool *pgxpool.Pool, users auth.Service, payments payment.Service) Service {
	return newService(newRepository(pool), users, payments, audit.NewWriter(pool))
}

type service struct {
	repo     repository
	users    auth.Service
	payments payment.Service
	audit    auditWriter
}

func newService(repo repository, users auth.Service, payments payment.Service, aw auditWriter) *service {
	return &service{repo: repo, users: users, payments: payments, audit: aw}
}

func (s *service) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	var err error
	if st.Users, err = s.repo.UserCounts(ctx); err != nil {
		return Stats{}, err
	}
	if st.Orders, err = s.repo.OrderCounts(ctx); err != nil {
		return Stats{}, err
	}
	for _, o := range st.Orders {
		if gmvStatuses[o.Status] {
			st.GMV += o.Total
		}
	}
	if st.Ledger, err = s.payments.PlatformTotals(ctx); err != nil {
		return Stats{}, err
	}
	if st.OpenDisputes, err = s.repo.OpenDisputes(ctx); err != nil {
		return Stats{}, err
	}
	st.PendingPayouts, st.PendingPayoutAmount, err = s.repo.RequestedPayouts(ctx)
	return st, err
}

func (s *service) Orders(ctx context.Context, q OrderQuery) (Page[Order], error) {
	before, err := pagination.Decode(q.Cursor, pagination.Newest)
	if err != nil {
		return Page[Order]{}, err
	}
	rows, err := s.repo.Orders(ctx, orderFilter{
		Status: q.Status, Code: escapeLike(strings.ToUpper(strings.TrimSpace(q.Code))), Before: before, Limit: q.Limit + 1,
	})
	if err != nil {
		return Page[Order]{}, err
	}
	items, next := pagination.Page(rows, q.Limit, func(o Order) pagination.Cursor {
		return pagination.Cursor{At: o.CreatedAt, ID: o.ID}
	})
	return Page[Order]{Items: items, NextCursor: next}, nil
}

func (s *service) Transactions(ctx context.Context, kind string, limit int, cursor string) (Page[Transaction], error) {
	before, err := pagination.Decode(cursor, pagination.Newest)
	if err != nil {
		return Page[Transaction]{}, err
	}
	rows, err := s.repo.Transactions(ctx, kind, before, limit+1)
	if err != nil {
		return Page[Transaction]{}, err
	}
	items, next := pagination.Page(rows, limit, func(t Transaction) pagination.Cursor {
		return pagination.Cursor{At: t.CreatedAt, ID: t.ID}
	})
	return Page[Transaction]{Items: items, NextCursor: next}, nil
}

func (s *service) Users(ctx context.Context, q UserQuery) (Page[User], error) {
	before, err := pagination.Decode(q.Cursor, pagination.Newest)
	if err != nil {
		return Page[User]{}, err
	}
	rows, err := s.repo.Users(ctx, userFilter{
		Role: q.Role, Status: q.Status, Search: escapeLike(strings.TrimSpace(q.Search)), Before: before, Limit: q.Limit + 1,
	})
	if err != nil {
		return Page[User]{}, err
	}
	items, next := pagination.Page(rows, q.Limit, func(u User) pagination.Cursor {
		return pagination.Cursor{At: u.CreatedAt, ID: u.ID}
	})
	return Page[User]{Items: items, NextCursor: next}, nil
}

func (s *service) SetUserStatus(ctx context.Context, adminID, userID uuid.UUID, status auth.Status) (User, error) {
	var out User
	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		u, err := s.users.SetStatus(ctx, userID, status)
		if err != nil {
			return err
		}
		action := "user.suspended"
		if status == auth.StatusActive {
			action = "user.activated"
		}
		out = User{
			ID: u.ID, Name: u.Name, Email: u.Email, Role: string(u.Role), Status: string(u.Status), Phone: u.Phone,
			CreatedAt: u.CreatedAt,
		}
		return s.audit.Write(ctx, audit.Entry{
			ActorID:    &adminID,
			ActorType:  string(auth.RoleAdmin),
			Action:     action,
			TargetType: "user",
			TargetID:   userID.String(),
			Metadata:   map[string]any{"role": u.Role, "status": u.Status},
		})
	})
	return out, err
}

func (s *service) AuditLogs(ctx context.Context, q AuditQuery) (Page[AuditLog], error) {
	before, err := pagination.DecodeID(q.Cursor)
	if err != nil {
		return Page[AuditLog]{}, err
	}
	rows, err := s.repo.AuditLogs(ctx, auditFilter{
		TargetType: q.TargetType, TargetID: q.TargetID, ActorID: q.ActorID, BeforeID: before, Limit: q.Limit + 1,
	})
	if err != nil {
		return Page[AuditLog]{}, err
	}
	if len(rows) <= q.Limit {
		return Page[AuditLog]{Items: rows}, nil
	}
	rows = rows[:q.Limit]
	return Page[AuditLog]{Items: rows, NextCursor: pagination.EncodeID(rows[len(rows)-1].ID)}, nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
