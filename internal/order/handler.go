package order

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/platform/pagination"
	"github.com/Titip-Dong/titipdong-be/internal/trip"
	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
)

const tag = "order"

type Handler struct {
	svc Service
}

func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) RegisterRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID:   "createOrder",
		Method:        http.MethodPost,
		Path:          "/trips/{slug}/orders",
		Summary:       "Request an item on a trip",
		Tags:          []string{tag},
		Security:      auth.Security(auth.RoleBuyer),
		DefaultStatus: http.StatusCreated,
		Errors:        []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}, h.create)

	huma.Register(api, huma.Operation{
		OperationID: "listOrders",
		Method:      http.MethodGet,
		Path:        "/orders",
		Summary:     "List the caller's orders as buyer or jastiper",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleBuyer, auth.RoleJastiper),
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity},
	}, h.list)

	huma.Register(api, huma.Operation{
		OperationID: "getOrder",
		Method:      http.MethodGet,
		Path:        "/orders/{id}",
		Summary:     "Get an order with its status timeline",
		Tags:        []string{tag},
		Security:    auth.Security(),
		Errors:      []int{http.StatusUnauthorized, http.StatusNotFound},
	}, h.get)

	huma.Register(api, huma.Operation{
		OperationID: "approveOrder",
		Method:      http.MethodPost,
		Path:        "/orders/{id}/approve",
		Summary:     "Approve a requested order and set its price",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleJastiper),
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}, h.approve)

	huma.Register(api, huma.Operation{
		OperationID: "rejectOrder",
		Method:      http.MethodPost,
		Path:        "/orders/{id}/reject",
		Summary:     "Reject a requested order",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleJastiper),
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}, h.reject)

	huma.Register(api, huma.Operation{
		OperationID: "cancelOrder",
		Method:      http.MethodPost,
		Path:        "/orders/{id}/cancel",
		Summary:     "Cancel an order before it is paid",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleBuyer),
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
	}, h.cancel)
}

type createOrderRequest struct {
	ProductName string `json:"productName" minLength:"1" maxLength:"200"`
	ProductURL  string `json:"productUrl,omitempty" format:"uri" maxLength:"2000"`
	Quantity    int    `json:"quantity" minimum:"1" maximum:"100"`
	Notes       string `json:"notes,omitempty" maxLength:"1000"`
}

type approveOrderRequest struct {
	ItemPrice int64 `json:"itemPrice" minimum:"1" maximum:"1000000000" doc:"Per unit, whole rupiah"`
	JastipFee int64 `json:"jastipFee" minimum:"0" maximum:"1000000000" doc:"For the whole order, whole rupiah"`
}

type reasonRequest struct {
	Reason string `json:"reason" maxLength:"500"`
}

type party struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type tripRef struct {
	ID    uuid.UUID `json:"id"`
	Slug  string    `json:"slug"`
	Title string    `json:"title"`
}

type orderResponse struct {
	ID              uuid.UUID  `json:"id"`
	Code            string     `json:"code"`
	Trip            tripRef    `json:"trip"`
	Buyer           party      `json:"buyer"`
	Jastiper        party      `json:"jastiper"`
	ProductName     string     `json:"productName"`
	ProductURL      string     `json:"productUrl"`
	Quantity        int        `json:"quantity"`
	Notes           string     `json:"notes"`
	ItemPrice       *int64     `json:"itemPrice,omitempty"`
	JastipFee       *int64     `json:"jastipFee,omitempty"`
	Total           *int64     `json:"total,omitempty"`
	Status          string     `json:"status" enum:"requested,approved,rejected,cancelled,paid,shipped,delivered,disputed,released,refunded"`
	StatusReason    string     `json:"statusReason"`
	PaymentDeadline *time.Time `json:"paymentDeadline,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
}

type eventResponse struct {
	FromStatus *string    `json:"fromStatus,omitempty"`
	ToStatus   string     `json:"toStatus"`
	ActorID    *uuid.UUID `json:"actorId,omitempty"`
	ActorType  string     `json:"actorType" enum:"buyer,jastiper,admin,system"`
	Reason     string     `json:"reason"`
	CreatedAt  time.Time  `json:"createdAt"`
}

type orderDetailResponse struct {
	Order  orderResponse   `json:"order"`
	Events []eventResponse `json:"events"`
}

type orderPage struct {
	Items      []orderResponse `json:"items"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

type orderOutput struct {
	Body orderResponse
}

type idInput struct {
	ID uuid.UUID `path:"id"`
}

func (h *Handler) create(ctx context.Context, in *struct {
	Slug string `path:"slug"`
	Body createOrderRequest
}) (*orderOutput, error) {
	p, _ := auth.PrincipalFrom(ctx)
	o, err := h.svc.Create(ctx, p.UserID, in.Slug, CreateParams{
		ProductName: in.Body.ProductName,
		ProductURL:  in.Body.ProductURL,
		Quantity:    in.Body.Quantity,
		Notes:       in.Body.Notes,
	})
	if err != nil {
		return nil, mapError(err)
	}
	return &orderOutput{Body: toOrderResponse(o)}, nil
}

func (h *Handler) list(ctx context.Context, in *struct {
	Status string `query:"status" enum:"requested,approved,rejected,cancelled,paid,shipped,delivered,disputed,released,refunded"`
	TripID string `query:"tripId" format:"uuid"`
	Limit  int    `query:"limit" default:"20" minimum:"1" maximum:"100"`
	Cursor string `query:"cursor"`
}) (*struct{ Body orderPage }, error) {
	p, _ := auth.PrincipalFrom(ctx)
	params := ListParams{Status: Status(in.Status), Limit: in.Limit, Cursor: in.Cursor}
	if in.TripID != "" {
		id, err := uuid.Parse(in.TripID)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("tripId must be a UUID.")
		}
		params.TripID = &id
	}
	page, err := h.svc.List(ctx, p.UserID, params)
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]orderResponse, len(page.Items))
	for i, o := range page.Items {
		items[i] = toOrderResponse(o)
	}
	return &struct{ Body orderPage }{Body: orderPage{Items: items, NextCursor: page.NextCursor}}, nil
}

func (h *Handler) get(ctx context.Context, in *idInput) (*struct{ Body orderDetailResponse }, error) {
	p, _ := auth.PrincipalFrom(ctx)
	o, events, err := h.svc.Get(ctx, Actor{ID: p.UserID, Type: ActorType(p.Role)}, in.ID)
	if err != nil {
		return nil, mapError(err)
	}
	body := orderDetailResponse{Order: toOrderResponse(o), Events: make([]eventResponse, len(events))}
	for i, e := range events {
		body.Events[i] = eventResponse{
			ToStatus:  string(e.ToStatus),
			ActorID:   e.ActorID,
			ActorType: string(e.ActorType),
			Reason:    e.Reason,
			CreatedAt: e.CreatedAt,
		}
		if e.FromStatus != nil {
			from := string(*e.FromStatus)
			body.Events[i].FromStatus = &from
		}
	}
	return &struct{ Body orderDetailResponse }{Body: body}, nil
}

func (h *Handler) approve(ctx context.Context, in *struct {
	ID   uuid.UUID `path:"id"`
	Body approveOrderRequest
}) (*orderOutput, error) {
	p, _ := auth.PrincipalFrom(ctx)
	return result(h.svc.Approve(ctx, p.UserID, in.ID, in.Body.ItemPrice, in.Body.JastipFee))
}

func (h *Handler) reject(ctx context.Context, in *struct {
	ID   uuid.UUID `path:"id"`
	Body reasonRequest
}) (*orderOutput, error) {
	p, _ := auth.PrincipalFrom(ctx)
	return result(h.svc.Reject(ctx, p.UserID, in.ID, in.Body.Reason))
}

func (h *Handler) cancel(ctx context.Context, in *struct {
	ID   uuid.UUID `path:"id"`
	Body *reasonRequest
}) (*orderOutput, error) {
	p, _ := auth.PrincipalFrom(ctx)
	reason := ""
	if in.Body != nil {
		reason = in.Body.Reason
	}
	return result(h.svc.Cancel(ctx, p.UserID, in.ID, reason))
}

func result(o Order, err error) (*orderOutput, error) {
	if err != nil {
		return nil, mapError(err)
	}
	return &orderOutput{Body: toOrderResponse(o)}, nil
}

func mapError(err error) error {
	var inputErr *InputError
	switch {
	case errors.As(err, &inputErr):
		return huma.Error422UnprocessableEntity(inputErr.Detail)
	case errors.Is(err, pagination.ErrInvalidCursor):
		return huma.Error422UnprocessableEntity("Cursor is invalid.")
	case errors.Is(err, ErrNotFound):
		return huma.Error404NotFound("Order not found.")
	case errors.Is(err, trip.ErrNotFound):
		return huma.Error404NotFound("Trip not found.")
	case errors.Is(err, ErrTripClosed):
		return huma.Error409Conflict("Trip no longer accepts requests.")
	case errors.Is(err, ErrInvalidTransition):
		return huma.Error409Conflict("Order status does not allow this action.")
	}
	return err
}

func toOrderResponse(o Order) orderResponse {
	return orderResponse{
		ID:              o.ID,
		Code:            o.Code,
		Trip:            tripRef{ID: o.TripID, Slug: o.TripSlug, Title: o.TripTitle},
		Buyer:           party{ID: o.BuyerID, Name: o.BuyerName},
		Jastiper:        party{ID: o.JastiperID, Name: o.JastiperName},
		ProductName:     o.ProductName,
		ProductURL:      o.ProductURL,
		Quantity:        o.Quantity,
		Notes:           o.Notes,
		ItemPrice:       o.ItemPrice,
		JastipFee:       o.JastipFee,
		Total:           o.Total,
		Status:          string(o.Status),
		StatusReason:    o.StatusReason,
		PaymentDeadline: o.PaymentDeadline,
		CreatedAt:       o.CreatedAt,
	}
}
