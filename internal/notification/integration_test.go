package notification

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/Titip-Dong/titipdong-be/db/migrations"
	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/platform/database"
	"github.com/google/uuid"
)

func TestNotifyFollowsTransaction(t *testing.T) {
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
	user, err := auth.Provision(ctx, pool, auth.RegisterParams{
		Name: "Notified", Email: uuid.NewString() + "@test.local", Password: "password123", Role: auth.RoleBuyer,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(pool)
	event := Event{Type: TypeOrderApproved, Recipients: []uuid.UUID{user.ID}, Data: map[string]any{"total": 150000}}

	errRollback := errors.New("rollback")
	err = database.WithTx(ctx, pool, func(ctx context.Context) error {
		if err := svc.Notify(ctx, event); err != nil {
			return err
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("WithTx err = %v", err)
	}
	if n, _ := svc.UnreadCount(ctx, user.ID); n != 0 {
		t.Fatalf("unread after rollback = %d, want 0", n)
	}

	if err := database.WithTx(ctx, pool, func(ctx context.Context) error { return svc.Notify(ctx, event) }); err != nil {
		t.Fatal(err)
	}
	page, err := svc.List(ctx, user.ID, ListParams{UnreadOnly: true, Limit: 20})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("list = %+v, err = %v", page, err)
	}
	if got := page.Items[0].Data["total"]; got != float64(150000) {
		t.Errorf("data.total = %v", got)
	}
	if err := svc.MarkAllRead(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := svc.UnreadCount(ctx, user.ID); n != 0 {
		t.Errorf("unread after read-all = %d, want 0", n)
	}

	admins, err := svc.AdminIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range admins {
		if id == user.ID {
			t.Error("AdminIDs returned a buyer")
		}
	}
}
