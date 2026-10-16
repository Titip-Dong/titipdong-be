package upload

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
	Create(ctx context.Context, u Upload) error
	// Lock returns the upload locked for update; ctx must carry a transaction.
	Lock(ctx context.Context, id uuid.UUID) (Upload, error)
	MarkAttached(ctx context.Context, id uuid.UUID, size int64, at time.Time) error
	ListStalePending(ctx context.Context, before time.Time, limit int) ([]Upload, error)
	DeletePending(ctx context.Context, id uuid.UUID) error
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

func (r *pgRepository) Create(ctx context.Context, u Upload) error {
	return r.q(ctx).CreateUpload(ctx, db.CreateUploadParams{
		ID:          u.ID,
		OwnerID:     u.OwnerID,
		Purpose:     string(u.Purpose),
		ObjectKey:   u.ObjectKey,
		ContentType: u.ContentType,
		SizeBytes:   u.SizeBytes,
	})
}

func (r *pgRepository) Lock(ctx context.Context, id uuid.UUID) (Upload, error) {
	row, err := r.q(ctx).LockUpload(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Upload{}, ErrNotFound
	}
	if err != nil {
		return Upload{}, err
	}
	return toUpload(row), nil
}

func (r *pgRepository) MarkAttached(ctx context.Context, id uuid.UUID, size int64, at time.Time) error {
	return r.q(ctx).MarkUploadAttached(ctx, db.MarkUploadAttachedParams{ID: id, SizeBytes: size, AttachedAt: &at})
}

func (r *pgRepository) ListStalePending(ctx context.Context, before time.Time, limit int) ([]Upload, error) {
	rows, err := r.q(ctx).ListStalePendingUploads(ctx, db.ListStalePendingUploadsParams{Before: before, RowLimit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]Upload, len(rows))
	for i, row := range rows {
		out[i] = toUpload(row)
	}
	return out, nil
}

func (r *pgRepository) DeletePending(ctx context.Context, id uuid.UUID) error {
	return r.q(ctx).DeleteUpload(ctx, id)
}

func toUpload(row db.Upload) Upload {
	return Upload{
		ID:          row.ID,
		OwnerID:     row.OwnerID,
		Purpose:     Purpose(row.Purpose),
		ObjectKey:   row.ObjectKey,
		ContentType: row.ContentType,
		SizeBytes:   row.SizeBytes,
		Status:      Status(row.Status),
		CreatedAt:   row.CreatedAt,
	}
}
