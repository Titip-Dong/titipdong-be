// Package jobs runs periodic background work inside the API process. Each job
// holds a PostgreSQL advisory lock while it runs, so only one replica runs it.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Job struct {
	Name  string
	Every time.Duration
	// Run must be idempotent: a run can be skipped, repeated, or interrupted.
	Run func(ctx context.Context) error
}

type locker interface {
	// TryRun runs fn while holding the lock for name and reports false when
	// another holder has it.
	TryRun(ctx context.Context, name string, fn func(ctx context.Context) error) (bool, error)
}

type Runner struct {
	locker  locker
	jobs    []Job
	observe func(job, outcome string, duration time.Duration)
	wg      sync.WaitGroup
}

func New(pool *pgxpool.Pool, jobs ...Job) *Runner {
	return &Runner{locker: pgLocker{pool: pool}, jobs: jobs}
}

// Observe reports every completed run with outcome "success" or "error".
func (r *Runner) Observe(fn func(job, outcome string, duration time.Duration)) {
	r.observe = fn
}

// Start runs every job once and then on its interval until ctx is done.
func (r *Runner) Start(ctx context.Context) {
	for _, j := range r.jobs {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.loop(ctx, j)
		}()
	}
}

// Wait blocks until every job loop has returned after ctx was cancelled.
func (r *Runner) Wait() {
	r.wg.Wait()
}

func (r *Runner) loop(ctx context.Context, j Job) {
	ticker := time.NewTicker(j.Every)
	defer ticker.Stop()
	for {
		r.runOnce(ctx, j)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runner) runOnce(ctx context.Context, j Job) {
	if ctx.Err() != nil {
		return
	}
	started := time.Now()
	ran, err := r.locker.TryRun(ctx, j.Name, func(ctx context.Context) (err error) {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("panic: %v", p)
			}
		}()
		return j.Run(ctx)
	})
	if ctx.Err() != nil || (!ran && err == nil) {
		return
	}
	outcome := "success"
	if err != nil {
		outcome = "error"
		slog.ErrorContext(ctx, "job_failed", "job", j.Name, "error", err)
	} else {
		slog.DebugContext(ctx, "job_completed", "job", j.Name, "duration_ms", time.Since(started).Milliseconds())
	}
	if r.observe != nil {
		r.observe(j.Name, outcome, time.Since(started))
	}
}

type pgLocker struct {
	pool *pgxpool.Pool
}

func (l pgLocker) TryRun(ctx context.Context, name string, fn func(ctx context.Context) error) (bool, error) {
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()

	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, "job:"+name).Scan(&locked); err != nil {
		return false, err
	}
	if !locked {
		return false, nil
	}
	runErr := fn(ctx)
	// Unlock even when ctx is cancelled; a connection that keeps the lock is closed instead.
	if _, err := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, "job:"+name); err != nil {
		_ = conn.Conn().Close(context.WithoutCancel(ctx))
	}
	return true, runErr
}
