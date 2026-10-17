package admin

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Titip-Dong/titipdong-be/db/migrations"
	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/notification"
	"github.com/Titip-Dong/titipdong-be/internal/order"
	"github.com/Titip-Dong/titipdong-be/internal/payment"
	"github.com/Titip-Dong/titipdong-be/internal/platform/database"
	"github.com/Titip-Dong/titipdong-be/internal/trip"
	"github.com/google/uuid"
)

func TestAdminBackOffice(t *testing.T) {
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
	user := func(role auth.Role) auth.User {
		u, err := auth.Provision(ctx, pool, auth.RegisterParams{
			Name: "Admin Test " + string(role), Email: uuid.NewString() + "@admin-test.local", Password: "password123", Role: role,
		})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	buyer, jastiper, adminUser := user(auth.RoleBuyer), user(auth.RoleJastiper), user(auth.RoleAdmin)
	users := auth.NewService(pool, "test-secret-that-is-at-least-32-bytes")
	notifier := notification.NewService(pool)
	trips := trip.NewService(pool)
	orders := order.NewService(pool, trips, notifier)
	payments := payment.NewService(pool, orders, notifier, payment.FakeGateway{})
	svc := NewService(pool, users, payments)

	tr, err := trips.Create(ctx, jastiper.ID, trip.CreateParams{
		Title: "Jastip Test", DestinationCity: "Tokyo", DestinationCountry: "JP",
		DepartsOn: time.Now().AddDate(0, 0, 1), ReturnsOn: time.Now().AddDate(0, 0, 10),
		RequestDeadline: time.Now().Add(72 * time.Hour), Capacity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	o, err := orders.Create(ctx, buyer.ID, tr.Slug, order.CreateParams{ProductName: "Item", Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := orders.Approve(ctx, jastiper.ID, o.ID, 70_000, 0); err != nil {
		t.Fatal(err)
	}
	p, err := payments.Checkout(ctx, buyer.ID, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := payments.SimulatePaid(ctx, buyer.ID, p.ID); err != nil {
		t.Fatal(err)
	}

	st, err := svc.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var paidTotal int64
	for _, oc := range st.Orders {
		if oc.Status == "paid" {
			paidTotal = oc.Total
		}
	}
	if paidTotal < 70_000 || st.GMV < paidTotal || st.Ledger.EscrowHeld < 70_000 || len(st.Users) == 0 {
		t.Errorf("stats = %+v, missing this test's paid order", st)
	}

	found, err := svc.Orders(ctx, OrderQuery{Code: o.Code[3:], Status: "paid", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if !containsOrder(found.Items, o.ID) {
		t.Errorf("Orders(code prefix without TD-) misses %s", o.Code)
	}
	found, _ = svc.Orders(ctx, OrderQuery{Code: o.Code, Status: "shipped", Limit: 20})
	if containsOrder(found.Items, o.ID) {
		t.Error("Orders(status shipped) returned a paid order")
	}

	txs, err := svc.Transactions(ctx, "payment_received", 100, "")
	if err != nil {
		t.Fatal(err)
	}
	var tx *Transaction
	for i := range txs.Items {
		if txs.Items[i].OrderID != nil && *txs.Items[i].OrderID == o.ID {
			tx = &txs.Items[i]
		}
	}
	if tx == nil || tx.OrderCode != o.Code || len(tx.Entries) != 2 {
		t.Fatalf("payment transaction = %+v", tx)
	}
	var debits, credits int64
	for _, e := range tx.Entries {
		if e.Direction == "debit" {
			debits += e.Amount
		} else {
			credits += e.Amount
		}
	}
	if debits != 70_000 || credits != 70_000 {
		t.Errorf("entries debit %d / credit %d, want 70000 each", debits, credits)
	}

	list, err := svc.Users(ctx, UserQuery{Search: buyer.Email[:12], Limit: 20})
	if err != nil || len(list.Items) != 1 || list.Items[0].ID != buyer.ID {
		t.Fatalf("Users(search) = %+v, %v", list, err)
	}

	suspended, err := svc.SetUserStatus(ctx, adminUser.ID, buyer.ID, auth.StatusSuspended)
	if err != nil || suspended.Status != "suspended" {
		t.Fatalf("SetUserStatus(suspended) = %+v, %v", suspended, err)
	}
	if _, err := users.Login(ctx, buyer.Email, "password123"); !errors.Is(err, auth.ErrSuspended) {
		t.Errorf("Login(suspended) error = %v, want ErrSuspended", err)
	}
	if list, _ := svc.Users(ctx, UserQuery{Search: buyer.Email, Status: "suspended", Limit: 20}); len(list.Items) != 1 {
		t.Errorf("Users(status suspended) = %+v", list)
	}
	if _, err := svc.SetUserStatus(ctx, adminUser.ID, buyer.ID, auth.StatusActive); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetUserStatus(ctx, adminUser.ID, adminUser.ID, auth.StatusSuspended); !errors.Is(err, auth.ErrAdminStatus) {
		t.Errorf("SetUserStatus(self) error = %v, want ErrAdminStatus", err)
	}
	if _, err := svc.SetUserStatus(ctx, adminUser.ID, uuid.New(), auth.StatusSuspended); !errors.Is(err, auth.ErrUserNotFound) {
		t.Errorf("SetUserStatus(unknown) error = %v, want ErrUserNotFound", err)
	}

	logs, err := svc.AuditLogs(ctx, AuditQuery{TargetType: "user", TargetID: buyer.ID.String(), Limit: 1})
	if err != nil || len(logs.Items) != 1 || logs.Items[0].Action != "user.activated" || logs.NextCursor == "" {
		t.Fatalf("AuditLogs(first page) = %+v, %v", logs, err)
	}
	next, err := svc.AuditLogs(ctx, AuditQuery{TargetType: "user", TargetID: buyer.ID.String(), Limit: 1, Cursor: logs.NextCursor})
	if err != nil || len(next.Items) != 1 || next.Items[0].Action != "user.suspended" || next.Items[0].ActorName == "" {
		t.Fatalf("AuditLogs(second page) = %+v, %v", next, err)
	}
	if byActor, _ := svc.AuditLogs(ctx, AuditQuery{ActorID: &adminUser.ID, Limit: 20}); len(byActor.Items) != 2 {
		t.Errorf("AuditLogs(actor) = %d rows, want 2", len(byActor.Items))
	}
}

func containsOrder(items []Order, id uuid.UUID) bool {
	for _, o := range items {
		if o.ID == id {
			return true
		}
	}
	return false
}
