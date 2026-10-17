package payment

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/order"
	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
)

const tag = "payment"

type Handler struct {
	svc      Service
	simulate bool
}

// NewHandler registers the simulate endpoint only when simulate is true (fake gateway).
func NewHandler(svc Service, simulate bool) *Handler {
	return &Handler{svc: svc, simulate: simulate}
}

func (h *Handler) RegisterRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "checkoutOrder",
		Method:      http.MethodPost,
		Path:        "/orders/{id}/payments",
		Summary:     "Start or resume payment for an approved order",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleBuyer),
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
	}, h.checkout)

	if h.simulate {
		huma.Register(api, huma.Operation{
			OperationID: "simulatePayment",
			Method:      http.MethodPost,
			Path:        "/payments/{id}/simulate",
			Summary:     "Mark a pending payment as paid (fake gateway only)",
			Tags:        []string{tag},
			Security:    auth.Security(auth.RoleBuyer),
			Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
		}, h.simulatePaid)
	}

	huma.Register(api, huma.Operation{
		OperationID: "cancelPaidOrder",
		Method:      http.MethodPost,
		Path:        "/orders/{id}/cancel-paid",
		Summary:     "Cancel a paid order and refund the buyer",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleJastiper),
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
			http.StatusUnprocessableEntity},
	}, h.cancelPaid)

	huma.Register(api, huma.Operation{
		OperationID: "getWallet",
		Method:      http.MethodGet,
		Path:        "/wallet",
		Summary:     "Return the jastiper's released earnings not yet paid out",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleJastiper),
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden},
	}, h.wallet)
}

type paymentResponse struct {
	ID            uuid.UUID  `json:"id"`
	OrderID       uuid.UUID  `json:"orderId"`
	Amount        int64      `json:"amount"`
	Provider      string     `json:"provider" enum:"fake,midtrans"`
	Status        string     `json:"status" enum:"pending,paid,failed,expired,cancelled"`
	CheckoutToken string     `json:"checkoutToken"`
	CheckoutURL   string     `json:"checkoutUrl,omitempty"`
	ExpiresAt     time.Time  `json:"expiresAt"`
	PaidAt        *time.Time `json:"paidAt,omitempty"`
}

type paymentOutput struct {
	Body paymentResponse
}

type refundResponse struct {
	ID          uuid.UUID  `json:"id"`
	OrderID     uuid.UUID  `json:"orderId"`
	Amount      int64      `json:"amount"`
	Reason      string     `json:"reason" enum:"jastiper_cancelled,dispute_refund"`
	Status      string     `json:"status" enum:"pending,succeeded,failed"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

type walletResponse struct {
	Balance int64 `json:"balance" doc:"Whole rupiah"`
}

func (h *Handler) checkout(ctx context.Context, in *struct {
	ID uuid.UUID `path:"id"`
}) (*paymentOutput, error) {
	p, _ := auth.PrincipalFrom(ctx)
	return result(h.svc.Checkout(ctx, p.UserID, in.ID))
}

func (h *Handler) simulatePaid(ctx context.Context, in *struct {
	ID uuid.UUID `path:"id"`
}) (*paymentOutput, error) {
	p, _ := auth.PrincipalFrom(ctx)
	return result(h.svc.SimulatePaid(ctx, p.UserID, in.ID))
}

func (h *Handler) cancelPaid(ctx context.Context, in *struct {
	ID   uuid.UUID `path:"id"`
	Body struct {
		Reason string `json:"reason" minLength:"1" maxLength:"500"`
	}
}) (*struct{ Body refundResponse }, error) {
	p, _ := auth.PrincipalFrom(ctx)
	rf, err := h.svc.CancelPaid(ctx, p.UserID, in.ID, in.Body.Reason)
	var inputErr *InputError
	switch {
	case errors.As(err, &inputErr):
		return nil, huma.Error422UnprocessableEntity(inputErr.Detail)
	case errors.Is(err, order.ErrNotFound):
		return nil, huma.Error404NotFound("Order not found.")
	case errors.Is(err, order.ErrInvalidTransition):
		return nil, huma.Error409Conflict("Only a paid order can be cancelled with a refund.")
	case err != nil:
		return nil, err
	}
	return &struct{ Body refundResponse }{Body: refundResponse{
		ID:          rf.ID,
		OrderID:     rf.OrderID,
		Amount:      rf.Amount,
		Reason:      string(rf.Reason),
		Status:      string(rf.Status),
		CompletedAt: rf.CompletedAt,
	}}, nil
}

func (h *Handler) wallet(ctx context.Context, _ *struct{}) (*struct{ Body walletResponse }, error) {
	p, _ := auth.PrincipalFrom(ctx)
	balance, err := h.svc.JastiperBalance(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	return &struct{ Body walletResponse }{Body: walletResponse{Balance: balance}}, nil
}

func result(p Payment, err error) (*paymentOutput, error) {
	switch {
	case errors.Is(err, order.ErrNotFound):
		return nil, huma.Error404NotFound("Order not found.")
	case errors.Is(err, ErrNotFound):
		return nil, huma.Error404NotFound("Payment not found.")
	case errors.Is(err, ErrOrderNotPayable), errors.Is(err, order.ErrInvalidTransition):
		return nil, huma.Error409Conflict("Order is not awaiting payment.")
	case errors.Is(err, ErrDeadlinePassed):
		return nil, huma.Error409Conflict("Payment deadline has passed.")
	case errors.Is(err, ErrNotPending):
		return nil, huma.Error409Conflict("Payment is no longer pending.")
	case err != nil:
		return nil, err
	}
	return &paymentOutput{Body: paymentResponse{
		ID:            p.ID,
		OrderID:       p.OrderID,
		Amount:        p.Amount,
		Provider:      p.Provider,
		Status:        string(p.Status),
		CheckoutToken: p.CheckoutToken,
		CheckoutURL:   p.CheckoutURL,
		ExpiresAt:     p.ExpiresAt,
		PaidAt:        p.PaidAt,
	}}, nil
}
