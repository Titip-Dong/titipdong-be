package main

import (
	"encoding/json"
	"testing"
)

func TestOpenAPISpecIncludesEveryModule(t *testing.T) {
	raw, err := openAPISpec()
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		OpenAPI string                    `json:"openapi"`
		Paths   map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.OpenAPI != "3.1.0" {
		t.Errorf("openapi = %q, want 3.1.0", spec.OpenAPI)
	}
	for _, path := range []string{
		"/health", "/health/ready", "/auth/login", "/trips", "/trips/{slug}/orders",
		"/orders/{id}/payments", "/payments/{id}/simulate", "/orders/{id}/shipment",
	} {
		if _, ok := spec.Paths[path]; !ok {
			t.Errorf("spec does not document %s", path)
		}
	}
}
