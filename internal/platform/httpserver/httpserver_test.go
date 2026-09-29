package httpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestMetrics(t *testing.T) {
	app, _ := New()
	if _, err := app.Test(httptest.NewRequest(http.MethodGet, "/health", nil)); err != nil {
		t.Fatalf("request failed: %v", err)
	}

	res, err := app.Test(httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Errorf("status code = %d, want %d", res.StatusCode, http.StatusOK)
	}
	if contentType := res.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/plain") {
		t.Errorf("Content-Type = %q, want Prometheus text format", contentType)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	for _, metric := range []string{
		"http_server_requests_total",
		"http_server_request_duration_seconds",
		"http_server_in_flight_requests",
		"go_goroutines",
		"process_cpu_seconds_total",
	} {
		if !strings.Contains(string(body), metric) {
			t.Errorf("metrics response does not contain %q", metric)
		}
	}
}

func TestRequestLogMiddlewareSetsRequestID(t *testing.T) {
	app, _ := New()
	res, err := app.Test(httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer res.Body.Close()

	if requestID := res.Header.Get("X-Request-ID"); requestID == "" {
		t.Error("X-Request-ID header is empty")
	}
}
