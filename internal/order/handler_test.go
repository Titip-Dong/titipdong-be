package order

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/platform/httpserver"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type fakeAuth struct {
	auth.Service
}

var tokens = map[string]auth.Principal{
	"buyer":    {UserID: buyerID, Role: auth.RoleBuyer},
	"stranger": {UserID: uuid.New(), Role: auth.RoleBuyer},
	"jastiper": {UserID: jastiperID, Role: auth.RoleJastiper},
}

func (fakeAuth) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	p, ok := tokens[token]
	if !ok {
		return auth.Principal{}, auth.ErrInvalidToken
	}
	return p, nil
}

func newTestApp(t *testing.T) *fiber.App {
	t.Helper()
	app, api := httpserver.New()
	api.UseMiddleware(auth.Middleware(api, fakeAuth{}))
	NewHandler(newTestService(newClock())).RegisterRoutes(api)
	return app
}

func do(t *testing.T, app *fiber.App, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestOrderFlow(t *testing.T) {
	app := newTestApp(t)

	status, body := do(t, app, http.MethodPost, "/trips/tokyo/orders", "buyer", `{"productName":"Tokyo Banana","quantity":2}`)
	if status != http.StatusCreated || body["status"] != "requested" {
		t.Fatalf("create status = %d, body = %v", status, body)
	}
	id, _ := body["id"].(string)

	status, body = do(t, app, http.MethodPost, "/orders/"+id+"/approve", "jastiper", `{"itemPrice":50000,"jastipFee":20000}`)
	if status != http.StatusOK || body["total"] != float64(120000) || body["paymentDeadline"] == nil {
		t.Fatalf("approve status = %d, body = %v", status, body)
	}

	status, body = do(t, app, http.MethodGet, "/orders/"+id, "buyer", "")
	order, _ := body["order"].(map[string]any)
	if events, _ := body["events"].([]any); status != http.StatusOK || len(events) != 2 || order["code"] == nil {
		t.Fatalf("get status = %d, body = %v", status, body)
	}

	status, body = do(t, app, http.MethodGet, "/orders?status=approved", "jastiper", "")
	if items, _ := body["items"].([]any); status != http.StatusOK || len(items) != 1 {
		t.Fatalf("list status = %d, body = %v", status, body)
	}

	status, body = do(t, app, http.MethodPost, "/orders/"+id+"/cancel", "buyer", "")
	if status != http.StatusOK || body["status"] != "cancelled" {
		t.Fatalf("cancel status = %d, body = %v", status, body)
	}
}

func TestOrderErrors(t *testing.T) {
	app := newTestApp(t)
	_, created := do(t, app, http.MethodPost, "/trips/tokyo/orders", "buyer", `{"productName":"Tokyo Banana","quantity":1}`)
	id, _ := created["id"].(string)

	tests := []struct {
		name, method, path, token, body string
		want                            int
	}{
		{"create as jastiper", http.MethodPost, "/trips/tokyo/orders", "jastiper", `{"productName":"X","quantity":1}`, http.StatusForbidden},
		{"create on closed trip", http.MethodPost, "/trips/closed/orders", "buyer", `{"productName":"X","quantity":1}`, http.StatusConflict},
		{"create beyond capacity", http.MethodPost, "/trips/tokyo/orders", "buyer", `{"productName":"X","quantity":50}`, http.StatusConflict},
		{"create on unknown trip", http.MethodPost, "/trips/nope/orders", "buyer", `{"productName":"X","quantity":1}`, http.StatusNotFound},
		{"zero quantity", http.MethodPost, "/trips/tokyo/orders", "buyer", `{"productName":"X","quantity":0}`, http.StatusUnprocessableEntity},
		{"approve as buyer", http.MethodPost, "/orders/" + id + "/approve", "buyer", `{"itemPrice":1000,"jastipFee":0}`, http.StatusForbidden},
		{"approve zero price", http.MethodPost, "/orders/" + id + "/approve", "jastiper", `{"itemPrice":0,"jastipFee":0}`, http.StatusUnprocessableEntity},
		{"reject blank reason", http.MethodPost, "/orders/" + id + "/reject", "jastiper", `{"reason":"  "}`, http.StatusUnprocessableEntity},
		{"get as stranger", http.MethodGet, "/orders/" + id, "stranger", "", http.StatusNotFound},
		{"cancel as stranger", http.MethodPost, "/orders/" + id + "/cancel", "stranger", "", http.StatusNotFound},
		{"get without token", http.MethodGet, "/orders/" + id, "", "", http.StatusUnauthorized},
		{"trip inbox unknown trip", http.MethodGet, "/me/trips/" + uuid.NewString() + "/orders", "jastiper", "", http.StatusNotFound},
		{"trip inbox as buyer", http.MethodGet, "/me/trips/" + uuid.NewString() + "/orders", "buyer", "", http.StatusForbidden},
		{"trip inbox bad status", http.MethodGet, "/me/trips/" + uuid.NewString() + "/orders?status=lost", "jastiper", "", http.StatusUnprocessableEntity},
	}
	if _, body := do(t, app, http.MethodPost, "/trips/tokyo/orders", "buyer", `{"productName":"X","quantity":50}`); body["detail"] != "Trip is full." {
		t.Errorf("full trip detail = %v, want Trip is full.", body["detail"])
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if status, body := do(t, app, tt.method, tt.path, tt.token, tt.body); status != tt.want {
				t.Errorf("status = %d, want %d (body %v)", status, tt.want, body)
			}
		})
	}

	do(t, app, http.MethodPost, "/orders/"+id+"/reject", "jastiper", `{"reason":"Stok habis"}`)
	if status, _ := do(t, app, http.MethodPost, "/orders/"+id+"/approve", "jastiper", `{"itemPrice":1000,"jastipFee":0}`); status != http.StatusConflict {
		t.Errorf("approve rejected order status = %d, want 409", status)
	}
}
