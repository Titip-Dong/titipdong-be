package trip

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

// fakeAuth accepts the tokens "jastiper" and "buyer".
type fakeAuth struct {
	auth.Service
	jastiperID uuid.UUID
}

func (f fakeAuth) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	switch token {
	case "jastiper":
		return auth.Principal{UserID: f.jastiperID, Role: auth.RoleJastiper}, nil
	case "buyer":
		return auth.Principal{UserID: uuid.New(), Role: auth.RoleBuyer}, nil
	}
	return auth.Principal{}, auth.ErrInvalidToken
}

func newTestApp(t *testing.T) *fiber.App {
	t.Helper()
	app, api := httpserver.New()
	api.UseMiddleware(auth.Middleware(api, fakeAuth{jastiperID: uuid.New()}))
	svc := newTestService()
	svc.uploads = fakeUploads{}
	NewHandler(svc, fakeUploads{}).RegisterRoutes(api)
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

const createBody = `{"title":"Jastip Tokyo","destinationCity":"Tokyo","destinationCountry":"JP",
	"departsOn":"2026-10-10","returnsOn":"2026-10-20","requestDeadline":"2026-10-15T12:00:00Z","capacity":10}`

func TestTripFlow(t *testing.T) {
	app := newTestApp(t)

	status, body := do(t, app, http.MethodPost, "/trips", "jastiper", createBody)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, body = %v", status, body)
	}
	slug, _ := body["slug"].(string)
	id, _ := body["id"].(string)
	if body["departsOn"] != "2026-10-10" || body["status"] != "open" || body["capacity"] != float64(10) || body["remainingCapacity"] != float64(10) {
		t.Errorf("create body = %v", body)
	}

	status, body = do(t, app, http.MethodGet, "/trips/"+slug, "", "")
	if status != http.StatusOK || body["id"] != id {
		t.Fatalf("get status = %d, body = %v", status, body)
	}

	status, body = do(t, app, http.MethodGet, "/trips?city=tok", "", "")
	if items, _ := body["items"].([]any); status != http.StatusOK || len(items) != 1 {
		t.Fatalf("list status = %d, body = %v", status, body)
	}

	status, body = do(t, app, http.MethodPost, "/trips/"+id+"/close", "jastiper", "")
	if status != http.StatusOK || body["status"] != "closed" {
		t.Fatalf("close status = %d, body = %v", status, body)
	}

	status, body = do(t, app, http.MethodGet, "/me/trips", "jastiper", "")
	if items, _ := body["items"].([]any); status != http.StatusOK || len(items) != 1 {
		t.Fatalf("my trips status = %d, body = %v", status, body)
	}
}

func TestTripErrors(t *testing.T) {
	app := newTestApp(t)
	_, created := do(t, app, http.MethodPost, "/trips", "jastiper", createBody)
	id, _ := created["id"].(string)

	tests := []struct {
		name, method, path, token, body string
		want                            int
	}{
		{"create without token", http.MethodPost, "/trips", "", createBody, http.StatusUnauthorized},
		{"create as buyer", http.MethodPost, "/trips", "buyer", createBody, http.StatusForbidden},
		{"no capacity", http.MethodPost, "/trips", "jastiper", strings.Replace(createBody, `,"capacity":10`, "", 1), http.StatusUnprocessableEntity},
		{"capacity too high", http.MethodPost, "/trips", "jastiper", strings.Replace(createBody, `"capacity":10`, `"capacity":1001`, 1), http.StatusUnprocessableEntity},
		{"bad date", http.MethodPost, "/trips", "jastiper", strings.Replace(createBody, "2026-10-10", "10/10/2026", 1), http.StatusUnprocessableEntity},
		{"unknown slug", http.MethodGet, "/trips/nope", "", "", http.StatusNotFound},
		{"bad cursor", http.MethodGet, "/trips?cursor=garbage", "", "", http.StatusUnprocessableEntity},
		{"limit too high", http.MethodGet, "/trips?limit=500", "", "", http.StatusUnprocessableEntity},
		{"close unknown", http.MethodPost, "/trips/" + uuid.NewString() + "/close", "jastiper", "", http.StatusNotFound},
		{"my trips as buyer", http.MethodGet, "/me/trips", "buyer", "", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if status, body := do(t, app, tt.method, tt.path, tt.token, tt.body); status != tt.want {
				t.Errorf("status = %d, want %d (body %v)", status, tt.want, body)
			}
		})
	}

	do(t, app, http.MethodPost, "/trips/"+id+"/close", "jastiper", "")
	if status, _ := do(t, app, http.MethodPost, "/trips/"+id+"/close", "jastiper", ""); status != http.StatusConflict {
		t.Errorf("close twice status = %d, want 409", status)
	}
}

func TestCatalogEndpoints(t *testing.T) {
	app := newTestApp(t)
	_, created := do(t, app, http.MethodPost, "/trips", "jastiper", createBody)
	id, _ := created["id"].(string)
	slug, _ := created["slug"].(string)
	items := "/trips/" + id + "/items"

	status, body := do(t, app, http.MethodPost, items, "jastiper", `{"name":"Tokyo Banana","referencePrice":45000,"photoUploadId":"`+uuid.NewString()+`"}`)
	if status != http.StatusCreated || body["name"] != "Tokyo Banana" || body["photoUrl"] == nil || body["position"] != float64(0) {
		t.Fatalf("create item status = %d, body = %v", status, body)
	}
	itemID, _ := body["id"].(string)
	if status, body := do(t, app, http.MethodPatch, items+"/"+itemID, "jastiper", `{"description":"Isi 8"}`); status != http.StatusOK || body["description"] != "Isi 8" {
		t.Errorf("update item status = %d, body = %v", status, body)
	}
	status, body = do(t, app, http.MethodGet, "/trips/"+slug, "", "")
	if list, _ := body["items"].([]any); status != http.StatusOK || len(list) != 1 || body["capacity"] != float64(10) {
		t.Errorf("getTrip status = %d, body = %v", status, body)
	}
	if status, body := do(t, app, http.MethodGet, items, "", ""); status != http.StatusOK {
		t.Errorf("list items status = %d, body = %v", status, body)
	}

	tests := []struct {
		name, method, path, token, body string
		want                            int
	}{
		{"create as buyer", http.MethodPost, items, "buyer", `{"name":"X"}`, http.StatusForbidden},
		{"create without token", http.MethodPost, items, "", `{"name":"X"}`, http.StatusUnauthorized},
		{"negative price", http.MethodPost, items, "jastiper", `{"name":"X","referencePrice":-1}`, http.StatusUnprocessableEntity},
		{"name too long", http.MethodPost, items, "jastiper", `{"name":"` + strings.Repeat("x", 121) + `"}`, http.StatusUnprocessableEntity},
		{"update unknown", http.MethodPatch, items + "/" + uuid.NewString(), "jastiper", `{"name":"X"}`, http.StatusNotFound},
		{"items of unknown trip", http.MethodGet, "/trips/" + uuid.NewString() + "/items", "", "", http.StatusNotFound},
		{"delete", http.MethodDelete, items + "/" + itemID, "jastiper", "", http.StatusNoContent},
		{"delete again", http.MethodDelete, items + "/" + itemID, "jastiper", "", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if status, body := do(t, app, tt.method, tt.path, tt.token, tt.body); status != tt.want {
				t.Errorf("status = %d, want %d (body %v)", status, tt.want, body)
			}
		})
	}
}
