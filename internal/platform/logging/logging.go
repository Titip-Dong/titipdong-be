// Package logging owns safe, structured application logging.
package logging

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// New creates a JSON logger suitable for container stdout collection.
func New() *slog.Logger {
	return newLogger(os.Stdout)
}

// ConfigureDefault makes every application log event use the same JSON format.
// When LOG_FILE is set, the event is also copied to that file so a local Alloy
// container can ship logs from a Go process running directly on the host.
func ConfigureDefault() error {
	writers := []io.Writer{os.Stdout}
	if path := strings.TrimSpace(os.Getenv("LOG_FILE")); path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		writers = append(writers, file)
	}
	slog.SetDefault(newLogger(io.MultiWriter(writers...)))
	return nil
}

func newLogger(writer io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: levelFromEnvironment()}))
}

// RequestMiddleware emits one structured event for each application request.
// It excludes health and metrics scrapes to keep logs useful, and never records
// headers, request bodies, credentials, or personally identifiable data.
func RequestMiddleware(logger *slog.Logger) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if c.Path() == "/health" || c.Path() == "/health/ready" || c.Path() == "/metrics" {
			return c.Next()
		}

		requestID := uuid.NewString()
		c.Locals("request_id", requestID)
		c.Set("X-Request-ID", requestID)
		started := time.Now()

		err := c.Next()
		status := statusForError(c, err)
		level := slog.LevelInfo
		if status >= fiber.StatusInternalServerError {
			level = slog.LevelError
		} else if status >= fiber.StatusBadRequest {
			level = slog.LevelWarn
		}

		logger.Log(c.UserContext(), level, "request_completed",
			slog.String("service", "titipdong-api"),
			slog.String("request_id", requestID),
			slog.String("method", c.Method()),
			slog.String("route", routeTemplate(c)),
			slog.Int("status", status),
			slog.Int64("duration_ms", time.Since(started).Milliseconds()),
			slog.String("error_class", errorClass(err)),
		)

		return err
	}
}

func levelFromEnvironment() slog.Level {
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func statusForError(c *fiber.Ctx, err error) int {
	if fiberError, ok := err.(*fiber.Error); ok {
		return fiberError.Code
	}
	if err != nil {
		return fiber.StatusInternalServerError
	}
	return c.Response().StatusCode()
}

func errorClass(err error) string {
	if err == nil {
		return ""
	}
	if _, ok := err.(*fiber.Error); ok {
		return "fiber_error"
	}
	return "internal_error"
}

func routeTemplate(c *fiber.Ctx) string {
	route := c.Route()
	if route == nil || route.Path == "" || route.Path == "*" {
		return "unmatched"
	}
	return route.Path
}
