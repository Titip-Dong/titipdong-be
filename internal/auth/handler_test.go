package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/platform/httpserver"
	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v2"
)

// newTestApp wires the auth module into the real HTTP server with an in-memory
// repository, plus an admin-only probe route for testing role checks.
func newTestApp(t *testing.T) *fiber.App {
	t.Helper()
	app, api := httpserver.New()
	svc := newTestService(time.Now)
	api.UseMiddleware(Middleware(api, svc))
	NewHandler(svc, false).RegisterRoutes(api)
	huma.Register(api, huma.Operation{
		OperationID: "adminProbe",
		Method:      http.MethodGet,
		Path:        "/admin-probe",
		Security:    Security(RoleAdmin),
	}, func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })
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

func TestRegisterLoginMeFlow(t *testing.T) {
	app := newTestApp(t)

	status, body := do(t, app, http.MethodPost, "/auth/register", "", `{"name":"Carlo","email":"carlo@example.com","password":"rahasia123","role":"jastiper"}`)
	if status != http.StatusCreated {
		t.Fatalf("register status = %d, body = %v", status, body)
	}

	status, body = do(t, app, http.MethodPost, "/auth/login", "", `{"email":"carlo@example.com","password":"rahasia123"}`)
	if status != http.StatusOK {
		t.Fatalf("login status = %d, body = %v", status, body)
	}
	token, _ := body["accessToken"].(string)

	status, body = do(t, app, http.MethodGet, "/me", token, "")
	if status != http.StatusOK {
		t.Fatalf("me status = %d, body = %v", status, body)
	}
	if body["email"] != "carlo@example.com" || body["role"] != "jastiper" {
		t.Errorf("me body = %v", body)
	}
	if _, leaked := body["passwordHash"]; leaked {
		t.Error("me response contains the password hash")
	}

	status, _ = do(t, app, http.MethodGet, "/admin-probe", token, "")
	if status != http.StatusForbidden {
		t.Errorf("admin probe as jastiper status = %d, want 403", status)
	}
}

func TestErrorResponses(t *testing.T) {
	app := newTestApp(t)
	register := `{"name":"Carlo","email":"carlo@example.com","password":"rahasia123","role":"buyer"}`
	if status, body := do(t, app, http.MethodPost, "/auth/register", "", register); status != http.StatusCreated {
		t.Fatalf("register status = %d, body = %v", status, body)
	}

	tests := []struct {
		name, method, path, token, body string
		want                            int
	}{
		{"duplicate email", http.MethodPost, "/auth/register", "", register, http.StatusConflict},
		{"admin self-registration", http.MethodPost, "/auth/register", "", `{"name":"A","email":"a@example.com","password":"rahasia123","role":"admin"}`, http.StatusUnprocessableEntity},
		{"short password", http.MethodPost, "/auth/register", "", `{"name":"A","email":"a@example.com","password":"short","role":"buyer"}`, http.StatusUnprocessableEntity},
		{"blank name", http.MethodPost, "/auth/register", "", `{"name":"   ","email":"a@example.com","password":"rahasia123","role":"buyer"}`, http.StatusUnprocessableEntity},
		{"wrong password", http.MethodPost, "/auth/login", "", `{"email":"carlo@example.com","password":"wrong-password"}`, http.StatusUnauthorized},
		{"me without token", http.MethodGet, "/me", "", "", http.StatusUnauthorized},
		{"me with bad token", http.MethodGet, "/me", "not-a-jwt", "", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := do(t, app, tt.method, tt.path, tt.token, tt.body)
			if status != tt.want {
				t.Errorf("status = %d, want %d (body %v)", status, tt.want, body)
			}
			if body["status"] != float64(tt.want) || body["title"] == nil {
				t.Errorf("body = %v, want an RFC 9457 problem", body)
			}
		})
	}
}

func postWithCookie(t *testing.T, app *fiber.App, path string, cookie *http.Cookie, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	res, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func refreshCookieOf(t *testing.T, res *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range res.Cookies() {
		if c.Name == refreshCookieName {
			return c
		}
	}
	t.Fatalf("response has no %s cookie", refreshCookieName)
	return nil
}

func TestRefreshCookieFlow(t *testing.T) {
	app := newTestApp(t)

	res := postWithCookie(t, app, "/auth/register", nil, `{"name":"Carlo","email":"carlo@example.com","password":"rahasia123","role":"buyer"}`)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("register status = %d", res.StatusCode)
	}
	cookie := refreshCookieOf(t, res)
	if !cookie.HttpOnly || cookie.Path != "/auth" || cookie.Value == "" {
		t.Errorf("register cookie = %+v, want HttpOnly, Path=/auth, non-empty", cookie)
	}

	res = postWithCookie(t, app, "/auth/refresh", cookie, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("refresh status = %d", res.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body["accessToken"] == "" || body["user"] == nil {
		t.Errorf("refresh body = %v, want accessToken and user", body)
	}
	cookie = refreshCookieOf(t, res)

	res = postWithCookie(t, app, "/auth/logout", cookie, "")
	if res.StatusCode != http.StatusNoContent || refreshCookieOf(t, res).MaxAge >= 0 {
		t.Fatalf("logout status = %d, want 204 and a cleared cookie", res.StatusCode)
	}

	res = postWithCookie(t, app, "/auth/refresh", cookie, "")
	if res.StatusCode != http.StatusUnauthorized || refreshCookieOf(t, res).MaxAge >= 0 {
		t.Errorf("refresh after logout status = %d, want 401 and a cleared cookie", res.StatusCode)
	}

	res = postWithCookie(t, app, "/auth/refresh", nil, "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("refresh without cookie status = %d, want 401", res.StatusCode)
	}
}
