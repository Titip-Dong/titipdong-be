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

func TestCORS(t *testing.T) {
	app, _ := New("http://localhost:5173")
	for _, tc := range []struct {
		origin, want string
	}{
		{"http://localhost:5173", "http://localhost:5173"},
		{"https://evil.example", ""},
	} {
		req := httptest.NewRequest(http.MethodOptions, "/health", nil)
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("Access-Control-Request-Method", http.MethodGet)
		res, err := app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		_ = res.Body.Close()
		if got := res.Header.Get("Access-Control-Allow-Origin"); got != tc.want {
			t.Errorf("origin %s: Allow-Origin = %q, want %q", tc.origin, got, tc.want)
		}
		if tc.want != "" && res.Header.Get("Access-Control-Allow-Credentials") != "true" {
			t.Errorf("origin %s: Allow-Credentials not set", tc.origin)
		}
	}
}
