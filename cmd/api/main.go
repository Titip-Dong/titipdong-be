package main

import (
	"context"
	"log"
	"os"

	"github.com/Titip-Dong/titipdong-be/db/migrations"
	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/order"
	"github.com/Titip-Dong/titipdong-be/internal/payment"
	"github.com/Titip-Dong/titipdong-be/internal/platform/config"
	"github.com/Titip-Dong/titipdong-be/internal/platform/database"
	"github.com/Titip-Dong/titipdong-be/internal/platform/httpserver"
	"github.com/Titip-Dong/titipdong-be/internal/trip"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	pool, err := database.Connect(context.Background(), cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	if err := database.Migrate(context.Background(), pool, migrations.FS); err != nil {
		log.Fatal(err)
	}

	if len(os.Args) > 1 && os.Args[1] == "seed-demo" {
		if err := seedDemo(context.Background(), pool); err != nil {
			log.Fatal(err)
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

	log.Fatal(app.Listen(":" + cfg.Port))
}
