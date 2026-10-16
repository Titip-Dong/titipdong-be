package jobs

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/platform/database"
)

type fakeLocker struct {
	mu   sync.Mutex
	held map[string]bool
}

func (l *fakeLocker) TryRun(ctx context.Context, name string, fn func(ctx context.Context) error) (bool, error) {
	l.mu.Lock()
	if l.held[name] {
		l.mu.Unlock()
		return false, nil
	}
	l.held[name] = true
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.held, name)
		l.mu.Unlock()
	}()
	return true, fn(ctx)
}

func newTestRunner(jobs ...Job) *Runner {
	return &Runner{locker: &fakeLocker{held: map[string]bool{}}, jobs: jobs}
}

func TestRunnerRunsOnStartAndOnInterval(t *testing.T) {
	var runs atomic.Int32
	r := newTestRunner(Job{Name: "count", Every: 10 * time.Millisecond, Run: func(context.Context) error {
		runs.Add(1)
		return nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	time.Sleep(55 * time.Millisecond)
	cancel()
	r.Wait()

	if got := runs.Load(); got < 3 {
		t.Fatalf("runs = %d, want at least 3", got)
	}
}

func TestRunnerKeepsRunningAfterErrorAndPanic(t *testing.T) {
	var runs atomic.Int32
	r := newTestRunner(Job{Name: "flaky", Every: 5 * time.Millisecond, Run: func(context.Context) error {
		switch runs.Add(1) {
		case 1:
			return errors.New("boom")
		case 2:
			panic("kaboom")
		}
		return nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	time.Sleep(40 * time.Millisecond)
	cancel()
	r.Wait()

	if got := runs.Load(); got < 3 {
		t.Fatalf("runs = %d, want the loop to survive an error and a panic", got)
	}
}

func TestRunnerObservesOutcomes(t *testing.T) {
	var mu sync.Mutex
	outcomes := map[string]int{}
	var runs atomic.Int32
	r := newTestRunner(Job{Name: "observed", Every: 5 * time.Millisecond, Run: func(context.Context) error {
		if runs.Add(1) == 1 {
			return errors.New("boom")
		}
		return nil
	}})
	r.Observe(func(job, outcome string, _ time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		if job != "observed" {
			t.Errorf("job = %q, want observed", job)
		}
		outcomes[outcome]++
	})
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	time.Sleep(30 * time.Millisecond)
	cancel()
	r.Wait()

	mu.Lock()
	defer mu.Unlock()
	if outcomes["error"] != 1 || outcomes["success"] < 1 {
		t.Fatalf("outcomes = %v, want 1 error and at least 1 success", outcomes)
	}
}

func TestRunnerStopsOnCancel(t *testing.T) {
	r := newTestRunner(Job{Name: "slow", Every: time.Hour, Run: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}})
	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	cancel()

	done := make(chan struct{})
	go func() { r.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after cancel")
	}
}

func TestPgLockerAllowsOneHolder(t *testing.T) {
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
	l := pgLocker{pool: pool}

	var innerRan bool
	ran, err := l.TryRun(ctx, "test-lock", func(ctx context.Context) error {
		ran, err := l.TryRun(ctx, "test-lock", func(context.Context) error {
			innerRan = true
			return nil
		})
		if err != nil {
			return err
		}
		if ran {
			return errors.New("second holder acquired the lock")
		}
		return nil
	})
	if err != nil || !ran {
		t.Fatalf("outer TryRun = %v, %v; want true, nil", ran, err)
	}
	if innerRan {
		t.Fatal("inner job ran while the lock was held")
	}

	ran, err = l.TryRun(ctx, "test-lock", func(context.Context) error { return nil })
	if err != nil || !ran {
		t.Fatalf("TryRun after release = %v, %v; want true, nil", ran, err)
	}
}
