package order

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/platform/pagination"
	"github.com/Titip-Dong/titipdong-be/internal/trip"
	"github.com/google/uuid"
)

type fakeRepository struct {
	mu     sync.Mutex
	orders map[uuid.UUID]Order
	events map[uuid.UUID][]Event
	now    func() time.Time
}

func newFakeRepository(now func() time.Time) *fakeRepository {
	return &fakeRepository{orders: map[uuid.UUID]Order{}, events: map[uuid.UUID][]Event{}, now: now}
}

func (r *fakeRepository) WithTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

func (r *fakeRepository) CreateOrder(_ context.Context, o Order) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	o.CreatedAt = r.now()
	r.orders[o.ID] = o
	return nil
}

func (r *fakeRepository) Order(_ context.Context, id uuid.UUID) (Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.orders[id]
	if !ok {
		return Order{}, ErrNotFound
	}
	return o, nil
}

func (r *fakeRepository) Lock(ctx context.Context, id uuid.UUID) (Order, error) {
	return r.Order(ctx, id)
}

func (r *fakeRepository) List(_ context.Context, userID uuid.UUID, status Status, tripID *uuid.UUID, before pagination.Cursor, limit int) ([]Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Order
	for _, o := range r.orders {
		mine := o.BuyerID == userID || o.JastiperID == userID
		earlier := o.CreatedAt.Before(before.At) || (o.CreatedAt.Equal(before.At) && o.ID.String() < before.ID.String())
		if mine && earlier && (status == "" || o.Status == status) && (tripID == nil || o.TripID == *tripID) {
			out = append(out, o)
		}
	}
	slices.SortFunc(out, func(a, b Order) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), strings.Compare(b.ID.String(), a.ID.String()))
	})
	return out[:min(len(out), limit)], nil
}

func (r *fakeRepository) UpdateStatus(_ context.Context, o Order) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.orders[o.ID] = o
	return nil
}

func (r *fakeRepository) InsertEvent(_ context.Context, orderID uuid.UUID, e Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e.CreatedAt = r.now()
	r.events[orderID] = append(r.events[orderID], e)
	return nil
}

func (r *fakeRepository) Events(_ context.Context, orderID uuid.UUID) ([]Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events[orderID], nil
}

func (r *fakeRepository) ExpireUnpaid(_ context.Context, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, o := range r.orders {
		if o.Status == StatusApproved && !o.PaymentDeadline.After(now) {
			from := StatusApproved
			o.Status, o.StatusReason = StatusCancelled, "Payment deadline passed."
			r.orders[id] = o
			r.events[id] = append(r.events[id], Event{FromStatus: &from, ToStatus: StatusCancelled, ActorType: ActorSystem})
		}
	}
	return nil
}

type fakeTrips struct {
	trip.Service
	trips map[string]trip.Trip
}

func (f fakeTrips) BySlug(_ context.Context, slug string) (trip.Trip, error) {
	t, ok := f.trips[slug]
	if !ok {
		return trip.Trip{}, trip.ErrNotFound
	}
	return t, nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

var (
	jastiperID = uuid.New()
	buyerID    = uuid.New()
)

func newTestService(c *clock) *service {
	deadline := c.t.Add(7 * 24 * time.Hour)
	trips := fakeTrips{trips: map[string]trip.Trip{
		"tokyo":  {ID: uuid.New(), JastiperID: jastiperID, Slug: "tokyo", Status: trip.StatusOpen, RequestDeadline: deadline},
		"closed": {ID: uuid.New(), JastiperID: jastiperID, Slug: "closed", Status: trip.StatusClosed, RequestDeadline: deadline},
	}}
	return newService(newFakeRepository(c.now), trips, c.now)
}

func newClock() *clock {
	return &clock{t: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
}

func createOrder(t *testing.T, svc *service) Order {
	t.Helper()
	o, err := svc.Create(t.Context(), buyerID, "tokyo", CreateParams{ProductName: " Tokyo Banana ", Quantity: 3})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return o
}

func TestCreate(t *testing.T) {
	svc := newTestService(newClock())
	o := createOrder(t, svc)
	if o.Status != StatusRequested || o.ProductName != "Tokyo Banana" || o.JastiperID != jastiperID || !strings.HasPrefix(o.Code, "TD-") {
		t.Errorf("order = %+v", o)
	}

	tests := map[string]struct {
		buyer uuid.UUID
		slug  string
		name  string
		want  error
	}{
		"closed trip":   {buyerID, "closed", "Item", ErrTripClosed},
		"unknown trip":  {buyerID, "nope", "Item", trip.ErrNotFound},
		"own trip":      {jastiperID, "tokyo", "Item", &InputError{}},
		"blank product": {buyerID, "tokyo", "   ", &InputError{}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Create(t.Context(), tt.buyer, tt.slug, CreateParams{ProductName: tt.name, Quantity: 1})
			var inputErr *InputError
			if _, wantInput := tt.want.(*InputError); wantInput {
				if !errors.As(err, &inputErr) {
					t.Errorf("error = %v, want *InputError", err)
				}
			} else if !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestApproveComputesTotalAndDeadline(t *testing.T) {
	c := newClock()
	svc := newTestService(c)
	o := createOrder(t, svc)

	approved, err := svc.Approve(t.Context(), jastiperID, o.ID, 25_000, 15_000)
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	if *approved.Total != 90_000 || !approved.PaymentDeadline.Equal(c.t.Add(paymentWindow)) {
		t.Errorf("total = %d, deadline = %v", *approved.Total, approved.PaymentDeadline)
	}

	if _, err := svc.Approve(t.Context(), jastiperID, o.ID, 25_000, 15_000); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("Approve(again) error = %v, want ErrInvalidTransition", err)
	}
}

func TestOwnershipIsHiddenAsNotFound(t *testing.T) {
	svc := newTestService(newClock())
	o := createOrder(t, svc)
	stranger := uuid.New()

	if _, err := svc.Approve(t.Context(), stranger, o.ID, 1000, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("Approve(other jastiper) error = %v, want ErrNotFound", err)
	}
	if _, err := svc.Cancel(t.Context(), stranger, o.ID, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("Cancel(other buyer) error = %v, want ErrNotFound", err)
	}
	if _, _, err := svc.Get(t.Context(), Actor{ID: stranger, Type: ActorBuyer}, o.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(other buyer) error = %v, want ErrNotFound", err)
	}
	if _, _, err := svc.Get(t.Context(), Actor{ID: uuid.New(), Type: ActorAdmin}, o.ID); err != nil {
		t.Errorf("Get(admin) error = %v", err)
	}
}

func TestHappyPathRecordsTimeline(t *testing.T) {
	svc := newTestService(newClock())
	ctx := t.Context()
	o := createOrder(t, svc)

	steps := []func() (Order, error){
		func() (Order, error) { return svc.Approve(ctx, jastiperID, o.ID, 10_000, 5_000) },
		func() (Order, error) { return svc.MarkPaid(ctx, o.ID) },
		func() (Order, error) { return svc.MarkShipped(ctx, jastiperID, o.ID) },
		func() (Order, error) { return svc.MarkDelivered(ctx, Actor{ID: buyerID, Type: ActorBuyer}, o.ID) },
		func() (Order, error) { return svc.MarkReleased(ctx, o.ID) },
	}
	for i, step := range steps {
		if _, err := step(); err != nil {
			t.Fatalf("step %d error = %v", i, err)
		}
	}

	got, events, err := svc.Get(ctx, Actor{ID: buyerID, Type: ActorBuyer}, o.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != StatusReleased || len(events) != 6 {
		t.Fatalf("status = %s, %d events; want released and 6 events", got.Status, len(events))
	}
	if events[0].FromStatus != nil || events[1].ActorType != ActorJastiper || events[2].ActorID != nil {
		t.Errorf("events = %+v", events)
	}
}

func TestInvalidTransitions(t *testing.T) {
	svc := newTestService(newClock())
	ctx := t.Context()
	o := createOrder(t, svc)

	if _, err := svc.MarkPaid(ctx, o.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("MarkPaid(requested) error = %v", err)
	}
	if _, err := svc.MarkShipped(ctx, jastiperID, o.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("MarkShipped(requested) error = %v", err)
	}
	_, _ = svc.Approve(ctx, jastiperID, o.ID, 10_000, 0)
	_, _ = svc.MarkPaid(ctx, o.ID)
	if _, err := svc.Cancel(ctx, buyerID, o.ID, ""); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("Cancel(paid) error = %v, want ErrInvalidTransition", err)
	}
	if _, err := svc.MarkDelivered(ctx, Actor{ID: uuid.New(), Type: ActorAdmin}, o.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("MarkDelivered(admin, paid) error = %v, want ErrInvalidTransition", err)
	}
}

func TestUnpaidOrderExpiresOnRead(t *testing.T) {
	c := newClock()
	svc := newTestService(c)
	o := createOrder(t, svc)
	_, _ = svc.Approve(t.Context(), jastiperID, o.ID, 10_000, 0)

	c.t = c.t.Add(paymentWindow)
	page, err := svc.List(t.Context(), buyerID, ListParams{Limit: 20})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Status != StatusCancelled {
		t.Fatalf("items = %+v, want one cancelled order", page.Items)
	}
	if _, err := svc.MarkPaid(t.Context(), o.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("MarkPaid(expired) error = %v, want ErrInvalidTransition", err)
	}
}

func TestListFiltersByStatus(t *testing.T) {
	svc := newTestService(newClock())
	first := createOrder(t, svc)
	createOrder(t, svc)
	_, _ = svc.Reject(t.Context(), jastiperID, first.ID, "Stok habis")

	page, err := svc.List(t.Context(), jastiperID, ListParams{Status: StatusRejected, Limit: 20})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].StatusReason != "Stok habis" {
		t.Errorf("items = %+v, want the rejected order", page.Items)
	}
}
