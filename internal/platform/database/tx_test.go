package database

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS tx_test; CREATE TABLE tx_test (n int)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DROP TABLE tx_test`) })
	return pool
}

func count(t *testing.T, pool *pgxpool.Pool) int {
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM tx_test`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func insert(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := Conn(ctx, pool).Exec(ctx, `INSERT INTO tx_test VALUES (1)`)
	return err
}

func TestWithTx(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	errBoom := errors.New("boom")

	if err := WithTx(ctx, pool, func(ctx context.Context) error { return insert(ctx, pool) }); err != nil {
		t.Fatal(err)
	}
	if got := count(t, pool); got != 1 {
		t.Fatalf("after commit: count = %d, want 1", got)
	}

	err := WithTx(ctx, pool, func(ctx context.Context) error {
		if err := insert(ctx, pool); err != nil {
			return err
		}
		return WithTx(ctx, pool, func(ctx context.Context) error {
			if err := insert(ctx, pool); err != nil {
				return err
			}
			return errBoom
		})
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want %v", err, errBoom)
	}
	if got := count(t, pool); got != 1 {
		t.Fatalf("after rollback: count = %d, want 1", got)
	}
}
