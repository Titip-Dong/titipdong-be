package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/platform/httpserver"
	"github.com/Titip-Dong/titipdong-be/internal/platform/pagination"
	"github.com/google/uuid"
)

type fakeAuth struct{ auth.Service }

func (fakeAuth) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	switch token {
	case "admin", "buyer":
		return auth.Principal{UserID: uuid.New(), Role: auth.Role(token)}, nil
	}
	return auth.Principal{}, auth.ErrInvalidToken
}

var (
	adminTargetID   = uuid.New()
	missingTargetID = uuid.New()
)

type fakeService struct{ Service }

func (fakeService) Stats(context.Context) (Stats, error) { return Stats{}, nil }

func (fakeService) Orders(context.Context, OrderQuery) (Page[Order], error) {
	return Page[Order]{}, nil
}

func (fakeService) Users(context.Context, UserQuery) (Page[User], error) { return Page[User]{}, nil }

func (fakeService) SetUserStatus(_ context.Context, _, id uuid.UUID, status auth.Status) (User, error) {
	switch id {
	case adminTargetID:
		return User{}, auth.ErrAdminStatus
	case missingTargetID:
		return User{}, auth.ErrUserNotFound
	}
	return User{ID: id, Status: string(status)}, nil
}

func (f fakeService) AuditLogs(ctx context.Context, q AuditQuery) (Page[AuditLog], error) {
	if _, err := pagination.DecodeID(q.Cursor); err != nil {
		return Page[AuditLog]{}, err
	}
	return Page[AuditLog]{}, nil
}

func TestAdminEndpoints(t *testing.T) {
	app, api := httpserver.New()
	api.UseMiddleware(auth.Middleware(api, fakeAuth{}))
	NewHandler(fakeService{}).RegisterRoutes(api)
	tests := []struct {
		name, method, path, token, body string
		want                            int
	}{
		{"stats", http.MethodGet, "/admin/stats", "admin", "", http.StatusOK},
		{"stats without token", http.MethodGet, "/admin/stats", "", "", http.StatusUnauthorized},
		{"stats as buyer", http.MethodGet, "/admin/stats", "buyer", "", http.StatusForbidden},
		{"orders by status", http.MethodGet, "/admin/orders?status=paid&code=ABC", "admin", "", http.StatusOK},
		{"orders bad status", http.MethodGet, "/admin/orders?status=lost", "admin", "", http.StatusUnprocessableEntity},
		{"users bad role", http.MethodGet, "/admin/users?role=root", "admin", "", http.StatusUnprocessableEntity},
		{"suspend", http.MethodPatch, "/admin/users/" + uuid.NewString(), "admin", `{"status":"suspended"}`, http.StatusOK},
		{"suspend admin", http.MethodPatch, "/admin/users/" + adminTargetID.String(), "admin", `{"status":"suspended"}`, http.StatusUnprocessableEntity},
		{"suspend unknown", http.MethodPatch, "/admin/users/" + missingTargetID.String(), "admin", `{"status":"suspended"}`, http.StatusNotFound},
		{"bad status value", http.MethodPatch, "/admin/users/" + uuid.NewString(), "admin", `{"status":"banned"}`, http.StatusUnprocessableEntity},
		{"suspend as buyer", http.MethodPatch, "/admin/users/" + uuid.NewString(), "buyer", `{"status":"suspended"}`, http.StatusForbidden},
		{"audit bad cursor", http.MethodGet, "/admin/audit-logs?cursor=!!", "admin", "", http.StatusUnprocessableEntity},
		{"audit bad target type", http.MethodGet, "/admin/audit-logs?targetType=trip", "admin", "", http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			res, err := app.Test(req, -1)
			if err != nil {
				t.Fatal(err)
			}
			_ = res.Body.Close()
			if res.StatusCode != tt.want {
				t.Errorf("status = %d, want %d", res.StatusCode, tt.want)
			}
		})
	}
}
