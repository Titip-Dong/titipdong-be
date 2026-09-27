package httpserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealth(t *testing.T) {
	app := New(nil)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)

	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Errorf("status code = %d, want %d", res.StatusCode, http.StatusOK)
	}

	if contentType := res.Header.Get("Content-Type"); contentType != "application/json" {
		t.Errorf("Content-Type = %q, want %q", contentType, "application/json")
	}
}

func TestOpenAPISpec(t *testing.T) {
	app := New(nil)
	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)

	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Errorf("status code = %d, want %d", res.StatusCode, http.StatusOK)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	for _, path := range []string{"/health", "/auth/register", "/auth/login", "/me"} {
		if !strings.Contains(string(body), path) {
			t.Errorf("OpenAPI spec does not contain %q", path)
		}
	}
}
