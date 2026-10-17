package payment

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/order"
	"github.com/Titip-Dong/titipdong-be/internal/platform/pagination"
	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
)

const (
	tag      = "payment"
	adminTag = "admin"
)

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

	jastiperErrors := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity}
	huma.Register(api, huma.Operation{
		OperationID: "getPayoutAccount",
		Method:      http.MethodGet,
		Path:        "/me/payout-account",
		Summary:     "Return the jastiper's saved bank account",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleJastiper),
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
	}, h.getPayoutAccount)

	huma.Register(api, huma.Operation{
		OperationID: "savePayoutAccount",
		Method:      http.MethodPut,
		Path:        "/me/payout-account",
		Summary:     "Save the jastiper's bank account for payouts",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleJastiper),
		Errors:      jastiperErrors,
	}, h.savePayoutAccount)

	huma.Register(api, huma.Operation{
		OperationID:   "requestPayout",
		Method:        http.MethodPost,
		Path:          "/payouts",
		Summary:       "Request a payout of available earnings",
		Tags:          []string{tag},
		Security:      auth.Security(auth.RoleJastiper),
		DefaultStatus: http.StatusCreated,
		Errors:        append([]int{http.StatusConflict}, jastiperErrors...),
	}, h.requestPayout)

	huma.Register(api, huma.Operation{
		OperationID: "listMyPayouts",
		Method:      http.MethodGet,
		Path:        "/payouts",
		Summary:     "List the jastiper's payout requests, newest first",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleJastiper),
		Errors:      jastiperErrors,
	}, h.listMyPayouts)

	adminErrors := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
		http.StatusUnprocessableEntity}
	huma.Register(api, huma.Operation{
		OperationID: "listPayouts",
		Method:      http.MethodGet,
		Path:        "/admin/payouts",
		Summary:     "List payout requests, oldest first",
		Tags:        []string{adminTag},
		Security:    auth.Security(auth.RoleAdmin),
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity},
	}, h.listPayouts)

	huma.Register(api, huma.Operation{
		OperationID: "markPayoutPaid",
		Method:      http.MethodPost,
		Path:        "/admin/payouts/{id}/paid",
		Summary:     "Record the bank transfer of a payout",
		Tags:        []string{adminTag},
		Security:    auth.Security(auth.RoleAdmin),
		Errors:      adminErrors,
	}, h.markPayoutPaid)

	huma.Register(api, huma.Operation{
		OperationID: "rejectPayout",
		Method:      http.MethodPost,
		Path:        "/admin/payouts/{id}/reject",
		Summary:     "Reject a payout request",
		Tags:        []string{adminTag},
		Security:    auth.Security(auth.RoleAdmin),
		Errors:      adminErrors,
	}, h.rejectPayout)

	huma.Register(api, huma.Operation{
		OperationID: "getWallet",
		Method:      http.MethodGet,
		Path:        "/wallet",
		Summary:     "Return the jastiper's balance, available amount, money in escrow, and pending payouts",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleJastiper),
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden},
	}, h.wallet)

	huma.Register(api, huma.Operation{
		OperationID: "listWalletTransactions",
		Method:      http.MethodGet,
		Path:        "/wallet/transactions",
		Summary:     "List the postings on the jastiper's balance, newest first",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleJastiper),
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity},
	}, h.walletEntries)
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

type payoutAccountBody struct {
	BankName      string `json:"bankName" minLength:"1" maxLength:"100"`
	AccountNumber string `json:"accountNumber" pattern:"^[0-9]{5,30}$"`
	AccountName   string `json:"accountName" minLength:"1" maxLength:"100"`
}

type payoutAccountResponse struct {
	BankName      string    `json:"bankName"`
	AccountNumber string    `json:"accountNumber"`
	AccountName   string    `json:"accountName"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type payoutResponse struct {
	ID              uuid.UUID      `json:"id"`
	Jastiper        payoutJastiper `json:"jastiper"`
	Amount          int64          `json:"amount"`
	BankName        string         `json:"bankName"`
	AccountNumber   string         `json:"accountNumber"`
	AccountName     string         `json:"accountName"`
	Status          string         `json:"status" enum:"requested,paid,rejected"`
	Reference       string         `json:"reference,omitempty"`
	RejectionReason string         `json:"rejectionReason,omitempty"`
	RequestedAt     time.Time      `json:"requestedAt"`
	DecidedAt       *time.Time     `json:"decidedAt,omitempty"`
}

type payoutJastiper struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type payoutPage struct {
	Items      []payoutResponse `json:"items"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

type payoutOutput struct {
	Body payoutResponse
}

type walletResponse struct {
	Balance       int64 `json:"balance" doc:"Released earnings not paid out, whole rupiah"`
	Available     int64 `json:"available" doc:"Balance minus requested payouts: what can be requested now"`
	InEscrow      int64 `json:"inEscrow" doc:"Totals of the jastiper's orders still held in escrow, before the 5% fee"`
	PendingPayout int64 `json:"pendingPayout" doc:"Payouts requested and not yet decided"`
}

type walletEntryResponse struct {
	ID        int64      `json:"id"`
	Kind      string     `json:"kind" enum:"release,payout"`
	Direction string     `json:"direction" enum:"credit,debit" doc:"credit adds to the balance, debit takes from it"`
	Amount    int64      `json:"amount"`
	OrderID   *uuid.UUID `json:"orderId,omitempty"`
	OrderCode string     `json:"orderCode,omitempty"`
	PayoutID  *uuid.UUID `json:"payoutId,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

type walletEntryPage struct {
	Items      []walletEntryResponse `json:"items"`
	NextCursor string                `json:"nextCursor,omitempty"`
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

func (h *Handler) getPayoutAccount(ctx context.Context, _ *struct{}) (*struct{ Body payoutAccountResponse }, error) {
	p, _ := auth.PrincipalFrom(ctx)
	a, err := h.svc.PayoutAccount(ctx, p.UserID)
	if err != nil {
		return nil, mapPayoutError(err)
	}
	return &struct{ Body payoutAccountResponse }{Body: toPayoutAccountResponse(a)}, nil
}

func (h *Handler) savePayoutAccount(ctx context.Context, in *struct{ Body payoutAccountBody }) (*struct{ Body payoutAccountResponse }, error) {
	p, _ := auth.PrincipalFrom(ctx)
	a, err := h.svc.SavePayoutAccount(ctx, PayoutAccount{
		UserID:        p.UserID,
		BankName:      in.Body.BankName,
		AccountNumber: in.Body.AccountNumber,
		AccountName:   in.Body.AccountName,
	})
	if err != nil {
		return nil, mapPayoutError(err)
	}
	return &struct{ Body payoutAccountResponse }{Body: toPayoutAccountResponse(a)}, nil
}

func (h *Handler) requestPayout(ctx context.Context, in *struct {
	Body struct {
		Amount int64 `json:"amount" minimum:"1" doc:"Whole rupiah"`
	}
}) (*payoutOutput, error) {
	p, _ := auth.PrincipalFrom(ctx)
	return payoutResult(h.svc.RequestPayout(ctx, p.UserID, in.Body.Amount))
}

func (h *Handler) listMyPayouts(ctx context.Context, in *struct {
	Limit  int    `query:"limit" default:"20" minimum:"1" maximum:"100"`
	Cursor string `query:"cursor"`
}) (*struct{ Body payoutPage }, error) {
	p, _ := auth.PrincipalFrom(ctx)
	page, err := h.svc.ListMyPayouts(ctx, p.UserID, in.Limit, in.Cursor)
	if err != nil {
		return nil, mapPayoutError(err)
	}
	return &struct{ Body payoutPage }{Body: toPayoutPage(page)}, nil
}

func (h *Handler) listPayouts(ctx context.Context, in *struct {
	Status string `query:"status" enum:"requested,paid,rejected"`
	Limit  int    `query:"limit" default:"20" minimum:"1" maximum:"100"`
	Cursor string `query:"cursor"`
}) (*struct{ Body payoutPage }, error) {
	page, err := h.svc.ListPayouts(ctx, PayoutStatus(in.Status), in.Limit, in.Cursor)
	if err != nil {
		return nil, mapPayoutError(err)
	}
	return &struct{ Body payoutPage }{Body: toPayoutPage(page)}, nil
}

func (h *Handler) markPayoutPaid(ctx context.Context, in *struct {
	ID   uuid.UUID `path:"id"`
	Body struct {
		Reference string `json:"reference" minLength:"1" maxLength:"100" doc:"Bank transfer reference"`
	}
}) (*payoutOutput, error) {
	p, _ := auth.PrincipalFrom(ctx)
	return payoutResult(h.svc.MarkPayoutPaid(ctx, p.UserID, in.ID, in.Body.Reference))
}

func (h *Handler) rejectPayout(ctx context.Context, in *struct {
	ID   uuid.UUID `path:"id"`
	Body struct {
		Reason string `json:"reason" minLength:"1" maxLength:"500"`
	}
}) (*payoutOutput, error) {
	p, _ := auth.PrincipalFrom(ctx)
	return payoutResult(h.svc.RejectPayout(ctx, p.UserID, in.ID, in.Body.Reason))
}

func payoutResult(p Payout, err error) (*payoutOutput, error) {
	if err != nil {
		return nil, mapPayoutError(err)
	}
	return &payoutOutput{Body: toPayoutResponse(p)}, nil
}

func mapPayoutError(err error) error {
	var inputErr *InputError
	switch {
	case errors.As(err, &inputErr):
		return huma.Error422UnprocessableEntity(inputErr.Detail)
	case errors.Is(err, pagination.ErrInvalidCursor):
		return huma.Error422UnprocessableEntity("Cursor is invalid.")
	case errors.Is(err, ErrNoPayoutAccount):
		return huma.Error404NotFound("No payout account saved.")
	case errors.Is(err, ErrPayoutNotFound):
		return huma.Error404NotFound("Payout not found.")
	case errors.Is(err, ErrPayoutOpen):
		return huma.Error409Conflict("A payout request is already open.")
	case errors.Is(err, ErrInsufficientFunds):
		return huma.Error409Conflict("Amount exceeds the available balance.")
	case errors.Is(err, ErrPayoutNotRequested):
		return huma.Error409Conflict("Payout has already been decided.")
	}
	return err
}

func toPayoutAccountResponse(a PayoutAccount) payoutAccountResponse {
	return payoutAccountResponse{
		BankName:      a.BankName,
		AccountNumber: a.AccountNumber,
		AccountName:   a.AccountName,
		UpdatedAt:     a.UpdatedAt,
	}
}

func toPayoutPage(p PayoutPage) payoutPage {
	items := make([]payoutResponse, len(p.Items))
	for i, po := range p.Items {
		items[i] = toPayoutResponse(po)
	}
	return payoutPage{Items: items, NextCursor: p.NextCursor}
}

func toPayoutResponse(p Payout) payoutResponse {
	return payoutResponse{
		ID:              p.ID,
		Jastiper:        payoutJastiper{ID: p.JastiperID, Name: p.JastiperName},
		Amount:          p.Amount,
		BankName:        p.BankName,
		AccountNumber:   p.AccountNumber,
		AccountName:     p.AccountName,
		Status:          string(p.Status),
		Reference:       p.Reference,
		RejectionReason: p.RejectionReason,
		RequestedAt:     p.RequestedAt,
		DecidedAt:       p.DecidedAt,
	}
}

func (h *Handler) wallet(ctx context.Context, _ *struct{}) (*struct{ Body walletResponse }, error) {
	p, _ := auth.PrincipalFrom(ctx)
	w, err := h.svc.Wallet(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	return &struct{ Body walletResponse }{Body: walletResponse(w)}, nil
}

func (h *Handler) walletEntries(ctx context.Context, in *struct {
	Limit  int    `query:"limit" default:"20" minimum:"1" maximum:"100"`
	Cursor string `query:"cursor"`
}) (*struct{ Body walletEntryPage }, error) {
	p, _ := auth.PrincipalFrom(ctx)
	page, err := h.svc.WalletEntries(ctx, p.UserID, in.Limit, in.Cursor)
	if err != nil {
		return nil, mapPayoutError(err)
	}
	items := make([]walletEntryResponse, len(page.Items))
	for i, e := range page.Items {
		items[i] = walletEntryResponse(e)
	}
	return &struct{ Body walletEntryPage }{Body: walletEntryPage{Items: items, NextCursor: page.NextCursor}}, nil
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
