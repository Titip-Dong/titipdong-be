package shipment

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
	"github.com/Titip-Dong/titipdong-be/internal/upload"
	"github.com/google/uuid"
)

func TestShipValidation(t *testing.T) {
	svc := newService(nil, nil, nil, nil, nil, time.Now)
	tests := map[string]ShipParams{
		"unknown method":       {Method: "drone"},
		"courier without code": {Method: MethodCourier, TrackingNumber: "JP123"},
		"courier without resi": {Method: MethodCourier, CourierCode: "jne"},
	}
	for name, p := range tests {
		t.Run(name, func(t *testing.T) {
			var inputErr *InputError
			if _, err := svc.Ship(t.Context(), uuid.New(), uuid.New(), p); !errors.As(err, &inputErr) {
				t.Errorf("Ship() error = %v, want *InputError", err)
			}
		})
	}
}

func TestShipAndConfirmReceipt(t *testing.T) {
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

	trips := trip.NewService(pool, nil)
	notifier := notification.NewService(pool)
	orders := order.NewService(pool, trips, upload.NewService(pool, nil), notifier)
	payments := payment.NewService(pool, orders, notifier, payment.FakeGateway{})
	svc := NewService(pool, orders, payments, upload.NewService(pool, nil), notifier)

	tr, err := trips.Create(ctx, jastiper, trip.CreateParams{
		Title: "Jastip Test", DestinationCity: "Tokyo", DestinationCountry: "JP",
		DepartsOn: time.Now().AddDate(0, 0, 1), ReturnsOn: time.Now().AddDate(0, 0, 10), RequestDeadline: time.Now().Add(72 * time.Hour),
		Capacity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	o, err := orders.Create(ctx, buyer, tr.Slug, order.CreateParams{ProductName: "Item", Quantity: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := orders.Approve(ctx, jastiper, o.ID, 50_000, 20_000); err != nil {
		t.Fatal(err)
	}
	courier := ShipParams{Method: MethodCourier, CourierCode: " JNE ", TrackingNumber: "JP123"}

	if _, err := svc.Ship(ctx, jastiper, o.ID, courier); !errors.Is(err, order.ErrInvalidTransition) {
		t.Fatalf("Ship(unpaid) error = %v, want ErrInvalidTransition", err)
	}
	p, err := payments.Checkout(ctx, buyer, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := payments.SimulatePaid(ctx, buyer, p.ID); err != nil {
		t.Fatal(err)
	}

	proof := uuid.New()
	withProof := courier
	withProof.ProofUpload = &proof
	if _, err := svc.Ship(ctx, jastiper, o.ID, withProof); !errors.Is(err, upload.ErrUnavailable) {
		t.Fatalf("Ship(proof, no blob store) error = %v, want upload.ErrUnavailable", err)
	}
	if got, _, _ := orders.Get(ctx, order.Actor{ID: buyer, Type: order.ActorBuyer}, o.ID); got.Status != order.StatusPaid {
		t.Fatalf("order after a failed proof attach = %s, want paid", got.Status)
	}
	if _, err := svc.Ship(ctx, uuid.New(), o.ID, courier); !errors.Is(err, order.ErrNotFound) {
		t.Errorf("Ship(other jastiper) error = %v, want order.ErrNotFound", err)
	}
	sh, err := svc.Ship(ctx, jastiper, o.ID, courier)
	if err != nil {
		t.Fatalf("Ship() error = %v", err)
	}
	if sh.OrderStatus != order.StatusShipped || sh.CourierCode != "jne" {
		t.Errorf("shipment = %+v", sh)
	}
	page, err := notifier.List(ctx, buyer, notification.ListParams{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if n := page.Items; len(n) != 1 || n[0].Type != notification.TypeOrderShipped || n[0].Data["trackingNumber"] != "JP123" {
		t.Errorf("buyer's latest notification = %+v, want order.shipped", n)
	}
	if _, err := svc.Ship(ctx, jastiper, o.ID, courier); !errors.Is(err, order.ErrInvalidTransition) {
		t.Errorf("Ship(again) error = %v, want ErrInvalidTransition", err)
	}

	if _, err := svc.Get(ctx, order.Actor{ID: uuid.New(), Type: order.ActorBuyer}, o.ID); !errors.Is(err, order.ErrNotFound) {
		t.Errorf("Get(stranger) error = %v, want order.ErrNotFound", err)
	}
	if _, err := svc.ConfirmReceipt(ctx, uuid.New(), o.ID); !errors.Is(err, order.ErrNotFound) {
		t.Errorf("ConfirmReceipt(stranger) error = %v, want order.ErrNotFound", err)
	}

	confirmed, err := svc.ConfirmReceipt(ctx, buyer, o.ID)
	if err != nil {
		t.Fatalf("ConfirmReceipt() error = %v", err)
	}
	if confirmed.OrderStatus != order.StatusReleased || confirmed.DeliveredAt == nil {
		t.Errorf("confirmed = %+v, want released with deliveredAt", confirmed)
	}
	if balance, _ := payments.JastiperBalance(ctx, jastiper); balance != 114_000 {
		t.Errorf("jastiper balance = %d, want 114000 (95%% of 120000)", balance)
	}
	if _, err := svc.ConfirmReceipt(ctx, buyer, o.ID); !errors.Is(err, order.ErrInvalidTransition) {
		t.Errorf("ConfirmReceipt(again) error = %v, want ErrInvalidTransition", err)
	}
}
