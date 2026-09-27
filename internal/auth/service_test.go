package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeRepository is an in-memory repository with the same error contract as
// pgRepository.
type fakeRepository struct {
	mu    sync.Mutex
	users map[uuid.UUID]newUser
	now   time.Time
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{users: map[uuid.UUID]newUser{}, now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (r *fakeRepository) CreateUser(_ context.Context, u newUser) (User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.users {
		if strings.EqualFold(existing.Email, u.Email) {
			return User{}, ErrEmailTaken
		}
	}
	r.users[u.ID] = u
	return r.toUser(u), nil
}

func (r *fakeRepository) UserByEmail(_ context.Context, email string) (User, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, u := range r.users {
		if strings.EqualFold(u.Email, email) {
			return r.toUser(u), u.PasswordHash, nil
		}
	}
	return User{}, "", ErrUserNotFound
}

func (r *fakeRepository) UserByID(_ context.Context, id uuid.UUID) (User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.users[id]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return r.toUser(u), nil
}

func (r *fakeRepository) toUser(u newUser) User {
	return User{ID: u.ID, Name: u.Name, Email: u.Email, Role: u.Role, CreatedAt: r.now}
}

const testSecret = "test-secret-that-is-at-least-32-bytes"

func newTestService(now func() time.Time) *service {
	return newService(newFakeRepository(), newTokens([]byte(testSecret), accessTokenLifetime, now))
}

func TestRegisterNormalizesInputAndIssuesToken(t *testing.T) {
	svc := newTestService(time.Now)
	s, err := svc.Register(t.Context(), RegisterParams{Name: "  Carlo  ", Email: " Carlo@Example.COM ", Password: "rahasia123", Role: RoleBuyer})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if s.User.Name != "Carlo" || s.User.Email != "carlo@example.com" || s.User.Role != RoleBuyer {
		t.Errorf("user = %+v, want trimmed name, lowercased email, buyer role", s.User)
	}
	p, err := svc.Authenticate(t.Context(), s.AccessToken)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if p.UserID != s.User.ID || p.Role != RoleBuyer {
		t.Errorf("principal = %+v, want user %s as buyer", p, s.User.ID)
	}
}

func TestRegisterRejectsInvalidInput(t *testing.T) {
	svc := newTestService(time.Now)
	for name, p := range map[string]RegisterParams{
		"blank name": {Name: "   ", Email: "a@example.com", Password: "rahasia123", Role: RoleBuyer},
		"admin role": {Name: "Admin", Email: "b@example.com", Password: "rahasia123", Role: RoleAdmin},
	} {
		t.Run(name, func(t *testing.T) {
			var inputErr *InputError
			if _, err := svc.Register(t.Context(), p); !errors.As(err, &inputErr) {
				t.Errorf("Register() error = %v, want *InputError", err)
			}
		})
	}
}

func TestRegisterRejectsDuplicateEmail(t *testing.T) {
	svc := newTestService(time.Now)
	p := RegisterParams{Name: "Carlo", Email: "carlo@example.com", Password: "rahasia123", Role: RoleJastiper}
	if _, err := svc.Register(t.Context(), p); err != nil {
		t.Fatalf("first Register() error = %v", err)
	}
	p.Email = "CARLO@example.com"
	if _, err := svc.Register(t.Context(), p); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("second Register() error = %v, want ErrEmailTaken", err)
	}
}

func TestLogin(t *testing.T) {
	svc := newTestService(time.Now)
	if _, err := svc.Register(t.Context(), RegisterParams{Name: "Carlo", Email: "carlo@example.com", Password: "rahasia123", Role: RoleBuyer}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	if _, err := svc.Login(t.Context(), " CARLO@example.com", "rahasia123"); err != nil {
		t.Errorf("Login() with correct password error = %v", err)
	}
	if _, err := svc.Login(t.Context(), "carlo@example.com", "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("Login() with wrong password error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := svc.Login(t.Context(), "nobody@example.com", "rahasia123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("Login() with unknown email error = %v, want ErrInvalidCredentials", err)
	}
}

func TestAuthenticateRejectsBadTokens(t *testing.T) {
	now := time.Now()
	svc := newTestService(func() time.Time { return now })
	s, err := svc.Register(t.Context(), RegisterParams{Name: "Carlo", Email: "carlo@example.com", Password: "rahasia123", Role: RoleBuyer})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	other := newTokens([]byte("another-secret-that-is-32-bytes-long"), accessTokenLifetime, time.Now)
	foreign, err := other.issue(s.User)
	if err != nil {
		t.Fatalf("issue() error = %v", err)
	}
	for name, token := range map[string]string{
		"garbage":        "not-a-jwt",
		"other secret":   foreign,
		"tampered":       s.AccessToken[:len(s.AccessToken)-2] + "xx",
		"alg none token": "eyJhbGciOiJub25lIn0." + strings.Split(s.AccessToken, ".")[1] + ".",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.Authenticate(t.Context(), token); !errors.Is(err, ErrInvalidToken) {
				t.Errorf("Authenticate() error = %v, want ErrInvalidToken", err)
			}
		})
	}

	t.Run("expired", func(t *testing.T) {
		now = now.Add(accessTokenLifetime + time.Second)
		if _, err := svc.Authenticate(t.Context(), s.AccessToken); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("Authenticate() error = %v, want ErrInvalidToken", err)
		}
	})
}
