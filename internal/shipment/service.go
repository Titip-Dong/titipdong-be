// Package shipment implements F05 Shipment: the jastiper hands the item over and
// the buyer confirms receipt, which releases the escrow.
package shipment

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/notification"
	"github.com/Titip-Dong/titipdong-be/internal/order"
	"github.com/Titip-Dong/titipdong-be/internal/payment"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Method string

const (
	MethodCOD     Method = "cod"
	MethodCourier Method = "courier"
)

var ErrNotFound = errors.New("shipment not found")

type InputError struct {
	Detail string
}

func (e *InputError) Error() string {
	return "invalid input: " + e.Detail
}

type Shipment struct {
	OrderID        uuid.UUID
	OrderStatus    order.Status
	Method         Method
	CourierCode    string
	TrackingNumber string
	HandoverNote   string
	ShippedAt      time.Time
	DeliveredAt    *time.Time
}

type ShipParams struct {
	Method         Method
	CourierCode    string
	TrackingNumber string
	HandoverNote   string
}

type Service interface {
	Ship(ctx context.Context, jastiperID, orderID uuid.UUID, p ShipParams) (Shipment, error)
	Get(ctx context.Context, actor order.Actor, orderID uuid.UUID) (Shipment, error)
	ConfirmReceipt(ctx context.Context, buyerID, orderID uuid.UUID) (Shipment, error)
}

func NewService(pool *pgxpool.Pool, orders order.Service, payments payment.Service, notifier notification.Service) Service {
	return newService(newRepository(pool), orders, payments, notifier, time.Now)
}

type service struct {
	repo     repository
	orders   order.Service
	payments payment.Service
	notifier notification.Service
	now      func() time.Time
}

func newService(repo repository, orders order.Service, payments payment.Service, notifier notification.Service, now func() time.Time) *service {
	return &service{repo: repo, orders: orders, payments: payments, notifier: notifier, now: now}
}

func (s *service) Ship(ctx context.Context, jastiperID, orderID uuid.UUID, p ShipParams) (Shipment, error) {
	sh := Shipment{
		OrderID:        orderID,
		Method:         p.Method,
		CourierCode:    strings.ToLower(strings.TrimSpace(p.CourierCode)),
		TrackingNumber: strings.TrimSpace(p.TrackingNumber),
		HandoverNote:   strings.TrimSpace(p.HandoverNote),
		ShippedAt:      s.now(),
	}
	switch sh.Method {
	case MethodCOD:
		sh.CourierCode, sh.TrackingNumber = "", ""
	case MethodCourier:
		if sh.CourierCode == "" || sh.TrackingNumber == "" {
			return Shipment{}, &InputError{Detail: "Courier and tracking number are required for courier shipments."}
		}
	default:
		return Shipment{}, &InputError{Detail: "Method must be cod or courier."}
	}

	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		o, err := s.orders.MarkShipped(ctx, jastiperID, orderID)
		if err != nil {
			return err
		}
		sh.OrderStatus = o.Status
		if err := s.repo.Create(ctx, sh); err != nil {
			return err
		}
		return s.notifier.Notify(ctx, notification.Event{
			Type:       notification.TypeOrderShipped,
			Recipients: []uuid.UUID{o.BuyerID},
			Data: map[string]any{
				"orderId": o.ID, "orderCode": o.Code, "method": sh.Method, "trackingNumber": sh.TrackingNumber,
			},
		})
	})
	return sh, err
}

func (s *service) Get(ctx context.Context, actor order.Actor, orderID uuid.UUID) (Shipment, error) {
	o, _, err := s.orders.Get(ctx, actor, orderID)
	if err != nil {
		return Shipment{}, err
	}
	sh, err := s.repo.ByOrder(ctx, orderID)
	if err != nil {
		return Shipment{}, err
	}
	sh.OrderStatus = o.Status
	return sh, nil
}

func (s *service) ConfirmReceipt(ctx context.Context, buyerID, orderID uuid.UUID) (Shipment, error) {
	var sh Shipment
	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		if _, err := s.orders.MarkDelivered(ctx, order.Actor{ID: buyerID, Type: order.ActorBuyer}, orderID); err != nil {
			return err
		}
		deliveredAt := s.now()
		if err := s.repo.MarkDelivered(ctx, orderID, deliveredAt); err != nil {
			return err
		}
		o, err := s.payments.Release(ctx, orderID)
		if err != nil {
			return err
		}
		sh, err = s.repo.ByOrder(ctx, orderID)
		sh.OrderStatus = o.Status
		return err
	})
	return sh, err
}
