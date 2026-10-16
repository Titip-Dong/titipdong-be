package logging

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestRequestMiddlewareLogsSafeStructuredFields(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	app := fiber.New()
	app.Use(RequestMiddleware(logger))
	app.Post("/example", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusCreated)
	})

	req := httptest.NewRequest(http.MethodPost, "/example", strings.NewReader(`{"password":"do-not-log"}`))
	req.Header.Set(fiber.HeaderAuthorization, "Bearer do-not-log")
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	line := output.String()
	for _, value := range []string{"request_completed", `"route":"/example"`, `"status":201`} {
		if !strings.Contains(line, value) {
			t.Errorf("log does not contain %q: %s", value, line)
		}
	}
	if strings.Contains(line, "do-not-log") {
		t.Errorf("log contains sensitive request data: %s", line)
	}
	if res.Header.Get("X-Request-ID") == "" {
		t.Error("X-Request-ID header is empty")
	}
}
