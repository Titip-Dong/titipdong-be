package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/Titip-Dong/titipdong-be/internal/platform/config"
	"github.com/Titip-Dong/titipdong-be/internal/platform/httpserver"
)

// openAPISpec registers every operation without a database and returns the spec.
func openAPISpec() ([]byte, error) {
	_, api := httpserver.New()
	httpserver.RegisterReadiness(api, func(context.Context) error { return nil })
	registerRoutes(api, nil, config.Config{JWTSecret: strings.Repeat("x", 32), PaymentGateway: "fake"}, nil)
	raw, err := json.Marshal(api.OpenAPI())
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}
