package auth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
)

const tag = "auth"

type Handler struct {
	svc Service
}

func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) RegisterRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID:   "register",
		Method:        http.MethodPost,
		Path:          "/auth/register",
		Summary:       "Register a buyer or jastiper account",
		Tags:          []string{tag},
		DefaultStatus: http.StatusCreated,
		Errors:        []int{http.StatusConflict, http.StatusUnprocessableEntity},
	}, h.register)

	huma.Register(api, huma.Operation{
		OperationID: "login",
		Method:      http.MethodPost,
		Path:        "/auth/login",
		Summary:     "Obtain a short-lived access token",
		Tags:        []string{tag},
		Errors:      []int{http.StatusUnauthorized, http.StatusUnprocessableEntity},
	}, h.login)

	huma.Register(api, huma.Operation{
		OperationID: "getCurrentUser",
		Method:      http.MethodGet,
		Path:        "/me",
		Summary:     "Return the authenticated user's profile",
		Tags:        []string{tag},
		Security:    Security(),
		Errors:      []int{http.StatusUnauthorized},
	}, h.me)
}

type registerRequest struct {
	Name     string `json:"name" minLength:"1" maxLength:"100"`
	Email    string `json:"email" format:"email" maxLength:"254"`
	Password string `json:"password" format:"password" minLength:"8" maxLength:"128"`
	Role     string `json:"role" enum:"buyer,jastiper"`
}

type loginRequest struct {
	Email    string `json:"email" format:"email" maxLength:"254"`
	Password string `json:"password" format:"password" maxLength:"128"`
}

type userResponse struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email" format:"email"`
	Role      string    `json:"role" enum:"buyer,jastiper,admin"`
	CreatedAt time.Time `json:"createdAt"`
}

type authResponse struct {
	AccessToken string       `json:"accessToken"`
	User        userResponse `json:"user"`
}

type authOutput struct {
	Body authResponse
}

func (h *Handler) register(ctx context.Context, in *struct{ Body registerRequest }) (*authOutput, error) {
	s, err := h.svc.Register(ctx, RegisterParams{
		Name:     in.Body.Name,
		Email:    in.Body.Email,
		Password: in.Body.Password,
		Role:     Role(in.Body.Role),
	})
	var inputErr *InputError
	switch {
	case errors.As(err, &inputErr):
		return nil, huma.Error422UnprocessableEntity(inputErr.Detail)
	case errors.Is(err, ErrEmailTaken):
		return nil, huma.Error409Conflict("Email is already registered. Use another email or log in.")
	case err != nil:
		return nil, err
	}
	return &authOutput{Body: toAuthResponse(s)}, nil
}

func (h *Handler) login(ctx context.Context, in *struct{ Body loginRequest }) (*authOutput, error) {
	s, err := h.svc.Login(ctx, in.Body.Email, in.Body.Password)
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		return nil, huma.Error401Unauthorized("Email or password is incorrect.")
	case err != nil:
		return nil, err
	}
	return &authOutput{Body: toAuthResponse(s)}, nil
}

func (h *Handler) me(ctx context.Context, _ *struct{}) (*struct{ Body userResponse }, error) {
	p, _ := PrincipalFrom(ctx)
	u, err := h.svc.User(ctx, p.UserID)
	switch {
	case errors.Is(err, ErrUserNotFound):
		// The token is valid but its account no longer exists.
		return nil, huma.Error401Unauthorized("Provide a valid access token.")
	case err != nil:
		return nil, err
	}
	return &struct{ Body userResponse }{Body: toUserResponse(u)}, nil
}

func toAuthResponse(s Session) authResponse {
	return authResponse{AccessToken: s.AccessToken, User: toUserResponse(s.User)}
}

func toUserResponse(u User) userResponse {
	return userResponse{ID: u.ID, Name: u.Name, Email: u.Email, Role: string(u.Role), CreatedAt: u.CreatedAt}
}
