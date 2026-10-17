package order

import (
	"context"
	"errors"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/db"
	"github.com/Titip-Dong/titipdong-be/internal/platform/database"
	"github.com/Titip-Dong/titipdong-be/internal/platform/pagination"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type repository interface {
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error
	// CreateOrder returns errCodeTaken on a code collision.
	CreateOrder(ctx context.Context, o Order) error
	Order(ctx context.Context, id uuid.UUID) (Order, error)
	Lock(ctx context.Context, id uuid.UUID) (Order, error)
	List(ctx context.Context, userID uuid.UUID, status Status, tripID *uuid.UUID, before pagination.Cursor, limit int) ([]Order, error)
	UpdateStatus(ctx context.Context, o Order) error
	InsertEvent(ctx context.Context, orderID uuid.UUID, e Event) error
	Events(ctx context.Context, orderID uuid.UUID) ([]Event, error)
	// DueUnpaid returns approved orders whose payment deadline is at or before now, oldest deadline first.
	DueUnpaid(ctx context.Context, now time.Time, limit int) ([]uuid.UUID, error)
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

func (r *pgRepository) CreateOrder(ctx context.Context, o Order) error {
	err := r.q(ctx).CreateOrder(ctx, db.CreateOrderParams{
		ID:          o.ID,
		Code:        o.Code,
		TripID:      o.TripID,
		BuyerID:     o.BuyerID,
		JastiperID:  o.JastiperID,
		ProductName: o.ProductName,
		ProductUrl:  o.ProductURL,
		Quantity:    int32(o.Quantity),
		Notes:       o.Notes,
		PhotoKey:    nonEmpty(o.PhotoKey),
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "orders_code_key" {
		return errCodeTaken
	}
	return err
}

func (r *pgRepository) Order(ctx context.Context, id uuid.UUID) (Order, error) {
	row, err := r.q(ctx).GetOrder(ctx, id)
	if err != nil {
		return Order{}, notFound(err)
	}
	return toOrder(row.Order, row.TripSlug, row.TripTitle, row.BuyerName, row.JastiperName), nil
}

func (r *pgRepository) Lock(ctx context.Context, id uuid.UUID) (Order, error) {
	row, err := r.q(ctx).LockOrder(ctx, id)
	if err != nil {
		return Order{}, notFound(err)
	}
	return toOrder(row.Order, row.TripSlug, row.TripTitle, row.BuyerName, row.JastiperName), nil
}

func (r *pgRepository) List(ctx context.Context, userID uuid.UUID, status Status, tripID *uuid.UUID, before pagination.Cursor, limit int) ([]Order, error) {
	rows, err := r.q(ctx).ListOrders(ctx, db.ListOrdersParams{
		UserID:          userID,
		Status:          string(status),
		TripID:          tripID,
		BeforeCreatedAt: before.At,
		BeforeID:        before.ID,
		RowLimit:        int32(limit),
	})
	if err != nil {
		return nil, err
	}
	orders := make([]Order, len(rows))
	for i, row := range rows {
		orders[i] = toOrder(row.Order, row.TripSlug, row.TripTitle, row.BuyerName, row.JastiperName)
	}
	return orders, nil
}

func (r *pgRepository) UpdateStatus(ctx context.Context, o Order) error {
	return r.q(ctx).UpdateOrderStatus(ctx, db.UpdateOrderStatusParams{
		ID:              o.ID,
		Status:          string(o.Status),
		StatusReason:    o.StatusReason,
		ItemPrice:       o.ItemPrice,
		JastipFee:       o.JastipFee,
		Total:           o.Total,
		PaymentDeadline: o.PaymentDeadline,
	})
}

func (r *pgRepository) InsertEvent(ctx context.Context, orderID uuid.UUID, e Event) error {
	var from *string
	if e.FromStatus != nil {
		s := string(*e.FromStatus)
		from = &s
	}
	return r.q(ctx).InsertOrderEvent(ctx, db.InsertOrderEventParams{
		OrderID:    orderID,
		FromStatus: from,
		ToStatus:   string(e.ToStatus),
		ActorID:    e.ActorID,
		ActorType:  string(e.ActorType),
		Reason:     e.Reason,
	})
}

func (r *pgRepository) Events(ctx context.Context, orderID uuid.UUID) ([]Event, error) {
	rows, err := r.q(ctx).ListOrderEvents(ctx, orderID)
	if err != nil {
		return nil, err
	}
	events := make([]Event, len(rows))
	for i, row := range rows {
		events[i] = Event{
			ToStatus:  Status(row.ToStatus),
			ActorID:   row.ActorID,
			ActorType: ActorType(row.ActorType),
			Reason:    row.Reason,
			CreatedAt: row.CreatedAt,
		}
		if row.FromStatus != nil {
			from := Status(*row.FromStatus)
			events[i].FromStatus = &from
		}
	}
	return events, nil
}

func (r *pgRepository) DueUnpaid(ctx context.Context, now time.Time, limit int) ([]uuid.UUID, error) {
	return r.q(ctx).ListUnpaidPastDeadline(ctx, db.ListUnpaidPastDeadlineParams{Now: now, RowLimit: int32(limit)})
}

func toOrder(o db.Order, tripSlug, tripTitle, buyerName, jastiperName string) Order {
	return Order{
		ID:              o.ID,
		Code:            o.Code,
		TripID:          o.TripID,
		TripSlug:        tripSlug,
		TripTitle:       tripTitle,
		BuyerID:         o.BuyerID,
		BuyerName:       buyerName,
		JastiperID:      o.JastiperID,
		JastiperName:    jastiperName,
		ProductName:     o.ProductName,
		ProductURL:      o.ProductUrl,
		Quantity:        int(o.Quantity),
		Notes:           o.Notes,
		ItemPrice:       o.ItemPrice,
		JastipFee:       o.JastipFee,
		Total:           o.Total,
		Status:          Status(o.Status),
		StatusReason:    o.StatusReason,
		PaymentDeadline: o.PaymentDeadline,
		PhotoKey:        deref(o.PhotoKey),
		CreatedAt:       o.CreatedAt,
	}
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
