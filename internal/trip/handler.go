package trip

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/platform/pagination"
	"github.com/Titip-Dong/titipdong-be/internal/upload"
	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
)

const (
	tag        = "trip"
	dateLayout = "2006-01-02"
)

// photoSigner turns a stored blob key into a short-lived read URL.
type photoSigner interface {
	ReadURL(key string) string
}

type Handler struct {
	svc    Service
	photos photoSigner
}

func NewHandler(svc Service, photos photoSigner) *Handler {
	return &Handler{svc: svc, photos: photos}
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

	ownerErrors := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
		http.StatusUnprocessableEntity, http.StatusServiceUnavailable}
	huma.Register(api, huma.Operation{
		OperationID: "listTripItems",
		Method:      http.MethodGet,
		Path:        "/trips/{id}/items",
		Summary:     "List a trip's catalog in display order",
		Tags:        []string{tag},
		Errors:      []int{http.StatusNotFound},
	}, h.listItems)

	huma.Register(api, huma.Operation{
		OperationID:   "createTripItem",
		Method:        http.MethodPost,
		Path:          "/trips/{id}/items",
		Summary:       "Add an item to the trip's catalog",
		Tags:          []string{tag},
		Security:      auth.Security(auth.RoleJastiper),
		DefaultStatus: http.StatusCreated,
		Errors:        ownerErrors,
	}, h.createItem)

	huma.Register(api, huma.Operation{
		OperationID: "updateTripItem",
		Method:      http.MethodPatch,
		Path:        "/trips/{id}/items/{itemId}",
		Summary:     "Edit a catalog item",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleJastiper),
		Errors:      ownerErrors,
	}, h.updateItem)

	huma.Register(api, huma.Operation{
		OperationID:   "deleteTripItem",
		Method:        http.MethodDelete,
		Path:          "/trips/{id}/items/{itemId}",
		Summary:       "Remove a catalog item",
		Tags:          []string{tag},
		Security:      auth.Security(auth.RoleJastiper),
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
	}, h.deleteItem)

	huma.Register(api, huma.Operation{
		OperationID: "cancelTrip",
		Method:      http.MethodPost,
		Path:        "/trips/{id}/cancel",
		Summary:     "Cancel a trip that has no active orders",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleJastiper),
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
	}, h.cancel)

	huma.Register(api, huma.Operation{
		OperationID: "updateTrip",
		Method:      http.MethodPatch,
		Path:        "/trips/{id}",
		Summary:     "Edit an open trip",
		Tags:        []string{tag},
		Security:    auth.Security(auth.RoleJastiper),
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
			http.StatusUnprocessableEntity},
	}, h.update)

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
	Capacity           int       `json:"capacity" minimum:"1" maximum:"1000" doc:"Items the jastiper can carry"`
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
	Capacity           int       `json:"capacity"`
	RemainingCapacity  int       `json:"remainingCapacity"`
	Status             string    `json:"status" enum:"open,closed,completed,cancelled"`
	AcceptsRequests    bool      `json:"acceptsRequests"`
	Jastiper           jastiper  `json:"jastiper"`
	CreatedAt          time.Time `json:"createdAt"`
}

type updateTripRequest struct {
	Title              *string    `json:"title,omitempty" minLength:"1" maxLength:"120"`
	Description        *string    `json:"description,omitempty" maxLength:"2000"`
	DestinationCity    *string    `json:"destinationCity,omitempty" minLength:"1" maxLength:"100"`
	DestinationCountry *string    `json:"destinationCountry,omitempty" pattern:"^[A-Za-z]{2}$" doc:"ISO 3166-1 alpha-2"`
	DepartsOn          *string    `json:"departsOn,omitempty" format:"date"`
	ReturnsOn          *string    `json:"returnsOn,omitempty" format:"date"`
	RequestDeadline    *time.Time `json:"requestDeadline,omitempty"`
	Capacity           *int       `json:"capacity,omitempty" minimum:"1" maximum:"1000"`
}

type itemRequest struct {
	Name           *string    `json:"name,omitempty" minLength:"1" maxLength:"120"`
	Description    *string    `json:"description,omitempty" maxLength:"1000"`
	ReferencePrice *int64     `json:"referencePrice,omitempty" minimum:"0" maximum:"1000000000" doc:"Indicative, whole rupiah"`
	Position       *int       `json:"position,omitempty" minimum:"0" maximum:"1000" doc:"Display order; defaults to last"`
	PhotoUpload    *uuid.UUID `json:"photoUploadId,omitempty" doc:"Upload with purpose trip_item_photo; replaces the photo"`
}

type itemResponse struct {
	ID             uuid.UUID `json:"id"`
	Name           string    `json:"name"`
	Description    string    `json:"description,omitempty"`
	ReferencePrice *int64    `json:"referencePrice,omitempty"`
	PhotoURL       string    `json:"photoUrl,omitempty"`
	Position       int       `json:"position"`
}

// tripDetailResponse spells out tripResponse's fields: Huma does not flatten embedded structs into the schema.
type tripDetailResponse struct {
	ID                 uuid.UUID      `json:"id"`
	Slug               string         `json:"slug"`
	Title              string         `json:"title"`
	Description        string         `json:"description"`
	DestinationCity    string         `json:"destinationCity"`
	DestinationCountry string         `json:"destinationCountry"`
	DepartsOn          string         `json:"departsOn" format:"date"`
	ReturnsOn          string         `json:"returnsOn" format:"date"`
	RequestDeadline    time.Time      `json:"requestDeadline"`
	Capacity           int            `json:"capacity"`
	RemainingCapacity  int            `json:"remainingCapacity"`
	Status             string         `json:"status" enum:"open,closed,completed,cancelled"`
	AcceptsRequests    bool           `json:"acceptsRequests"`
	Jastiper           jastiper       `json:"jastiper"`
	CreatedAt          time.Time      `json:"createdAt"`
	Items              []itemResponse `json:"items"`
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
		Capacity:           in.Body.Capacity,
	})
	if err != nil {
		return nil, mapError(err)
	}
	return &tripOutput{Body: toTripResponse(t)}, nil
}

func (h *Handler) list(ctx context.Context, in *struct {
	City   string `query:"city" maxLength:"100" doc:"Destination city prefix, case-insensitive"`
	From   string `query:"from" format:"date" doc:"Earliest departure date, inclusive"`
	To     string `query:"to" format:"date" doc:"Latest departure date, inclusive"`
	Limit  int    `query:"limit" default:"20" minimum:"1" maximum:"100"`
	Cursor string `query:"cursor"`
}) (*struct{ Body tripPage }, error) {
	from, err1 := optionalDate(in.From)
	to, err2 := optionalDate(in.To)
	if err := errors.Join(err1, err2); err != nil {
		return nil, huma.Error422UnprocessableEntity("Dates must use the YYYY-MM-DD format.")
	}
	page, err := h.svc.List(ctx, ListParams{City: in.City, From: from, To: to, Limit: in.Limit, Cursor: in.Cursor})
	if err != nil {
		return nil, mapError(err)
	}
	return &struct{ Body tripPage }{Body: toTripPage(page)}, nil
}

func (h *Handler) get(ctx context.Context, in *struct {
	Slug string `path:"slug"`
}) (*struct{ Body tripDetailResponse }, error) {
	t, err := h.svc.BySlug(ctx, in.Slug)
	if err != nil {
		return nil, mapError(err)
	}
	items, err := h.svc.Items(ctx, t.ID)
	if err != nil {
		return nil, mapError(err)
	}
	r := toTripResponse(t)
	return &struct{ Body tripDetailResponse }{Body: tripDetailResponse{
		ID: r.ID, Slug: r.Slug, Title: r.Title, Description: r.Description, DestinationCity: r.DestinationCity,
		DestinationCountry: r.DestinationCountry, DepartsOn: r.DepartsOn, ReturnsOn: r.ReturnsOn,
		RequestDeadline: r.RequestDeadline, Capacity: r.Capacity, RemainingCapacity: r.RemainingCapacity, Status: r.Status,
		AcceptsRequests: r.AcceptsRequests, Jastiper: r.Jastiper, CreatedAt: r.CreatedAt, Items: h.toItemResponses(items),
	}}, nil
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

func (h *Handler) cancel(ctx context.Context, in *struct {
	ID uuid.UUID `path:"id"`
}) (*tripOutput, error) {
	p, _ := auth.PrincipalFrom(ctx)
	t, err := h.svc.Cancel(ctx, p.UserID, in.ID)
	if err != nil {
		return nil, mapError(err)
	}
	return &tripOutput{Body: toTripResponse(t)}, nil
}

func (h *Handler) update(ctx context.Context, in *struct {
	ID   uuid.UUID `path:"id"`
	Body updateTripRequest
}) (*tripOutput, error) {
	p, _ := auth.PrincipalFrom(ctx)
	departs, err1 := optionalDatePtr(in.Body.DepartsOn)
	returns, err2 := optionalDatePtr(in.Body.ReturnsOn)
	if err := errors.Join(err1, err2); err != nil {
		return nil, huma.Error422UnprocessableEntity("Dates must use the YYYY-MM-DD format.")
	}
	t, err := h.svc.Update(ctx, p.UserID, in.ID, UpdateParams{
		Title:              in.Body.Title,
		Description:        in.Body.Description,
		DestinationCity:    in.Body.DestinationCity,
		DestinationCountry: in.Body.DestinationCountry,
		DepartsOn:          departs,
		ReturnsOn:          returns,
		RequestDeadline:    in.Body.RequestDeadline,
		Capacity:           in.Body.Capacity,
	})
	if err != nil {
		return nil, mapError(err)
	}
	return &tripOutput{Body: toTripResponse(t)}, nil
}

func optionalDatePtr(s *string) (*time.Time, error) {
	if s == nil {
		return nil, nil
	}
	return optionalDate(*s)
}

func (h *Handler) listMine(ctx context.Context, in *pageInput) (*struct{ Body tripPage }, error) {
	p, _ := auth.PrincipalFrom(ctx)
	page, err := h.svc.ListByJastiper(ctx, p.UserID, in.Limit, in.Cursor)
	if err != nil {
		return nil, mapError(err)
	}
	return &struct{ Body tripPage }{Body: toTripPage(page)}, nil
}

func (h *Handler) listItems(ctx context.Context, in *struct {
	ID uuid.UUID `path:"id"`
}) (*struct {
	Body struct {
		Items []itemResponse `json:"items"`
	}
}, error) {
	items, err := h.svc.Items(ctx, in.ID)
	if err != nil {
		return nil, mapError(err)
	}
	out := &struct {
		Body struct {
			Items []itemResponse `json:"items"`
		}
	}{}
	out.Body.Items = h.toItemResponses(items)
	return out, nil
}

func (h *Handler) createItem(ctx context.Context, in *struct {
	ID   uuid.UUID `path:"id"`
	Body itemRequest
}) (*struct{ Body itemResponse }, error) {
	p, _ := auth.PrincipalFrom(ctx)
	it, err := h.svc.AddItem(ctx, p.UserID, in.ID, toItemParams(in.Body))
	if err != nil {
		return nil, mapError(err)
	}
	return &struct{ Body itemResponse }{Body: h.toItemResponse(it)}, nil
}

func (h *Handler) updateItem(ctx context.Context, in *struct {
	ID     uuid.UUID `path:"id"`
	ItemID uuid.UUID `path:"itemId"`
	Body   itemRequest
}) (*struct{ Body itemResponse }, error) {
	p, _ := auth.PrincipalFrom(ctx)
	it, err := h.svc.UpdateItem(ctx, p.UserID, in.ID, in.ItemID, toItemParams(in.Body))
	if err != nil {
		return nil, mapError(err)
	}
	return &struct{ Body itemResponse }{Body: h.toItemResponse(it)}, nil
}

func (h *Handler) deleteItem(ctx context.Context, in *struct {
	ID     uuid.UUID `path:"id"`
	ItemID uuid.UUID `path:"itemId"`
}) (*struct{}, error) {
	p, _ := auth.PrincipalFrom(ctx)
	if err := h.svc.DeleteItem(ctx, p.UserID, in.ID, in.ItemID); err != nil {
		return nil, mapError(err)
	}
	return &struct{}{}, nil
}

func toItemParams(r itemRequest) ItemParams {
	return ItemParams(r)
}

func (h *Handler) toItemResponses(items []Item) []itemResponse {
	out := make([]itemResponse, len(items))
	for i, it := range items {
		out[i] = h.toItemResponse(it)
	}
	return out
}

func (h *Handler) toItemResponse(it Item) itemResponse {
	return itemResponse{
		ID: it.ID, Name: it.Name, Description: it.Description, ReferencePrice: it.ReferencePrice,
		PhotoURL: h.photos.ReadURL(it.PhotoKey), Position: it.Position,
	}
}

func optionalDate(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	d, err := time.Parse(dateLayout, s)
	return &d, err
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
	case errors.Is(err, ErrItemNotFound):
		return huma.Error404NotFound("Item not found.")
	case errors.Is(err, ErrNotCancelable):
		return huma.Error409Conflict("Only an open or closed trip can be cancelled.")
	case errors.Is(err, ErrActiveOrders):
		return huma.Error409Conflict("Trip still has active orders. Settle them before cancelling.")
	case errors.Is(err, ErrCatalogLocked):
		return huma.Error409Conflict("The catalog of a completed or cancelled trip cannot change.")
	}
	return upload.MapError(err)
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
		Capacity:           t.Capacity,
		RemainingCapacity:  t.RemainingCapacity(),
		Status:             string(t.Status),
		AcceptsRequests:    t.AcceptsRequests(time.Now()),
		Jastiper:           jastiper{ID: t.JastiperID, Name: t.JastiperName},
		CreatedAt:          t.CreatedAt,
	}
}
