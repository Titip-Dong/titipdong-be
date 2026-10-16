package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Titip-Dong/titipdong-be/db/migrations"
	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/order"
	"github.com/Titip-Dong/titipdong-be/internal/payment"
	"github.com/Titip-Dong/titipdong-be/internal/platform/config"
	"github.com/Titip-Dong/titipdong-be/internal/platform/database"
	"github.com/Titip-Dong/titipdong-be/internal/platform/httpserver"
	"github.com/Titip-Dong/titipdong-be/internal/platform/jobs"
	"github.com/Titip-Dong/titipdong-be/internal/platform/logging"
	"github.com/Titip-Dong/titipdong-be/internal/platform/monitoring"
	"github.com/Titip-Dong/titipdong-be/internal/shipment"
	"github.com/Titip-Dong/titipdong-be/internal/trip"
	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

const shutdownTimeout = 20 * time.Second

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	if err := logging.ConfigureDefault(); err != nil {
		log.Fatal(err)
	}
	if err := run(cfg); err != nil {
		slog.Error("API stopped", "error", err)
		os.Exit(1)
	}
}

func run(cfg config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := database.Migrate(ctx, pool, migrations.FS); err != nil {
		return err
	}

	if len(os.Args) > 1 && os.Args[1] == "seed-demo" {
		return seedDemo(ctx, pool)
	}

	metrics := monitoring.New()
	app, api := httpserver.NewWithMetrics(metrics, cfg.CORSOrigins...)
	httpserver.RegisterReadiness(api, pool.Ping)
	registerRoutes(api, pool, cfg)

	runner := jobs.New(pool)
	runner.Observe(func(job, outcome string, d time.Duration) { metrics.ObserveBackgroundJob(job, outcome, d) })
	runner.Start(ctx)

	listenErr := make(chan error, 1)
	go func() { listenErr <- app.Listen(":" + cfg.Port) }()

	select {
	case err := <-listenErr:
		stop()
		runner.Wait()
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting_down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err = app.ShutdownWithContext(shutdownCtx)
	runner.Wait()
	if err := errors.Join(err, <-listenErr); err != nil {
		return err
	}
	slog.Info("shutdown_complete")
	return nil
}

func registerRoutes(api huma.API, pool *pgxpool.Pool, cfg config.Config) {
	authService := auth.NewService(pool, cfg.JWTSecret)
	api.UseMiddleware(auth.Middleware(api, authService))
	auth.NewHandler(authService, cfg.CookieSecure).RegisterRoutes(api)
	tripService := trip.NewService(pool)
	trip.NewHandler(tripService).RegisterRoutes(api)
	orderService := order.NewService(pool, tripService)
	order.NewHandler(orderService).RegisterRoutes(api)
	paymentService := payment.NewService(pool, orderService, payment.FakeGateway{})
	payment.NewHandler(paymentService, cfg.PaymentGateway == "fake").RegisterRoutes(api)
	shipment.NewHandler(shipment.NewService(pool, orderService, paymentService)).RegisterRoutes(api)
}
