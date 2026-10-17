package auth

import (
	"context"
	"os"
	"testing"

	"github.com/Titip-Dong/titipdong-be/db/migrations"
	"github.com/Titip-Dong/titipdong-be/internal/platform/database"
	"github.com/google/uuid"
)

func TestPurgeExpiredTokens(t *testing.T) {
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
	svc := NewService(pool, "test-secret-that-is-at-least-32-bytes")
	email := uuid.NewString() + "@test.local"
	first, err := svc.Register(ctx, RegisterParams{Name: "Purge", Email: email, Password: "password123", Role: RoleBuyer})
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := svc.Refresh(ctx, first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Login(ctx, email, "password123"); err != nil {
		t.Fatal(err)
	}
	// Expire the rotated pair; the separate login stays valid.
	_, err = pool.Exec(ctx, `UPDATE refresh_tokens SET expires_at = now() - interval '1 day'
		WHERE token_hash = ANY($1)`, [][]byte{hashRefreshToken(first.RefreshToken), hashRefreshToken(rotated.RefreshToken)})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.PurgeExpiredTokens(ctx); err != nil {
		t.Fatalf("PurgeExpiredTokens() error = %v", err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM refresh_tokens t JOIN users u ON u.id = t.user_id WHERE u.email = $1`, email).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 1 {
		t.Errorf("tokens left = %d, want 1 (the unexpired login)", left)
	}
}
