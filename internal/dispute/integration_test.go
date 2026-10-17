package dispute

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Titip-Dong/titipdong-be/db/migrations"
	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/db"
	"github.com/Titip-Dong/titipdong-be/internal/notification"
	"github.com/Titip-Dong/titipdong-be/internal/order"
	"github.com/Titip-Dong/titipdong-be/internal/payment"
	"github.com/Titip-Dong/titipdong-be/internal/platform/audit"
	"github.com/Titip-Dong/titipdong-be/internal/platform/database"
	"github.com/Titip-Dong/titipdong-be/internal/trip"
	"github.com/Titip-Dong/titipdong-be/internal/upload"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeUploads attaches any upload ID as a key, so these tests need no blob storage.
type fakeUploads struct{ upload.Service }

func (fakeUploads) Attach(_ context.Context, _, id uuid.UUID, purpose upload.Purpose) (string, error) {
	return string(purpose) + "/" + id.String() + ".jpg", nil
}

func (fakeUploads) ReadURL(key string) string { return "https://blob.test/" + key }

type fixture struct {
	pool                   *pgxpool.Pool
	orders                 order.Service
	payments               payment.Service
	notifier               notification.Service
	svc                    *service
	buyer, jastiper, admin uuid.UUID
	buyerA, jastiperA, adm order.Actor
}

func newFixture(t *testing.T) fixture {
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
	notifier := notification.NewService(pool)
	orders := order.NewService(pool, trip.NewService(pool), notifier)
	payments := payment.NewService(pool, orders, notifier, payment.FakeGateway{})
	f := fixture{
		pool: pool, orders: orders, payments: payments, notifier: notifier,
		svc:   newService(newRepository(pool), orders, payments, fakeUploads{}, notifier, audit.NewWriter(pool), time.Now),
		buyer: user(auth.RoleBuyer), jastiper: user(auth.RoleJastiper), admin: user(auth.RoleAdmin),
	}
	f.buyerA = order.Actor{ID: f.buyer, Type: order.ActorBuyer}
	f.jastiperA = order.Actor{ID: f.jastiper, Type: order.ActorJastiper}
	f.adm = order.Actor{ID: f.admin, Type: order.ActorAdmin}
	return f
}

// shippedOrder is a paid order of total 100.000 the jastiper has shipped.
func (f fixture) shippedOrder(t *testing.T) order.Order {
	t.Helper()
	ctx := context.Background()
	tr, err := trip.NewService(f.pool).Create(ctx, f.jastiper, trip.CreateParams{
		Title: "Jastip Test", DestinationCity: "Tokyo", DestinationCountry: "JP",
		DepartsOn: time.Now().AddDate(0, 0, 1), ReturnsOn: time.Now().AddDate(0, 0, 10),
		RequestDeadline: time.Now().Add(72 * time.Hour), Capacity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	o, err := f.orders.Create(ctx, f.buyer, tr.Slug, order.CreateParams{ProductName: "Item", Quantity: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.orders.Approve(ctx, f.jastiper, o.ID, 45_000, 10_000); err != nil {
		t.Fatal(err)
	}
	p, err := f.payments.Checkout(ctx, f.buyer, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.payments.SimulatePaid(ctx, f.buyer, p.ID); err != nil {
		t.Fatal(err)
	}
	if o, err = f.orders.MarkShipped(ctx, f.jastiper, o.ID); err != nil {
		t.Fatal(err)
	}
	return o
}

func (f fixture) status(t *testing.T, orderID uuid.UUID) order.Status {
	t.Helper()
	o, _, err := f.orders.Get(context.Background(), f.adm, orderID)
	if err != nil {
		t.Fatal(err)
	}
	return o.Status
}

func (f fixture) balance(t *testing.T, kind string, orderID uuid.UUID) int64 {
	t.Helper()
	b, err := db.New(f.pool).LedgerOrderBalance(context.Background(), db.LedgerOrderBalanceParams{Kind: kind, OrderID: &orderID})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (f fixture) unread(t *testing.T, userID uuid.UUID, typ string) int {
	t.Helper()
	page, err := f.notifier.List(context.Background(), userID, notification.ListParams{UnreadOnly: true, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, it := range page.Items {
		if it.Type == typ {
			n++
		}
	}
	return n
}

func TestDisputeRefund(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	o := f.shippedOrder(t)

	if _, err := f.svc.Open(ctx, uuid.New(), o.ID, OpenParams{Category: CategoryDamaged, Description: "Rusak"}); !errors.Is(err, order.ErrNotFound) {
		t.Errorf("Open(other buyer) error = %v, want order.ErrNotFound", err)
	}
	d, err := f.svc.Open(ctx, f.buyer, o.ID, OpenParams{
		Category:    CategoryDamaged,
		Description: "Kotak penyok dan isinya pecah",
		Evidence:    []EvidenceInput{{UploadID: uuid.New(), Note: "Kotak"}, {UploadID: uuid.New()}},
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if d.Status != StatusOpen || len(d.Evidence) != 2 || d.Evidence[0].URL == "" || d.Evidence[0].UploaderRole != "buyer" {
		t.Errorf("dispute = %+v", d)
	}
	if got := f.status(t, o.ID); got != order.StatusDisputed {
		t.Fatalf("order status = %s, want disputed", got)
	}
	if f.unread(t, f.jastiper, notification.TypeDisputeOpened) != 1 || f.unread(t, f.admin, notification.TypeDisputeOpened) != 1 {
		t.Error("dispute.opened did not reach the jastiper and the admin")
	}
	if _, err := f.svc.Open(ctx, f.buyer, o.ID, OpenParams{Category: CategoryOther, Description: "Lagi"}); !errors.Is(err, order.ErrInvalidTransition) {
		t.Errorf("Open(again) error = %v, want ErrInvalidTransition", err)
	}

	stranger := order.Actor{ID: uuid.New(), Type: order.ActorBuyer}
	if _, err := f.svc.ForOrder(ctx, stranger, o.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("ForOrder(stranger) error = %v, want ErrNotFound", err)
	}
	if _, err := f.svc.PostMessage(ctx, stranger, d.ID, "Halo"); !errors.Is(err, ErrNotFound) {
		t.Errorf("PostMessage(stranger) error = %v, want ErrNotFound", err)
	}
	if _, err := f.svc.AddEvidence(ctx, f.jastiperA, d.ID, []EvidenceInput{{UploadID: uuid.New(), Note: "Resi"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.PostMessage(ctx, f.buyerA, d.ID, "Barangnya pecah"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.PostMessage(ctx, f.adm, d.ID, "Kami cek dulu"); err != nil {
		t.Fatal(err)
	}
	if f.unread(t, f.jastiper, notification.TypeDisputeMessage) != 2 || f.unread(t, f.buyer, notification.TypeDisputeMessage) != 1 {
		t.Error("dispute.message should reach the other parties only")
	}
	seen, err := f.svc.ForOrder(ctx, f.jastiperA, o.ID)
	if err != nil || len(seen.Evidence) != 3 || len(seen.Messages) != 2 || seen.Messages[1].AuthorRole != "admin" {
		t.Fatalf("ForOrder(jastiper) = %+v, %v", seen, err)
	}

	resolved, err := f.svc.Decide(ctx, f.admin, d.ID, DecisionRefund, "Bukti foto menunjukkan barang rusak")
	if err != nil {
		t.Fatalf("Decide(refund) error = %v", err)
	}
	if resolved.Status != StatusResolved || resolved.Decision != DecisionRefund || resolved.DecidedAt == nil {
		t.Errorf("resolved = %+v", resolved)
	}
	if got := f.status(t, o.ID); got != order.StatusRefunded {
		t.Errorf("order status = %s, want refunded", got)
	}
	for _, kind := range []string{"escrow", "buyer_refundable", "gateway_clearing"} {
		if b := f.balance(t, kind, o.ID); b != 0 {
			t.Errorf("%s balance = %d, want 0", kind, b)
		}
	}
	if f.unread(t, f.buyer, notification.TypeDisputeResolved) != 1 || f.unread(t, f.jastiper, notification.TypeDisputeResolved) != 1 {
		t.Error("dispute.resolved did not reach both parties")
	}
	var audits int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action = 'dispute.decided' AND target_id = $1`, d.ID.String()).Scan(&audits); err != nil || audits != 1 {
		t.Errorf("dispute.decided audit rows = %d, %v; want 1", audits, err)
	}

	if _, err := f.svc.Decide(ctx, f.admin, d.ID, DecisionRelease, "Berubah pikiran"); !errors.Is(err, ErrResolved) {
		t.Errorf("Decide(again) error = %v, want ErrResolved", err)
	}
	if _, err := f.svc.PostMessage(ctx, f.buyerA, d.ID, "Terima kasih"); !errors.Is(err, ErrResolved) {
		t.Errorf("PostMessage(resolved) error = %v, want ErrResolved", err)
	}
	if b := f.balance(t, "escrow", o.ID); b != 0 {
		t.Errorf("escrow after a refused second decision = %d, want 0", b)
	}
}

func TestDisputeRelease(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	o := f.shippedOrder(t)
	d, err := f.svc.Open(ctx, f.buyer, o.ID, OpenParams{Category: CategoryNotReceived, Description: "Belum sampai"})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := f.payments.JastiperBalance(ctx, f.jastiper)

	if _, err := f.svc.Decide(ctx, f.admin, d.ID, DecisionRelease, "Resi menunjukkan sudah diterima"); err != nil {
		t.Fatalf("Decide(release) error = %v", err)
	}
	after, _ := f.payments.JastiperBalance(ctx, f.jastiper)
	if got := f.status(t, o.ID); got != order.StatusReleased || after-before != 95_000 || f.balance(t, "escrow", o.ID) != 0 {
		t.Errorf("status %s, jastiper +%d, escrow %d; want released, +95000, 0", got, after-before, f.balance(t, "escrow", o.ID))
	}

	open, err := f.svc.List(ctx, StatusOpen, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range open.Items {
		if it.ID == d.ID {
			t.Error("resolved dispute listed as open")
		}
	}
	detail, err := f.svc.Get(ctx, f.admin, d.ID)
	if err != nil || detail.Order.ID != o.ID || len(detail.Events) == 0 || detail.Dispute.Decision != DecisionRelease {
		t.Errorf("Get() = %+v, %v", detail, err)
	}
}
