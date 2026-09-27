package auth

import (
	"context"
	"errors"

	"github.com/Titip-Dong/titipdong-be/internal/db"
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

// repository is the persistence boundary of the auth module. It returns
// ErrEmailTaken and ErrUserNotFound instead of driver errors.
type repository interface {
	CreateUser(ctx context.Context, u newUser) (User, error)
	// UserByEmail also returns the stored password hash.
	UserByEmail(ctx context.Context, email string) (User, string, error)
	UserByID(ctx context.Context, id uuid.UUID) (User, error)
}

type pgRepository struct {
	q *db.Queries
}

func newRepository(pool *pgxpool.Pool) *pgRepository {
	return &pgRepository{q: db.New(pool)}
}

func (r *pgRepository) CreateUser(ctx context.Context, u newUser) (User, error) {
	row, err := r.q.CreateUser(ctx, db.CreateUserParams{
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
	return User{ID: row.ID, Name: row.Name, Email: row.Email, Role: Role(row.Role), CreatedAt: row.CreatedAt}, nil
}

func (r *pgRepository) UserByEmail(ctx context.Context, email string) (User, string, error) {
	row, err := r.q.GetUserByEmail(ctx, email)
	if err != nil {
		return User{}, "", notFound(err)
	}
	return User{ID: row.ID, Name: row.Name, Email: row.Email, Role: Role(row.Role), CreatedAt: row.CreatedAt}, row.PasswordHash, nil
}

func (r *pgRepository) UserByID(ctx context.Context, id uuid.UUID) (User, error) {
	row, err := r.q.GetUserByID(ctx, id)
	if err != nil {
		return User{}, notFound(err)
	}
	return User{ID: row.ID, Name: row.Name, Email: row.Email, Role: Role(row.Role), CreatedAt: row.CreatedAt}, nil
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUserNotFound
	}
	return err
}
