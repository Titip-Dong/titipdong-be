package shipment

import (
	"context"
	"errors"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/db"
	"github.com/Titip-Dong/titipdong-be/internal/platform/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type repository interface {
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error
	Create(ctx context.Context, s Shipment) error
	ByOrder(ctx context.Context, orderID uuid.UUID) (Shipment, error)
	MarkDelivered(ctx context.Context, orderID uuid.UUID, at time.Time) error
	// DueForRelease returns orders still shipped whose shipment is not delivered and was shipped at or before cutoff.
	DueForRelease(ctx context.Context, cutoff time.Time, limit int) ([]uuid.UUID, error)
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

func (r *pgRepository) Create(ctx context.Context, s Shipment) error {
	return r.q(ctx).CreateShipment(ctx, db.CreateShipmentParams{
		OrderID:        s.OrderID,
		Method:         string(s.Method),
		CourierCode:    s.CourierCode,
		TrackingNumber: s.TrackingNumber,
		HandoverNote:   s.HandoverNote,
		ShippedAt:      s.ShippedAt,
		ProofKey:       nonEmpty(s.ProofKey),
	})
}

func (r *pgRepository) ByOrder(ctx context.Context, orderID uuid.UUID) (Shipment, error) {
	row, err := r.q(ctx).GetShipment(ctx, orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Shipment{}, ErrNotFound
	}
	if err != nil {
		return Shipment{}, err
	}
	return Shipment{
		OrderID:        row.OrderID,
		Method:         Method(row.Method),
		CourierCode:    row.CourierCode,
		TrackingNumber: row.TrackingNumber,
		HandoverNote:   row.HandoverNote,
		ShippedAt:      row.ShippedAt,
		DeliveredAt:    row.DeliveredAt,
		ProofKey:       deref(row.ProofKey),
	}, nil
}

func (r *pgRepository) MarkDelivered(ctx context.Context, orderID uuid.UUID, at time.Time) error {
	return r.q(ctx).MarkShipmentDelivered(ctx, db.MarkShipmentDeliveredParams{OrderID: orderID, DeliveredAt: at})
}

func (r *pgRepository) DueForRelease(ctx context.Context, cutoff time.Time, limit int) ([]uuid.UUID, error) {
	return r.q(ctx).ListShipmentsDueForRelease(ctx, db.ListShipmentsDueForReleaseParams{Cutoff: cutoff, RowLimit: int32(limit)})
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
