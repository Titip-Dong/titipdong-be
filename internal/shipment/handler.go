package shipment

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

const tag = "shipment"

type Handler struct {
	svc Service
}

func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) RegisterRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "shipOrder",
		Method:      http.MethodPut,
		Path:        "/orders/{id}/shipment",
		Summary:     "Record the handover of a paid order",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleJastiper),
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}, h.ship)

	huma.Register(api, huma.Operation{
		OperationID: "getShipment",
		Method:      http.MethodGet,
		Path:        "/orders/{id}/shipment",
		Summary:     "Get an order's shipment",
		Tags:        []string{tag},
		Security:    auth.Security(),
		Errors:      []int{http.StatusUnauthorized, http.StatusNotFound},
	}, h.get)

	huma.Register(api, huma.Operation{
		OperationID: "confirmReceipt",
		Method:      http.MethodPost,
		Path:        "/orders/{id}/confirm-receipt",
		Summary:     "Confirm the item arrived and release the escrow to the jastiper",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleBuyer),
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
	}, h.confirmReceipt)
}

type shipRequest struct {
	Method         string `json:"method" enum:"cod,courier"`
	CourierCode    string `json:"courierCode,omitempty" maxLength:"20" doc:"Required for courier, e.g. jne, jnt, sicepat"`
	TrackingNumber string `json:"trackingNumber,omitempty" maxLength:"50" doc:"Required for courier"`
	HandoverNote   string `json:"handoverNote,omitempty" maxLength:"1000"`
}

type shipmentResponse struct {
	OrderID        uuid.UUID  `json:"orderId"`
	OrderStatus    string     `json:"orderStatus" enum:"requested,approved,rejected,cancelled,paid,shipped,delivered,disputed,released,refunded"`
	Method         string     `json:"method" enum:"cod,courier"`
	CourierCode    string     `json:"courierCode"`
	TrackingNumber string     `json:"trackingNumber"`
	HandoverNote   string     `json:"handoverNote"`
	ShippedAt      time.Time  `json:"shippedAt"`
	DeliveredAt    *time.Time `json:"deliveredAt,omitempty"`
}

type shipmentOutput struct {
	Body shipmentResponse
}

func (h *Handler) ship(ctx context.Context, in *struct {
	ID   uuid.UUID `path:"id"`
	Body shipRequest
}) (*shipmentOutput, error) {
	p, _ := auth.PrincipalFrom(ctx)
	return result(h.svc.Ship(ctx, p.UserID, in.ID, ShipParams{
		Method:         Method(in.Body.Method),
		CourierCode:    in.Body.CourierCode,
		TrackingNumber: in.Body.TrackingNumber,
		HandoverNote:   in.Body.HandoverNote,
	}))
}

func (h *Handler) get(ctx context.Context, in *struct {
	ID uuid.UUID `path:"id"`
}) (*shipmentOutput, error) {
	p, _ := auth.PrincipalFrom(ctx)
	return result(h.svc.Get(ctx, order.Actor{ID: p.UserID, Type: order.ActorType(p.Role)}, in.ID))
}

func (h *Handler) confirmReceipt(ctx context.Context, in *struct {
	ID uuid.UUID `path:"id"`
}) (*shipmentOutput, error) {
	p, _ := auth.PrincipalFrom(ctx)
	return result(h.svc.ConfirmReceipt(ctx, p.UserID, in.ID))
}

func result(s Shipment, err error) (*shipmentOutput, error) {
	var inputErr *InputError
	switch {
	case errors.As(err, &inputErr):
		return nil, huma.Error422UnprocessableEntity(inputErr.Detail)
	case errors.Is(err, order.ErrNotFound):
		return nil, huma.Error404NotFound("Order not found.")
	case errors.Is(err, ErrNotFound):
		return nil, huma.Error404NotFound("Order has not been shipped yet.")
	case errors.Is(err, order.ErrInvalidTransition):
		return nil, huma.Error409Conflict("Order status does not allow this action.")
	case err != nil:
		return nil, err
	}
	return &shipmentOutput{Body: shipmentResponse{
		OrderID:        s.OrderID,
		OrderStatus:    string(s.OrderStatus),
		Method:         string(s.Method),
		CourierCode:    s.CourierCode,
		TrackingNumber: s.TrackingNumber,
		HandoverNote:   s.HandoverNote,
		ShippedAt:      s.ShippedAt,
		DeliveredAt:    s.DeliveredAt,
	}}, nil
}
