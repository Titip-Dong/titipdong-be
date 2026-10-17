package dispute

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/order"
	"github.com/Titip-Dong/titipdong-be/internal/platform/httpserver"
	"github.com/google/uuid"
)

type fakeAuth struct{ auth.Service }

func (fakeAuth) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	switch token {
	case "buyer", "jastiper", "admin":
		return auth.Principal{UserID: uuid.New(), Role: auth.Role(token)}, nil
	}
	return auth.Principal{}, auth.ErrInvalidToken
}

var (
	unshippedOrderID  = uuid.New()
	resolvedDisputeID = uuid.New()
)

// fakeService returns canned results so the tests exercise routing, auth, validation, and error mapping.
type fakeService struct{ Service }

func (fakeService) Open(_ context.Context, _, orderID uuid.UUID, p OpenParams) (Dispute, error) {
	if orderID == unshippedOrderID {
		return Dispute{}, order.ErrInvalidTransition
	}
	return Dispute{ID: uuid.New(), OrderID: orderID, Category: p.Category, Status: StatusOpen}, nil
}

func (fakeService) ForOrder(context.Context, order.Actor, uuid.UUID) (Dispute, error) {
	return Dispute{}, ErrNotFound
}

func (fakeService) PostMessage(_ context.Context, _ order.Actor, id uuid.UUID, _ string) (Dispute, error) {
	if id == resolvedDisputeID {
		return Dispute{}, ErrResolved
	}
	return Dispute{ID: id, Status: StatusOpen}, nil
}

func (fakeService) Decide(_ context.Context, _, id uuid.UUID, d Decision, _ string) (Dispute, error) {
	if id == resolvedDisputeID {
		return Dispute{}, ErrResolved
	}
	return Dispute{ID: id, Status: StatusResolved, Decision: d}, nil
}

func (fakeService) List(context.Context, Status, int, string) (Page, error) {
	return Page{}, nil
}

func TestDisputeEndpoints(t *testing.T) {
	app, api := httpserver.New()
	api.UseMiddleware(auth.Middleware(api, fakeAuth{}))
	NewHandler(fakeService{}).RegisterRoutes(api)
	orderPath := "/orders/" + uuid.NewString() + "/dispute"
	const openBody = `{"category":"damaged","description":"Pecah"}`
	tests := []struct {
		name, method, path, token, body string
		want                            int
	}{
		{"open", http.MethodPost, orderPath, "buyer", openBody, http.StatusCreated},
		{"open without token", http.MethodPost, orderPath, "", openBody, http.StatusUnauthorized},
		{"open as jastiper", http.MethodPost, orderPath, "jastiper", openBody, http.StatusForbidden},
		{"open unshipped", http.MethodPost, "/orders/" + unshippedOrderID.String() + "/dispute", "buyer", openBody, http.StatusConflict},
		{"bad category", http.MethodPost, orderPath, "buyer", `{"category":"late","description":"x"}`, http.StatusUnprocessableEntity},
		{"too much evidence", http.MethodPost, orderPath, "buyer",
			`{"category":"damaged","description":"x","evidence":[` + strings.Repeat(`{"uploadId":"`+uuid.NewString()+`"},`, 5) + `{"uploadId":"` + uuid.NewString() + `"}]}`,
			http.StatusUnprocessableEntity},
		{"get missing", http.MethodGet, orderPath, "buyer", "", http.StatusNotFound},
		{"message", http.MethodPost, "/disputes/" + uuid.NewString() + "/messages", "admin", `{"body":"Halo"}`, http.StatusCreated},
		{"message blank", http.MethodPost, "/disputes/" + uuid.NewString() + "/messages", "buyer", `{"body":""}`, http.StatusUnprocessableEntity},
		{"message resolved", http.MethodPost, "/disputes/" + resolvedDisputeID.String() + "/messages", "buyer", `{"body":"Halo"}`, http.StatusConflict},
		{"evidence as admin", http.MethodPost, "/disputes/" + uuid.NewString() + "/evidence", "admin", `{"evidence":[{"uploadId":"` + uuid.NewString() + `"}]}`, http.StatusForbidden},
		{"evidence empty", http.MethodPost, "/disputes/" + uuid.NewString() + "/evidence", "buyer", `{"evidence":[]}`, http.StatusUnprocessableEntity},
		{"admin list", http.MethodGet, "/admin/disputes", "admin", "", http.StatusOK},
		{"admin list as buyer", http.MethodGet, "/admin/disputes", "buyer", "", http.StatusForbidden},
		{"decide", http.MethodPost, "/admin/disputes/" + uuid.NewString() + "/decision", "admin", `{"decision":"refund","note":"Rusak"}`, http.StatusOK},
		{"decide bad decision", http.MethodPost, "/admin/disputes/" + uuid.NewString() + "/decision", "admin", `{"decision":"split","note":"x"}`, http.StatusUnprocessableEntity},
		{"decide resolved", http.MethodPost, "/admin/disputes/" + resolvedDisputeID.String() + "/decision", "admin", `{"decision":"release","note":"x"}`, http.StatusConflict},
		{"decide as jastiper", http.MethodPost, "/admin/disputes/" + uuid.NewString() + "/decision", "jastiper", `{"decision":"release","note":"x"}`, http.StatusForbidden},
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

func TestInputValidation(t *testing.T) {
	svc := newService(nil, nil, nil, nil, nil, nil, nil)
	ctx := t.Context()
	tooMany := make([]EvidenceInput, 6)
	if _, err := svc.Open(ctx, uuid.New(), uuid.New(), OpenParams{Category: "late", Description: "x"}); err == nil {
		t.Error("Open(unknown category) succeeded")
	}
	if _, err := svc.Open(ctx, uuid.New(), uuid.New(), OpenParams{Category: CategoryOther, Description: " "}); err == nil {
		t.Error("Open(blank description) succeeded")
	}
	if _, err := svc.Open(ctx, uuid.New(), uuid.New(), OpenParams{Category: CategoryOther, Description: "x", Evidence: tooMany}); err == nil {
		t.Error("Open(6 evidence files) succeeded")
	}
	if _, err := svc.AddEvidence(ctx, order.Actor{}, uuid.New(), nil); err == nil {
		t.Error("AddEvidence(none) succeeded")
	}
	if _, err := svc.PostMessage(ctx, order.Actor{}, uuid.New(), "  "); err == nil {
		t.Error("PostMessage(blank) succeeded")
	}
	if _, err := svc.Decide(ctx, uuid.New(), uuid.New(), "split", "x"); err == nil {
		t.Error("Decide(unknown decision) succeeded")
	}
	if _, err := svc.Decide(ctx, uuid.New(), uuid.New(), DecisionRefund, " "); err == nil {
		t.Error("Decide(blank note) succeeded")
	}
}
