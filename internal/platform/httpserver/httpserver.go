// Package httpserver owns shared HTTP application setup. Feature routes are
// registered by their respective use-case packages as they are implemented.
package httpserver

import "github.com/gofiber/fiber/v2"

// New creates the Fiber application. This foundation intentionally registers
// no feature routes.
func New() *fiber.App {
	return fiber.New()
}
