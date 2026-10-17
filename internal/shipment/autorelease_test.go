package shipment

import (
	"context"
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
	"github.com/Titip-Dong/titipdong-be/internal/upload"
	"github.com/google/uuid"
)

func TestAutoRelease(t *testing.T) {
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
	buyer, jastiper := user(auth.RoleBuyer), user(auth.RoleJastiper)
	trips := trip.NewService(pool)
	notifier := notification.NewService(pool)
	orders := order.NewService(pool, trips, upload.NewService(pool, nil), notifier)
	payments := payment.NewService(pool, orders, notifier, payment.FakeGateway{})
	svc := NewService(pool, orders, payments, upload.NewService(pool, nil), notifier)

	tr, err := trips.Create(ctx, jastiper, trip.CreateParams{
		Title: "Jastip Test", DestinationCity: "Tokyo", DestinationCountry: "JP",
		DepartsOn: time.Now().AddDate(0, 0, 1), ReturnsOn: time.Now().AddDate(0, 0, 10),
		RequestDeadline: time.Now().Add(72 * time.Hour), Capacity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	// shipped returns a paid order of total 100.000, shipped `ago` in the past.
	shipped := func(ago time.Duration) uuid.UUID {
		t.Helper()
		o, err := orders.Create(ctx, buyer, tr.Slug, order.CreateParams{ProductName: "Item", Quantity: 1})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := orders.Approve(ctx, jastiper, o.ID, 100_000, 0); err != nil {
			t.Fatal(err)
		}
		p, err := payments.Checkout(ctx, buyer, o.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := payments.SimulatePaid(ctx, buyer, p.ID); err != nil {
			t.Fatal(err)
		}
		sh, err := svc.Ship(ctx, jastiper, o.ID, ShipParams{Method: MethodCOD})
		if err != nil {
			t.Fatal(err)
		}
		if want := sh.ShippedAt.Add(autoReleaseAfter); sh.AutoReleaseAt == nil || !sh.AutoReleaseAt.Equal(want) {
			t.Errorf("autoReleaseAt = %v, want %v", sh.AutoReleaseAt, want)
		}
		if _, err := pool.Exec(ctx, `UPDATE shipments SET shipped_at = now() - $2::interval WHERE order_id = $1`,
			o.ID, ago.String()); err != nil {
			t.Fatal(err)
		}
		return o.ID
	}
	due := shipped(8 * 24 * time.Hour)
	recent := shipped(2 * 24 * time.Hour)
	disputed := shipped(9 * 24 * time.Hour)
	if _, err := orders.MarkDisputed(ctx, buyer, disputed, "Rusak"); err != nil {
		t.Fatal(err)
	}
	before, _ := payments.JastiperBalance(ctx, jastiper)

	for range 2 {
		if err := svc.AutoRelease(ctx); err != nil {
			t.Fatalf("AutoRelease() error = %v", err)
		}
	}

	status := func(id uuid.UUID) order.Status {
		o, _, err := orders.Get(ctx, order.Actor{ID: buyer, Type: order.ActorBuyer}, id)
		if err != nil {
			t.Fatal(err)
		}
		return o.Status
	}
	if got := status(due); got != order.StatusReleased {
		t.Errorf("due order = %s, want released", got)
	}
	if got := status(recent); got != order.StatusShipped {
		t.Errorf("recent order = %s, want shipped", got)
	}
	if got := status(disputed); got != order.StatusDisputed {
		t.Errorf("disputed order = %s, want disputed", got)
	}
	if after, _ := payments.JastiperBalance(ctx, jastiper); after-before != 95_000 {
		t.Errorf("jastiper balance +%d after two runs, want +95000 once", after-before)
	}

	sh, err := svc.Get(ctx, order.Actor{ID: buyer, Type: order.ActorBuyer}, due)
	if err != nil || sh.DeliveredAt == nil || sh.AutoReleaseAt != nil {
		t.Errorf("released shipment = %+v, %v; want deliveredAt set and no autoReleaseAt", sh, err)
	}
	if sh, _ := svc.Get(ctx, order.Actor{ID: buyer, Type: order.ActorBuyer}, recent); sh.AutoReleaseAt == nil {
		t.Error("recent shipment has no autoReleaseAt")
	}
	_, events, _ := orders.Get(ctx, order.Actor{ID: buyer, Type: order.ActorBuyer}, due)
	var systemDelivered bool
	for _, e := range events {
		systemDelivered = systemDelivered || (e.ToStatus == order.StatusDelivered && e.ActorType == order.ActorSystem)
	}
	if !systemDelivered {
		t.Error("timeline has no system delivered event")
	}
}
