// Package httpserver owns shared HTTP application setup. Feature routes are
// registered by their respective use-case packages as they are implemented.
package httpserver

import (
	_ "embed"

	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/gofiber/fiber/v2"
)

// openAPISpec documents the routes that are available now and the proposed
// Auth & User wire contract for the next milestone. Proposed paths are not
// registered yet; their status is explicit in the document.
//
//go:embed openapi.json
var openAPISpec []byte

// New creates the Fiber application and registers infrastructure endpoints.
func New(authHandler *auth.Handler) *fiber.App {
	app := fiber.New()

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusOK).JSON(fiber.Map{"status": "ok"})
	})

	app.Get("/openapi.json", func(c *fiber.Ctx) error {
		c.Type("json")
		return c.Send(openAPISpec)
	})

	if authHandler != nil {
		authHandler.RegisterRoutes(app)
	}

	return app
}
