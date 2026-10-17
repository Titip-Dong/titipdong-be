package auth

import (
	"context"
	"errors"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/db"
	"github.com/Titip-Dong/titipdong-be/internal/platform/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type newUser struct {
	ID           uuid.UUID
	Name         string
	Email        string
	PasswordHash string
	Role         Role
}

type refreshToken struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	FamilyID  uuid.UUID
	ExpiresAt time.Time
	RevokedAt time.Time
}

// repository is the persistence boundary of the auth module. It returns
// ErrEmailTaken and ErrUserNotFound instead of driver errors.
type repository interface {
	CreateUser(ctx context.Context, u newUser) (User, error)
	// UserByEmail also returns the stored password hash.
	UserByEmail(ctx context.Context, email string) (User, string, error)
	UserByID(ctx context.Context, id uuid.UUID) (User, error)
	SetUserStatus(ctx context.Context, id uuid.UUID, status Status) (User, error)

	WithTx(ctx context.Context, fn func(ctx context.Context) error) error
	CreateRefreshToken(ctx context.Context, t refreshToken, hash []byte) error
	// RefreshTokenByHash locks the row and returns ErrInvalidRefreshToken when it does not exist.
	RefreshTokenByHash(ctx context.Context, hash []byte) (refreshToken, error)
	RotateRefreshToken(ctx context.Context, oldID, newID uuid.UUID, at time.Time) error
	RevokeRefreshTokenFamily(ctx context.Context, familyID uuid.UUID, at time.Time) error
	RefreshTokenFamilyActive(ctx context.Context, familyID uuid.UUID) (bool, error)
	RevokeUserRefreshTokens(ctx context.Context, userID uuid.UUID, at time.Time) error
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

func (r *pgRepository) CreateUser(ctx context.Context, u newUser) (User, error) {
	row, err := r.q(ctx).CreateUser(ctx, db.CreateUserParams{
		ID:           u.ID,
		Name:         u.Name,
		Email:        u.Email,
		PasswordHash: u.PasswordHash,
		Role:         string(u.Role),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return User{}, ErrEmailTaken
		}
		return User{}, err
	}
	return toUser(row.ID, row.Name, row.Email, row.Role, row.Status, row.Phone, row.CreatedAt), nil
}

func (r *pgRepository) UserByEmail(ctx context.Context, email string) (User, string, error) {
	row, err := r.q(ctx).GetUserByEmail(ctx, email)
	if err != nil {
		return User{}, "", notFound(err)
	}
	return toUser(row.ID, row.Name, row.Email, row.Role, row.Status, row.Phone, row.CreatedAt), row.PasswordHash, nil
}

func (r *pgRepository) UserByID(ctx context.Context, id uuid.UUID) (User, error) {
	row, err := r.q(ctx).GetUserByID(ctx, id)
	if err != nil {
		return User{}, notFound(err)
	}
	return toUser(row.ID, row.Name, row.Email, row.Role, row.Status, row.Phone, row.CreatedAt), nil
}

func (r *pgRepository) SetUserStatus(ctx context.Context, id uuid.UUID, status Status) (User, error) {
	row, err := r.q(ctx).SetUserStatus(ctx, db.SetUserStatusParams{ID: id, Status: string(status)})
	if err != nil {
		return User{}, notFound(err)
	}
	return toUser(row.ID, row.Name, row.Email, row.Role, row.Status, row.Phone, row.CreatedAt), nil
}

func (r *pgRepository) CreateRefreshToken(ctx context.Context, t refreshToken, hash []byte) error {
	return r.q(ctx).CreateRefreshToken(ctx, db.CreateRefreshTokenParams{
		ID:        t.ID,
		UserID:    t.UserID,
		FamilyID:  t.FamilyID,
		TokenHash: hash,
		ExpiresAt: t.ExpiresAt,
	})
}

func (r *pgRepository) RefreshTokenByHash(ctx context.Context, hash []byte) (refreshToken, error) {
	row, err := r.q(ctx).GetRefreshTokenByHashForUpdate(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return refreshToken{}, ErrInvalidRefreshToken
	}
	if err != nil {
		return refreshToken{}, err
	}
	t := refreshToken{ID: row.ID, UserID: row.UserID, FamilyID: row.FamilyID, ExpiresAt: row.ExpiresAt}
	if row.RevokedAt != nil {
		t.RevokedAt = *row.RevokedAt
	}
	return t, nil
}

func (r *pgRepository) RotateRefreshToken(ctx context.Context, oldID, newID uuid.UUID, at time.Time) error {
	return r.q(ctx).RotateRefreshToken(ctx, db.RotateRefreshTokenParams{ID: oldID, ReplacedBy: newID, RevokedAt: at})
}

func (r *pgRepository) RevokeRefreshTokenFamily(ctx context.Context, familyID uuid.UUID, at time.Time) error {
	return r.q(ctx).RevokeRefreshTokenFamily(ctx, db.RevokeRefreshTokenFamilyParams{FamilyID: familyID, RevokedAt: at})
}

func (r *pgRepository) RefreshTokenFamilyActive(ctx context.Context, familyID uuid.UUID) (bool, error) {
	return r.q(ctx).RefreshTokenFamilyActive(ctx, familyID)
}

func (r *pgRepository) RevokeUserRefreshTokens(ctx context.Context, userID uuid.UUID, at time.Time) error {
	return r.q(ctx).RevokeUserRefreshTokens(ctx, db.RevokeUserRefreshTokensParams{UserID: userID, RevokedAt: at})
}

func toUser(id uuid.UUID, name, email, role, status string, phone *string, createdAt time.Time) User {
	u := User{ID: id, Name: name, Email: email, Role: Role(role), Status: Status(status), CreatedAt: createdAt}
	if phone != nil {
		u.Phone = *phone
	}
	return u
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUserNotFound
	}
	return err
}
