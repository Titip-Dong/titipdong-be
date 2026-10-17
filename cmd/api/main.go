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
	"github.com/Titip-Dong/titipdong-be/internal/dispute"
	"github.com/Titip-Dong/titipdong-be/internal/notification"
	"github.com/Titip-Dong/titipdong-be/internal/order"
	"github.com/Titip-Dong/titipdong-be/internal/payment"
	"github.com/Titip-Dong/titipdong-be/internal/platform/blob"
	"github.com/Titip-Dong/titipdong-be/internal/platform/config"
	"github.com/Titip-Dong/titipdong-be/internal/platform/database"
	"github.com/Titip-Dong/titipdong-be/internal/platform/httpserver"
	"github.com/Titip-Dong/titipdong-be/internal/platform/jobs"
	"github.com/Titip-Dong/titipdong-be/internal/platform/logging"
	"github.com/Titip-Dong/titipdong-be/internal/platform/monitoring"
	"github.com/Titip-Dong/titipdong-be/internal/shipment"
	"github.com/Titip-Dong/titipdong-be/internal/trip"
	"github.com/Titip-Dong/titipdong-be/internal/upload"
	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

const shutdownTimeout = 20 * time.Second

func main() {
	if len(os.Args) > 1 && os.Args[1] == "openapi" {
		spec, err := openAPISpec()
		if err != nil {
			log.Fatal(err)
		}
		_, _ = os.Stdout.Write(spec)
		return
	}
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

	store, err := blobStore(ctx, cfg)
	if err != nil {
		return err
	}

	metrics := monitoring.New()
	app, api := httpserver.NewWithMetrics(metrics, cfg.CORSOrigins...)
	httpserver.RegisterReadiness(api, pool.Ping)
	backgroundJobs := registerRoutes(api, pool, cfg, store)

	runner := jobs.New(pool, backgroundJobs...)
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

func blobStore(ctx context.Context, cfg config.Config) (*blob.Store, error) {
	if cfg.BlobConnectionString == "" {
		slog.Warn("file uploads disabled: AZURE_STORAGE_CONNECTION_STRING is not set")
		return nil, nil
	}
	store, err := blob.New(blob.Config{
		ConnectionString: cfg.BlobConnectionString,
		Container:        cfg.BlobContainer,
		PublicURL:        cfg.BlobPublicURL,
	})
	if err != nil {
		return nil, err
	}
	if err := store.Setup(ctx, cfg.CORSOrigins); err != nil {
		return nil, err
	}
	return store, nil
}

// registerRoutes wires every module and returns their background jobs.
func registerRoutes(api huma.API, pool *pgxpool.Pool, cfg config.Config, store *blob.Store) []jobs.Job {
	uploadService := upload.NewService(pool, store)
	authService := auth.NewService(pool, cfg.JWTSecret)
	api.UseMiddleware(auth.Middleware(api, authService))
	auth.NewHandler(authService, cfg.CookieSecure).RegisterRoutes(api)
	upload.NewHandler(uploadService).RegisterRoutes(api)
	notificationService := notification.NewService(pool)
	notification.NewHandler(notificationService).RegisterRoutes(api)
	tripService := trip.NewService(pool)
	trip.NewHandler(tripService).RegisterRoutes(api)
	orderService := order.NewService(pool, tripService, notificationService)
	order.NewHandler(orderService).RegisterRoutes(api)
	paymentService := payment.NewService(pool, orderService, notificationService, payment.FakeGateway{})
	payment.NewHandler(paymentService, cfg.PaymentGateway == "fake").RegisterRoutes(api)
	shipment.NewHandler(shipment.NewService(pool, orderService, paymentService, notificationService)).RegisterRoutes(api)
	disputeService := dispute.NewService(pool, orderService, paymentService, uploadService, notificationService)
	dispute.NewHandler(disputeService).RegisterRoutes(api)

	return []jobs.Job{
		{Name: "uploads.purge_pending", Every: time.Hour, Run: uploadService.PurgePending},
	}
}
