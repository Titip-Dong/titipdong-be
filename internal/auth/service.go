// Package auth implements F01 Auth & User: registration, login, access tokens,
// and the bearer-token middleware other features use to protect their routes.
package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Role is a user's role. Self-registration only allows RoleBuyer and RoleJastiper.
type Role string

const (
	RoleBuyer    Role = "buyer"
	RoleJastiper Role = "jastiper"
	RoleAdmin    Role = "admin"
)

func (r Role) valid() bool {
	return r == RoleBuyer || r == RoleJastiper || r == RoleAdmin
}

const accessTokenLifetime = 15 * time.Minute

var (
	ErrEmailTaken         = errors.New("email already registered")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrInvalidToken       = errors.New("invalid access token")
	ErrUserNotFound       = errors.New("user not found")
)

// InputError is a business-rule violation the caller can fix. Detail is safe to
// show to the user.
type InputError struct {
	Detail string
}

func (e *InputError) Error() string {
	return "invalid input: " + e.Detail
}

type User struct {
	ID        uuid.UUID
	Name      string
	Email     string
	Role      Role
	CreatedAt time.Time
}

// Principal is the authenticated caller, taken from a verified access token.
type Principal struct {
	UserID uuid.UUID
	Role   Role
}

type Session struct {
	AccessToken string
	User        User
}

type RegisterParams struct {
	Name     string
	Email    string
	Password string
	Role     Role
}

// Service is the Auth & User API that handlers and other features depend on.
type Service interface {
	Register(ctx context.Context, p RegisterParams) (Session, error)
	Login(ctx context.Context, email, password string) (Session, error)
	User(ctx context.Context, id uuid.UUID) (User, error)
	Authenticate(ctx context.Context, accessToken string) (Principal, error)
}

// NewService returns the PostgreSQL-backed Service.
func NewService(pool *pgxpool.Pool, jwtSecret string) Service {
	return newService(newRepository(pool), newTokens([]byte(jwtSecret), accessTokenLifetime, time.Now))
}

type service struct {
	repo   repository
	tokens tokens
}

func newService(repo repository, tokens tokens) *service {
	return &service{repo: repo, tokens: tokens}
}

func (s *service) Register(ctx context.Context, p RegisterParams) (Session, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return Session{}, &InputError{Detail: "Name must not be blank."}
	}
	if p.Role != RoleBuyer && p.Role != RoleJastiper {
		return Session{}, &InputError{Detail: "Role must be buyer or jastiper."}
	}
	hash, err := hashPassword(p.Password)
	if err != nil {
		return Session{}, fmt.Errorf("hash password: %w", err)
	}
	u, err := s.repo.CreateUser(ctx, newUser{
		ID:           uuid.New(),
		Name:         name,
		Email:        normalizeEmail(p.Email),
		PasswordHash: hash,
		Role:         p.Role,
	})
	if err != nil {
		return Session{}, err
	}
	return s.session(u)
}

func (s *service) Login(ctx context.Context, email, password string) (Session, error) {
	u, hash, err := s.repo.UserByEmail(ctx, normalizeEmail(email))
	if errors.Is(err, ErrUserNotFound) {
		// Spend the same hashing time as a real check so response timing does
		// not reveal which emails are registered.
		verifyPassword(dummyHash(), password)
		return Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return Session{}, err
	}
	if !verifyPassword(hash, password) {
		return Session{}, ErrInvalidCredentials
	}
	return s.session(u)
}

func (s *service) User(ctx context.Context, id uuid.UUID) (User, error) {
	return s.repo.UserByID(ctx, id)
}

func (s *service) Authenticate(_ context.Context, accessToken string) (Principal, error) {
	return s.tokens.parse(accessToken)
}

func (s *service) session(u User) (Session, error) {
	token, err := s.tokens.issue(u)
	if err != nil {
		return Session{}, err
	}
	return Session{AccessToken: token, User: u}, nil
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
