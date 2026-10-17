package admin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/platform/pagination"
	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
)

const tag = "admin"

type Handler struct {
	svc Service
}

func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) RegisterRoutes(api huma.API) {
	listErrors := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity}
	huma.Register(api, huma.Operation{
		OperationID: "getAdminStats",
		Method:      http.MethodGet,
		Path:        "/admin/stats",
		Summary:     "Dashboard figures: users, orders, GMV, ledger balances, open disputes, pending payouts",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleAdmin),
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden},
	}, h.stats)

	huma.Register(api, huma.Operation{
		OperationID: "listAdminOrders",
		Method:      http.MethodGet,
		Path:        "/admin/orders",
		Summary:     "List every order, newest first",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleAdmin),
		Errors:      listErrors,
	}, h.orders)

	huma.Register(api, huma.Operation{
		OperationID: "listAdminTransactions",
		Method:      http.MethodGet,
		Path:        "/admin/transactions",
		Summary:     "List ledger transactions with their entries, newest first",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleAdmin),
		Errors:      listErrors,
	}, h.transactions)

	huma.Register(api, huma.Operation{
		OperationID: "listAdminUsers",
		Method:      http.MethodGet,
		Path:        "/admin/users",
		Summary:     "List users, newest first",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleAdmin),
		Errors:      listErrors,
	}, h.users)

	huma.Register(api, huma.Operation{
		OperationID: "updateAdminUser",
		Method:      http.MethodPatch,
		Path:        "/admin/users/{id}",
		Summary:     "Suspend or reactivate a buyer or jastiper",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleAdmin),
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, h.updateUser)

	huma.Register(api, huma.Operation{
		OperationID: "listAuditLogs",
		Method:      http.MethodGet,
		Path:        "/admin/audit-logs",
		Summary:     "List audit log entries, newest first",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleAdmin),
		Errors:      listErrors,
	}, h.auditLogs)
}

type adminUserCount struct {
	Role   string `json:"role" enum:"buyer,jastiper,admin"`
	Status string `json:"status" enum:"active,suspended"`
	Count  int64  `json:"count"`
}

type adminOrderCount struct {
	Status string `json:"status"`
	Count  int64  `json:"count"`
	Total  int64  `json:"total" doc:"Sum of order totals in this status, whole rupiah"`
}

type adminPendingPayouts struct {
	Count  int64 `json:"count"`
	Amount int64 `json:"amount"`
}

type adminStats struct {
	Users              []adminUserCount    `json:"users"`
	Orders             []adminOrderCount   `json:"orders"`
	GMV                int64               `json:"gmv" doc:"Totals of orders paid and not refunded"`
	EscrowHeld         int64               `json:"escrowHeld"`
	PlatformRevenue    int64               `json:"platformRevenue"`
	PayableOutstanding int64               `json:"payableOutstanding" doc:"Released to jastipers, not yet paid out"`
	BuyerRefundable    int64               `json:"buyerRefundable" doc:"Refunds the gateway has not paid out"`
	OpenDisputes       int64               `json:"openDisputes"`
	PendingPayouts     adminPendingPayouts `json:"pendingPayouts"`
}

type adminParty struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type adminTripRef struct {
	ID    uuid.UUID `json:"id"`
	Slug  string    `json:"slug"`
	Title string    `json:"title"`
}

type adminOrder struct {
	ID          uuid.UUID    `json:"id"`
	Code        string       `json:"code"`
	Status      string       `json:"status"`
	ProductName string       `json:"productName"`
	Quantity    int          `json:"quantity"`
	Total       *int64       `json:"total,omitempty"`
	Trip        adminTripRef `json:"trip"`
	Buyer       adminParty   `json:"buyer"`
	Jastiper    adminParty   `json:"jastiper"`
	CreatedAt   time.Time    `json:"createdAt"`
}

type adminEntry struct {
	Account   string     `json:"account" enum:"gateway_clearing,escrow,jastiper_payable,platform_revenue,buyer_refundable"`
	UserID    *uuid.UUID `json:"userId,omitempty"`
	Direction string     `json:"direction" enum:"debit,credit"`
	Amount    int64      `json:"amount"`
}

type adminTransaction struct {
	ID        uuid.UUID    `json:"id"`
	Kind      string       `json:"kind" enum:"payment_received,release,refund,refund_paid,payout"`
	OrderID   *uuid.UUID   `json:"orderId,omitempty"`
	OrderCode string       `json:"orderCode,omitempty"`
	CreatedBy *uuid.UUID   `json:"createdBy,omitempty"`
	CreatedAt time.Time    `json:"createdAt"`
	Entries   []adminEntry `json:"entries"`
}

type adminUser struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email" format:"email"`
	Role      string    `json:"role" enum:"buyer,jastiper,admin"`
	Status    string    `json:"status" enum:"active,suspended"`
	Phone     string    `json:"phone,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

type adminAuditLog struct {
	ID         int64          `json:"id"`
	ActorID    *uuid.UUID     `json:"actorId,omitempty"`
	ActorName  string         `json:"actorName,omitempty"`
	ActorType  string         `json:"actorType" enum:"buyer,jastiper,admin,system"`
	Action     string         `json:"action"`
	TargetType string         `json:"targetType"`
	TargetID   string         `json:"targetId"`
	Metadata   map[string]any `json:"metadata"`
	RequestID  string         `json:"requestId,omitempty"`
	CreatedAt  time.Time      `json:"createdAt"`
}

type adminPage[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

func (h *Handler) stats(ctx context.Context, _ *struct{}) (*struct{ Body adminStats }, error) {
	st, err := h.svc.Stats(ctx)
	if err != nil {
		return nil, err
	}
	body := adminStats{
		Users:              make([]adminUserCount, len(st.Users)),
		Orders:             make([]adminOrderCount, len(st.Orders)),
		GMV:                st.GMV,
		EscrowHeld:         st.Ledger.EscrowHeld,
		PlatformRevenue:    st.Ledger.PlatformRevenue,
		PayableOutstanding: st.Ledger.PayableOutstanding,
		BuyerRefundable:    st.Ledger.BuyerRefundable,
		OpenDisputes:       st.OpenDisputes,
		PendingPayouts:     adminPendingPayouts{Count: st.PendingPayouts, Amount: st.PendingPayoutAmount},
	}
	for i, u := range st.Users {
		body.Users[i] = adminUserCount(u)
	}
	for i, o := range st.Orders {
		body.Orders[i] = adminOrderCount(o)
	}
	return &struct{ Body adminStats }{Body: body}, nil
}

func (h *Handler) orders(ctx context.Context, in *struct {
	Status string `query:"status" enum:"requested,approved,rejected,cancelled,paid,shipped,delivered,disputed,released,refunded"`
	Code   string `query:"code" maxLength:"20" doc:"Part of the order code, case-insensitive"`
	Limit  int    `query:"limit" default:"20" minimum:"1" maximum:"100"`
	Cursor string `query:"cursor"`
}) (*struct{ Body adminPage[adminOrder] }, error) {
	page, err := h.svc.Orders(ctx, OrderQuery{Status: in.Status, Code: in.Code, Limit: in.Limit, Cursor: in.Cursor})
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]adminOrder, len(page.Items))
	for i, o := range page.Items {
		items[i] = adminOrder{
			ID: o.ID, Code: o.Code, Status: o.Status, ProductName: o.ProductName, Quantity: o.Quantity, Total: o.Total,
			Trip:      adminTripRef{ID: o.TripID, Slug: o.TripSlug, Title: o.TripTitle},
			Buyer:     adminParty{ID: o.BuyerID, Name: o.BuyerName},
			Jastiper:  adminParty{ID: o.JastiperID, Name: o.JastiperName},
			CreatedAt: o.CreatedAt,
		}
	}
	return &struct{ Body adminPage[adminOrder] }{Body: adminPage[adminOrder]{Items: items, NextCursor: page.NextCursor}}, nil
}

func (h *Handler) transactions(ctx context.Context, in *struct {
	Kind   string `query:"kind" enum:"payment_received,release,refund,refund_paid,payout"`
	Limit  int    `query:"limit" default:"20" minimum:"1" maximum:"100"`
	Cursor string `query:"cursor"`
}) (*struct{ Body adminPage[adminTransaction] }, error) {
	page, err := h.svc.Transactions(ctx, in.Kind, in.Limit, in.Cursor)
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]adminTransaction, len(page.Items))
	for i, t := range page.Items {
		items[i] = adminTransaction{
			ID: t.ID, Kind: t.Kind, OrderID: t.OrderID, OrderCode: t.OrderCode, CreatedBy: t.CreatedBy, CreatedAt: t.CreatedAt,
			Entries: make([]adminEntry, len(t.Entries)),
		}
		for j, e := range t.Entries {
			items[i].Entries[j] = adminEntry(e)
		}
	}
	return &struct{ Body adminPage[adminTransaction] }{Body: adminPage[adminTransaction]{Items: items, NextCursor: page.NextCursor}}, nil
}

func (h *Handler) users(ctx context.Context, in *struct {
	Role   string `query:"role" enum:"buyer,jastiper,admin"`
	Status string `query:"status" enum:"active,suspended"`
	Search string `query:"q" maxLength:"100" doc:"Part of the name or email, case-insensitive"`
	Limit  int    `query:"limit" default:"20" minimum:"1" maximum:"100"`
	Cursor string `query:"cursor"`
}) (*struct{ Body adminPage[adminUser] }, error) {
	page, err := h.svc.Users(ctx, UserQuery{Role: in.Role, Status: in.Status, Search: in.Search, Limit: in.Limit, Cursor: in.Cursor})
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]adminUser, len(page.Items))
	for i, u := range page.Items {
		items[i] = adminUser(u)
	}
	return &struct{ Body adminPage[adminUser] }{Body: adminPage[adminUser]{Items: items, NextCursor: page.NextCursor}}, nil
}

func (h *Handler) updateUser(ctx context.Context, in *struct {
	ID   uuid.UUID `path:"id"`
	Body struct {
		Status string `json:"status" enum:"active,suspended"`
	}
}) (*struct{ Body adminUser }, error) {
	p, _ := auth.PrincipalFrom(ctx)
	u, err := h.svc.SetUserStatus(ctx, p.UserID, in.ID, auth.Status(in.Body.Status))
	if err != nil {
		return nil, mapError(err)
	}
	return &struct{ Body adminUser }{Body: adminUser(u)}, nil
}

func (h *Handler) auditLogs(ctx context.Context, in *struct {
	TargetType string    `query:"targetType" enum:"order,dispute,payout,user"`
	TargetID   string    `query:"targetId" maxLength:"64"`
	ActorID    uuid.UUID `query:"actorId"`
	Limit      int       `query:"limit" default:"20" minimum:"1" maximum:"100"`
	Cursor     string    `query:"cursor"`
}) (*struct{ Body adminPage[adminAuditLog] }, error) {
	q := AuditQuery{TargetType: in.TargetType, TargetID: in.TargetID, Limit: in.Limit, Cursor: in.Cursor}
	if in.ActorID != uuid.Nil {
		q.ActorID = &in.ActorID
	}
	page, err := h.svc.AuditLogs(ctx, q)
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]adminAuditLog, len(page.Items))
	for i, l := range page.Items {
		items[i] = adminAuditLog(l)
	}
	return &struct{ Body adminPage[adminAuditLog] }{Body: adminPage[adminAuditLog]{Items: items, NextCursor: page.NextCursor}}, nil
}

func mapError(err error) error {
	var inputErr *auth.InputError
	switch {
	case errors.Is(err, pagination.ErrInvalidCursor):
		return huma.Error422UnprocessableEntity("Cursor is invalid.")
	case errors.Is(err, auth.ErrUserNotFound):
		return huma.Error404NotFound("User not found.")
	case errors.Is(err, auth.ErrAdminStatus):
		return huma.Error422UnprocessableEntity("An admin's status cannot be changed.")
	case errors.As(err, &inputErr):
		return huma.Error422UnprocessableEntity(inputErr.Detail)
	}
	return err
}
