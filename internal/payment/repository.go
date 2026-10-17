package payment

import (
	"context"
	"errors"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/db"
	"github.com/Titip-Dong/titipdong-be/internal/platform/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type accountKind string

const (
	accountGatewayClearing accountKind = "gateway_clearing"
	accountEscrow          accountKind = "escrow"
	accountJastiperPayable accountKind = "jastiper_payable"
	accountPlatformRevenue accountKind = "platform_revenue"
	accountBuyerRefundable accountKind = "buyer_refundable"
)

type direction string

const (
	debit  direction = "debit"
	credit direction = "credit"
)

type account struct {
	Kind   accountKind
	UserID *uuid.UUID
}

type entry struct {
	Account   account
	Direction direction
	Amount    int64
}

type ledgerTransaction struct {
	Kind           string
	OrderID        uuid.UUID
	IdempotencyKey string
	Entries        []entry
}

type repository interface {
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error
	Create(ctx context.Context, p Payment) error
	SetCheckout(ctx context.Context, id uuid.UUID, token, url string) error
	// PendingByOrder returns ErrNotFound when the order has no pending payment.
	PendingByOrder(ctx context.Context, orderID uuid.UUID) (Payment, error)
	Lock(ctx context.Context, id uuid.UUID) (Payment, error)
	MarkPaid(ctx context.Context, id uuid.UUID, providerTransactionID string, paidAt time.Time) error
	MarkExpired(ctx context.Context, id uuid.UUID) error
	// PaidByOrder returns ErrNotFound when the order has no paid payment.
	PaidByOrder(ctx context.Context, orderID uuid.UUID) (Payment, error)
	CreateRefund(ctx context.Context, r Refund) error
	MarkRefundSucceeded(ctx context.Context, id uuid.UUID, providerRefundID string, at time.Time) error
	MarkRefundFailed(ctx context.Context, id uuid.UUID, reason string, at time.Time) error
	// Post writes the transaction and its entries, or nothing if its idempotency key was already used.
	Post(ctx context.Context, id uuid.UUID, t ledgerTransaction) error
	AccountBalance(ctx context.Context, a account) (int64, error)
}

type pgRepository struct {
	pool *pgxpool.Pool
}

func newRepository(pool *pgxpool.Pool) *pgRepository {
	return &pgRepository{pool: pool}
}

func (r *pgRepository) q(ctx context.Context) *db.Queries {
	return db.New(database.Conn(ctx, r.pool))
}

func (r *pgRepository) WithTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return database.WithTx(ctx, r.pool, fn)
}

func (r *pgRepository) Create(ctx context.Context, p Payment) error {
	return r.q(ctx).CreatePayment(ctx, db.CreatePaymentParams{
		ID:        p.ID,
		OrderID:   p.OrderID,
		Amount:    p.Amount,
		Provider:  p.Provider,
		ExpiresAt: p.ExpiresAt,
	})
}

func (r *pgRepository) SetCheckout(ctx context.Context, id uuid.UUID, token, url string) error {
	return r.q(ctx).SetPaymentCheckout(ctx, db.SetPaymentCheckoutParams{ID: id, CheckoutToken: token, CheckoutUrl: url})
}

func (r *pgRepository) PendingByOrder(ctx context.Context, orderID uuid.UUID) (Payment, error) {
	row, err := r.q(ctx).GetPendingPaymentByOrder(ctx, orderID)
	if err != nil {
		return Payment{}, notFound(err)
	}
	return toPayment(row), nil
}

func (r *pgRepository) Lock(ctx context.Context, id uuid.UUID) (Payment, error) {
	row, err := r.q(ctx).LockPayment(ctx, id)
	if err != nil {
		return Payment{}, notFound(err)
	}
	return toPayment(row), nil
}

func (r *pgRepository) MarkPaid(ctx context.Context, id uuid.UUID, providerTransactionID string, paidAt time.Time) error {
	return r.q(ctx).MarkPaymentPaid(ctx, db.MarkPaymentPaidParams{
		ID:                    id,
		ProviderTransactionID: &providerTransactionID,
		PaidAt:                paidAt,
	})
}

func (r *pgRepository) MarkExpired(ctx context.Context, id uuid.UUID) error {
	return r.q(ctx).MarkPaymentExpired(ctx, id)
}

func (r *pgRepository) PaidByOrder(ctx context.Context, orderID uuid.UUID) (Payment, error) {
	row, err := r.q(ctx).GetPaidPaymentByOrder(ctx, orderID)
	if err != nil {
		return Payment{}, notFound(err)
	}
	return toPayment(row), nil
}

func (r *pgRepository) CreateRefund(ctx context.Context, rf Refund) error {
	return r.q(ctx).CreateRefund(ctx, db.CreateRefundParams{
		ID:        rf.ID,
		OrderID:   rf.OrderID,
		PaymentID: rf.PaymentID,
		Amount:    rf.Amount,
		Reason:    string(rf.Reason),
	})
}

func (r *pgRepository) MarkRefundSucceeded(ctx context.Context, id uuid.UUID, providerRefundID string, at time.Time) error {
	return r.q(ctx).MarkRefundSucceeded(ctx, db.MarkRefundSucceededParams{ID: id, ProviderRefundID: &providerRefundID, CompletedAt: at})
}

func (r *pgRepository) MarkRefundFailed(ctx context.Context, id uuid.UUID, reason string, at time.Time) error {
	return r.q(ctx).MarkRefundFailed(ctx, db.MarkRefundFailedParams{ID: id, FailureReason: &reason, CompletedAt: at})
}

func (r *pgRepository) Post(ctx context.Context, id uuid.UUID, t ledgerTransaction) error {
	q := r.q(ctx)
	inserted, err := q.InsertLedgerTransaction(ctx, db.InsertLedgerTransactionParams{
		ID:             id,
		Kind:           t.Kind,
		OrderID:        &t.OrderID,
		IdempotencyKey: t.IdempotencyKey,
	})
	if err != nil || inserted == 0 {
		return err
	}
	for _, e := range t.Entries {
		accountID, err := r.accountID(ctx, e.Account)
		if err != nil {
			return err
		}
		err = q.InsertLedgerEntry(ctx, db.InsertLedgerEntryParams{
			TransactionID: id,
			AccountID:     accountID,
			OrderID:       &t.OrderID,
			Direction:     string(e.Direction),
			Amount:        e.Amount,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *pgRepository) accountID(ctx context.Context, a account) (uuid.UUID, error) {
	q := r.q(ctx)
	err := q.InsertLedgerAccount(ctx, db.InsertLedgerAccountParams{ID: uuid.Must(uuid.NewV7()), Kind: string(a.Kind), UserID: a.UserID})
	if err != nil {
		return uuid.Nil, err
	}
	return q.GetLedgerAccount(ctx, db.GetLedgerAccountParams{Kind: string(a.Kind), UserID: a.UserID})
}

func (r *pgRepository) AccountBalance(ctx context.Context, a account) (int64, error) {
	return r.q(ctx).LedgerAccountBalance(ctx, db.LedgerAccountBalanceParams{Kind: string(a.Kind), UserID: a.UserID})
}

func toPayment(p db.Payment) Payment {
	return Payment{
		ID:            p.ID,
		OrderID:       p.OrderID,
		Amount:        p.Amount,
		Provider:      p.Provider,
		ProviderTxID:  deref(p.ProviderTransactionID),
		Status:        Status(p.Status),
		CheckoutToken: p.CheckoutToken,
		CheckoutURL:   p.CheckoutUrl,
		ExpiresAt:     p.ExpiresAt,
		PaidAt:        p.PaidAt,
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
