// Package order implements F03 Order and owns the order state machine. Other
// modules change an order's status only through the Mark* methods.
package order

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/notification"
	"github.com/Titip-Dong/titipdong-be/internal/platform/pagination"
	"github.com/Titip-Dong/titipdong-be/internal/trip"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Status string

const (
	StatusRequested Status = "requested"
	StatusApproved  Status = "approved"
	StatusRejected  Status = "rejected"
	StatusCancelled Status = "cancelled"
	StatusPaid      Status = "paid"
	StatusShipped   Status = "shipped"
	StatusDelivered Status = "delivered"
	StatusDisputed  Status = "disputed"
	StatusReleased  Status = "released"
	StatusRefunded  Status = "refunded"
)

type ActorType string

const (
	ActorBuyer    ActorType = "buyer"
	ActorJastiper ActorType = "jastiper"
	ActorAdmin    ActorType = "admin"
	ActorSystem   ActorType = "system"
)

type Actor struct {
	ID   uuid.UUID
	Type ActorType
}

var System = Actor{Type: ActorSystem}

const (
	paymentWindow  = 24 * time.Hour
	expiryBatch    = 100
	deadlineReason = "Payment deadline passed."
)

var (
	ErrNotFound          = errors.New("order not found")
	ErrInvalidTransition = errors.New("order status does not allow this action")
	ErrTripClosed        = errors.New("trip does not accept requests")
	errCodeTaken         = errors.New("order code taken")
)

type InputError struct {
	Detail string
}

func (e *InputError) Error() string {
	return "invalid input: " + e.Detail
}

type Order struct {
	ID              uuid.UUID
	Code            string
	TripID          uuid.UUID
	TripSlug        string
	TripTitle       string
	BuyerID         uuid.UUID
	BuyerName       string
	JastiperID      uuid.UUID
	JastiperName    string
	ProductName     string
	ProductURL      string
	Quantity        int
	Notes           string
	ItemPrice       *int64
	JastipFee       *int64
	Total           *int64
	Status          Status
	StatusReason    string
	PaymentDeadline *time.Time
	CreatedAt       time.Time
}

type Event struct {
	FromStatus *Status
	ToStatus   Status
	ActorID    *uuid.UUID
	ActorType  ActorType
	Reason     string
	CreatedAt  time.Time
}

type CreateParams struct {
	ProductName string
	ProductURL  string
	Quantity    int
	Notes       string
}

type ListParams struct {
	Status Status
	TripID *uuid.UUID
	Limit  int
	Cursor string
}

type Page struct {
	Items      []Order
	NextCursor string
}

type transition struct {
	from, to Status
	actor    ActorType
}

var transitions = []transition{
	{StatusRequested, StatusApproved, ActorJastiper},
	{StatusRequested, StatusRejected, ActorJastiper},
	{StatusRequested, StatusCancelled, ActorBuyer},
	{StatusApproved, StatusCancelled, ActorBuyer},
	{StatusApproved, StatusCancelled, ActorSystem},
	{StatusApproved, StatusPaid, ActorSystem},
	{StatusPaid, StatusShipped, ActorJastiper},
	{StatusPaid, StatusRefunded, ActorJastiper},
	{StatusShipped, StatusDelivered, ActorBuyer},
	{StatusShipped, StatusDelivered, ActorSystem},
	{StatusShipped, StatusDisputed, ActorBuyer},
	{StatusDisputed, StatusDelivered, ActorAdmin},
	{StatusDisputed, StatusRefunded, ActorAdmin},
	{StatusDelivered, StatusReleased, ActorSystem},
}

func allowed(from, to Status, actor ActorType) bool {
	return slices.Contains(transitions, transition{from, to, actor})
}

type Service interface {
	Create(ctx context.Context, buyerID uuid.UUID, tripSlug string, p CreateParams) (Order, error)
	List(ctx context.Context, userID uuid.UUID, p ListParams) (Page, error)
	// Get returns ErrNotFound unless the actor is the order's buyer or jastiper, or an admin.
	Get(ctx context.Context, actor Actor, id uuid.UUID) (Order, []Event, error)
	Approve(ctx context.Context, jastiperID, id uuid.UUID, itemPrice, jastipFee int64) (Order, error)
	Reject(ctx context.Context, jastiperID, id uuid.UUID, reason string) (Order, error)
	Cancel(ctx context.Context, buyerID, id uuid.UUID, reason string) (Order, error)

	// Lock returns the order locked for update; ctx must carry a transaction.
	Lock(ctx context.Context, id uuid.UUID) (Order, error)
	MarkPaid(ctx context.Context, id uuid.UUID) (Order, error)
	MarkShipped(ctx context.Context, jastiperID, id uuid.UUID) (Order, error)
	MarkDelivered(ctx context.Context, actor Actor, id uuid.UUID) (Order, error)
	MarkDisputed(ctx context.Context, buyerID, id uuid.UUID, reason string) (Order, error)
	MarkReleased(ctx context.Context, id uuid.UUID) (Order, error)
	MarkRefunded(ctx context.Context, actor Actor, id uuid.UUID, reason string) (Order, error)
	// ExpireUnpaid cancels approved orders past their payment deadline, one transaction each; it backs the
	// orders.expire_unpaid job and the read paths.
	ExpireUnpaid(ctx context.Context) error
}

func NewService(pool *pgxpool.Pool, trips trip.Service, notifier notification.Service) Service {
	return newService(newRepository(pool), trips, notifier, time.Now)
}

type service struct {
	repo     repository
	trips    trip.Service
	notifier notification.Service
	now      func() time.Time
}

func newService(repo repository, trips trip.Service, notifier notification.Service, now func() time.Time) *service {
	return &service{repo: repo, trips: trips, notifier: notifier, now: now}
}

func (s *service) Create(ctx context.Context, buyerID uuid.UUID, tripSlug string, p CreateParams) (Order, error) {
	t, err := s.trips.BySlug(ctx, tripSlug)
	if err != nil {
		return Order{}, err
	}
	if t.JastiperID == buyerID {
		return Order{}, &InputError{Detail: "You cannot order from your own trip."}
	}
	o := Order{
		ID:          uuid.Must(uuid.NewV7()),
		TripID:      t.ID,
		BuyerID:     buyerID,
		JastiperID:  t.JastiperID,
		ProductName: strings.TrimSpace(p.ProductName),
		ProductURL:  strings.TrimSpace(p.ProductURL),
		Quantity:    p.Quantity,
		Notes:       strings.TrimSpace(p.Notes),
		Status:      StatusRequested,
	}
	if o.ProductName == "" {
		return Order{}, &InputError{Detail: "Product name must not be blank."}
	}

	for range 3 {
		o.Code = newCode()
		err = s.repo.WithTx(ctx, func(ctx context.Context) error {
			if err := s.trips.Reserve(ctx, t.ID, o.Quantity); err != nil {
				if errors.Is(err, trip.ErrTripClosed) {
					return ErrTripClosed
				}
				return err
			}
			if err := s.repo.CreateOrder(ctx, o); err != nil {
				return err
			}
			if err := s.repo.InsertEvent(ctx, o.ID, Event{ToStatus: StatusRequested, ActorID: &buyerID, ActorType: ActorBuyer}); err != nil {
				return err
			}
			created, err := s.repo.Order(ctx, o.ID)
			if err != nil {
				return err
			}
			return s.notifier.Notify(ctx, notification.Event{
				Type:       notification.TypeOrderRequested,
				Recipients: []uuid.UUID{created.JastiperID},
				Data: map[string]any{
					"orderId": created.ID, "orderCode": created.Code,
					"productName": created.ProductName, "buyerName": created.BuyerName,
				},
			})
		})
		if !errors.Is(err, errCodeTaken) {
			break
		}
	}
	if err != nil {
		return Order{}, err
	}
	return s.repo.Order(ctx, o.ID)
}

func (s *service) List(ctx context.Context, userID uuid.UUID, p ListParams) (Page, error) {
	before, err := pagination.Decode(p.Cursor, pagination.Newest)
	if err != nil {
		return Page{}, err
	}
	if err := s.ExpireUnpaid(ctx); err != nil {
		return Page{}, err
	}
	orders, err := s.repo.List(ctx, userID, p.Status, p.TripID, before, p.Limit+1)
	if err != nil {
		return Page{}, err
	}
	items, next := pagination.Page(orders, p.Limit, func(o Order) pagination.Cursor {
		return pagination.Cursor{At: o.CreatedAt, ID: o.ID}
	})
	return Page{Items: items, NextCursor: next}, nil
}

func (s *service) Get(ctx context.Context, actor Actor, id uuid.UUID) (Order, []Event, error) {
	if err := s.expireOne(ctx, id); err != nil {
		return Order{}, nil, err
	}
	o, err := s.repo.Order(ctx, id)
	if err != nil {
		return Order{}, nil, err
	}
	if !canSee(o, actor) {
		return Order{}, nil, ErrNotFound
	}
	events, err := s.repo.Events(ctx, id)
	if err != nil {
		return Order{}, nil, err
	}
	return o, events, nil
}

func (s *service) Approve(ctx context.Context, jastiperID, id uuid.UUID, itemPrice, jastipFee int64) (Order, error) {
	if itemPrice < 1 || jastipFee < 0 {
		return Order{}, &InputError{Detail: "Item price must be positive and the jastip fee must not be negative."}
	}
	actor := Actor{ID: jastiperID, Type: ActorJastiper}
	return s.transition(ctx, actor, id, StatusApproved, "", func(o *Order) {
		total := itemPrice*int64(o.Quantity) + jastipFee
		deadline := s.now().Add(paymentWindow)
		o.ItemPrice, o.JastipFee, o.Total, o.PaymentDeadline = &itemPrice, &jastipFee, &total, &deadline
	}, func(o Order) notification.Event {
		return notification.Event{
			Type:       notification.TypeOrderApproved,
			Recipients: []uuid.UUID{o.BuyerID},
			Data: map[string]any{
				"orderId": o.ID, "orderCode": o.Code, "total": *o.Total, "paymentDeadline": *o.PaymentDeadline,
			},
		}
	})
}

func (s *service) Reject(ctx context.Context, jastiperID, id uuid.UUID, reason string) (Order, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Order{}, &InputError{Detail: "Reason must not be blank."}
	}
	return s.transition(ctx, Actor{ID: jastiperID, Type: ActorJastiper}, id, StatusRejected, reason, nil,
		func(o Order) notification.Event {
			return notification.Event{
				Type:       notification.TypeOrderRejected,
				Recipients: []uuid.UUID{o.BuyerID},
				Data:       map[string]any{"orderId": o.ID, "orderCode": o.Code, "reason": reason},
			}
		})
}

func (s *service) Cancel(ctx context.Context, buyerID, id uuid.UUID, reason string) (Order, error) {
	reason = strings.TrimSpace(reason)
	return s.transition(ctx, Actor{ID: buyerID, Type: ActorBuyer}, id, StatusCancelled, reason, nil,
		func(o Order) notification.Event { return cancelledEvent(o, []uuid.UUID{o.JastiperID}) })
}

func (s *service) Lock(ctx context.Context, id uuid.UUID) (Order, error) {
	return s.repo.Lock(ctx, id)
}

func (s *service) MarkPaid(ctx context.Context, id uuid.UUID) (Order, error) {
	return s.transition(ctx, System, id, StatusPaid, "", nil, nil)
}

func (s *service) MarkShipped(ctx context.Context, jastiperID, id uuid.UUID) (Order, error) {
	return s.transition(ctx, Actor{ID: jastiperID, Type: ActorJastiper}, id, StatusShipped, "", nil, nil)
}

func (s *service) MarkDelivered(ctx context.Context, actor Actor, id uuid.UUID) (Order, error) {
	return s.transition(ctx, actor, id, StatusDelivered, "", nil, nil)
}

func (s *service) MarkDisputed(ctx context.Context, buyerID, id uuid.UUID, reason string) (Order, error) {
	return s.transition(ctx, Actor{ID: buyerID, Type: ActorBuyer}, id, StatusDisputed, reason, nil, nil)
}

func (s *service) MarkReleased(ctx context.Context, id uuid.UUID) (Order, error) {
	return s.transition(ctx, System, id, StatusReleased, "", nil, nil)
}

func (s *service) MarkRefunded(ctx context.Context, actor Actor, id uuid.UUID, reason string) (Order, error) {
	return s.transition(ctx, actor, id, StatusRefunded, reason, nil, nil)
}

// transition locks the order, checks ownership and the transition table, then
// writes the new status and an event in one transaction.
func (s *service) transition(ctx context.Context, actor Actor, id uuid.UUID, to Status, reason string, apply func(*Order), event func(Order) notification.Event) (Order, error) {
	var out Order
	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		o, err := s.repo.Lock(ctx, id)
		if err != nil {
			return err
		}
		if !canSee(o, actor) {
			return ErrNotFound
		}
		if !allowed(o.Status, to, actor.Type) {
			return ErrInvalidTransition
		}
		from := o.Status
		o.Status, o.StatusReason = to, reason
		if apply != nil {
			apply(&o)
		}
		if err := s.repo.UpdateStatus(ctx, o); err != nil {
			return err
		}
		if releasesCapacity(to) {
			if err := s.trips.Release(ctx, o.TripID, o.Quantity); err != nil {
				return err
			}
		}
		e := Event{FromStatus: &from, ToStatus: to, ActorType: actor.Type, Reason: reason}
		if actor.Type != ActorSystem {
			e.ActorID = &actor.ID
		}
		if err := s.repo.InsertEvent(ctx, o.ID, e); err != nil {
			return err
		}
		out = o
		if event == nil {
			return nil
		}
		return s.notifier.Notify(ctx, event(o))
	})
	return out, err
}

func (s *service) ExpireUnpaid(ctx context.Context) error {
	due, err := s.repo.DueUnpaid(ctx, s.now(), expiryBatch)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range due {
		if err := s.expireOne(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("expire order %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// expireOne cancels the order if it is still approved past its deadline; anything else is a no-op.
func (s *service) expireOne(ctx context.Context, id uuid.UUID) error {
	return s.repo.WithTx(ctx, func(ctx context.Context) error {
		o, err := s.repo.Lock(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if o.Status != StatusApproved || o.PaymentDeadline == nil || s.now().Before(*o.PaymentDeadline) {
			return nil
		}
		_, err = s.transition(ctx, System, id, StatusCancelled, deadlineReason, nil, func(o Order) notification.Event {
			return cancelledEvent(o, []uuid.UUID{o.BuyerID, o.JastiperID})
		})
		return err
	})
}

// releasesCapacity reports whether an order leaves the trip without being carried.
func releasesCapacity(to Status) bool {
	return to == StatusRejected || to == StatusCancelled || to == StatusRefunded
}

func cancelledEvent(o Order, recipients []uuid.UUID) notification.Event {
	return notification.Event{
		Type:       notification.TypeOrderCancelled,
		Recipients: recipients,
		Data:       map[string]any{"orderId": o.ID, "orderCode": o.Code, "reason": o.StatusReason},
	}
}

func canSee(o Order, actor Actor) bool {
	switch actor.Type {
	case ActorBuyer:
		return o.BuyerID == actor.ID
	case ActorJastiper:
		return o.JastiperID == actor.ID
	case ActorAdmin, ActorSystem:
		return true
	}
	return false
}

func newCode() string {
	const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return "TD-" + string(b)
}
