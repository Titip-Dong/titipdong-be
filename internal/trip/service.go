// Package trip implements F02 Trip: jastipers open trips and buyers browse them.
package trip

import (
	"context"
	"crypto/rand"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/platform/pagination"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Status string

const (
	StatusOpen      Status = "open"
	StatusClosed    Status = "closed"
	StatusCompleted Status = "completed"
	StatusCancelled Status = "cancelled"
)

var (
	ErrNotFound  = errors.New("trip not found")
	ErrNotOpen   = errors.New("trip is not open")
	ErrSlugTaken = errors.New("trip slug taken")
)

type InputError struct {
	Detail string
}

func (e *InputError) Error() string {
	return "invalid input: " + e.Detail
}

type Trip struct {
	ID                 uuid.UUID
	JastiperID         uuid.UUID
	JastiperName       string
	Slug               string
	Title              string
	Description        string
	DestinationCity    string
	DestinationCountry string
	DepartsOn          time.Time
	ReturnsOn          time.Time
	RequestDeadline    time.Time
	Status             Status
	CreatedAt          time.Time
}

func (t Trip) AcceptsRequests(now time.Time) bool {
	return t.Status == StatusOpen && now.Before(t.RequestDeadline)
}

type CreateParams struct {
	Title              string
	Description        string
	DestinationCity    string
	DestinationCountry string
	DepartsOn          time.Time
	ReturnsOn          time.Time
	RequestDeadline    time.Time
}

type ListParams struct {
	City   string
	Limit  int
	Cursor string
}

type Page struct {
	Items      []Trip
	NextCursor string
}

type Service interface {
	Create(ctx context.Context, jastiperID uuid.UUID, p CreateParams) (Trip, error)
	List(ctx context.Context, p ListParams) (Page, error)
	ListByJastiper(ctx context.Context, jastiperID uuid.UUID, limit int, cursor string) (Page, error)
	BySlug(ctx context.Context, slug string) (Trip, error)
	ByID(ctx context.Context, id uuid.UUID) (Trip, error)
	Close(ctx context.Context, jastiperID, id uuid.UUID) (Trip, error)
}

func NewService(pool *pgxpool.Pool) Service {
	return newService(newRepository(pool), time.Now)
}

type service struct {
	repo repository
	now  func() time.Time
}

func newService(repo repository, now func() time.Time) *service {
	return &service{repo: repo, now: now}
}

func (s *service) Create(ctx context.Context, jastiperID uuid.UUID, p CreateParams) (Trip, error) {
	t := Trip{
		ID:                 uuid.Must(uuid.NewV7()),
		JastiperID:         jastiperID,
		Title:              strings.TrimSpace(p.Title),
		Description:        strings.TrimSpace(p.Description),
		DestinationCity:    strings.TrimSpace(p.DestinationCity),
		DestinationCountry: strings.ToUpper(strings.TrimSpace(p.DestinationCountry)),
		DepartsOn:          p.DepartsOn,
		ReturnsOn:          p.ReturnsOn,
		RequestDeadline:    p.RequestDeadline,
		Status:             StatusOpen,
	}
	switch {
	case t.Title == "":
		return Trip{}, &InputError{Detail: "Title must not be blank."}
	case t.DestinationCity == "":
		return Trip{}, &InputError{Detail: "Destination city must not be blank."}
	case t.ReturnsOn.Before(t.DepartsOn):
		return Trip{}, &InputError{Detail: "Return date must not be before the departure date."}
	case !t.RequestDeadline.After(s.now()):
		return Trip{}, &InputError{Detail: "Request deadline must be in the future."}
	case !t.RequestDeadline.Before(t.ReturnsOn.AddDate(0, 0, 1)):
		return Trip{}, &InputError{Detail: "Request deadline must not be after the return date."}
	}

	for range 3 {
		t.Slug = slugify(t.Title) + "-" + randomSuffix()
		err := s.repo.CreateTrip(ctx, t)
		if errors.Is(err, ErrSlugTaken) {
			continue
		}
		if err != nil {
			return Trip{}, err
		}
		return s.repo.TripByID(ctx, t.ID)
	}
	return Trip{}, ErrSlugTaken
}

func (s *service) List(ctx context.Context, p ListParams) (Page, error) {
	after, err := pagination.Decode(p.Cursor, pagination.Cursor{})
	if err != nil {
		return Page{}, err
	}
	trips, err := s.repo.ListOpen(ctx, openFilter{
		Now:   s.now(),
		City:  escapeLike(strings.TrimSpace(p.City)),
		After: after,
		Limit: p.Limit + 1,
	})
	if err != nil {
		return Page{}, err
	}
	items, next := pagination.Page(trips, p.Limit, func(t Trip) pagination.Cursor {
		return pagination.Cursor{At: t.DepartsOn, ID: t.ID}
	})
	return Page{Items: items, NextCursor: next}, nil
}

func (s *service) ListByJastiper(ctx context.Context, jastiperID uuid.UUID, limit int, cursor string) (Page, error) {
	before, err := pagination.Decode(cursor, pagination.Newest)
	if err != nil {
		return Page{}, err
	}
	trips, err := s.repo.ListByJastiper(ctx, jastiperID, before, limit+1)
	if err != nil {
		return Page{}, err
	}
	items, next := pagination.Page(trips, limit, func(t Trip) pagination.Cursor {
		return pagination.Cursor{At: t.CreatedAt, ID: t.ID}
	})
	return Page{Items: items, NextCursor: next}, nil
}

func (s *service) BySlug(ctx context.Context, slug string) (Trip, error) {
	return s.repo.TripBySlug(ctx, slug)
}

func (s *service) ByID(ctx context.Context, id uuid.UUID) (Trip, error) {
	return s.repo.TripByID(ctx, id)
}

func (s *service) Close(ctx context.Context, jastiperID, id uuid.UUID) (Trip, error) {
	t, err := s.repo.TripByID(ctx, id)
	if err != nil {
		return Trip{}, err
	}
	if t.JastiperID != jastiperID {
		return Trip{}, ErrNotFound
	}
	closed, err := s.repo.CloseTrip(ctx, id)
	if err != nil {
		return Trip{}, err
	}
	if !closed {
		return Trip{}, ErrNotOpen
	}
	t.Status = StatusClosed
	return t, nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(title string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(s) > 60 {
		s = strings.TrimRight(s[:60], "-")
	}
	if s == "" {
		s = "trip"
	}
	return s
}

func randomSuffix() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
