// Package trip implements F02 Trip: jastipers open trips and buyers browse them.
package trip

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/platform/pagination"
	"github.com/Titip-Dong/titipdong-be/internal/upload"
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
	ErrNotFound      = errors.New("trip not found")
	ErrNotOpen       = errors.New("trip is not open")
	ErrSlugTaken     = errors.New("trip slug taken")
	ErrTripClosed    = errors.New("trip does not accept requests")
	ErrCapacityFull  = errors.New("trip capacity is full")
	ErrItemNotFound  = errors.New("trip item not found")
	ErrCatalogLocked = errors.New("trip catalog can no longer change")
)

const maxItemsPerTrip = 50

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
	Capacity           int
	ReservedCapacity   int
	Status             Status
	CreatedAt          time.Time
}

func (t Trip) RemainingCapacity() int {
	return t.Capacity - t.ReservedCapacity
}

func (t Trip) AcceptsRequests(now time.Time) bool {
	return t.Status == StatusOpen && now.Before(t.RequestDeadline) && t.RemainingCapacity() > 0
}

// Item is a catalog entry. ReferencePrice is indicative; the final price is set per order.
type Item struct {
	ID             uuid.UUID
	TripID         uuid.UUID
	Name           string
	Description    string
	PhotoKey       string
	ReferencePrice *int64
	Position       int
}

// ItemParams creates an item; on update only the non-nil fields change.
type ItemParams struct {
	Name           *string
	Description    *string
	ReferencePrice *int64
	Position       *int
	// PhotoUpload is an upload with purpose trip_item_photo owned by the jastiper; it replaces the photo.
	PhotoUpload *uuid.UUID
}

type CreateParams struct {
	Title              string
	Description        string
	DestinationCity    string
	DestinationCountry string
	DepartsOn          time.Time
	ReturnsOn          time.Time
	RequestDeadline    time.Time
	Capacity           int
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
	// Reserve takes units of capacity under a row lock: ErrTripClosed unless the trip accepts requests,
	// ErrCapacityFull when fewer units remain.
	Reserve(ctx context.Context, tripID uuid.UUID, units int) error
	// Items is the trip's catalog in display order.
	Items(ctx context.Context, tripID uuid.UUID) ([]Item, error)
	AddItem(ctx context.Context, jastiperID, tripID uuid.UUID, p ItemParams) (Item, error)
	UpdateItem(ctx context.Context, jastiperID, tripID, itemID uuid.UUID, p ItemParams) (Item, error)
	DeleteItem(ctx context.Context, jastiperID, tripID, itemID uuid.UUID) error
	// Release gives units of capacity back, never more than are reserved.
	Release(ctx context.Context, tripID uuid.UUID, units int) error
	// CloseExpired backs the trips.close_expired job: open trips past their request deadline close, and closed
	// trips whose return date (UTC) has passed complete once no order is active.
	CloseExpired(ctx context.Context) error
}

// uploadAttacher is the part of upload.Service the catalog needs; nil disables item photos.
type uploadAttacher interface {
	Attach(ctx context.Context, ownerID, id uuid.UUID, purpose upload.Purpose) (string, error)
}

func NewService(pool *pgxpool.Pool, uploads uploadAttacher) Service {
	s := newService(newRepository(pool), time.Now)
	s.uploads = uploads
	return s
}

type service struct {
	repo    repository
	uploads uploadAttacher
	now     func() time.Time
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
		Capacity:           p.Capacity,
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
	case t.Capacity < 1:
		return Trip{}, &InputError{Detail: "Capacity must be at least 1."}
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

func (s *service) Reserve(ctx context.Context, tripID uuid.UUID, units int) error {
	if units < 1 {
		return fmt.Errorf("reserve %d units: must be positive", units)
	}
	return s.repo.WithTx(ctx, func(ctx context.Context) error {
		t, err := s.repo.Lock(ctx, tripID)
		if err != nil {
			return err
		}
		if t.Status != StatusOpen || !s.now().Before(t.RequestDeadline) {
			return ErrTripClosed
		}
		if t.RemainingCapacity() < units {
			return ErrCapacityFull
		}
		return s.repo.AdjustReserved(ctx, tripID, units)
	})
}

func (s *service) Release(ctx context.Context, tripID uuid.UUID, units int) error {
	if units < 1 {
		return fmt.Errorf("release %d units: must be positive", units)
	}
	return s.repo.WithTx(ctx, func(ctx context.Context) error {
		t, err := s.repo.Lock(ctx, tripID)
		if err != nil {
			return err
		}
		if t.ReservedCapacity < units {
			// Orders created by code that predates capacity never reserved; do not block their transitions.
			slog.WarnContext(ctx, "trip_release_exceeds_reserved", "trip_id", tripID, "units", units, "reserved", t.ReservedCapacity)
			units = t.ReservedCapacity
		}
		if units == 0 {
			return nil
		}
		return s.repo.AdjustReserved(ctx, tripID, -units)
	})
}

func (s *service) CloseExpired(ctx context.Context) error {
	now := s.now()
	closed, err := s.repo.CloseExpired(ctx, now)
	if err != nil {
		return err
	}
	completed, err := s.repo.CompleteReturned(ctx, now.UTC().Truncate(24*time.Hour))
	if err != nil {
		return err
	}
	if closed+completed > 0 {
		slog.InfoContext(ctx, "trips_swept", "closed", closed, "completed", completed)
	}
	return nil
}

func (s *service) Items(ctx context.Context, tripID uuid.UUID) ([]Item, error) {
	if _, err := s.repo.TripByID(ctx, tripID); err != nil {
		return nil, err
	}
	return s.repo.Items(ctx, tripID)
}

func (s *service) AddItem(ctx context.Context, jastiperID, tripID uuid.UUID, p ItemParams) (Item, error) {
	if p.Name == nil {
		return Item{}, &InputError{Detail: "Name must not be blank."}
	}
	var it Item
	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		if err := s.lockCatalog(ctx, jastiperID, tripID); err != nil {
			return err
		}
		count, next, err := s.repo.ItemStats(ctx, tripID)
		if err != nil {
			return err
		}
		if count >= maxItemsPerTrip {
			return &InputError{Detail: "A trip can list at most 50 items."}
		}
		it = Item{ID: uuid.Must(uuid.NewV7()), TripID: tripID, Position: next}
		if err := s.applyItem(ctx, jastiperID, &it, p); err != nil {
			return err
		}
		return s.repo.CreateItem(ctx, it)
	})
	return it, err
}

func (s *service) UpdateItem(ctx context.Context, jastiperID, tripID, itemID uuid.UUID, p ItemParams) (Item, error) {
	var it Item
	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		if err := s.lockCatalog(ctx, jastiperID, tripID); err != nil {
			return err
		}
		var err error
		if it, err = s.repo.Item(ctx, tripID, itemID); err != nil {
			return err
		}
		if err := s.applyItem(ctx, jastiperID, &it, p); err != nil {
			return err
		}
		return s.repo.UpdateItem(ctx, it)
	})
	return it, err
}

func (s *service) DeleteItem(ctx context.Context, jastiperID, tripID, itemID uuid.UUID) error {
	return s.repo.WithTx(ctx, func(ctx context.Context) error {
		if err := s.lockCatalog(ctx, jastiperID, tripID); err != nil {
			return err
		}
		deleted, err := s.repo.DeleteItem(ctx, tripID, itemID)
		if err == nil && !deleted {
			return ErrItemNotFound
		}
		return err
	})
}

// lockCatalog locks the caller's trip and checks that its catalog can still change (open or closed).
func (s *service) lockCatalog(ctx context.Context, jastiperID, tripID uuid.UUID) error {
	t, err := s.repo.Lock(ctx, tripID)
	if err != nil {
		return err
	}
	if t.JastiperID != jastiperID {
		return ErrNotFound
	}
	if t.Status != StatusOpen && t.Status != StatusClosed {
		return ErrCatalogLocked
	}
	return nil
}

func (s *service) applyItem(ctx context.Context, jastiperID uuid.UUID, it *Item, p ItemParams) error {
	if p.Name != nil {
		if it.Name = strings.TrimSpace(*p.Name); it.Name == "" {
			return &InputError{Detail: "Name must not be blank."}
		}
	}
	if p.Description != nil {
		it.Description = strings.TrimSpace(*p.Description)
	}
	if p.ReferencePrice != nil {
		it.ReferencePrice = p.ReferencePrice
	}
	if p.Position != nil {
		it.Position = *p.Position
	}
	if p.PhotoUpload != nil {
		if s.uploads == nil {
			return upload.ErrUnavailable
		}
		key, err := s.uploads.Attach(ctx, jastiperID, *p.PhotoUpload, upload.PurposeTripItemPhoto)
		if err != nil {
			return err
		}
		it.PhotoKey = key
	}
	return nil
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
