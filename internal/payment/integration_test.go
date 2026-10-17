package payment

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Titip-Dong/titipdong-be/db/migrations"
	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/db"
	"github.com/Titip-Dong/titipdong-be/internal/notification"
	"github.com/Titip-Dong/titipdong-be/internal/order"
	"github.com/Titip-Dong/titipdong-be/internal/platform/audit"
	"github.com/Titip-Dong/titipdong-be/internal/platform/database"
	"github.com/Titip-Dong/titipdong-be/internal/trip"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fixture struct {
	pool     *pgxpool.Pool
	notifier notification.Service
	orders   order.Service
	svc      *service
	buyer    uuid.UUID
	jastiper uuid.UUID
}

func newFixture(t *testing.T, now func() time.Time) fixture {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := database.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}

	user := func(role auth.Role) uuid.UUID {
		u, err := auth.Provision(ctx, pool, auth.RegisterParams{
			Name: string(role), Email: uuid.NewString() + "@test.local", Password: "password123", Role: role,
		})
		if err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	trips := trip.NewService(pool)
	notifier := notification.NewService(pool)
	orders := order.NewService(pool, trips, notifier)
	return fixture{
		pool:     pool,
		notifier: notifier,
		orders:   orders,
		svc:      newService(newRepository(pool), orders, notifier, audit.NewWriter(pool), FakeGateway{}, now),
		buyer:    user(auth.RoleBuyer),
		jastiper: user(auth.RoleJastiper),
	}
}

func (f fixture) admin(t *testing.T) uuid.UUID {
	t.Helper()
	u, err := auth.Provision(context.Background(), f.pool, auth.RegisterParams{
		Name: "admin", Email: uuid.NewString() + "@test.local", Password: "password123", Role: auth.RoleAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// approvedOrder creates an order approved for quantity × itemPrice + fee.
func (f fixture) approvedOrder(t *testing.T, quantity int, itemPrice, fee int64) order.Order {
	t.Helper()
	ctx := context.Background()
	tr, err := trip.NewService(f.pool).Create(ctx, f.jastiper, trip.CreateParams{
		Title:              "Jastip Test",
		DestinationCity:    "Tokyo",
		DestinationCountry: "JP",
		DepartsOn:          time.Now().AddDate(0, 0, 1),
		ReturnsOn:          time.Now().AddDate(0, 0, 10),
		RequestDeadline:    time.Now().Add(72 * time.Hour),
		Capacity:           10,
	})
	if err != nil {
		t.Fatal(err)
	}
	o, err := f.orders.Create(ctx, f.buyer, tr.Slug, order.CreateParams{ProductName: "Item", Quantity: quantity})
	if err != nil {
		t.Fatal(err)
	}
	o, err = f.orders.Approve(ctx, f.jastiper, o.ID, itemPrice, fee)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func (f fixture) remainingCapacity(t *testing.T, tripID uuid.UUID) int {
	t.Helper()
	tr, err := trip.NewService(f.pool).ByID(context.Background(), tripID)
	if err != nil {
		t.Fatal(err)
	}
	return tr.RemainingCapacity()
}

func (f fixture) escrow(t *testing.T, orderID uuid.UUID) int64 {
	return f.orderBalance(t, accountEscrow, orderID)
}

func (f fixture) orderBalance(t *testing.T, kind accountKind, orderID uuid.UUID) int64 {
	t.Helper()
	b, err := db.New(f.pool).LedgerOrderBalance(context.Background(), db.LedgerOrderBalanceParams{Kind: string(kind), OrderID: &orderID})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// paidOrder is an approved order the buyer has paid.
func (f fixture) paidOrder(t *testing.T, quantity int, itemPrice, fee int64) order.Order {
	t.Helper()
	ctx := context.Background()
	o := f.approvedOrder(t, quantity, itemPrice, fee)
	p, err := f.svc.Checkout(ctx, f.buyer, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SimulatePaid(ctx, f.buyer, p.ID); err != nil {
		t.Fatal(err)
	}
	return o
}

func (f fixture) auditCount(t *testing.T, action string, orderID uuid.UUID) int {
	t.Helper()
	var n int
	err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_logs WHERE action = $1 AND target_type = 'order' AND target_id = $2`, action, orderID.String()).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// notifications returns the user's notifications of type typ, newest first.
func (f fixture) notifications(t *testing.T, userID uuid.UUID, typ string) []notification.Notification {
	t.Helper()
	page, err := f.notifier.List(context.Background(), userID, notification.ListParams{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var out []notification.Notification
	for _, n := range page.Items {
		if n.Type == typ {
			out = append(out, n)
		}
	}
	return out
}

func TestPaymentToReleaseFlow(t *testing.T) {
	f := newFixture(t, time.Now)
	ctx := context.Background()
	o := f.approvedOrder(t, 3, 30_000, 10_001) // total 100.001: fee 5.000, jastiper 95.001

	p, err := f.svc.Checkout(ctx, f.buyer, o.ID)
	if err != nil {
		t.Fatalf("Checkout() error = %v", err)
	}
	if p.Amount != 100_001 || p.Status != StatusPending || p.CheckoutToken == "" {
		t.Errorf("payment = %+v", p)
	}
	again, err := f.svc.Checkout(ctx, f.buyer, o.ID)
	if err != nil || again.ID != p.ID {
		t.Errorf("Checkout(again) = %v, %v; want the same pending payment", again.ID, err)
	}

	if _, err := f.svc.SimulatePaid(ctx, uuid.New(), p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("SimulatePaid(stranger) error = %v, want ErrNotFound", err)
	}
	for range 2 {
		if _, err := f.svc.SimulatePaid(ctx, f.buyer, p.ID); err != nil {
			t.Fatalf("SimulatePaid() error = %v", err)
		}
	}
	if got := f.escrow(t, o.ID); got != 100_001 {
		t.Errorf("escrow after paying twice = %d, want 100001", got)
	}
	for _, user := range []uuid.UUID{f.buyer, f.jastiper} {
		got := f.notifications(t, user, notification.TypePaymentReceived)
		if len(got) != 1 || got[0].Data["amount"] != float64(100_001) || got[0].Data["orderCode"] != o.Code {
			t.Errorf("payment.received for %s after paying twice = %+v, want one", user, got)
		}
	}
	if _, err := f.svc.Checkout(ctx, f.buyer, o.ID); !errors.Is(err, ErrOrderNotPayable) {
		t.Errorf("Checkout(paid) error = %v, want ErrOrderNotPayable", err)
	}

	if _, err := f.svc.Release(ctx, o.ID); !errors.Is(err, order.ErrInvalidTransition) {
		t.Errorf("Release(paid) error = %v, want ErrInvalidTransition", err)
	}
	before, _ := f.svc.JastiperBalance(ctx, f.jastiper)
	if _, err := f.orders.MarkShipped(ctx, f.jastiper, o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.orders.MarkDelivered(ctx, order.Actor{ID: f.buyer, Type: order.ActorBuyer}, o.ID); err != nil {
		t.Fatal(err)
	}
	released, err := f.svc.Release(ctx, o.ID)
	if err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	after, _ := f.svc.JastiperBalance(ctx, f.jastiper)
	if released.Status != order.StatusReleased || after-before != 95_001 || f.escrow(t, o.ID) != 0 {
		t.Errorf("status %s, jastiper +%d, escrow %d; want released, +95001, 0", released.Status, after-before, f.escrow(t, o.ID))
	}
	if _, err := f.svc.Release(ctx, o.ID); !errors.Is(err, order.ErrInvalidTransition) {
		t.Errorf("Release(again) error = %v, want ErrInvalidTransition", err)
	}
	got := f.notifications(t, f.jastiper, notification.TypeOrderReleased)
	if len(got) != 1 || got[0].Data["amount"] != float64(95_001) {
		t.Errorf("order.released = %+v, want one with the jastiper share", got)
	}
	if left := f.remainingCapacity(t, o.TripID); left != 7 {
		t.Errorf("remaining capacity after release = %d, want 7 (a carried order keeps its 3)", left)
	}
}

func TestCheckoutAfterDeadline(t *testing.T) {
	late := time.Now().Add(25 * time.Hour)
	f := newFixture(t, func() time.Time { return late })
	o := f.approvedOrder(t, 1, 10_000, 0)

	if _, err := f.svc.Checkout(context.Background(), f.buyer, o.ID); !errors.Is(err, ErrDeadlinePassed) {
		t.Errorf("Checkout() error = %v, want ErrDeadlinePassed", err)
	}
}

func TestLedgerIntegrity(t *testing.T) {
	f := newFixture(t, time.Now)
	ctx := context.Background()
	o := f.approvedOrder(t, 1, 10_000, 0)
	repo := newRepository(f.pool)

	err := repo.WithTx(ctx, func(ctx context.Context) error {
		return repo.Post(ctx, uuid.New(), ledgerTransaction{
			Kind:           "payment_received",
			OrderID:        o.ID,
			IdempotencyKey: "test:" + uuid.NewString(),
			Entries: []entry{
				{Account: account{Kind: accountGatewayClearing}, Direction: debit, Amount: 10_000},
				{Account: account{Kind: accountEscrow}, Direction: credit, Amount: 9_999},
			},
		})
	})
	if err == nil {
		t.Error("unbalanced ledger transaction committed")
	}

	if _, err := f.pool.Exec(ctx, `UPDATE ledger_entries SET amount = amount + 1`); err == nil {
		t.Error("UPDATE on ledger_entries succeeded, want append-only error")
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM ledger_transactions`); err == nil {
		t.Error("DELETE on ledger_transactions succeeded, want append-only error")
	}
}

func TestExpiredOrderNotifiesBothParties(t *testing.T) {
	f := newFixture(t, time.Now)
	ctx := context.Background()
	o := f.approvedOrder(t, 4, 10_000, 0)
	if left := f.remainingCapacity(t, o.TripID); left != 6 {
		t.Fatalf("remaining capacity after create = %d, want 6", left)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE orders SET payment_deadline = now() - interval '1 minute' WHERE id = $1`, o.ID); err != nil {
		t.Fatal(err)
	}

	got, _, err := f.orders.Get(ctx, order.Actor{ID: f.buyer, Type: order.ActorBuyer}, o.ID)
	if err != nil || got.Status != order.StatusCancelled {
		t.Fatalf("Get() = %s, %v; want cancelled", got.Status, err)
	}
	if _, _, err := f.orders.Get(ctx, order.Actor{ID: f.buyer, Type: order.ActorBuyer}, o.ID); err != nil {
		t.Fatal(err)
	}
	if left := f.remainingCapacity(t, o.TripID); left != 10 {
		t.Errorf("remaining capacity after expiry = %d, want 10", left)
	}
	for _, user := range []uuid.UUID{f.buyer, f.jastiper} {
		n := f.notifications(t, user, notification.TypeOrderCancelled)
		if len(n) != 1 || n[0].Data["orderCode"] != o.Code || n[0].Data["reason"] != "Payment deadline passed." {
			t.Errorf("order.cancelled for %s = %+v, want one", user, n)
		}
	}
}

func TestOrderCannotOverbookTrip(t *testing.T) {
	f := newFixture(t, time.Now)
	ctx := context.Background()
	o := f.approvedOrder(t, 10, 10_000, 0)
	tr, err := trip.NewService(f.pool).ByID(ctx, o.TripID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.orders.Create(ctx, f.buyer, tr.Slug, order.CreateParams{ProductName: "One more", Quantity: 1}); !errors.Is(err, trip.ErrCapacityFull) {
		t.Fatalf("Create(on a full trip) error = %v, want trip.ErrCapacityFull", err)
	}
	if _, err := f.orders.Cancel(ctx, f.buyer, o.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.orders.Create(ctx, f.buyer, tr.Slug, order.CreateParams{ProductName: "One more", Quantity: 10}); err != nil {
		t.Errorf("Create(after cancel freed the trip) error = %v", err)
	}
}

func TestCancelPaidRefundsBuyer(t *testing.T) {
	f := newFixture(t, time.Now)
	ctx := context.Background()
	o := f.paidOrder(t, 2, 50_000, 10_000) // total 110.000

	if _, err := f.svc.CancelPaid(ctx, f.jastiper, o.ID, "  "); err == nil {
		t.Error("CancelPaid(blank reason) succeeded")
	}
	if _, err := f.svc.CancelPaid(ctx, uuid.New(), o.ID, "Tidak jadi berangkat"); !errors.Is(err, order.ErrNotFound) {
		t.Errorf("CancelPaid(other jastiper) error = %v, want order.ErrNotFound", err)
	}

	rf, err := f.svc.CancelPaid(ctx, f.jastiper, o.ID, "Tidak jadi berangkat")
	if err != nil {
		t.Fatalf("CancelPaid() error = %v", err)
	}
	if rf.Amount != 110_000 || rf.Status != RefundSucceeded || rf.Reason != RefundJastiperCancelled || rf.CompletedAt == nil {
		t.Errorf("refund = %+v", rf)
	}
	got, _, err := f.orders.Get(ctx, order.Actor{ID: f.buyer, Type: order.ActorBuyer}, o.ID)
	if err != nil || got.Status != order.StatusRefunded || got.StatusReason != "Tidak jadi berangkat" {
		t.Fatalf("order = %s %q, %v; want refunded with the reason", got.Status, got.StatusReason, err)
	}
	f.assertSettled(t, o.ID)
	if left := f.remainingCapacity(t, o.TripID); left != 10 {
		t.Errorf("remaining capacity after refund = %d, want 10", left)
	}
	if n := f.notifications(t, f.buyer, notification.TypeOrderRefunded); len(n) != 1 || n[0].Data["amount"] != float64(110_000) {
		t.Errorf("buyer order.refunded = %+v, want one", n)
	}
	if n := f.notifications(t, f.jastiper, notification.TypeOrderRefunded); len(n) != 0 {
		t.Errorf("jastiper notified about their own cancellation: %+v", n)
	}
	if n := f.auditCount(t, "order.refunded", o.ID); n != 1 {
		t.Errorf("order.refunded audit rows = %d, want 1", n)
	}

	if _, err := f.svc.CancelPaid(ctx, f.jastiper, o.ID, "Lagi"); !errors.Is(err, order.ErrInvalidTransition) {
		t.Errorf("CancelPaid(again) error = %v, want ErrInvalidTransition", err)
	}
	f.assertSettled(t, o.ID)
	if n := f.auditCount(t, "order.refunded", o.ID); n != 1 {
		t.Errorf("audit rows after a refused retry = %d, want 1", n)
	}

	unpaid := f.approvedOrder(t, 1, 10_000, 0)
	if _, err := f.svc.CancelPaid(ctx, f.jastiper, unpaid.ID, "Batal"); !errors.Is(err, order.ErrInvalidTransition) {
		t.Errorf("CancelPaid(approved) error = %v, want ErrInvalidTransition", err)
	}
}

// assertSettled checks that a refunded order's money went back out through the gateway.
func (f fixture) assertSettled(t *testing.T, orderID uuid.UUID) {
	t.Helper()
	for _, kind := range []accountKind{accountEscrow, accountBuyerRefundable, accountGatewayClearing} {
		if b := f.orderBalance(t, kind, orderID); b != 0 {
			t.Errorf("%s balance for the order = %d, want 0", kind, b)
		}
	}
}

func TestAdminRefundsDisputedOrder(t *testing.T) {
	f := newFixture(t, time.Now)
	ctx := context.Background()
	o := f.paidOrder(t, 1, 80_000, 20_000)
	if _, err := f.orders.MarkShipped(ctx, f.jastiper, o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.orders.MarkDisputed(ctx, f.buyer, o.ID, "Barang rusak"); err != nil {
		t.Fatal(err)
	}
	admin := order.Actor{ID: f.admin(t), Type: order.ActorAdmin}

	rf, err := f.svc.Refund(ctx, admin, o.ID, RefundDispute, "Barang rusak saat diterima")
	if err != nil {
		t.Fatalf("Refund() error = %v", err)
	}
	if rf.Amount != 100_000 || rf.Status != RefundSucceeded || rf.Reason != RefundDispute {
		t.Errorf("refund = %+v", rf)
	}
	f.assertSettled(t, o.ID)
	for _, user := range []uuid.UUID{f.buyer, f.jastiper} {
		if n := f.notifications(t, user, notification.TypeOrderRefunded); len(n) != 1 {
			t.Errorf("order.refunded for %s = %+v, want one", user, n)
		}
	}
	if _, err := f.svc.Release(ctx, o.ID); !errors.Is(err, order.ErrInvalidTransition) {
		t.Errorf("Release(refunded) error = %v, want ErrInvalidTransition", err)
	}
}

type failingRefunds struct{ FakeGateway }

func (failingRefunds) Refund(context.Context, RefundRequest) (RefundResult, error) {
	return RefundResult{}, errors.New("gateway unavailable")
}

func TestRefundGatewayFailureKeepsDebtToBuyer(t *testing.T) {
	f := newFixture(t, time.Now)
	ctx := context.Background()
	o := f.paidOrder(t, 1, 40_000, 0)
	svc := newService(newRepository(f.pool), f.orders, f.notifier, audit.NewWriter(f.pool), failingRefunds{}, time.Now)

	rf, err := svc.CancelPaid(ctx, f.jastiper, o.ID, "Stok habis")
	if err != nil {
		t.Fatalf("CancelPaid() error = %v", err)
	}
	if rf.Status != RefundFailed || rf.FailureReason != "gateway unavailable" {
		t.Errorf("refund = %+v, want failed with the gateway error", rf)
	}
	if b := f.orderBalance(t, accountBuyerRefundable, o.ID); b != 40_000 {
		t.Errorf("buyer_refundable for the order = %d, want 40000", b)
	}
	if b := f.escrow(t, o.ID); b != 0 {
		t.Errorf("escrow = %d, want 0", b)
	}
	got, _, _ := f.orders.Get(ctx, order.Actor{ID: f.buyer, Type: order.ActorBuyer}, o.ID)
	if got.Status != order.StatusRefunded {
		t.Errorf("order status = %s, want refunded", got.Status)
	}
}

// releasedOrder pays and releases an order, crediting the jastiper total − 5%.
func (f fixture) releasedOrder(t *testing.T, itemPrice int64) order.Order {
	t.Helper()
	ctx := context.Background()
	o := f.paidOrder(t, 1, itemPrice, 0)
	if _, err := f.orders.MarkShipped(ctx, f.jastiper, o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.orders.MarkDelivered(ctx, order.Actor{ID: f.buyer, Type: order.ActorBuyer}, o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Release(ctx, o.ID); err != nil {
		t.Fatal(err)
	}
	return o
}

func (f fixture) payoutAuditCount(t *testing.T, action string, payoutID uuid.UUID) int {
	t.Helper()
	var n int
	err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_logs WHERE action = $1 AND target_type = 'payout' AND target_id = $2`, action, payoutID.String()).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPayoutFlow(t *testing.T) {
	f := newFixture(t, time.Now)
	ctx := context.Background()
	admin := f.admin(t)
	f.releasedOrder(t, 100_000) // jastiper earns 95.000

	if _, err := f.svc.RequestPayout(ctx, f.jastiper, 10_000); !errors.Is(err, ErrNoPayoutAccount) {
		t.Fatalf("RequestPayout(no account) error = %v, want ErrNoPayoutAccount", err)
	}
	acc := PayoutAccount{UserID: f.jastiper, BankName: "BCA", AccountNumber: "1234567890", AccountName: "Jastiper"}
	if _, err := f.svc.SavePayoutAccount(ctx, acc); err != nil {
		t.Fatal(err)
	}
	acc.BankName = "Mandiri"
	if saved, err := f.svc.SavePayoutAccount(ctx, acc); err != nil || saved.BankName != "Mandiri" {
		t.Fatalf("SavePayoutAccount(update) = %+v, %v", saved, err)
	}

	if _, err := f.svc.RequestPayout(ctx, f.jastiper, 95_001); !errors.Is(err, ErrInsufficientFunds) {
		t.Errorf("RequestPayout(more than earned) error = %v, want ErrInsufficientFunds", err)
	}
	first, err := f.svc.RequestPayout(ctx, f.jastiper, 50_000)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != PayoutRequested || first.BankName != "Mandiri" || first.JastiperName == "" {
		t.Errorf("payout = %+v", first)
	}
	if _, err := f.svc.RequestPayout(ctx, f.jastiper, 1_000); !errors.Is(err, ErrPayoutOpen) {
		t.Errorf("RequestPayout(second open) error = %v, want ErrPayoutOpen", err)
	}

	rejected, err := f.svc.RejectPayout(ctx, admin, first.ID, "Nama rekening tidak cocok")
	if err != nil || rejected.Status != PayoutRejected || rejected.DecidedAt == nil {
		t.Fatalf("RejectPayout() = %+v, %v", rejected, err)
	}
	if balance, _ := f.svc.JastiperBalance(ctx, f.jastiper); balance != 95_000 {
		t.Errorf("balance after reject = %d, want 95000", balance)
	}
	if _, err := f.svc.MarkPayoutPaid(ctx, admin, first.ID, "TRF-1"); !errors.Is(err, ErrPayoutNotRequested) {
		t.Errorf("MarkPayoutPaid(rejected) error = %v, want ErrPayoutNotRequested", err)
	}

	second, err := f.svc.RequestPayout(ctx, f.jastiper, 95_000)
	if err != nil {
		t.Fatal(err)
	}
	paid, err := f.svc.MarkPayoutPaid(ctx, admin, second.ID, "TRF-20261017-001")
	if err != nil || paid.Status != PayoutPaid || paid.Reference != "TRF-20261017-001" {
		t.Fatalf("MarkPayoutPaid() = %+v, %v", paid, err)
	}
	if _, err := f.svc.MarkPayoutPaid(ctx, admin, second.ID, "TRF-again"); !errors.Is(err, ErrPayoutNotRequested) {
		t.Errorf("MarkPayoutPaid(again) error = %v, want ErrPayoutNotRequested", err)
	}
	if balance, _ := f.svc.JastiperBalance(ctx, f.jastiper); balance != 0 {
		t.Errorf("balance after payout = %d, want 0", balance)
	}
	if _, err := f.svc.RequestPayout(ctx, f.jastiper, 1); !errors.Is(err, ErrInsufficientFunds) {
		t.Errorf("RequestPayout(after paying out everything) error = %v, want ErrInsufficientFunds", err)
	}

	if n := f.notifications(t, f.jastiper, notification.TypePayoutRejected); len(n) != 1 || n[0].Data["reason"] != "Nama rekening tidak cocok" {
		t.Errorf("payout.rejected = %+v", n)
	}
	if n := f.notifications(t, f.jastiper, notification.TypePayoutPaid); len(n) != 1 || n[0].Data["amount"] != float64(95_000) {
		t.Errorf("payout.paid = %+v", n)
	}
	if f.payoutAuditCount(t, "payout.rejected", first.ID) != 1 || f.payoutAuditCount(t, "payout.paid", second.ID) != 1 {
		t.Error("missing payout audit rows")
	}

	mine, err := f.svc.ListMyPayouts(ctx, f.jastiper, 1, "")
	if err != nil || len(mine.Items) != 1 || mine.Items[0].ID != second.ID || mine.NextCursor == "" {
		t.Fatalf("ListMyPayouts(first page) = %+v, %v", mine, err)
	}
	if next, _ := f.svc.ListMyPayouts(ctx, f.jastiper, 1, mine.NextCursor); len(next.Items) != 1 || next.Items[0].ID != first.ID {
		t.Errorf("ListMyPayouts(second page) = %+v", next)
	}
	queue, err := f.svc.ListPayouts(ctx, PayoutRejected, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range queue.Items {
		found = found || p.ID == first.ID
		if p.Status != PayoutRejected {
			t.Errorf("ListPayouts(rejected) returned %s", p.Status)
		}
	}
	if !found {
		t.Error("ListPayouts(rejected) misses the rejected payout")
	}
}

func TestConcurrentPayoutRequests(t *testing.T) {
	f := newFixture(t, time.Now)
	ctx := context.Background()
	f.releasedOrder(t, 100_000)
	if _, err := f.svc.SavePayoutAccount(ctx, PayoutAccount{UserID: f.jastiper, BankName: "BCA", AccountNumber: "1234567890", AccountName: "J"}); err != nil {
		t.Fatal(err)
	}

	const attempts = 10
	var wg sync.WaitGroup
	errs := make(chan error, attempts)
	for range attempts {
		wg.Go(func() {
			_, err := f.svc.RequestPayout(ctx, f.jastiper, 60_000)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	var ok int
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrPayoutOpen), errors.Is(err, ErrInsufficientFunds):
		default:
			t.Errorf("RequestPayout() error = %v", err)
		}
	}
	if ok != 1 {
		t.Errorf("successful concurrent requests = %d, want 1", ok)
	}
}
