package payment

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/db"
	"github.com/Titip-Dong/titipdong-be/internal/platform/database"
	"github.com/Titip-Dong/titipdong-be/internal/platform/pagination"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	Kind string
	// OrderID is uuid.Nil for postings not tied to an order (payouts).
	OrderID        uuid.UUID
	IdempotencyKey string
	CreatedBy      *uuid.UUID
	Entries        []entry
}

type payoutFilter struct {
	Status PayoutStatus
	After  pagination.Cursor
	Limit  int
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
	// EscrowHeldFor sums the escrow still held for the jastiper's orders.
	EscrowHeldFor(ctx context.Context, jastiperID uuid.UUID) (int64, error)
	// AccountEntries lists the entries of a per-user account below beforeID, newest first.
	AccountEntries(ctx context.Context, a account, beforeID int64, limit int) ([]WalletEntry, error)
	// BalancesByKind sums every account of each kind (credits minus debits).
	BalancesByKind(ctx context.Context) (map[accountKind]int64, error)

	SavePayoutAccount(ctx context.Context, a PayoutAccount) (PayoutAccount, error)
	// PayoutAccount returns ErrNoPayoutAccount when the user has not saved one.
	PayoutAccount(ctx context.Context, userID uuid.UUID) (PayoutAccount, error)
	LockPayoutAccount(ctx context.Context, userID uuid.UUID) (PayoutAccount, error)
	RequestedPayoutTotal(ctx context.Context, jastiperID uuid.UUID) (int64, error)
	// CreatePayout returns ErrPayoutOpen when the jastiper already has a requested payout.
	CreatePayout(ctx context.Context, p Payout) error
	Payout(ctx context.Context, id uuid.UUID) (Payout, error)
	LockPayout(ctx context.Context, id uuid.UUID) (Payout, error)
	ListPayoutsByJastiper(ctx context.Context, jastiperID uuid.UUID, before pagination.Cursor, limit int) ([]Payout, error)
	ListPayoutQueue(ctx context.Context, f payoutFilter) ([]Payout, error)
	DecidePayout(ctx context.Context, p Payout) error
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
	var orderID *uuid.UUID
	if t.OrderID != uuid.Nil {
		orderID = &t.OrderID
	}
	inserted, err := q.InsertLedgerTransaction(ctx, db.InsertLedgerTransactionParams{
		ID:             id,
		Kind:           t.Kind,
		OrderID:        orderID,
		IdempotencyKey: t.IdempotencyKey,
		CreatedBy:      t.CreatedBy,
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
			OrderID:       orderID,
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

func (r *pgRepository) EscrowHeldFor(ctx context.Context, jastiperID uuid.UUID) (int64, error) {
	return r.q(ctx).JastiperEscrowHeld(ctx, jastiperID)
}

func (r *pgRepository) AccountEntries(ctx context.Context, a account, beforeID int64, limit int) ([]WalletEntry, error) {
	rows, err := r.q(ctx).ListUserAccountEntries(ctx, db.ListUserAccountEntriesParams{
		Kind: string(a.Kind), UserID: *a.UserID, BeforeID: beforeID, RowLimit: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]WalletEntry, len(rows))
	for i, row := range rows {
		out[i] = WalletEntry{
			ID: row.ID, Kind: row.TransactionKind, Direction: row.Direction, Amount: row.Amount,
			OrderID: row.OrderID, OrderCode: deref(row.OrderCode), CreatedAt: row.CreatedAt,
		}
		// Payout postings carry no order; their idempotency key is payout:<payout_id>.
		if id, ok := strings.CutPrefix(row.IdempotencyKey, "payout:"); ok {
			if payoutID, err := uuid.Parse(id); err == nil {
				out[i].PayoutID = &payoutID
			}
		}
	}
	return out, nil
}

func (r *pgRepository) BalancesByKind(ctx context.Context) (map[accountKind]int64, error) {
	rows, err := r.q(ctx).LedgerBalancesByKind(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[accountKind]int64, len(rows))
	for _, row := range rows {
		out[accountKind(row.Kind)] = row.Balance
	}
	return out, nil
}

func (r *pgRepository) SavePayoutAccount(ctx context.Context, a PayoutAccount) (PayoutAccount, error) {
	row, err := r.q(ctx).UpsertPayoutAccount(ctx, db.UpsertPayoutAccountParams{
		UserID:        a.UserID,
		BankName:      a.BankName,
		AccountNumber: a.AccountNumber,
		AccountName:   a.AccountName,
	})
	if err != nil {
		return PayoutAccount{}, err
	}
	return toPayoutAccount(row), nil
}

func (r *pgRepository) PayoutAccount(ctx context.Context, userID uuid.UUID) (PayoutAccount, error) {
	row, err := r.q(ctx).GetPayoutAccount(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return PayoutAccount{}, ErrNoPayoutAccount
	}
	if err != nil {
		return PayoutAccount{}, err
	}
	return toPayoutAccount(row), nil
}

func (r *pgRepository) LockPayoutAccount(ctx context.Context, userID uuid.UUID) (PayoutAccount, error) {
	row, err := r.q(ctx).LockPayoutAccount(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return PayoutAccount{}, ErrNoPayoutAccount
	}
	if err != nil {
		return PayoutAccount{}, err
	}
	return toPayoutAccount(row), nil
}

func (r *pgRepository) RequestedPayoutTotal(ctx context.Context, jastiperID uuid.UUID) (int64, error) {
	return r.q(ctx).SumRequestedPayouts(ctx, jastiperID)
}

func (r *pgRepository) CreatePayout(ctx context.Context, p Payout) error {
	err := r.q(ctx).CreatePayout(ctx, db.CreatePayoutParams{
		ID:            p.ID,
		JastiperID:    p.JastiperID,
		Amount:        p.Amount,
		BankName:      p.BankName,
		AccountNumber: p.AccountNumber,
		AccountName:   p.AccountName,
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrPayoutOpen
	}
	return err
}

func (r *pgRepository) Payout(ctx context.Context, id uuid.UUID) (Payout, error) {
	row, err := r.q(ctx).GetPayout(ctx, id)
	if err != nil {
		return Payout{}, payoutNotFound(err)
	}
	return toPayout(row.Payout, row.JastiperName), nil
}

func (r *pgRepository) LockPayout(ctx context.Context, id uuid.UUID) (Payout, error) {
	row, err := r.q(ctx).LockPayout(ctx, id)
	if err != nil {
		return Payout{}, payoutNotFound(err)
	}
	return toPayout(row.Payout, row.JastiperName), nil
}

func (r *pgRepository) ListPayoutsByJastiper(ctx context.Context, jastiperID uuid.UUID, before pagination.Cursor, limit int) ([]Payout, error) {
	rows, err := r.q(ctx).ListPayoutsByJastiper(ctx, db.ListPayoutsByJastiperParams{
		JastiperID:        jastiperID,
		BeforeRequestedAt: before.At,
		BeforeID:          before.ID,
		RowLimit:          int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Payout, len(rows))
	for i, row := range rows {
		out[i] = toPayout(row.Payout, row.JastiperName)
	}
	return out, nil
}

func (r *pgRepository) ListPayoutQueue(ctx context.Context, f payoutFilter) ([]Payout, error) {
	rows, err := r.q(ctx).ListPayoutQueue(ctx, db.ListPayoutQueueParams{
		Status:           string(f.Status),
		AfterRequestedAt: f.After.At,
		AfterID:          f.After.ID,
		RowLimit:         int32(f.Limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Payout, len(rows))
	for i, row := range rows {
		out[i] = toPayout(row.Payout, row.JastiperName)
	}
	return out, nil
}

func (r *pgRepository) DecidePayout(ctx context.Context, p Payout) error {
	n, err := r.q(ctx).DecidePayout(ctx, db.DecidePayoutParams{
		Status:          string(p.Status),
		Reference:       nonEmpty(p.Reference),
		RejectionReason: nonEmpty(p.RejectionReason),
		DecidedBy:       *p.DecidedBy,
		DecidedAt:       *p.DecidedAt,
		ID:              p.ID,
	})
	if err == nil && n == 0 {
		return ErrPayoutNotRequested
	}
	return err
}

func toPayoutAccount(a db.PayoutAccount) PayoutAccount {
	return PayoutAccount{
		UserID:        a.UserID,
		BankName:      a.BankName,
		AccountNumber: a.AccountNumber,
		AccountName:   a.AccountName,
		UpdatedAt:     a.UpdatedAt,
	}
}

func toPayout(p db.Payout, jastiperName string) Payout {
	return Payout{
		ID:              p.ID,
		JastiperID:      p.JastiperID,
		JastiperName:    jastiperName,
		Amount:          p.Amount,
		BankName:        p.BankName,
		AccountNumber:   p.AccountNumber,
		AccountName:     p.AccountName,
		Status:          PayoutStatus(p.Status),
		Reference:       deref(p.Reference),
		RejectionReason: deref(p.RejectionReason),
		DecidedBy:       p.DecidedBy,
		RequestedAt:     p.RequestedAt,
		DecidedAt:       p.DecidedAt,
	}
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func payoutNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrPayoutNotFound
	}
	return err
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
