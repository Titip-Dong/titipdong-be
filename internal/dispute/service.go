// Package dispute implements F06 Dispute: the buyer contests a shipped order and an admin decides refund or release.
package dispute

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/notification"
	"github.com/Titip-Dong/titipdong-be/internal/order"
	"github.com/Titip-Dong/titipdong-be/internal/payment"
	"github.com/Titip-Dong/titipdong-be/internal/platform/audit"
	"github.com/Titip-Dong/titipdong-be/internal/platform/pagination"
	"github.com/Titip-Dong/titipdong-be/internal/upload"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Category string

const (
	CategoryNotReceived    Category = "not_received"
	CategoryNotAsDescribed Category = "not_as_described"
	CategoryDamaged        Category = "damaged"
	CategoryOther          Category = "other"
)

type Status string

const (
	StatusOpen     Status = "open"
	StatusResolved Status = "resolved"
)

type Decision string

const (
	DecisionRefund  Decision = "refund"
	DecisionRelease Decision = "release"
)

const maxEvidencePerRequest = 5

var (
	ErrNotFound        = errors.New("dispute not found")
	ErrAlreadyDisputed = errors.New("order already has a dispute")
	ErrResolved        = errors.New("dispute is already resolved")
)

type InputError struct {
	Detail string
}

func (e *InputError) Error() string {
	return "invalid input: " + e.Detail
}

type Dispute struct {
	ID           uuid.UUID
	OrderID      uuid.UUID
	OrderCode    string
	BuyerID      uuid.UUID
	JastiperID   uuid.UUID
	OpenedBy     uuid.UUID
	Category     Category
	Description  string
	Status       Status
	Decision     Decision
	DecisionNote string
	DecidedBy    *uuid.UUID
	DecidedAt    *time.Time
	CreatedAt    time.Time
	Evidence     []Evidence
	Messages     []Message
}

type Evidence struct {
	ID           uuid.UUID
	DisputeID    uuid.UUID
	UploadedBy   uuid.UUID
	UploaderName string
	UploaderRole string
	FileKey      string
	URL          string
	Note         string
	CreatedAt    time.Time
}

type Message struct {
	ID         int64
	AuthorID   uuid.UUID
	AuthorName string
	AuthorRole string
	Body       string
	CreatedAt  time.Time
}

type EvidenceInput struct {
	UploadID uuid.UUID
	Note     string
}

type OpenParams struct {
	Category    Category
	Description string
	Evidence    []EvidenceInput
}

type Page struct {
	Items      []Dispute
	NextCursor string
}

// Detail is the admin view: the dispute with its order and the order's timeline.
type Detail struct {
	Dispute Dispute
	Order   order.Order
	Events  []order.Event
}

type Service interface {
	Open(ctx context.Context, buyerID, orderID uuid.UUID, p OpenParams) (Dispute, error)
	// ForOrder returns ErrNotFound unless the actor is the order's buyer or jastiper, or an admin.
	ForOrder(ctx context.Context, actor order.Actor, orderID uuid.UUID) (Dispute, error)
	AddEvidence(ctx context.Context, actor order.Actor, disputeID uuid.UUID, items []EvidenceInput) (Dispute, error)
	PostMessage(ctx context.Context, actor order.Actor, disputeID uuid.UUID, body string) (Dispute, error)
	List(ctx context.Context, status Status, limit int, cursor string) (Page, error)
	Get(ctx context.Context, adminID, disputeID uuid.UUID) (Detail, error)
	Decide(ctx context.Context, adminID, disputeID uuid.UUID, decision Decision, note string) (Dispute, error)
}

type auditWriter interface {
	Write(ctx context.Context, e audit.Entry) error
}

func NewService(pool *pgxpool.Pool, orders order.Service, payments payment.Service, uploads upload.Service,
	notifier notification.Service) Service {
	return newService(newRepository(pool), orders, payments, uploads, notifier, audit.NewWriter(pool), time.Now)
}

type service struct {
	repo     repository
	orders   order.Service
	payments payment.Service
	uploads  upload.Service
	notifier notification.Service
	audit    auditWriter
	now      func() time.Time
}

func newService(repo repository, orders order.Service, payments payment.Service, uploads upload.Service,
	notifier notification.Service, aw auditWriter, now func() time.Time) *service {
	return &service{repo: repo, orders: orders, payments: payments, uploads: uploads, notifier: notifier, audit: aw, now: now}
}

func (s *service) Open(ctx context.Context, buyerID, orderID uuid.UUID, p OpenParams) (Dispute, error) {
	p.Description = strings.TrimSpace(p.Description)
	switch {
	case !slices.Contains([]Category{CategoryNotReceived, CategoryNotAsDescribed, CategoryDamaged, CategoryOther}, p.Category):
		return Dispute{}, &InputError{Detail: "Category is not supported."}
	case p.Description == "":
		return Dispute{}, &InputError{Detail: "Description must not be blank."}
	case len(p.Evidence) > maxEvidencePerRequest:
		return Dispute{}, &InputError{Detail: "At most 5 evidence files can be added at once."}
	}
	var id uuid.UUID
	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		o, err := s.orders.MarkDisputed(ctx, buyerID, orderID, p.Description)
		if err != nil {
			return err
		}
		d := Dispute{
			ID:          uuid.Must(uuid.NewV7()),
			OrderID:     orderID,
			OpenedBy:    buyerID,
			Category:    p.Category,
			Description: p.Description,
			Status:      StatusOpen,
		}
		id = d.ID
		if err := s.repo.Create(ctx, d); err != nil {
			return err
		}
		if err := s.attachEvidence(ctx, d.ID, buyerID, p.Evidence); err != nil {
			return err
		}
		admins, err := s.notifier.AdminIDs(ctx)
		if err != nil {
			return err
		}
		return s.notifier.Notify(ctx, notification.Event{
			Type:       notification.TypeDisputeOpened,
			Recipients: append([]uuid.UUID{o.JastiperID}, admins...),
			Data:       map[string]any{"disputeId": d.ID, "orderId": o.ID, "orderCode": o.Code, "category": d.Category},
		})
	})
	if err != nil {
		return Dispute{}, err
	}
	return s.load(ctx, s.repo.Dispute, id)
}

func (s *service) ForOrder(ctx context.Context, actor order.Actor, orderID uuid.UUID) (Dispute, error) {
	d, err := s.load(ctx, s.repo.ByOrder, orderID)
	if err != nil {
		return Dispute{}, err
	}
	if !canSee(d, actor) {
		return Dispute{}, ErrNotFound
	}
	return d, nil
}

func (s *service) AddEvidence(ctx context.Context, actor order.Actor, disputeID uuid.UUID, items []EvidenceInput) (Dispute, error) {
	switch {
	case len(items) == 0:
		return Dispute{}, &InputError{Detail: "Add at least one evidence file."}
	case len(items) > maxEvidencePerRequest:
		return Dispute{}, &InputError{Detail: "At most 5 evidence files can be added at once."}
	}
	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		d, err := s.lockOpen(ctx, actor, disputeID)
		if err != nil {
			return err
		}
		return s.attachEvidence(ctx, d.ID, actor.ID, items)
	})
	if err != nil {
		return Dispute{}, err
	}
	return s.load(ctx, s.repo.Dispute, disputeID)
}

func (s *service) PostMessage(ctx context.Context, actor order.Actor, disputeID uuid.UUID, body string) (Dispute, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return Dispute{}, &InputError{Detail: "Message must not be blank."}
	}
	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		d, err := s.lockOpen(ctx, actor, disputeID)
		if err != nil {
			return err
		}
		if err := s.repo.InsertMessage(ctx, d.ID, actor.ID, body); err != nil {
			return err
		}
		var others []uuid.UUID
		for _, id := range []uuid.UUID{d.BuyerID, d.JastiperID} {
			if id != actor.ID {
				others = append(others, id)
			}
		}
		return s.notifier.Notify(ctx, notification.Event{
			Type:       notification.TypeDisputeMessage,
			Recipients: others,
			Data:       map[string]any{"disputeId": d.ID, "orderId": d.OrderID, "orderCode": d.OrderCode},
		})
	})
	if err != nil {
		return Dispute{}, err
	}
	return s.load(ctx, s.repo.Dispute, disputeID)
}

func (s *service) List(ctx context.Context, status Status, limit int, cursor string) (Page, error) {
	after, err := pagination.Decode(cursor, pagination.Cursor{})
	if err != nil {
		return Page{}, err
	}
	if status == "" {
		status = StatusOpen
	}
	rows, err := s.repo.List(ctx, status, after, limit+1)
	if err != nil {
		return Page{}, err
	}
	items, next := pagination.Page(rows, limit, func(d Dispute) pagination.Cursor {
		return pagination.Cursor{At: d.CreatedAt, ID: d.ID}
	})
	return Page{Items: items, NextCursor: next}, nil
}

func (s *service) Get(ctx context.Context, adminID, disputeID uuid.UUID) (Detail, error) {
	d, err := s.load(ctx, s.repo.Dispute, disputeID)
	if err != nil {
		return Detail{}, err
	}
	o, events, err := s.orders.Get(ctx, order.Actor{ID: adminID, Type: order.ActorAdmin}, d.OrderID)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Dispute: d, Order: o, Events: events}, nil
}

func (s *service) Decide(ctx context.Context, adminID, disputeID uuid.UUID, decision Decision, note string) (Dispute, error) {
	note = strings.TrimSpace(note)
	switch {
	case decision != DecisionRefund && decision != DecisionRelease:
		return Dispute{}, &InputError{Detail: "Decision must be refund or release."}
	case note == "":
		return Dispute{}, &InputError{Detail: "Note must not be blank."}
	}
	admin := order.Actor{ID: adminID, Type: order.ActorAdmin}
	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		unlocked, err := s.repo.Dispute(ctx, disputeID)
		if err != nil {
			return err
		}
		// Order first, then dispute: the same lock order as every other money path.
		if _, err := s.orders.Lock(ctx, unlocked.OrderID); err != nil {
			return err
		}
		d, err := s.repo.Lock(ctx, disputeID)
		if err != nil {
			return err
		}
		if d.Status != StatusOpen {
			return ErrResolved
		}
		at := s.now()
		d.Status, d.Decision, d.DecisionNote, d.DecidedBy, d.DecidedAt = StatusResolved, decision, note, &adminID, &at
		if err := s.repo.Resolve(ctx, d); err != nil {
			return err
		}
		if decision == DecisionRefund {
			if _, err := s.payments.Refund(ctx, admin, d.OrderID, payment.RefundDispute, note); err != nil {
				return err
			}
		} else {
			if _, err := s.orders.MarkDelivered(ctx, admin, d.OrderID); err != nil {
				return err
			}
			if _, err := s.payments.Release(ctx, d.OrderID); err != nil {
				return err
			}
		}
		err = s.audit.Write(ctx, audit.Entry{
			ActorID:    &adminID,
			ActorType:  string(order.ActorAdmin),
			Action:     "dispute.decided",
			TargetType: "dispute",
			TargetID:   d.ID.String(),
			Metadata:   map[string]any{"orderId": d.OrderID, "decision": decision, "note": note},
		})
		if err != nil {
			return err
		}
		return s.notifier.Notify(ctx, notification.Event{
			Type:       notification.TypeDisputeResolved,
			Recipients: []uuid.UUID{d.BuyerID, d.JastiperID},
			Data:       map[string]any{"disputeId": d.ID, "orderId": d.OrderID, "orderCode": d.OrderCode, "decision": decision},
		})
	})
	if err != nil {
		return Dispute{}, err
	}
	return s.load(ctx, s.repo.Dispute, disputeID)
}

// lockOpen locks a dispute the actor takes part in and checks that it is still open.
func (s *service) lockOpen(ctx context.Context, actor order.Actor, disputeID uuid.UUID) (Dispute, error) {
	d, err := s.repo.Lock(ctx, disputeID)
	if err != nil {
		return Dispute{}, err
	}
	if !canSee(d, actor) {
		return Dispute{}, ErrNotFound
	}
	if d.Status != StatusOpen {
		return Dispute{}, ErrResolved
	}
	return d, nil
}

func (s *service) attachEvidence(ctx context.Context, disputeID, ownerID uuid.UUID, items []EvidenceInput) error {
	for _, it := range items {
		key, err := s.uploads.Attach(ctx, ownerID, it.UploadID, upload.PurposeDisputeEvidence)
		if err != nil {
			return err
		}
		err = s.repo.InsertEvidence(ctx, Evidence{
			ID:         uuid.Must(uuid.NewV7()),
			DisputeID:  disputeID,
			UploadedBy: ownerID,
			FileKey:    key,
			Note:       strings.TrimSpace(it.Note),
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// load reads a dispute with its evidence (signed URLs) and messages.
func (s *service) load(ctx context.Context, get func(context.Context, uuid.UUID) (Dispute, error), id uuid.UUID) (Dispute, error) {
	d, err := get(ctx, id)
	if err != nil {
		return Dispute{}, err
	}
	if d.Evidence, err = s.repo.Evidence(ctx, d.ID); err != nil {
		return Dispute{}, err
	}
	for i := range d.Evidence {
		d.Evidence[i].URL = s.uploads.ReadURL(d.Evidence[i].FileKey)
	}
	if d.Messages, err = s.repo.Messages(ctx, d.ID); err != nil {
		return Dispute{}, err
	}
	return d, nil
}

func canSee(d Dispute, actor order.Actor) bool {
	switch actor.Type {
	case order.ActorBuyer:
		return d.BuyerID == actor.ID
	case order.ActorJastiper:
		return d.JastiperID == actor.ID
	case order.ActorAdmin:
		return true
	}
	return false
}
