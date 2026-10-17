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

type Status string

const (
	StatusActive    Status = "active"
	StatusSuspended Status = "suspended"
)

func (r Role) valid() bool {
	return r == RoleBuyer || r == RoleJastiper || r == RoleAdmin
}

const (
	accessTokenLifetime  = 15 * time.Minute
	refreshTokenLifetime = 30 * 24 * time.Hour
	// Tabs that load at the same time present the same token; don't treat that as reuse.
	refreshReuseGrace = 10 * time.Second
)

var (
	ErrEmailTaken          = errors.New("email already registered")
	ErrInvalidCredentials  = errors.New("invalid credentials")
	ErrInvalidToken        = errors.New("invalid access token")
	ErrInvalidRefreshToken = errors.New("invalid refresh token")
	ErrUserNotFound        = errors.New("user not found")
	ErrSuspended           = errors.New("account is suspended")
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
	Status    Status
	Phone     string
	CreatedAt time.Time
}

// Principal is the authenticated caller, taken from a verified access token.
type Principal struct {
	UserID uuid.UUID
	Role   Role
}

type Session struct {
	AccessToken  string
	RefreshToken string
	User         User
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
	Refresh(ctx context.Context, refreshToken string) (Session, error)
	Logout(ctx context.Context, refreshToken string) error
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
	if p.Role != RoleBuyer && p.Role != RoleJastiper {
		return Session{}, &InputError{Detail: "Role must be buyer or jastiper."}
	}
	var sess Session
	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		u, err := s.createUser(ctx, p)
		if err != nil {
			return err
		}
		sess, _, err = s.session(ctx, u, uuid.New())
		return err
	})
	return sess, err
}

// Provision creates a user of any role, including admin. CLI use only.
func Provision(ctx context.Context, pool *pgxpool.Pool, p RegisterParams) (User, error) {
	if !p.Role.valid() {
		return User{}, &InputError{Detail: "Unknown role."}
	}
	return newService(newRepository(pool), tokens{}).createUser(ctx, p)
}

func (s *service) createUser(ctx context.Context, p RegisterParams) (User, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return User{}, &InputError{Detail: "Name must not be blank."}
	}
	hash, err := hashPassword(p.Password)
	if err != nil {
		return User{}, fmt.Errorf("hash password: %w", err)
	}
	return s.repo.CreateUser(ctx, newUser{
		ID:           uuid.New(),
		Name:         name,
		Email:        normalizeEmail(p.Email),
		PasswordHash: hash,
		Role:         p.Role,
	})
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
	if u.Status == StatusSuspended {
		return Session{}, ErrSuspended
	}
	sess, _, err := s.session(ctx, u, uuid.New())
	return sess, err
}

func (s *service) Refresh(ctx context.Context, refreshToken string) (Session, error) {
	var sess Session
	reused, suspended := false, false
	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		old, err := s.repo.RefreshTokenByHash(ctx, hashRefreshToken(refreshToken))
		if err != nil {
			return err
		}
		now := s.tokens.now()
		revoked := !old.RevokedAt.IsZero()
		if revoked {
			active, err := s.repo.RefreshTokenFamilyActive(ctx, old.FamilyID)
			if err != nil {
				return err
			}
			if !active {
				return ErrInvalidRefreshToken
			}
			if now.Sub(old.RevokedAt) > refreshReuseGrace {
				reused = true
				return s.repo.RevokeRefreshTokenFamily(ctx, old.FamilyID, now)
			}
		}
		if !now.Before(old.ExpiresAt) {
			return ErrInvalidRefreshToken
		}
		u, err := s.repo.UserByID(ctx, old.UserID)
		if errors.Is(err, ErrUserNotFound) {
			return ErrInvalidRefreshToken
		}
		if err != nil {
			return err
		}
		if u.Status == StatusSuspended {
			suspended = true
			return s.repo.RevokeRefreshTokenFamily(ctx, old.FamilyID, now)
		}
		var newID uuid.UUID
		sess, newID, err = s.session(ctx, u, old.FamilyID)
		if err != nil || revoked {
			return err
		}
		return s.repo.RotateRefreshToken(ctx, old.ID, newID, now)
	})
	if err == nil && (reused || suspended) {
		return Session{}, ErrInvalidRefreshToken
	}
	return sess, err
}

func (s *service) Logout(ctx context.Context, refreshToken string) error {
	return s.repo.WithTx(ctx, func(ctx context.Context) error {
		t, err := s.repo.RefreshTokenByHash(ctx, hashRefreshToken(refreshToken))
		if errors.Is(err, ErrInvalidRefreshToken) {
			return nil
		}
		if err != nil {
			return err
		}
		return s.repo.RevokeRefreshTokenFamily(ctx, t.FamilyID, s.tokens.now())
	})
}

func (s *service) User(ctx context.Context, id uuid.UUID) (User, error) {
	return s.repo.UserByID(ctx, id)
}

func (s *service) Authenticate(_ context.Context, accessToken string) (Principal, error) {
	return s.tokens.parse(accessToken)
}

func (s *service) session(ctx context.Context, u User, familyID uuid.UUID) (Session, uuid.UUID, error) {
	access, err := s.tokens.issue(u)
	if err != nil {
		return Session{}, uuid.Nil, err
	}
	raw, hash := newRefreshToken()
	t := refreshToken{
		ID:        uuid.New(),
		UserID:    u.ID,
		FamilyID:  familyID,
		ExpiresAt: s.tokens.now().Add(refreshTokenLifetime),
	}
	if err := s.repo.CreateRefreshToken(ctx, t, hash); err != nil {
		return Session{}, uuid.Nil, err
	}
	return Session{AccessToken: access, RefreshToken: raw, User: u}, t.ID, nil
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
