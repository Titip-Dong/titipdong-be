package dispute

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/order"
	"github.com/Titip-Dong/titipdong-be/internal/platform/pagination"
	"github.com/Titip-Dong/titipdong-be/internal/upload"
	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
)

const (
	tag      = "dispute"
	adminTag = "admin"
)

type Handler struct {
	svc Service
}

func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) RegisterRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID:   "openDispute",
		Method:        http.MethodPost,
		Path:          "/orders/{id}/dispute",
		Summary:       "Open a dispute on a shipped order",
		Tags:          []string{tag},
		Security:      auth.Security(auth.RoleBuyer),
		DefaultStatus: http.StatusCreated,
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
			http.StatusUnprocessableEntity, http.StatusServiceUnavailable},
	}, h.open)

	huma.Register(api, huma.Operation{
		OperationID: "getOrderDispute",
		Method:      http.MethodGet,
		Path:        "/orders/{id}/dispute",
		Summary:     "Get the dispute of an order with its evidence and messages",
		Tags:        []string{tag},
		Security:    auth.Security(),
		Errors:      []int{http.StatusUnauthorized, http.StatusNotFound},
	}, h.forOrder)

	huma.Register(api, huma.Operation{
		OperationID: "addDisputeEvidence",
		Method:      http.MethodPost,
		Path:        "/disputes/{id}/evidence",
		Summary:     "Add evidence files to an open dispute",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleBuyer, auth.RoleJastiper),
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
			http.StatusUnprocessableEntity, http.StatusServiceUnavailable},
	}, h.addEvidence)

	huma.Register(api, huma.Operation{
		OperationID:   "postDisputeMessage",
		Method:        http.MethodPost,
		Path:          "/disputes/{id}/messages",
		Summary:       "Post a message in an open dispute",
		Tags:          []string{tag},
		Security:      auth.Security(),
		DefaultStatus: http.StatusCreated,
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict,
			http.StatusUnprocessableEntity},
	}, h.postMessage)

	huma.Register(api, huma.Operation{
		OperationID: "listDisputes",
		Method:      http.MethodGet,
		Path:        "/admin/disputes",
		Summary:     "List disputes, oldest first",
		Tags:        []string{adminTag},
		Security:    auth.Security(auth.RoleAdmin),
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity},
	}, h.list)

	huma.Register(api, huma.Operation{
		OperationID: "getDispute",
		Method:      http.MethodGet,
		Path:        "/admin/disputes/{id}",
		Summary:     "Get a dispute with its order and timeline",
		Tags:        []string{adminTag},
		Security:    auth.Security(auth.RoleAdmin),
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
	}, h.get)

	huma.Register(api, huma.Operation{
		OperationID: "decideDispute",
		Method:      http.MethodPost,
		Path:        "/admin/disputes/{id}/decision",
		Summary:     "Decide a dispute: refund the buyer or release to the jastiper",
		Tags:        []string{adminTag},
		Security:    auth.Security(auth.RoleAdmin),
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
			http.StatusUnprocessableEntity},
	}, h.decide)
}

type evidenceInput struct {
	UploadID uuid.UUID `json:"uploadId"`
	Note     string    `json:"note,omitempty" maxLength:"500"`
}

type openDisputeRequest struct {
	Category    string          `json:"category" enum:"not_received,not_as_described,damaged,other"`
	Description string          `json:"description" minLength:"1" maxLength:"2000"`
	Evidence    []evidenceInput `json:"evidence,omitempty" maxItems:"5"`
}

type person struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Role string    `json:"role" enum:"buyer,jastiper,admin"`
}

type evidenceResponse struct {
	ID         uuid.UUID `json:"id"`
	UploadedBy person    `json:"uploadedBy"`
	URL        string    `json:"url" doc:"Short-lived signed download URL"`
	Note       string    `json:"note,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

type messageResponse struct {
	ID        int64     `json:"id"`
	Author    person    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}

type disputeResponse struct {
	ID           uuid.UUID          `json:"id"`
	OrderID      uuid.UUID          `json:"orderId"`
	OrderCode    string             `json:"orderCode"`
	Category     string             `json:"category" enum:"not_received,not_as_described,damaged,other"`
	Description  string             `json:"description"`
	Status       string             `json:"status" enum:"open,resolved"`
	Decision     string             `json:"decision,omitempty" enum:"refund,release"`
	DecisionNote string             `json:"decisionNote,omitempty"`
	DecidedAt    *time.Time         `json:"decidedAt,omitempty"`
	CreatedAt    time.Time          `json:"createdAt"`
	Evidence     []evidenceResponse `json:"evidence"`
	Messages     []messageResponse  `json:"messages"`
}

type disputeSummary struct {
	ID        uuid.UUID `json:"id"`
	OrderID   uuid.UUID `json:"orderId"`
	OrderCode string    `json:"orderCode"`
	Category  string    `json:"category" enum:"not_received,not_as_described,damaged,other"`
	Status    string    `json:"status" enum:"open,resolved"`
	Decision  string    `json:"decision,omitempty" enum:"refund,release"`
	CreatedAt time.Time `json:"createdAt"`
}

type disputePage struct {
	Items      []disputeSummary `json:"items"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

type disputeOrder struct {
	ID       uuid.UUID `json:"id"`
	Code     string    `json:"code"`
	Status   string    `json:"status"`
	Product  string    `json:"productName"`
	Quantity int       `json:"quantity"`
	Total    *int64    `json:"total,omitempty"`
	Buyer    person    `json:"buyer"`
	Jastiper person    `json:"jastiper"`
}

type timelineEvent struct {
	FromStatus *string    `json:"fromStatus,omitempty"`
	ToStatus   string     `json:"toStatus"`
	ActorID    *uuid.UUID `json:"actorId,omitempty"`
	ActorType  string     `json:"actorType" enum:"buyer,jastiper,admin,system"`
	Reason     string     `json:"reason"`
	CreatedAt  time.Time  `json:"createdAt"`
}

type disputeDetail struct {
	Dispute disputeResponse `json:"dispute"`
	Order   disputeOrder    `json:"order"`
	Events  []timelineEvent `json:"events"`
}

type disputeOutput struct {
	Body disputeResponse
}

type idInput struct {
	ID uuid.UUID `path:"id"`
}

func (h *Handler) open(ctx context.Context, in *struct {
	ID   uuid.UUID `path:"id"`
	Body openDisputeRequest
}) (*disputeOutput, error) {
	p, _ := auth.PrincipalFrom(ctx)
	return result(h.svc.Open(ctx, p.UserID, in.ID, OpenParams{
		Category:    Category(in.Body.Category),
		Description: in.Body.Description,
		Evidence:    toEvidenceInputs(in.Body.Evidence),
	}))
}

func (h *Handler) forOrder(ctx context.Context, in *idInput) (*disputeOutput, error) {
	return result(h.svc.ForOrder(ctx, actorFrom(ctx), in.ID))
}

func (h *Handler) addEvidence(ctx context.Context, in *struct {
	ID   uuid.UUID `path:"id"`
	Body struct {
		Evidence []evidenceInput `json:"evidence" minItems:"1" maxItems:"5"`
	}
}) (*disputeOutput, error) {
	return result(h.svc.AddEvidence(ctx, actorFrom(ctx), in.ID, toEvidenceInputs(in.Body.Evidence)))
}

func (h *Handler) postMessage(ctx context.Context, in *struct {
	ID   uuid.UUID `path:"id"`
	Body struct {
		Body string `json:"body" minLength:"1" maxLength:"2000"`
	}
}) (*disputeOutput, error) {
	return result(h.svc.PostMessage(ctx, actorFrom(ctx), in.ID, in.Body.Body))
}

func (h *Handler) list(ctx context.Context, in *struct {
	Status string `query:"status" default:"open" enum:"open,resolved"`
	Limit  int    `query:"limit" default:"20" minimum:"1" maximum:"100"`
	Cursor string `query:"cursor"`
}) (*struct{ Body disputePage }, error) {
	page, err := h.svc.List(ctx, Status(in.Status), in.Limit, in.Cursor)
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]disputeSummary, len(page.Items))
	for i, d := range page.Items {
		items[i] = disputeSummary{
			ID: d.ID, OrderID: d.OrderID, OrderCode: d.OrderCode, Category: string(d.Category),
			Status: string(d.Status), Decision: string(d.Decision), CreatedAt: d.CreatedAt,
		}
	}
	return &struct{ Body disputePage }{Body: disputePage{Items: items, NextCursor: page.NextCursor}}, nil
}

func (h *Handler) get(ctx context.Context, in *idInput) (*struct{ Body disputeDetail }, error) {
	p, _ := auth.PrincipalFrom(ctx)
	d, err := h.svc.Get(ctx, p.UserID, in.ID)
	if err != nil {
		return nil, mapError(err)
	}
	o := d.Order
	body := disputeDetail{
		Dispute: toDisputeResponse(d.Dispute),
		Order: disputeOrder{
			ID: o.ID, Code: o.Code, Status: string(o.Status), Product: o.ProductName, Quantity: o.Quantity, Total: o.Total,
			Buyer:    person{ID: o.BuyerID, Name: o.BuyerName, Role: string(auth.RoleBuyer)},
			Jastiper: person{ID: o.JastiperID, Name: o.JastiperName, Role: string(auth.RoleJastiper)},
		},
		Events: make([]timelineEvent, len(d.Events)),
	}
	for i, e := range d.Events {
		body.Events[i] = timelineEvent{
			ToStatus: string(e.ToStatus), ActorID: e.ActorID, ActorType: string(e.ActorType), Reason: e.Reason, CreatedAt: e.CreatedAt,
		}
		if e.FromStatus != nil {
			from := string(*e.FromStatus)
			body.Events[i].FromStatus = &from
		}
	}
	return &struct{ Body disputeDetail }{Body: body}, nil
}

func (h *Handler) decide(ctx context.Context, in *struct {
	ID   uuid.UUID `path:"id"`
	Body struct {
		Decision string `json:"decision" enum:"refund,release"`
		Note     string `json:"note" minLength:"1" maxLength:"2000" doc:"Shown to the buyer and the jastiper"`
	}
}) (*disputeOutput, error) {
	p, _ := auth.PrincipalFrom(ctx)
	return result(h.svc.Decide(ctx, p.UserID, in.ID, Decision(in.Body.Decision), in.Body.Note))
}

func actorFrom(ctx context.Context) order.Actor {
	p, _ := auth.PrincipalFrom(ctx)
	return order.Actor{ID: p.UserID, Type: order.ActorType(p.Role)}
}

func result(d Dispute, err error) (*disputeOutput, error) {
	if err != nil {
		return nil, mapError(err)
	}
	return &disputeOutput{Body: toDisputeResponse(d)}, nil
}

func mapError(err error) error {
	var inputErr *InputError
	switch {
	case errors.As(err, &inputErr):
		return huma.Error422UnprocessableEntity(inputErr.Detail)
	case errors.Is(err, pagination.ErrInvalidCursor):
		return huma.Error422UnprocessableEntity("Cursor is invalid.")
	case errors.Is(err, ErrNotFound):
		return huma.Error404NotFound("Dispute not found.")
	case errors.Is(err, order.ErrNotFound):
		return huma.Error404NotFound("Order not found.")
	case errors.Is(err, order.ErrInvalidTransition), errors.Is(err, ErrAlreadyDisputed):
		return huma.Error409Conflict("Only a shipped order can be disputed, and only once.")
	case errors.Is(err, ErrResolved):
		return huma.Error409Conflict("Dispute is already resolved.")
	}
	return upload.MapError(err)
}

func toEvidenceInputs(in []evidenceInput) []EvidenceInput {
	out := make([]EvidenceInput, len(in))
	for i, e := range in {
		out[i] = EvidenceInput(e)
	}
	return out
}

func toDisputeResponse(d Dispute) disputeResponse {
	out := disputeResponse{
		ID:           d.ID,
		OrderID:      d.OrderID,
		OrderCode:    d.OrderCode,
		Category:     string(d.Category),
		Description:  d.Description,
		Status:       string(d.Status),
		Decision:     string(d.Decision),
		DecisionNote: d.DecisionNote,
		DecidedAt:    d.DecidedAt,
		CreatedAt:    d.CreatedAt,
		Evidence:     make([]evidenceResponse, len(d.Evidence)),
		Messages:     make([]messageResponse, len(d.Messages)),
	}
	for i, e := range d.Evidence {
		out.Evidence[i] = evidenceResponse{
			ID:         e.ID,
			UploadedBy: person{ID: e.UploadedBy, Name: e.UploaderName, Role: e.UploaderRole},
			URL:        e.URL,
			Note:       e.Note,
			CreatedAt:  e.CreatedAt,
		}
	}
	for i, m := range d.Messages {
		out.Messages[i] = messageResponse{
			ID:        m.ID,
			Author:    person{ID: m.AuthorID, Name: m.AuthorName, Role: m.AuthorRole},
			Body:      m.Body,
			CreatedAt: m.CreatedAt,
		}
	}
	return out
}
