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
	"github.com/Titip-Dong/titipdong-be/internal/platform/pagination"
	"github.com/google/uuid"
)

type fakeAuth struct{ auth.Service }

func (fakeAuth) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	switch token {
	case "jastiper":
		return auth.Principal{UserID: uuid.New(), Role: auth.RoleJastiper}, nil
	case "admin":
		return auth.Principal{UserID: uuid.New(), Role: auth.RoleAdmin}, nil
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

var decidedPayoutID = uuid.New()

func (fakeService) PayoutAccount(context.Context, uuid.UUID) (PayoutAccount, error) {
	return PayoutAccount{}, ErrNoPayoutAccount
}

func (fakeService) SavePayoutAccount(_ context.Context, a PayoutAccount) (PayoutAccount, error) {
	return a, nil
}

func (fakeService) RequestPayout(_ context.Context, jastiperID uuid.UUID, amount int64) (Payout, error) {
	if amount > 1000 {
		return Payout{}, ErrInsufficientFunds
	}
	return Payout{ID: uuid.New(), JastiperID: jastiperID, Amount: amount, Status: PayoutRequested}, nil
}

func (fakeService) MarkPayoutPaid(_ context.Context, _, id uuid.UUID, _ string) (Payout, error) {
	if id == decidedPayoutID {
		return Payout{}, ErrPayoutNotRequested
	}
	return Payout{ID: id, Status: PayoutPaid}, nil
}

func (fakeService) ListPayouts(context.Context, PayoutStatus, int, string) (PayoutPage, error) {
	return PayoutPage{}, nil
}

func (fakeService) Wallet(context.Context, uuid.UUID) (Wallet, error) {
	return Wallet{Balance: 1000, Available: 400, PendingPayout: 600}, nil
}

func (fakeService) WalletEntries(_ context.Context, _ uuid.UUID, _ int, cursor string) (WalletPage, error) {
	if _, err := pagination.DecodeID(cursor); err != nil {
		return WalletPage{}, err
	}
	return WalletPage{}, nil
}

func TestPayoutEndpoints(t *testing.T) {
	app, api := httpserver.New()
	api.UseMiddleware(auth.Middleware(api, fakeAuth{}))
	NewHandler(fakeService{}, true).RegisterRoutes(api)
	const account = `{"bankName":"BCA","accountNumber":"1234567890","accountName":"Jastiper"}`
	tests := []struct {
		name, method, path, token, body string
		want                            int
	}{
		{"get account none", http.MethodGet, "/me/payout-account", "jastiper", "", http.StatusNotFound},
		{"wallet", http.MethodGet, "/wallet", "jastiper", "", http.StatusOK},
		{"wallet as buyer", http.MethodGet, "/wallet", "buyer", "", http.StatusForbidden},
		{"wallet transactions", http.MethodGet, "/wallet/transactions?limit=5", "jastiper", "", http.StatusOK},
		{"wallet transactions bad cursor", http.MethodGet, "/wallet/transactions?cursor=!!", "jastiper", "", http.StatusUnprocessableEntity},
		{"save account", http.MethodPut, "/me/payout-account", "jastiper", account, http.StatusOK},
		{"save account as buyer", http.MethodPut, "/me/payout-account", "buyer", account, http.StatusForbidden},
		{"bad account number", http.MethodPut, "/me/payout-account", "jastiper", strings.Replace(account, "1234567890", "12-34", 1), http.StatusUnprocessableEntity},
		{"request payout", http.MethodPost, "/payouts", "jastiper", `{"amount":1000}`, http.StatusCreated},
		{"request too much", http.MethodPost, "/payouts", "jastiper", `{"amount":5000}`, http.StatusConflict},
		{"request zero", http.MethodPost, "/payouts", "jastiper", `{"amount":0}`, http.StatusUnprocessableEntity},
		{"admin queue", http.MethodGet, "/admin/payouts?status=requested", "admin", "", http.StatusOK},
		{"admin queue as jastiper", http.MethodGet, "/admin/payouts", "jastiper", "", http.StatusForbidden},
		{"bad status filter", http.MethodGet, "/admin/payouts?status=done", "admin", "", http.StatusUnprocessableEntity},
		{"mark paid", http.MethodPost, "/admin/payouts/" + uuid.NewString() + "/paid", "admin", `{"reference":"TRF-1"}`, http.StatusOK},
		{"mark paid twice", http.MethodPost, "/admin/payouts/" + decidedPayoutID.String() + "/paid", "admin", `{"reference":"TRF-1"}`, http.StatusConflict},
		{"mark paid blank reference", http.MethodPost, "/admin/payouts/" + uuid.NewString() + "/paid", "admin", `{"reference":""}`, http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
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
