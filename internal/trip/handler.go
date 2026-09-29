package trip

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

const (
	tag        = "trip"
	dateLayout = "2006-01-02"
)

type Handler struct {
	svc Service
}

func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) RegisterRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID:   "createTrip",
		Method:        http.MethodPost,
		Path:          "/trips",
		Summary:       "Open a trip",
		Tags:          []string{tag},
		Security:      auth.Security(auth.RoleJastiper),
		DefaultStatus: http.StatusCreated,
		Errors:        []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity},
	}, h.create)

	huma.Register(api, huma.Operation{
		OperationID: "listTrips",
		Method:      http.MethodGet,
		Path:        "/trips",
		Summary:     "List open trips that still accept requests",
		Tags:        []string{tag},
		Errors:      []int{http.StatusUnprocessableEntity},
	}, h.list)

	huma.Register(api, huma.Operation{
		OperationID: "getTrip",
		Method:      http.MethodGet,
		Path:        "/trips/{slug}",
		Summary:     "Get a trip by slug",
		Tags:        []string{tag},
		Errors:      []int{http.StatusNotFound},
	}, h.get)

	huma.Register(api, huma.Operation{
		OperationID: "closeTrip",
		Method:      http.MethodPost,
		Path:        "/trips/{id}/close",
		Summary:     "Stop accepting requests for a trip",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleJastiper),
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
	}, h.close)

	huma.Register(api, huma.Operation{
		OperationID: "listMyTrips",
		Method:      http.MethodGet,
		Path:        "/me/trips",
		Summary:     "List the caller's trips",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleJastiper),
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity},
	}, h.listMine)
}

type createTripRequest struct {
	Title              string    `json:"title" minLength:"1" maxLength:"120"`
	Description        string    `json:"description,omitempty" maxLength:"2000"`
	DestinationCity    string    `json:"destinationCity" minLength:"1" maxLength:"100"`
	DestinationCountry string    `json:"destinationCountry" pattern:"^[A-Za-z]{2}$" doc:"ISO 3166-1 alpha-2"`
	DepartsOn          string    `json:"departsOn" format:"date"`
	ReturnsOn          string    `json:"returnsOn" format:"date"`
	RequestDeadline    time.Time `json:"requestDeadline"`
}

type tripResponse struct {
	ID                 uuid.UUID `json:"id"`
	Slug               string    `json:"slug"`
	Title              string    `json:"title"`
	Description        string    `json:"description"`
	DestinationCity    string    `json:"destinationCity"`
	DestinationCountry string    `json:"destinationCountry"`
	DepartsOn          string    `json:"departsOn" format:"date"`
	ReturnsOn          string    `json:"returnsOn" format:"date"`
	RequestDeadline    time.Time `json:"requestDeadline"`
	Status             string    `json:"status" enum:"open,closed,completed,cancelled"`
	AcceptsRequests    bool      `json:"acceptsRequests"`
	Jastiper           jastiper  `json:"jastiper"`
	CreatedAt          time.Time `json:"createdAt"`
}

type jastiper struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type tripPage struct {
	Items      []tripResponse `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

type pageInput struct {
	Limit  int    `query:"limit" default:"20" minimum:"1" maximum:"100"`
	Cursor string `query:"cursor"`
}

type tripOutput struct {
	Body tripResponse
}

func (h *Handler) create(ctx context.Context, in *struct{ Body createTripRequest }) (*tripOutput, error) {
	p, _ := auth.PrincipalFrom(ctx)
	departs, err1 := time.Parse(dateLayout, in.Body.DepartsOn)
	returns, err2 := time.Parse(dateLayout, in.Body.ReturnsOn)
	if err := errors.Join(err1, err2); err != nil {
		return nil, huma.Error422UnprocessableEntity("Dates must use the YYYY-MM-DD format.")
	}
	t, err := h.svc.Create(ctx, p.UserID, CreateParams{
		Title:              in.Body.Title,
		Description:        in.Body.Description,
		DestinationCity:    in.Body.DestinationCity,
		DestinationCountry: in.Body.DestinationCountry,
		DepartsOn:          departs,
		ReturnsOn:          returns,
		RequestDeadline:    in.Body.RequestDeadline,
	})
	if err != nil {
		return nil, mapError(err)
	}
	return &tripOutput{Body: toTripResponse(t)}, nil
}

func (h *Handler) list(ctx context.Context, in *struct {
	City   string `query:"city" maxLength:"100" doc:"Destination city prefix, case-insensitive"`
	Limit  int    `query:"limit" default:"20" minimum:"1" maximum:"100"`
	Cursor string `query:"cursor"`
}) (*struct{ Body tripPage }, error) {
	page, err := h.svc.List(ctx, ListParams{City: in.City, Limit: in.Limit, Cursor: in.Cursor})
	if err != nil {
		return nil, mapError(err)
	}
	return &struct{ Body tripPage }{Body: toTripPage(page)}, nil
}

func (h *Handler) get(ctx context.Context, in *struct {
	Slug string `path:"slug"`
}) (*tripOutput, error) {
	t, err := h.svc.BySlug(ctx, in.Slug)
	if err != nil {
		return nil, mapError(err)
	}
	return &tripOutput{Body: toTripResponse(t)}, nil
}

func (h *Handler) close(ctx context.Context, in *struct {
	ID uuid.UUID `path:"id"`
}) (*tripOutput, error) {
	p, _ := auth.PrincipalFrom(ctx)
	t, err := h.svc.Close(ctx, p.UserID, in.ID)
	if err != nil {
		return nil, mapError(err)
	}
	return &tripOutput{Body: toTripResponse(t)}, nil
}

func (h *Handler) listMine(ctx context.Context, in *pageInput) (*struct{ Body tripPage }, error) {
	p, _ := auth.PrincipalFrom(ctx)
	page, err := h.svc.ListByJastiper(ctx, p.UserID, in.Limit, in.Cursor)
	if err != nil {
		return nil, mapError(err)
	}
	return &struct{ Body tripPage }{Body: toTripPage(page)}, nil
}

func mapError(err error) error {
	var inputErr *InputError
	switch {
	case errors.As(err, &inputErr):
		return huma.Error422UnprocessableEntity(inputErr.Detail)
	case errors.Is(err, pagination.ErrInvalidCursor):
		return huma.Error422UnprocessableEntity("Cursor is invalid.")
	case errors.Is(err, ErrNotFound):
		return huma.Error404NotFound("Trip not found.")
	case errors.Is(err, ErrNotOpen):
		return huma.Error409Conflict("Trip is already closed.")
	}
	return err
}

func toTripPage(p Page) tripPage {
	items := make([]tripResponse, len(p.Items))
	for i, t := range p.Items {
		items[i] = toTripResponse(t)
	}
	return tripPage{Items: items, NextCursor: p.NextCursor}
}

func toTripResponse(t Trip) tripResponse {
	return tripResponse{
		ID:                 t.ID,
		Slug:               t.Slug,
		Title:              t.Title,
		Description:        t.Description,
		DestinationCity:    t.DestinationCity,
		DestinationCountry: t.DestinationCountry,
		DepartsOn:          t.DepartsOn.Format(dateLayout),
		ReturnsOn:          t.ReturnsOn.Format(dateLayout),
		RequestDeadline:    t.RequestDeadline,
		Status:             string(t.Status),
		AcceptsRequests:    t.AcceptsRequests(time.Now()),
		Jastiper:           jastiper{ID: t.JastiperID, Name: t.JastiperName},
		CreatedAt:          t.CreatedAt,
	}
}
