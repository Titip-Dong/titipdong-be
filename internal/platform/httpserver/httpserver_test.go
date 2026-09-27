package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealth(t *testing.T) {
	app, _ := New()
	res, err := app.Test(httptest.NewRequest(http.MethodGet, "/health", nil))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		t.Errorf("status code = %d, want %d", res.StatusCode, http.StatusOK)
	}
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body) != 1 || body["status"] != "ok" {
		t.Errorf("body = %v, want {\"status\":\"ok\"}", body)
	}
}

func TestOpenAPISpec(t *testing.T) {
	app, _ := New()
	res, err := app.Test(httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", res.StatusCode, http.StatusOK)
	}
	var spec struct {
		OpenAPI string         `json:"openapi"`
		Paths   map[string]any `json:"paths"`
	}
	if err := json.NewDecoder(res.Body).Decode(&spec); err != nil {
		t.Fatalf("decode spec: %v", err)
	}
	if spec.OpenAPI != "3.1.0" {
		t.Errorf("openapi = %q, want 3.1.0", spec.OpenAPI)
	}
	if _, ok := spec.Paths["/health"]; !ok {
		t.Error("spec does not document /health")
	}
}
