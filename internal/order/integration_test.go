package order

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Titip-Dong/titipdong-be/db/migrations"
	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/notification"
	"github.com/Titip-Dong/titipdong-be/internal/platform/database"
	"github.com/Titip-Dong/titipdong-be/internal/trip"
	"github.com/Titip-Dong/titipdong-be/internal/upload"
	"github.com/google/uuid"
)

// TestConcurrentTransitionsOnOneOrder races approvals against rejections of one requested order. Both are legal
// only from requested, so the row lock must let exactly one through. (Cancel is not used: a buyer may also cancel
// an approved order, so approve-then-cancel would be a legal sequence, not a race.)
func TestConcurrentTransitionsOnOneOrder(t *testing.T) {
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
	svc := NewService(pool, trips, upload.NewService(pool, nil), notification.NewService(pool))
	tr, err := trips.Create(ctx, jastiper, trip.CreateParams{
		Title: "Jastip Test", DestinationCity: "Tokyo", DestinationCountry: "JP",
		DepartsOn: time.Now().AddDate(0, 0, 1), ReturnsOn: time.Now().AddDate(0, 0, 10),
		RequestDeadline: time.Now().Add(72 * time.Hour), Capacity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	o, err := svc.Create(ctx, buyer, tr.Slug, CreateParams{ProductName: "Item", Quantity: 4})
	if err != nil {
		t.Fatal(err)
	}

	const each = 8
	var wg sync.WaitGroup
	type outcome struct {
		approve bool
		err     error
	}
	results := make(chan outcome, 2*each)
	for range each {
		wg.Go(func() {
			_, err := svc.Approve(ctx, jastiper, o.ID, 10_000, 0)
			results <- outcome{approve: true, err: err}
		})
		wg.Go(func() {
			_, err := svc.Reject(ctx, jastiper, o.ID, "Stok habis")
			results <- outcome{err: err}
		})
	}
	wg.Wait()
	close(results)

	var won []outcome
	for r := range results {
		switch {
		case r.err == nil:
			won = append(won, r)
		case !errors.Is(r.err, ErrInvalidTransition):
			t.Errorf("unexpected error = %v", r.err)
		}
	}
	if len(won) != 1 {
		t.Fatalf("successful transitions = %d, want exactly 1", len(won))
	}

	got, events, err := svc.Get(ctx, Actor{ID: buyer, Type: ActorBuyer}, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].FromStatus == nil || *events[1].FromStatus != StatusRequested {
		t.Fatalf("events = %+v, want the request and exactly one transition from requested", events)
	}
	left, err := trips.ByID(ctx, tr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if won[0].approve {
		if got.Status != StatusApproved || events[1].ToStatus != StatusApproved || left.RemainingCapacity() != 6 {
			t.Errorf("approve won: status %s, last event %s, remaining %d; want approved, approved, 6", got.Status, events[1].ToStatus, left.RemainingCapacity())
		}
	} else if got.Status != StatusRejected || events[1].ToStatus != StatusRejected || left.RemainingCapacity() != 10 {
		t.Errorf("reject won: status %s, last event %s, remaining %d; want rejected, rejected, 10", got.Status, events[1].ToStatus, left.RemainingCapacity())
	}
}
