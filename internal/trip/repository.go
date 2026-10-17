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
	"github.com/jackc/pgx/v5/pgxpool"
)

type openFilter struct {
	Now   time.Time
	City  string
	After pagination.Cursor
	Limit int
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
