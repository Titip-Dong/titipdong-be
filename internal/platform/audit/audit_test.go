package audit

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/Titip-Dong/titipdong-be/db/migrations"
	"github.com/Titip-Dong/titipdong-be/internal/platform/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
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
	return pool
}

func countFor(t *testing.T, pool *pgxpool.Pool, targetID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE target_id = $1`, targetID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestWriteCommitsAndRollsBackWithTheTransaction(t *testing.T) {
	pool := testPool(t)
	w := NewWriter(pool)
	ctx := context.Background()
	committed, rolledBack := uuid.NewString(), uuid.NewString()
	errBoom := errors.New("boom")

	err := database.WithTx(ctx, pool, func(ctx context.Context) error {
		return w.Write(ctx, Entry{ActorType: "system", Action: "order.released", TargetType: "order", TargetID: committed,
			Metadata: map[string]any{"amount": 950000}})
	})
	if err != nil {
		t.Fatal(err)
	}
	err = database.WithTx(ctx, pool, func(ctx context.Context) error {
		if err := w.Write(ctx, Entry{ActorType: "system", Action: "order.released", TargetType: "order", TargetID: rolledBack}); err != nil {
			return err
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want %v", err, errBoom)
	}

	if got := countFor(t, pool, committed); got != 1 {
		t.Errorf("committed entries = %d, want 1", got)
	}
	if got := countFor(t, pool, rolledBack); got != 0 {
		t.Errorf("rolled back entries = %d, want 0", got)
	}
}

func TestAuditLogIsAppendOnly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	target := uuid.NewString()
	if err := NewWriter(pool).Write(ctx, Entry{ActorType: "system", Action: "test", TargetType: "order", TargetID: target}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE audit_logs SET action = 'changed' WHERE target_id = $1`, target); err == nil {
		t.Error("UPDATE succeeded, want append-only error")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM audit_logs WHERE target_id = $1`, target); err == nil {
		t.Error("DELETE succeeded, want append-only error")
	}
}

func TestSystemActionsHaveNoActor(t *testing.T) {
	pool := testPool(t)
	actor := uuid.New()
	err := NewWriter(pool).Write(context.Background(), Entry{ActorID: &actor, ActorType: "system", Action: "test", TargetType: "order", TargetID: uuid.NewString()})
	if err == nil {
		t.Error("system entry with an actor ID was accepted")
	}
}
