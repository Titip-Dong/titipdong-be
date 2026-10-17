package admin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Titip-Dong/titipdong-be/internal/db"
	"github.com/Titip-Dong/titipdong-be/internal/platform/database"
	"github.com/Titip-Dong/titipdong-be/internal/platform/pagination"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type orderFilter struct {
	Status string
	Code   string
	Before pagination.Cursor
	Limit  int
}

type userFilter struct {
	Role   string
	Status string
	Search string
	Before pagination.Cursor
	Limit  int
}

type auditFilter struct {
	TargetType string
	TargetID   string
	ActorID    *uuid.UUID
	BeforeID   int64
	Limit      int
}

// repository only reads; admin writes through other modules' services.
type repository interface {
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error
	UserCounts(ctx context.Context) ([]UserCount, error)
	OrderCounts(ctx context.Context) ([]OrderCount, error)
	OpenDisputes(ctx context.Context) (int64, error)
	RequestedPayouts(ctx context.Context) (count, amount int64, err error)
	Orders(ctx context.Context, f orderFilter) ([]Order, error)
	Transactions(ctx context.Context, kind string, before pagination.Cursor, limit int) ([]Transaction, error)
	Users(ctx context.Context, f userFilter) ([]User, error)
	AuditLogs(ctx context.Context, f auditFilter) ([]AuditLog, error)
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

func (r *pgRepository) WithTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return database.WithTx(ctx, r.pool, fn)
}

func (r *pgRepository) UserCounts(ctx context.Context) ([]UserCount, error) {
	rows, err := r.q(ctx).CountUsersByRoleAndStatus(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]UserCount, len(rows))
	for i, row := range rows {
		out[i] = UserCount{Role: row.Role, Status: row.Status, Count: row.Users}
	}
	return out, nil
}

func (r *pgRepository) OrderCounts(ctx context.Context) ([]OrderCount, error) {
	rows, err := r.q(ctx).OrderTotalsByStatus(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]OrderCount, len(rows))
	for i, row := range rows {
		out[i] = OrderCount{Status: row.Status, Count: row.Orders, Total: row.Total}
	}
	return out, nil
}

func (r *pgRepository) OpenDisputes(ctx context.Context) (int64, error) {
	return r.q(ctx).CountOpenDisputes(ctx)
}

func (r *pgRepository) RequestedPayouts(ctx context.Context) (int64, int64, error) {
	row, err := r.q(ctx).RequestedPayoutsSummary(ctx)
	return row.Payouts, row.Amount, err
}

func (r *pgRepository) Orders(ctx context.Context, f orderFilter) ([]Order, error) {
	rows, err := r.q(ctx).ListAdminOrders(ctx, db.ListAdminOrdersParams{
		Status:          f.Status,
		Code:            f.Code,
		BeforeCreatedAt: f.Before.At,
		BeforeID:        f.Before.ID,
		RowLimit:        int32(f.Limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Order, len(rows))
	for i, row := range rows {
		o := row.Order
		out[i] = Order{
			ID: o.ID, Code: o.Code, Status: o.Status, ProductName: o.ProductName, Quantity: int(o.Quantity), Total: o.Total,
			TripID: o.TripID, TripSlug: row.TripSlug, TripTitle: row.TripTitle,
			BuyerID: o.BuyerID, BuyerName: row.BuyerName, JastiperID: o.JastiperID, JastiperName: row.JastiperName,
			CreatedAt: o.CreatedAt,
		}
	}
	return out, nil
}

func (r *pgRepository) Transactions(ctx context.Context, kind string, before pagination.Cursor, limit int) ([]Transaction, error) {
	q := r.q(ctx)
	rows, err := q.ListLedgerTransactions(ctx, db.ListLedgerTransactionsParams{
		Kind:            kind,
		BeforeCreatedAt: before.At,
		BeforeID:        before.ID,
		RowLimit:        int32(limit),
	})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	out := make([]Transaction, len(rows))
	index := make(map[uuid.UUID]int, len(rows))
	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		out[i] = Transaction{
			ID: row.ID, Kind: row.Kind, OrderID: row.OrderID, OrderCode: deref(row.OrderCode),
			CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt,
		}
		index[row.ID], ids[i] = i, row.ID
	}
	entries, err := q.ListLedgerEntriesForTransactions(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		t := &out[index[e.TransactionID]]
		t.Entries = append(t.Entries, Entry{
			Account: e.AccountKind, UserID: e.AccountUserID, Direction: e.Direction, Amount: e.Amount,
		})
	}
	return out, nil
}

func (r *pgRepository) Users(ctx context.Context, f userFilter) ([]User, error) {
	rows, err := r.q(ctx).ListAdminUsers(ctx, db.ListAdminUsersParams{
		Role:            f.Role,
		Status:          f.Status,
		Search:          f.Search,
		BeforeCreatedAt: f.Before.At,
		BeforeID:        f.Before.ID,
		RowLimit:        int32(f.Limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]User, len(rows))
	for i, row := range rows {
		out[i] = User{
			ID: row.ID, Name: row.Name, Email: row.Email, Role: row.Role, Status: row.Status,
			Phone: deref(row.Phone), CreatedAt: row.CreatedAt,
		}
	}
	return out, nil
}

func (r *pgRepository) AuditLogs(ctx context.Context, f auditFilter) ([]AuditLog, error) {
	rows, err := r.q(ctx).ListAuditLogs(ctx, db.ListAuditLogsParams{
		TargetType: f.TargetType,
		TargetID:   f.TargetID,
		ActorID:    f.ActorID,
		BeforeID:   f.BeforeID,
		RowLimit:   int32(f.Limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]AuditLog, len(rows))
	for i, row := range rows {
		var meta map[string]any
		if err := json.Unmarshal(row.Metadata, &meta); err != nil {
			return nil, fmt.Errorf("decode audit metadata: %w", err)
		}
		out[i] = AuditLog{
			ID: row.ID, ActorID: row.ActorID, ActorName: row.ActorName.String, ActorType: row.ActorType,
			Action: row.Action, TargetType: row.TargetType, TargetID: row.TargetID, Metadata: meta,
			RequestID: deref(row.RequestID), CreatedAt: row.CreatedAt,
		}
	}
	return out, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
