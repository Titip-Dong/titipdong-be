// Package httpserver owns shared HTTP application setup. Feature routes are
// registered by their respective use-case packages as they are implemented.
package httpserver

import "github.com/gofiber/fiber/v2"

// New creates the Fiber application and registers infrastructure endpoints.
func New() *fiber.App {
	app := fiber.New()

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusOK).JSON(fiber.Map{"status": "ok"})
	})

	return app
}
