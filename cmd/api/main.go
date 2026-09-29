package main

import (
	"context"
	"log"
	"log/slog"
	"os"

	"github.com/Titip-Dong/titipdong-be/db/migrations"
	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/order"
	"github.com/Titip-Dong/titipdong-be/internal/payment"
	"github.com/Titip-Dong/titipdong-be/internal/platform/config"
	"github.com/Titip-Dong/titipdong-be/internal/platform/database"
	"github.com/Titip-Dong/titipdong-be/internal/platform/httpserver"
	"github.com/Titip-Dong/titipdong-be/internal/platform/logging"
	"github.com/Titip-Dong/titipdong-be/internal/shipment"
	"github.com/Titip-Dong/titipdong-be/internal/trip"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	if err := logging.ConfigureDefault(); err != nil {
		log.Fatal(err)
	}
	pool, err := database.Connect(context.Background(), cfg.DatabaseURL)
	if err != nil {
		slog.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := database.Migrate(context.Background(), pool, migrations.FS); err != nil {
		slog.Error("database migration failed", "error", err)
		os.Exit(1)
	}

	if len(os.Args) > 1 && os.Args[1] == "seed-demo" {
		if err := seedDemo(context.Background(), pool); err != nil {
			slog.Error("demo seed failed", "error", err)
			os.Exit(1)
		}
		return
	}

	app, api := httpserver.New(cfg.CORSOrigins...)
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

	if err := app.Listen(":" + cfg.Port); err != nil {
		slog.Error("API server stopped", "error", err)
		os.Exit(1)
	}
}
