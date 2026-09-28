package payment

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const fakeProvider = "fake"

type CheckoutRequest struct {
	PaymentID uuid.UUID
	OrderCode string
	Amount    int64
	ExpiresAt time.Time
}

type Checkout struct {
	Token       string
	RedirectURL string
}

type Gateway interface {
	Name() string
	CreateCheckout(ctx context.Context, r CheckoutRequest) (Checkout, error)
}

// FakeGateway takes no real money; buyers settle through the simulate endpoint.
type FakeGateway struct{}

func (FakeGateway) Name() string { return fakeProvider }

func (FakeGateway) CreateCheckout(_ context.Context, r CheckoutRequest) (Checkout, error) {
	return Checkout{Token: "fake-" + r.PaymentID.String()}, nil
}
