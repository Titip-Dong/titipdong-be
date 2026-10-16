package payment

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/platform/httpserver"
	"github.com/google/uuid"
)

type fakeAuth struct{ auth.Service }

func (fakeAuth) Authenticate(context.Context, string) (auth.Principal, error) {
	return auth.Principal{UserID: uuid.New(), Role: auth.RoleBuyer}, nil
}

type fakeService struct{ Service }

func (fakeService) SimulatePaid(_ context.Context, _, id uuid.UUID) (Payment, error) {
	return Payment{ID: id, Status: StatusPaid}, nil
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
