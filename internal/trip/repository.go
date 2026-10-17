package trip

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
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type openFilter struct {
	Now  time.Time
	City string
	// From and To bound departs_on inclusively; nil leaves that side open.
	From, To *time.Time
	After    pagination.Cursor
	Limit    int
}

type repository interface {
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error
	// CreateTrip returns ErrSlugTaken on a slug collision.
	CreateTrip(ctx context.Context, t Trip) error
	TripByID(ctx context.Context, id uuid.UUID) (Trip, error)
	TripBySlug(ctx context.Context, slug string) (Trip, error)
	ListOpen(ctx context.Context, f openFilter) ([]Trip, error)
	ListByJastiper(ctx context.Context, jastiperID uuid.UUID, before pagination.Cursor, limit int) ([]Trip, error)
	// CloseTrip reports false when the trip was not open.
	CloseTrip(ctx context.Context, id uuid.UUID) (bool, error)
	// Lock returns the trip locked for update; ctx must carry a transaction.
	Lock(ctx context.Context, id uuid.UUID) (Trip, error)
	// AdjustReserved adds units (negative to release) to reserved_capacity.
	AdjustReserved(ctx context.Context, id uuid.UUID, units int) error
	UpdateTrip(ctx context.Context, t Trip) error
	// ActiveOrders counts the trip's orders that are neither finished nor dropped.
	ActiveOrders(ctx context.Context, tripID uuid.UUID) (int64, error)
	CancelTrip(ctx context.Context, id uuid.UUID) error
	CloseExpired(ctx context.Context, now time.Time) (int64, error)
	// CompleteReturned completes closed trips that returned before today and have no active order.
	CompleteReturned(ctx context.Context, today time.Time) (int64, error)

	CreateItem(ctx context.Context, it Item) error
	Items(ctx context.Context, tripID uuid.UUID) ([]Item, error)
	// Item returns ErrItemNotFound unless the item belongs to the trip.
	Item(ctx context.Context, tripID, itemID uuid.UUID) (Item, error)
	// ItemStats returns the trip's item count and the position after the last item.
	ItemStats(ctx context.Context, tripID uuid.UUID) (count int, nextPosition int, err error)
	UpdateItem(ctx context.Context, it Item) error
	// DeleteItem reports false when the item does not belong to the trip.
	DeleteItem(ctx context.Context, tripID, itemID uuid.UUID) (bool, error)
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

func (r *pgRepository) CreateTrip(ctx context.Context, t Trip) error {
	err := r.q(ctx).CreateTrip(ctx, db.CreateTripParams{
		ID:                 t.ID,
		JastiperID:         t.JastiperID,
		Slug:               t.Slug,
		Title:              t.Title,
		Description:        t.Description,
		DestinationCity:    t.DestinationCity,
		DestinationCountry: t.DestinationCountry,
		DepartsOn:          t.DepartsOn,
		ReturnsOn:          t.ReturnsOn,
		RequestDeadline:    t.RequestDeadline,
		Capacity:           int32(t.Capacity),
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrSlugTaken
	}
	return err
}

func (r *pgRepository) TripByID(ctx context.Context, id uuid.UUID) (Trip, error) {
	row, err := r.q(ctx).GetTripByID(ctx, id)
	if err != nil {
		return Trip{}, notFound(err)
	}
	return toTrip(row.Trip, row.JastiperName), nil
}

func (r *pgRepository) TripBySlug(ctx context.Context, slug string) (Trip, error) {
	row, err := r.q(ctx).GetTripBySlug(ctx, slug)
	if err != nil {
		return Trip{}, notFound(err)
	}
	return toTrip(row.Trip, row.JastiperName), nil
}

func (r *pgRepository) ListOpen(ctx context.Context, f openFilter) ([]Trip, error) {
	rows, err := r.q(ctx).ListOpenTrips(ctx, db.ListOpenTripsParams{
		Now:            f.Now,
		City:           f.City,
		DepartsFrom:    toDate(f.From),
		DepartsTo:      toDate(f.To),
		AfterDepartsOn: f.After.At,
		AfterID:        f.After.ID,
		RowLimit:       int32(f.Limit),
	})
	if err != nil {
		return nil, err
	}
	trips := make([]Trip, len(rows))
	for i, row := range rows {
		trips[i] = toTrip(row.Trip, row.JastiperName)
	}
	return trips, nil
}

func (r *pgRepository) ListByJastiper(ctx context.Context, jastiperID uuid.UUID, before pagination.Cursor, limit int) ([]Trip, error) {
	rows, err := r.q(ctx).ListTripsByJastiper(ctx, db.ListTripsByJastiperParams{
		JastiperID:      jastiperID,
		BeforeCreatedAt: before.At,
		BeforeID:        before.ID,
		RowLimit:        int32(limit),
	})
	if err != nil {
		return nil, err
	}
	trips := make([]Trip, len(rows))
	for i, row := range rows {
		trips[i] = toTrip(row.Trip, row.JastiperName)
	}
	return trips, nil
}

func (r *pgRepository) CloseTrip(ctx context.Context, id uuid.UUID) (bool, error) {
	n, err := r.q(ctx).CloseTrip(ctx, id)
	return n == 1, err
}

func (r *pgRepository) Lock(ctx context.Context, id uuid.UUID) (Trip, error) {
	row, err := r.q(ctx).LockTrip(ctx, id)
	if err != nil {
		return Trip{}, notFound(err)
	}
	return toTrip(row.Trip, row.JastiperName), nil
}

func (r *pgRepository) AdjustReserved(ctx context.Context, id uuid.UUID, units int) error {
	return r.q(ctx).AdjustTripReservedCapacity(ctx, db.AdjustTripReservedCapacityParams{Units: int32(units), ID: id})
}

func (r *pgRepository) UpdateTrip(ctx context.Context, t Trip) error {
	return r.q(ctx).UpdateTrip(ctx, db.UpdateTripParams{
		ID:                 t.ID,
		Title:              t.Title,
		Description:        t.Description,
		DestinationCity:    t.DestinationCity,
		DestinationCountry: t.DestinationCountry,
		DepartsOn:          t.DepartsOn,
		ReturnsOn:          t.ReturnsOn,
		RequestDeadline:    t.RequestDeadline,
		Capacity:           int32(t.Capacity),
	})
}

func (r *pgRepository) ActiveOrders(ctx context.Context, tripID uuid.UUID) (int64, error) {
	return r.q(ctx).CountActiveTripOrders(ctx, tripID)
}

func (r *pgRepository) CancelTrip(ctx context.Context, id uuid.UUID) error {
	return r.q(ctx).CancelTrip(ctx, id)
}

func (r *pgRepository) CloseExpired(ctx context.Context, now time.Time) (int64, error) {
	return r.q(ctx).CloseExpiredTrips(ctx, now)
}

func (r *pgRepository) CompleteReturned(ctx context.Context, today time.Time) (int64, error) {
	return r.q(ctx).CompleteReturnedTrips(ctx, today)
}

func (r *pgRepository) CreateItem(ctx context.Context, it Item) error {
	return r.q(ctx).CreateTripItem(ctx, db.CreateTripItemParams{
		ID: it.ID, TripID: it.TripID, Name: it.Name, Description: nonEmpty(it.Description),
		PhotoKey: nonEmpty(it.PhotoKey), ReferencePrice: it.ReferencePrice, Position: int32(it.Position),
	})
}

func (r *pgRepository) Items(ctx context.Context, tripID uuid.UUID) ([]Item, error) {
	rows, err := r.q(ctx).ListTripItems(ctx, tripID)
	if err != nil {
		return nil, err
	}
	items := make([]Item, len(rows))
	for i, row := range rows {
		items[i] = toItem(row)
	}
	return items, nil
}

func (r *pgRepository) Item(ctx context.Context, tripID, itemID uuid.UUID) (Item, error) {
	row, err := r.q(ctx).GetTripItem(ctx, db.GetTripItemParams{ID: itemID, TripID: tripID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, ErrItemNotFound
	}
	if err != nil {
		return Item{}, err
	}
	return toItem(row), nil
}

func (r *pgRepository) ItemStats(ctx context.Context, tripID uuid.UUID) (int, int, error) {
	row, err := r.q(ctx).CountTripItems(ctx, tripID)
	return int(row.Items), int(row.NextPosition), err
}

func (r *pgRepository) UpdateItem(ctx context.Context, it Item) error {
	return r.q(ctx).UpdateTripItem(ctx, db.UpdateTripItemParams{
		ID: it.ID, TripID: it.TripID, Name: it.Name, Description: nonEmpty(it.Description),
		PhotoKey: nonEmpty(it.PhotoKey), ReferencePrice: it.ReferencePrice, Position: int32(it.Position),
	})
}

func (r *pgRepository) DeleteItem(ctx context.Context, tripID, itemID uuid.UUID) (bool, error) {
	n, err := r.q(ctx).DeleteTripItem(ctx, db.DeleteTripItemParams{ID: itemID, TripID: tripID})
	return n == 1, err
}

func toItem(it db.TripItem) Item {
	return Item{
		ID: it.ID, TripID: it.TripID, Name: it.Name, Description: deref(it.Description), PhotoKey: deref(it.PhotoKey),
		ReferencePrice: it.ReferencePrice, Position: int(it.Position),
	}
}

func toDate(t *time.Time) pgtype.Date {
	if t == nil {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: *t, Valid: true}
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

func toTrip(t db.Trip, jastiperName string) Trip {
	return Trip{
		ID:                 t.ID,
		JastiperID:         t.JastiperID,
		JastiperName:       jastiperName,
		Slug:               t.Slug,
		Title:              t.Title,
		Description:        t.Description,
		DestinationCity:    t.DestinationCity,
		DestinationCountry: t.DestinationCountry,
		DepartsOn:          t.DepartsOn,
		ReturnsOn:          t.ReturnsOn,
		RequestDeadline:    t.RequestDeadline,
		Capacity:           int(t.Capacity),
		ReservedCapacity:   int(t.ReservedCapacity),
		Status:             Status(t.Status),
		CreatedAt:          t.CreatedAt,
	}
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
