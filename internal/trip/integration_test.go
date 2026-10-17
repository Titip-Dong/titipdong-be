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
	svc := NewService(pool)
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
	if err := svc.Release(ctx, tr.ID, 4); err == nil {
		t.Error("Release(more than reserved) succeeded")
	}
	if _, err := pool.Exec(ctx, `UPDATE trips SET reserved_capacity = capacity + 1 WHERE id = $1`, tr.ID); err == nil {
		t.Error("overbooking UPDATE succeeded, want CHECK violation")
	}
	if got, _ := svc.ByID(ctx, tr.ID); got.RemainingCapacity() != 2 {
		t.Errorf("remaining = %d, want 2", got.RemainingCapacity())
	}
}
