package upload

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/platform/httpserver"
	"github.com/google/uuid"
)

// fakeAuth accepts the tokens "buyer" and "jastiper".
type fakeAuth struct {
	auth.Service
}

func (fakeAuth) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	switch token {
	case "buyer":
		return auth.Principal{UserID: uuid.New(), Role: auth.RoleBuyer}, nil
	case "jastiper":
		return auth.Principal{UserID: uuid.New(), Role: auth.RoleJastiper}, nil
	}
	return auth.Principal{}, auth.ErrInvalidToken
}

func TestCreateUploadEndpoint(t *testing.T) {
	enabled, _, _ := newTestService()
	disabled := newService(&fakeRepository{uploads: map[uuid.UUID]Upload{}}, nil, testNowFunc)
	for _, tc := range []struct {
		name  string
		svc   Service
		token string
		body  string
		want  int
	}{
		{"created", enabled, "buyer", `{"purpose":"order_photo","contentType":"image/png","sizeBytes":1000}`, http.StatusCreated},
		{"no token", enabled, "", `{"purpose":"order_photo","contentType":"image/png","sizeBytes":1000}`, http.StatusUnauthorized},
		{"wrong role", enabled, "jastiper", `{"purpose":"order_photo","contentType":"image/png","sizeBytes":1000}`, http.StatusForbidden},
		{"not an image", enabled, "buyer", `{"purpose":"order_photo","contentType":"text/html","sizeBytes":1000}`, http.StatusUnprocessableEntity},
		{"too large", enabled, "buyer", `{"purpose":"order_photo","contentType":"image/png","sizeBytes":5242881}`, http.StatusUnprocessableEntity},
		{"storage off", disabled, "buyer", `{"purpose":"order_photo","contentType":"image/png","sizeBytes":1000}`, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, api := httpserver.New()
			api.UseMiddleware(auth.Middleware(api, fakeAuth{}))
			NewHandler(tc.svc).RegisterRoutes(api)
			req := httptest.NewRequest(http.MethodPost, "/uploads", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			res, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.want)
			}
			if tc.want != http.StatusCreated {
				return
			}
			var body uploadResponse
			if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.UploadMethod != http.MethodPut || body.UploadHeaders["x-ms-blob-type"] != "BlockBlob" ||
				body.UploadHeaders["Content-Type"] != "image/png" || !strings.HasPrefix(body.UploadURL, "https://blob.test/put/order_photo/") {
				t.Errorf("body = %+v", body)
			}
		})
	}
}
