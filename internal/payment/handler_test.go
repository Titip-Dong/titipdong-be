package payment

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
	if token == "jastiper" {
		return auth.Principal{UserID: uuid.New(), Role: auth.RoleJastiper}, nil
	}
	return auth.Principal{UserID: uuid.New(), Role: auth.RoleBuyer}, nil
}

type fakeService struct{ Service }

func (fakeService) SimulatePaid(_ context.Context, _, id uuid.UUID) (Payment, error) {
	return Payment{ID: id, Status: StatusPaid}, nil
}

var (
	unpaidOrderID  = uuid.New()
	unknownOrderID = uuid.New()
)

func (fakeService) CancelPaid(_ context.Context, _, orderID uuid.UUID, _ string) (Refund, error) {
	switch orderID {
	case unpaidOrderID:
		return Refund{}, order.ErrInvalidTransition
	case unknownOrderID:
		return Refund{}, order.ErrNotFound
	}
	return Refund{ID: uuid.New(), OrderID: orderID, Amount: 1000, Reason: RefundJastiperCancelled, Status: RefundSucceeded}, nil
}

func TestCancelPaidEndpoint(t *testing.T) {
	app, api := httpserver.New()
	api.UseMiddleware(auth.Middleware(api, fakeAuth{}))
	NewHandler(fakeService{}, true).RegisterRoutes(api)
	const body = `{"reason":"Tidak jadi berangkat"}`
	tests := []struct {
		name, token, body string
		orderID           uuid.UUID
		want              int
	}{
		{"jastiper", "jastiper", body, uuid.New(), http.StatusOK},
		{"buyer", "buyer", body, uuid.New(), http.StatusForbidden},
		{"blank reason", "jastiper", `{"reason":""}`, uuid.New(), http.StatusUnprocessableEntity},
		{"not paid", "jastiper", body, unpaidOrderID, http.StatusConflict},
		{"not found", "jastiper", body, unknownOrderID, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/orders/"+tt.orderID.String()+"/cancel-paid", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+tt.token)
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

func TestSimulateRouteOnlyInFakeMode(t *testing.T) {
	for _, simulate := range []bool{true, false} {
		app, api := httpserver.New()
		api.UseMiddleware(auth.Middleware(api, fakeAuth{}))
		NewHandler(fakeService{}, simulate).RegisterRoutes(api)

		req := httptest.NewRequest(http.MethodPost, "/payments/"+uuid.NewString()+"/simulate", nil)
		req.Header.Set("Authorization", "Bearer buyer")
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()

		want := http.StatusNotFound
		if simulate {
			want = http.StatusOK
		}
		if res.StatusCode != want {
			t.Errorf("simulate=%v: status = %d, want %d", simulate, res.StatusCode, want)
		}
	}
}
