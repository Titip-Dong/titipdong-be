package notification

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/platform/httpserver"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// fakeAuth accepts the tokens "buyer" and "jastiper".
type fakeAuth struct {
	auth.Service
	buyerID, jastiperID uuid.UUID
}

func (f fakeAuth) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	switch token {
	case "buyer":
		return auth.Principal{UserID: f.buyerID, Role: auth.RoleBuyer}, nil
	case "jastiper":
		return auth.Principal{UserID: f.jastiperID, Role: auth.RoleJastiper}, nil
	}
	return auth.Principal{}, auth.ErrInvalidToken
}

func newTestApp(t *testing.T) (*fiber.App, *service, fakeAuth) {
	t.Helper()
	app, api := httpserver.New()
	users := fakeAuth{buyerID: uuid.New(), jastiperID: uuid.New()}
	api.UseMiddleware(auth.Middleware(api, users))
	svc, _ := newTestService()
	NewHandler(svc).RegisterRoutes(api)
	return app, svc, users
}

func do(t *testing.T, app *fiber.App, method, path, token string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
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

func TestNotificationFlow(t *testing.T) {
	app, svc, users := newTestApp(t)
	err := svc.Notify(context.Background(), Event{
		Type:       TypePaymentReceived,
		Recipients: []uuid.UUID{users.buyerID, users.jastiperID},
		Data:       map[string]any{"orderCode": "TD-ABC123", "amount": 150000},
	})
	if err != nil {
		t.Fatal(err)
	}

	status, body := do(t, app, http.MethodGet, "/notifications/unread-count", "buyer")
	if status != http.StatusOK || body["count"] != float64(1) {
		t.Fatalf("unread-count status = %d, body = %v", status, body)
	}

	status, body = do(t, app, http.MethodGet, "/notifications?unread=true", "buyer")
	items, _ := body["items"].([]any)
	if status != http.StatusOK || len(items) != 1 {
		t.Fatalf("list status = %d, body = %v", status, body)
	}
	item, _ := items[0].(map[string]any)
	data, _ := item["data"].(map[string]any)
	if item["type"] != TypePaymentReceived || data["orderCode"] != "TD-ABC123" || item["readAt"] != nil {
		t.Errorf("item = %v", item)
	}
	id, _ := item["id"].(string)

	if status, body := do(t, app, http.MethodPost, "/notifications/"+id+"/read", "jastiper"); status != http.StatusNotFound {
		t.Errorf("read another user's notification status = %d, body = %v", status, body)
	}
	if status, _ := do(t, app, http.MethodPost, "/notifications/"+id+"/read", "buyer"); status != http.StatusNoContent {
		t.Errorf("read status = %d, want 204", status)
	}
	if _, body := do(t, app, http.MethodGet, "/notifications/unread-count", "buyer"); body["count"] != float64(0) {
		t.Errorf("unread after read = %v", body)
	}

	if status, _ := do(t, app, http.MethodPost, "/notifications/read-all", "jastiper"); status != http.StatusNoContent {
		t.Errorf("read-all status = %d, want 204", status)
	}
	if _, body := do(t, app, http.MethodGet, "/notifications/unread-count", "jastiper"); body["count"] != float64(0) {
		t.Errorf("jastiper unread after read-all = %v", body)
	}
}

func TestNotificationErrors(t *testing.T) {
	app, _, _ := newTestApp(t)
	tests := []struct {
		name, method, path, token string
		want                      int
	}{
		{"list without token", http.MethodGet, "/notifications", "", http.StatusUnauthorized},
		{"count without token", http.MethodGet, "/notifications/unread-count", "", http.StatusUnauthorized},
		{"read-all without token", http.MethodPost, "/notifications/read-all", "", http.StatusUnauthorized},
		{"bad cursor", http.MethodGet, "/notifications?cursor=garbage", "buyer", http.StatusUnprocessableEntity},
		{"limit too high", http.MethodGet, "/notifications?limit=500", "buyer", http.StatusUnprocessableEntity},
		{"read unknown", http.MethodPost, "/notifications/" + uuid.NewString() + "/read", "buyer", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if status, body := do(t, app, tt.method, tt.path, tt.token); status != tt.want {
				t.Errorf("status = %d, want %d (body %v)", status, tt.want, body)
			}
		})
	}
}
