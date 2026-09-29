// Package payment implements F04 Payment & Escrow. It is the only writer of the ledger.
package payment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/order"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Status string

const (
	StatusPending Status = "pending"
	StatusPaid    Status = "paid"
	StatusExpired Status = "expired"
)

const platformFeePercent = 5

var (
	ErrNotFound         = errors.New("payment not found")
	ErrOrderNotPayable  = errors.New("order is not awaiting payment")
	ErrDeadlinePassed   = errors.New("payment deadline passed")
	ErrNotPending       = errors.New("payment is not pending")
	ErrSimulateDisabled = errors.New("payment simulation is disabled")
)

type Payment struct {
	ID            uuid.UUID
	OrderID       uuid.UUID
	Amount        int64
	Provider      string
	Status        Status
	CheckoutToken string
	CheckoutURL   string
	ExpiresAt     time.Time
	PaidAt        *time.Time
}

type Service interface {
	// Checkout returns the order's pending payment, creating one when there is none.
	Checkout(ctx context.Context, buyerID, orderID uuid.UUID) (Payment, error)
	// Settle records a successful payment: ledger into escrow and the order to paid.
	// Settling an already paid payment is a no-op.
	Settle(ctx context.Context, paymentID uuid.UUID, providerTransactionID string) (Payment, error)
	SimulatePaid(ctx context.Context, buyerID, paymentID uuid.UUID) (Payment, error)
	// Release moves a delivered order's escrow to the jastiper and the platform fee.
	Release(ctx context.Context, orderID uuid.UUID) (order.Order, error)
	JastiperBalance(ctx context.Context, jastiperID uuid.UUID) (int64, error)
}

func NewService(pool *pgxpool.Pool, orders order.Service, gw Gateway) Service {
	return newService(newRepository(pool), orders, gw, time.Now)
}

type service struct {
	repo    repository
	orders  order.Service
	gateway Gateway
	now     func() time.Time
}

func newService(repo repository, orders order.Service, gw Gateway, now func() time.Time) *service {
	return &service{repo: repo, orders: orders, gateway: gw, now: now}
}

func (s *service) Checkout(ctx context.Context, buyerID, orderID uuid.UUID) (Payment, error) {
	var p Payment
	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		o, err := s.orders.Lock(ctx, orderID)
		if err != nil {
			return err
		}
		if o.BuyerID != buyerID {
			return order.ErrNotFound
		}
		if o.Status != order.StatusApproved {
			return ErrOrderNotPayable
		}
		now := s.now()
		if !now.Before(*o.PaymentDeadline) {
			return ErrDeadlinePassed
		}

		existing, err := s.repo.PendingByOrder(ctx, orderID)
		switch {
		case err == nil && now.Before(existing.ExpiresAt):
			p = existing
			return nil
		case err == nil:
			if err := s.repo.MarkExpired(ctx, existing.ID); err != nil {
				return err
			}
		case !errors.Is(err, ErrNotFound):
			return err
		}

		p = Payment{
			ID:        uuid.Must(uuid.NewV7()),
			OrderID:   orderID,
			Amount:    *o.Total,
			Provider:  s.gateway.Name(),
			Status:    StatusPending,
			ExpiresAt: *o.PaymentDeadline,
		}
		if err := s.repo.Create(ctx, p); err != nil {
			return err
		}
		c, err := s.gateway.CreateCheckout(ctx, CheckoutRequest{
			PaymentID: p.ID,
			OrderCode: o.Code,
			Amount:    p.Amount,
			ExpiresAt: p.ExpiresAt,
		})
		if err != nil {
			return fmt.Errorf("create checkout: %w", err)
		}
		p.CheckoutToken, p.CheckoutURL = c.Token, c.RedirectURL
		return s.repo.SetCheckout(ctx, p.ID, c.Token, c.RedirectURL)
	})
	return p, err
}

func (s *service) Settle(ctx context.Context, paymentID uuid.UUID, providerTransactionID string) (Payment, error) {
	var p Payment
	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		var err error
		p, err = s.repo.Lock(ctx, paymentID)
		if err != nil {
			return err
		}
		if p.Status == StatusPaid {
			return nil
		}
		if p.Status != StatusPending {
			return ErrNotPending
		}
		paidAt := s.now()
		if err := s.repo.MarkPaid(ctx, p.ID, providerTransactionID, paidAt); err != nil {
			return err
		}
		p.Status, p.PaidAt = StatusPaid, &paidAt

		err = s.post(ctx, ledgerTransaction{
			Kind:           "payment_received",
			OrderID:        p.OrderID,
			IdempotencyKey: "payment:" + p.ID.String(),
			Entries: []entry{
				{Account: account{Kind: accountGatewayClearing}, Direction: debit, Amount: p.Amount},
				{Account: account{Kind: accountEscrow}, Direction: credit, Amount: p.Amount},
			},
		})
		if err != nil {
			return err
		}
		_, err = s.orders.MarkPaid(ctx, p.OrderID)
		return err
	})
	return p, err
}

func (s *service) SimulatePaid(ctx context.Context, buyerID, paymentID uuid.UUID) (Payment, error) {
	if s.gateway.Name() != fakeProvider {
		return Payment{}, ErrSimulateDisabled
	}
	var p Payment
	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		existing, err := s.repo.Lock(ctx, paymentID)
		if err != nil {
			return err
		}
		o, err := s.orders.Lock(ctx, existing.OrderID)
		if err != nil {
			return err
		}
		if o.BuyerID != buyerID {
			return ErrNotFound
		}
		p, err = s.Settle(ctx, paymentID, "fake-"+paymentID.String())
		return err
	})
	return p, err
}

func (s *service) Release(ctx context.Context, orderID uuid.UUID) (order.Order, error) {
	var released order.Order
	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		o, err := s.orders.Lock(ctx, orderID)
		if err != nil {
			return err
		}
		if o.Status != order.StatusDelivered {
			return order.ErrInvalidTransition
		}
		total := *o.Total
		fee := total * platformFeePercent / 100
		err = s.post(ctx, ledgerTransaction{
			Kind:           "release",
			OrderID:        o.ID,
			IdempotencyKey: "release:" + o.ID.String(),
			Entries: []entry{
				{Account: account{Kind: accountEscrow}, Direction: debit, Amount: total},
				{Account: account{Kind: accountJastiperPayable, UserID: &o.JastiperID}, Direction: credit, Amount: total - fee},
				{Account: account{Kind: accountPlatformRevenue}, Direction: credit, Amount: fee},
			},
		})
		if err != nil {
			return err
		}
		released, err = s.orders.MarkReleased(ctx, o.ID)
		return err
	})
	return released, err
}

func (s *service) JastiperBalance(ctx context.Context, jastiperID uuid.UUID) (int64, error) {
	return s.repo.AccountBalance(ctx, account{Kind: accountJastiperPayable, UserID: &jastiperID})
}

func (s *service) post(ctx context.Context, t ledgerTransaction) error {
	return s.repo.Post(ctx, uuid.Must(uuid.NewV7()), t)
}
