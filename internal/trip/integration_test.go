package trip

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Titip-Dong/titipdong-be/db/migrations"
	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/platform/database"
	"github.com/google/uuid"
)

func TestConcurrentReserveNeverOverbooks(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := database.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	jastiper, err := auth.Provision(ctx, pool, auth.RegisterParams{
		Name: "Jastiper", Email: uuid.NewString() + "@test.local", Password: "password123", Role: auth.RoleJastiper,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(pool, nil)
	tr, err := svc.Create(ctx, jastiper.ID, CreateParams{
		Title: "Jastip Test", DestinationCity: "Tokyo", DestinationCountry: "JP",
		DepartsOn: time.Now().AddDate(0, 0, 1), ReturnsOn: time.Now().AddDate(0, 0, 10),
		RequestDeadline: time.Now().Add(72 * time.Hour), Capacity: 5,
	})
	if err != nil {
		t.Fatal(err)
	}

	const attempts = 20
	var wg sync.WaitGroup
	errs := make(chan error, attempts)
	for range attempts {
		wg.Go(func() { errs <- svc.Reserve(ctx, tr.ID, 1) })
	}
	wg.Wait()
	close(errs)
	var ok, full int
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrCapacityFull):
			full++
		default:
			t.Errorf("Reserve() error = %v", err)
		}
	}
	got, err := svc.ByID(ctx, tr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok != 5 || full != attempts-5 || got.ReservedCapacity != 5 {
		t.Fatalf("ok %d, full %d, reserved %d; want 5, %d, 5", ok, full, got.ReservedCapacity, attempts-5)
	}

	if err := svc.Release(ctx, tr.ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE trips SET reserved_capacity = capacity + 1 WHERE id = $1`, tr.ID); err == nil {
		t.Error("overbooking UPDATE succeeded, want CHECK violation")
	}
	if got, _ := svc.ByID(ctx, tr.ID); got.RemainingCapacity() != 2 {
		t.Errorf("remaining = %d, want 2", got.RemainingCapacity())
	}
}

func TestCloseExpiredKeepsTripsWithActiveOrders(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := database.Connect(ctx, dbURL)
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
	jastiper, buyer := user(auth.RoleJastiper), user(auth.RoleBuyer)
	svc := NewService(pool, nil)
	newTrip := func() Trip {
		t.Helper()
		tr, err := svc.Create(ctx, jastiper, CreateParams{
			Title: "Jastip Test", DestinationCity: "Tokyo", DestinationCountry: "JP",
			DepartsOn: time.Now().AddDate(0, 0, 1), ReturnsOn: time.Now().AddDate(0, 0, 10),
			RequestDeadline: time.Now().Add(72 * time.Hour), Capacity: 5,
		})
		if err != nil {
			t.Fatal(err)
		}
		// Move the trip into the past: deadline and return date both passed.
		_, err = pool.Exec(ctx, `UPDATE trips SET departs_on = current_date - 10, returns_on = current_date - 2,
			request_deadline = now() - interval '5 days' WHERE id = $1`, tr.ID)
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	idle, busy := newTrip(), newTrip()
	_, err = pool.Exec(ctx, `INSERT INTO orders (id, code, trip_id, buyer_id, jastiper_id, product_name, quantity)
		VALUES ($1, $2, $3, $4, $5, 'Item', 1)`, uuid.New(), "TD-"+uuid.NewString()[:6], busy.ID, buyer, jastiper)
	if err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := svc.CloseExpired(ctx); err != nil {
			t.Fatalf("CloseExpired() error = %v", err)
		}
	}
	if got, _ := svc.ByID(ctx, idle.ID); got.Status != StatusCompleted {
		t.Errorf("trip without orders = %s, want completed", got.Status)
	}
	if got, _ := svc.ByID(ctx, busy.ID); got.Status != StatusClosed {
		t.Errorf("trip with a requested order = %s, want closed", got.Status)
	}
}

func TestCatalogQueries(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := database.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	jastiper, err := auth.Provision(ctx, pool, auth.RegisterParams{
		Name: "Jastiper", Email: uuid.NewString() + "@test.local", Password: "password123", Role: auth.RoleJastiper,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(pool, nil)
	tr, err := svc.Create(ctx, jastiper.ID, CreateParams{
		Title: "Jastip Test", DestinationCity: "Tokyo", DestinationCountry: "JP",
		DepartsOn: time.Now().AddDate(0, 0, 1), ReturnsOn: time.Now().AddDate(0, 0, 10),
		RequestDeadline: time.Now().Add(72 * time.Hour), Capacity: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	name := func(s string) *string { return &s }
	price := int64(30_000)
	a, err := svc.AddItem(ctx, jastiper.ID, tr.ID, ItemParams{Name: name("A"), ReferencePrice: &price})
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.AddItem(ctx, jastiper.ID, tr.ID, ItemParams{Name: name("B"), Description: name("Kedua")})
	if err != nil || a.Position != 0 || b.Position != 1 {
		t.Fatalf("positions = %d, %d (%v); want 0, 1", a.Position, b.Position, err)
	}
	last := 5
	if _, err := svc.UpdateItem(ctx, jastiper.ID, tr.ID, a.ID, ItemParams{Position: &last, Name: name("A2")}); err != nil {
		t.Fatal(err)
	}
	items, err := svc.Items(ctx, tr.ID)
	if err != nil || len(items) != 2 || items[0].ID != b.ID || items[1].Name != "A2" || *items[1].ReferencePrice != 30_000 || items[0].Description != "Kedua" {
		t.Fatalf("items = %+v, %v", items, err)
	}
	if err := svc.DeleteItem(ctx, jastiper.ID, tr.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	c, err := svc.AddItem(ctx, jastiper.ID, tr.ID, ItemParams{Name: name("C")})
	if err != nil || c.Position != 2 {
		t.Errorf("position after deleting the last item = %d, %v; want 2 (after B)", c.Position, err)
	}
}

func TestListDateFilterQuery(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := database.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	jastiper, err := auth.Provision(ctx, pool, auth.RegisterParams{
		Name: "Jastiper", Email: uuid.NewString() + "@test.local", Password: "password123", Role: auth.RoleJastiper,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(pool, nil)
	// A unique far-future departure keeps other tests' trips out of the window.
	departs := time.Date(2090+time.Now().Nanosecond()%9, time.March, 1+time.Now().Nanosecond()%27, 0, 0, 0, 0, time.UTC)
	city := "Datefilter-" + uuid.NewString()[:8]
	tr, err := svc.Create(ctx, jastiper.ID, CreateParams{
		Title: "Jastip Test", DestinationCity: city, DestinationCountry: "JP",
		DepartsOn: departs, ReturnsOn: departs.AddDate(0, 0, 5), RequestDeadline: time.Now().Add(72 * time.Hour), Capacity: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	find := func(from, to time.Time) bool {
		page, err := svc.List(ctx, ListParams{City: city, From: &from, To: &to, Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		return len(page.Items) == 1 && page.Items[0].ID == tr.ID
	}
	if !find(departs, departs) {
		t.Error("a trip departing on both bounds is missing")
	}
	if find(departs.AddDate(0, 0, 1), departs.AddDate(0, 0, 9)) || find(departs.AddDate(0, 0, -9), departs.AddDate(0, 0, -1)) {
		t.Error("a trip outside the window was listed")
	}
	if page, _ := svc.List(ctx, ListParams{City: city, Limit: 20}); len(page.Items) != 1 {
		t.Error("the trip is missing without date filters")
	}
}

func TestUpdateAndCancelQueries(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := database.Connect(ctx, dbURL)
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
	jastiper, buyer := user(auth.RoleJastiper), user(auth.RoleBuyer)
	svc := NewService(pool, nil)
	tr, err := svc.Create(ctx, jastiper, CreateParams{
		Title: "Jastip Test", DestinationCity: "Tokyo", DestinationCountry: "JP",
		DepartsOn: time.Now().AddDate(0, 0, 1), ReturnsOn: time.Now().AddDate(0, 0, 10),
		RequestDeadline: time.Now().Add(72 * time.Hour), Capacity: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	title, city, capacity := "Jastip Osaka", "Osaka", 9
	updated, err := svc.Update(ctx, jastiper, tr.ID, UpdateParams{Title: &title, DestinationCity: &city, Capacity: &capacity})
	if err != nil || updated.Title != title || updated.DestinationCity != city || updated.Capacity != 9 || updated.Slug != tr.Slug {
		t.Fatalf("Update() = %+v, %v", updated, err)
	}

	orderID := uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO orders (id, code, trip_id, buyer_id, jastiper_id, product_name, quantity)
		VALUES ($1, $2, $3, $4, $5, 'Item', 1)`, orderID, "TD-"+uuid.NewString()[:6], tr.ID, buyer, jastiper)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Cancel(ctx, jastiper, tr.ID); !errors.Is(err, ErrActiveOrders) {
		t.Fatalf("Cancel(requested order) error = %v, want ErrActiveOrders", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE orders SET status = 'rejected' WHERE id = $1`, orderID); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.Cancel(ctx, jastiper, tr.ID); err != nil || got.Status != StatusCancelled {
		t.Fatalf("Cancel(rejected order only) = %s, %v; want cancelled", got.Status, err)
	}
	if got, _ := svc.ByID(ctx, tr.ID); got.Status != StatusCancelled {
		t.Errorf("stored status = %s, want cancelled", got.Status)
	}
}
