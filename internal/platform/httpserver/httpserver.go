// Package httpserver owns the shared Fiber app and Huma API. Feature modules
// register their operations on the returned huma.API; Huma generates the
// OpenAPI 3.1 spec served at /openapi.json from those registrations.
package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Titip-Dong/titipdong-be/internal/platform/logging"
	"github.com/Titip-Dong/titipdong-be/internal/platform/monitoring"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humafiber"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	"github.com/gofiber/fiber/v2/middleware/cors"
)

func init() {
	// Handlers always return empty slices instead of nil, so arrays are never null.
	huma.DefaultArrayNullable = false

	// Huma copies the handler's error message into 5xx responses by default.
	// Log it instead so SQL and other internal details never reach clients.
	base := huma.NewErrorWithContext
	huma.NewErrorWithContext = func(ctx huma.Context, status int, msg string, errs ...error) huma.StatusError {
		if status < http.StatusInternalServerError {
			return base(ctx, status, msg, errs...)
		}
		attrs := []any{"status", status, "error", errors.Join(errs...)}
		logCtx := context.Background()
		if ctx != nil {
			logCtx = ctx.Context()
			attrs = append(attrs, "method", ctx.Method(), "path", ctx.URL().Path)
		}
		slog.ErrorContext(logCtx, msg, attrs...)
		return base(ctx, status, msg)
	}
}

type healthStatus struct {
	Status string `json:"status" example:"ok"`
}

// New creates the Fiber app and its Huma API with infrastructure endpoints
// registered.
func New(corsOrigins ...string) (*fiber.App, huma.API) {
	app := fiber.New()
	metrics := monitoring.New()
	app.Use(metrics.FiberMiddleware())
	app.Use(logging.RequestMiddleware(slog.Default()))

	if len(corsOrigins) > 0 {
		app.Use(cors.New(cors.Config{
			AllowOrigins:     strings.Join(corsOrigins, ","),
			AllowCredentials: true,
		}))
	}

	config := huma.DefaultConfig("TITIP DONG API", "1.0.0")
	// Drop the default $schema link transformer so response bodies match the
	// documented schemas exactly.
	config.CreateHooks = nil
	api := humafiber.NewV2(app, config)
	app.Get("/metrics", adaptor.HTTPHandler(metrics.Handler()))

	huma.Register(api, huma.Operation{
		OperationID: "health",
		Method:      http.MethodGet,
		Path:        "/health",
		Summary:     "Liveness and readiness probe",
		Tags:        []string{"system"},
	}, func(context.Context, *struct{}) (*struct{ Body healthStatus }, error) {
		return &struct{ Body healthStatus }{Body: healthStatus{Status: "ok"}}, nil
	})

	return app, api
}
