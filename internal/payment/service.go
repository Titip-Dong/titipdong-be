// Package payment implements F04 Payment & Escrow. It is the only writer of the ledger.
package payment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/notification"
	"github.com/Titip-Dong/titipdong-be/internal/order"
	"github.com/Titip-Dong/titipdong-be/internal/platform/audit"
	"github.com/Titip-Dong/titipdong-be/internal/platform/pagination"
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

type RefundReason string

const (
	RefundJastiperCancelled RefundReason = "jastiper_cancelled"
	RefundDispute           RefundReason = "dispute_refund"
)

type PayoutStatus string

const (
	PayoutRequested PayoutStatus = "requested"
	PayoutPaid      PayoutStatus = "paid"
	PayoutRejected  PayoutStatus = "rejected"
)

type RefundStatus string

const (
	RefundPending   RefundStatus = "pending"
	RefundSucceeded RefundStatus = "succeeded"
	RefundFailed    RefundStatus = "failed"
)

var (
	ErrNotFound         = errors.New("payment not found")
	ErrOrderNotPayable  = errors.New("order is not awaiting payment")
	ErrDeadlinePassed   = errors.New("payment deadline passed")
	ErrNotPending       = errors.New("payment is not pending")
	ErrSimulateDisabled = errors.New("payment simulation is disabled")

	ErrNoPayoutAccount    = errors.New("no payout account saved")
	ErrPayoutOpen         = errors.New("a payout request is already open")
	ErrInsufficientFunds  = errors.New("amount exceeds the available balance")
	ErrPayoutNotFound     = errors.New("payout not found")
	ErrPayoutNotRequested = errors.New("payout is no longer requested")
)

type InputError struct {
	Detail string
}

func (e *InputError) Error() string {
	return "invalid input: " + e.Detail
}

type Payment struct {
	ID            uuid.UUID
	OrderID       uuid.UUID
	Amount        int64
	Provider      string
	ProviderTxID  string
	Status        Status
	CheckoutToken string
	CheckoutURL   string
	ExpiresAt     time.Time
	PaidAt        *time.Time
}

type PayoutAccount struct {
	UserID        uuid.UUID
	BankName      string
	AccountNumber string
	AccountName   string
	UpdatedAt     time.Time
}

type Payout struct {
	ID              uuid.UUID
	JastiperID      uuid.UUID
	JastiperName    string
	Amount          int64
	BankName        string
	AccountNumber   string
	AccountName     string
	Status          PayoutStatus
	Reference       string
	RejectionReason string
	DecidedBy       *uuid.UUID
	RequestedAt     time.Time
	DecidedAt       *time.Time
}

type PayoutPage struct {
	Items      []Payout
	NextCursor string
}

type Refund struct {
	ID               uuid.UUID
	OrderID          uuid.UUID
	PaymentID        uuid.UUID
	Amount           int64
	Reason           RefundReason
	Status           RefundStatus
	ProviderRefundID string
	FailureReason    string
	CompletedAt      *time.Time
}

type Service interface {
	// Checkout returns the order's pending payment, creating one when there is none.
	Checkout(ctx context.Context, buyerID, orderID uuid.UUID) (Payment, error)
	// Settle records a successful payment: ledger into escrow and the order to paid.
	// Settling an already paid payment is a no-op.
	Settle(ctx context.Context, paymentID uuid.UUID, providerTransactionID string) (Payment, error)
	SimulatePaid(ctx context.Context, buyerID, paymentID uuid.UUID) (Payment, error)
	// Release moves a delivered order's escrow to the jastiper and the platform fee; actor is who delivered it
	// (buyer, admin deciding a dispute, or the system) and is recorded in the audit log.
	Release(ctx context.Context, actor order.Actor, orderID uuid.UUID) (order.Order, error)
	JastiperBalance(ctx context.Context, jastiperID uuid.UUID) (int64, error)
	// Refund returns a paid or disputed order's whole total to the buyer and marks the order refunded.
	// note becomes the order's status reason.
	Refund(ctx context.Context, actor order.Actor, orderID uuid.UUID, reason RefundReason, note string) (Refund, error)
	// CancelPaid is the jastiper cancelling a paid order, which refunds the buyer.
	CancelPaid(ctx context.Context, jastiperID, orderID uuid.UUID, note string) (Refund, error)

	SavePayoutAccount(ctx context.Context, a PayoutAccount) (PayoutAccount, error)
	PayoutAccount(ctx context.Context, userID uuid.UUID) (PayoutAccount, error)
	// RequestPayout copies the saved bank account onto a new request of at most the available balance.
	RequestPayout(ctx context.Context, jastiperID uuid.UUID, amount int64) (Payout, error)
	ListMyPayouts(ctx context.Context, jastiperID uuid.UUID, limit int, cursor string) (PayoutPage, error)
	ListPayouts(ctx context.Context, status PayoutStatus, limit int, cursor string) (PayoutPage, error)
	MarkPayoutPaid(ctx context.Context, adminID, payoutID uuid.UUID, reference string) (Payout, error)
	RejectPayout(ctx context.Context, adminID, payoutID uuid.UUID, reason string) (Payout, error)
}

type auditWriter interface {
	Write(ctx context.Context, e audit.Entry) error
}

func NewService(pool *pgxpool.Pool, orders order.Service, notifier notification.Service, gw Gateway) Service {
	return newService(newRepository(pool), orders, notifier, audit.NewWriter(pool), gw, time.Now)
}

type service struct {
	repo     repository
	orders   order.Service
	notifier notification.Service
	audit    auditWriter
	gateway  Gateway
	now      func() time.Time
}

func newService(repo repository, orders order.Service, notifier notification.Service, aw auditWriter, gw Gateway, now func() time.Time) *service {
	return &service{repo: repo, orders: orders, notifier: notifier, audit: aw, gateway: gw, now: now}
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
		o, err := s.orders.MarkPaid(ctx, p.OrderID)
		if err != nil {
			return err
		}
		return s.notifier.Notify(ctx, notification.Event{
			Type:       notification.TypePaymentReceived,
			Recipients: []uuid.UUID{o.BuyerID, o.JastiperID},
			Data:       map[string]any{"orderId": o.ID, "orderCode": o.Code, "amount": p.Amount},
		})
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

func (s *service) Release(ctx context.Context, actor order.Actor, orderID uuid.UUID) (order.Order, error) {
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
		if err != nil {
			return err
		}
		err = s.notifier.Notify(ctx, notification.Event{
			Type:       notification.TypeOrderReleased,
			Recipients: []uuid.UUID{o.JastiperID},
			Data:       map[string]any{"orderId": o.ID, "orderCode": o.Code, "amount": total - fee},
		})
		if err != nil {
			return err
		}
		return s.audit.Write(ctx, audit.Entry{
			ActorID:    actorID(actor),
			ActorType:  string(actor.Type),
			Action:     "order.released",
			TargetType: "order",
			TargetID:   o.ID.String(),
			Metadata:   map[string]any{"total": total, "fee": fee, "jastiperAmount": total - fee},
		})
	})
	return released, err
}

func (s *service) Refund(ctx context.Context, actor order.Actor, orderID uuid.UUID, reason RefundReason, note string) (Refund, error) {
	var rf Refund
	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		// MarkRefunded checks ownership and the transition before any money moves.
		o, err := s.orders.MarkRefunded(ctx, actor, orderID, note)
		if err != nil {
			return err
		}
		p, err := s.repo.PaidByOrder(ctx, orderID)
		if err != nil {
			return err
		}
		rf = Refund{
			ID:        uuid.Must(uuid.NewV7()),
			OrderID:   orderID,
			PaymentID: p.ID,
			Amount:    p.Amount,
			Reason:    reason,
			Status:    RefundPending,
		}
		if err := s.repo.CreateRefund(ctx, rf); err != nil {
			return err
		}
		err = s.post(ctx, ledgerTransaction{
			Kind:           "refund",
			OrderID:        orderID,
			IdempotencyKey: "refund:" + orderID.String(),
			Entries: []entry{
				{Account: account{Kind: accountEscrow}, Direction: debit, Amount: rf.Amount},
				{Account: account{Kind: accountBuyerRefundable, UserID: &o.BuyerID}, Direction: credit, Amount: rf.Amount},
			},
		})
		if err != nil {
			return err
		}
		if err := s.settleRefund(ctx, &rf, p, o.BuyerID); err != nil {
			return err
		}

		recipients := []uuid.UUID{o.BuyerID, o.JastiperID}
		if actor.Type == order.ActorJastiper {
			recipients = []uuid.UUID{o.BuyerID}
		}
		err = s.notifier.Notify(ctx, notification.Event{
			Type:       notification.TypeOrderRefunded,
			Recipients: recipients,
			Data:       map[string]any{"orderId": o.ID, "orderCode": o.Code, "amount": rf.Amount},
		})
		if err != nil {
			return err
		}
		return s.audit.Write(ctx, audit.Entry{
			ActorID:    actorID(actor),
			ActorType:  string(actor.Type),
			Action:     "order.refunded",
			TargetType: "order",
			TargetID:   orderID.String(),
			Metadata: map[string]any{
				"refundId": rf.ID, "amount": rf.Amount, "reason": rf.Reason, "refundStatus": rf.Status,
			},
		})
	})
	return rf, err
}

// settleRefund pays the refund out through the gateway. A gateway failure is recorded, not returned: the order
// stays refunded and the debt stays in buyer_refundable for an admin to settle.
func (s *service) settleRefund(ctx context.Context, rf *Refund, p Payment, buyerID uuid.UUID) error {
	res, gwErr := s.gateway.Refund(ctx, RefundRequest{
		RefundID: rf.ID, PaymentID: p.ID, ProviderTransactionID: p.ProviderTxID, Amount: rf.Amount,
	})
	at := s.now()
	rf.CompletedAt = &at
	if gwErr != nil {
		rf.Status, rf.FailureReason = RefundFailed, gwErr.Error()
		slog.ErrorContext(ctx, "gateway_refund_failed", "refund_id", rf.ID, "error", gwErr)
		return s.repo.MarkRefundFailed(ctx, rf.ID, rf.FailureReason, at)
	}
	rf.Status, rf.ProviderRefundID = RefundSucceeded, res.ProviderRefundID
	if err := s.repo.MarkRefundSucceeded(ctx, rf.ID, res.ProviderRefundID, at); err != nil {
		return err
	}
	return s.post(ctx, ledgerTransaction{
		Kind:           "refund_paid",
		OrderID:        rf.OrderID,
		IdempotencyKey: "refund_paid:" + rf.ID.String(),
		Entries: []entry{
			{Account: account{Kind: accountBuyerRefundable, UserID: &buyerID}, Direction: debit, Amount: rf.Amount},
			{Account: account{Kind: accountGatewayClearing}, Direction: credit, Amount: rf.Amount},
		},
	})
}

func (s *service) CancelPaid(ctx context.Context, jastiperID, orderID uuid.UUID, note string) (Refund, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		return Refund{}, &InputError{Detail: "Reason must not be blank."}
	}
	return s.Refund(ctx, order.Actor{ID: jastiperID, Type: order.ActorJastiper}, orderID, RefundJastiperCancelled, note)
}

func (s *service) SavePayoutAccount(ctx context.Context, a PayoutAccount) (PayoutAccount, error) {
	a.BankName, a.AccountName = strings.TrimSpace(a.BankName), strings.TrimSpace(a.AccountName)
	a.AccountNumber = strings.TrimSpace(a.AccountNumber)
	if a.BankName == "" || a.AccountName == "" {
		return PayoutAccount{}, &InputError{Detail: "Bank name and account name must not be blank."}
	}
	return s.repo.SavePayoutAccount(ctx, a)
}

func (s *service) PayoutAccount(ctx context.Context, userID uuid.UUID) (PayoutAccount, error) {
	return s.repo.PayoutAccount(ctx, userID)
}

func (s *service) RequestPayout(ctx context.Context, jastiperID uuid.UUID, amount int64) (Payout, error) {
	if amount < 1 {
		return Payout{}, &InputError{Detail: "Amount must be positive."}
	}
	var p Payout
	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		// The account row lock serializes a jastiper's requests, so the balance check cannot race.
		acc, err := s.repo.LockPayoutAccount(ctx, jastiperID)
		if err != nil {
			return err
		}
		available, err := s.availableBalance(ctx, jastiperID)
		if err != nil {
			return err
		}
		if amount > available {
			return ErrInsufficientFunds
		}
		p = Payout{
			ID:            uuid.Must(uuid.NewV7()),
			JastiperID:    jastiperID,
			Amount:        amount,
			BankName:      acc.BankName,
			AccountNumber: acc.AccountNumber,
			AccountName:   acc.AccountName,
			Status:        PayoutRequested,
		}
		if err := s.repo.CreatePayout(ctx, p); err != nil {
			return err
		}
		p, err = s.repo.Payout(ctx, p.ID)
		return err
	})
	return p, err
}

func (s *service) availableBalance(ctx context.Context, jastiperID uuid.UUID) (int64, error) {
	payable, err := s.repo.AccountBalance(ctx, account{Kind: accountJastiperPayable, UserID: &jastiperID})
	if err != nil {
		return 0, err
	}
	requested, err := s.repo.RequestedPayoutTotal(ctx, jastiperID)
	return payable - requested, err
}

func (s *service) ListMyPayouts(ctx context.Context, jastiperID uuid.UUID, limit int, cursor string) (PayoutPage, error) {
	before, err := pagination.Decode(cursor, pagination.Newest)
	if err != nil {
		return PayoutPage{}, err
	}
	rows, err := s.repo.ListPayoutsByJastiper(ctx, jastiperID, before, limit+1)
	if err != nil {
		return PayoutPage{}, err
	}
	return newPayoutPage(rows, limit), nil
}

func (s *service) ListPayouts(ctx context.Context, status PayoutStatus, limit int, cursor string) (PayoutPage, error) {
	after, err := pagination.Decode(cursor, pagination.Cursor{})
	if err != nil {
		return PayoutPage{}, err
	}
	rows, err := s.repo.ListPayoutQueue(ctx, payoutFilter{Status: status, After: after, Limit: limit + 1})
	if err != nil {
		return PayoutPage{}, err
	}
	return newPayoutPage(rows, limit), nil
}

func newPayoutPage(rows []Payout, limit int) PayoutPage {
	items, next := pagination.Page(rows, limit, func(p Payout) pagination.Cursor {
		return pagination.Cursor{At: p.RequestedAt, ID: p.ID}
	})
	return PayoutPage{Items: items, NextCursor: next}
}

func (s *service) MarkPayoutPaid(ctx context.Context, adminID, payoutID uuid.UUID, reference string) (Payout, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return Payout{}, &InputError{Detail: "Reference must not be blank."}
	}
	return s.decidePayout(ctx, adminID, payoutID, func(ctx context.Context, p *Payout) error {
		p.Status, p.Reference = PayoutPaid, reference
		return s.post(ctx, ledgerTransaction{
			Kind:           "payout",
			IdempotencyKey: "payout:" + p.ID.String(),
			CreatedBy:      &adminID,
			Entries: []entry{
				{Account: account{Kind: accountJastiperPayable, UserID: &p.JastiperID}, Direction: debit, Amount: p.Amount},
				{Account: account{Kind: accountGatewayClearing}, Direction: credit, Amount: p.Amount},
			},
		})
	})
}

func (s *service) RejectPayout(ctx context.Context, adminID, payoutID uuid.UUID, reason string) (Payout, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Payout{}, &InputError{Detail: "Reason must not be blank."}
	}
	return s.decidePayout(ctx, adminID, payoutID, func(_ context.Context, p *Payout) error {
		p.Status, p.RejectionReason = PayoutRejected, reason
		return nil
	})
}

// decidePayout locks a requested payout, applies the decision, then records it with a notification and audit row.
func (s *service) decidePayout(ctx context.Context, adminID, payoutID uuid.UUID, decide func(ctx context.Context, p *Payout) error) (Payout, error) {
	var p Payout
	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		var err error
		p, err = s.repo.LockPayout(ctx, payoutID)
		if err != nil {
			return err
		}
		if p.Status != PayoutRequested {
			return ErrPayoutNotRequested
		}
		if err := decide(ctx, &p); err != nil {
			return err
		}
		at := s.now()
		p.DecidedBy, p.DecidedAt = &adminID, &at
		if err := s.repo.DecidePayout(ctx, p); err != nil {
			return err
		}

		typ, data := notification.TypePayoutPaid, map[string]any{"payoutId": p.ID, "amount": p.Amount}
		meta := map[string]any{"amount": p.Amount, "jastiperId": p.JastiperID, "reference": p.Reference}
		if p.Status == PayoutRejected {
			typ, data["reason"] = notification.TypePayoutRejected, p.RejectionReason
			meta = map[string]any{"amount": p.Amount, "jastiperId": p.JastiperID, "reason": p.RejectionReason}
		}
		err = s.notifier.Notify(ctx, notification.Event{Type: typ, Recipients: []uuid.UUID{p.JastiperID}, Data: data})
		if err != nil {
			return err
		}
		return s.audit.Write(ctx, audit.Entry{
			ActorID:    &adminID,
			ActorType:  string(order.ActorAdmin),
			Action:     "payout." + string(p.Status),
			TargetType: "payout",
			TargetID:   p.ID.String(),
			Metadata:   meta,
		})
	})
	return p, err
}

func actorID(a order.Actor) *uuid.UUID {
	if a.Type == order.ActorSystem {
		return nil
	}
	return &a.ID
}

func (s *service) JastiperBalance(ctx context.Context, jastiperID uuid.UUID) (int64, error) {
	return s.repo.AccountBalance(ctx, account{Kind: accountJastiperPayable, UserID: &jastiperID})
}

func (s *service) post(ctx context.Context, t ledgerTransaction) error {
	return s.repo.Post(ctx, uuid.Must(uuid.NewV7()), t)
}
