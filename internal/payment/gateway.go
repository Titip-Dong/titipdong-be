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

type RefundRequest struct {
	RefundID              uuid.UUID
	PaymentID             uuid.UUID
	ProviderTransactionID string
	Amount                int64
}

type RefundResult struct {
	ProviderRefundID string
}

type Gateway interface {
	Name() string
	CreateCheckout(ctx context.Context, r CheckoutRequest) (Checkout, error)
	Refund(ctx context.Context, r RefundRequest) (RefundResult, error)
}

// FakeGateway takes no real money; buyers settle through the simulate endpoint.
type FakeGateway struct{}

func (FakeGateway) Name() string { return fakeProvider }

func (FakeGateway) CreateCheckout(_ context.Context, r CheckoutRequest) (Checkout, error) {
	return Checkout{Token: "fake-" + r.PaymentID.String()}, nil
}

// Refund always succeeds immediately.
func (FakeGateway) Refund(_ context.Context, r RefundRequest) (RefundResult, error) {
	return RefundResult{ProviderRefundID: "fake-refund-" + r.RefundID.String()}, nil
}
