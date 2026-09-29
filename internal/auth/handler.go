package auth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
)

const (
	tag               = "auth"
	refreshCookieName = "refresh_token"
)

type Handler struct {
	svc          Service
	secureCookie bool
}

func NewHandler(svc Service, secureCookie bool) *Handler {
	return &Handler{svc: svc, secureCookie: secureCookie}
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
		OperationID: "refreshSession",
		Method:      http.MethodPost,
		Path:        "/auth/refresh",
		Summary:     "Rotate the refresh token cookie and obtain a new access token",
		Tags:        []string{tag},
		Errors:      []int{http.StatusUnauthorized},
	}, h.refresh)

	huma.Register(api, huma.Operation{
		OperationID:   "logout",
		Method:        http.MethodPost,
		Path:          "/auth/logout",
		Summary:       "Revoke the refresh token and clear its cookie",
		Tags:          []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, h.logout)

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
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      authResponse
}

type refreshCookieInput struct {
	RefreshToken string `cookie:"refresh_token"`
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
	return h.authOutput(s), nil
}

func (h *Handler) login(ctx context.Context, in *struct{ Body loginRequest }) (*authOutput, error) {
	s, err := h.svc.Login(ctx, in.Body.Email, in.Body.Password)
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		return nil, huma.Error401Unauthorized("Email or password is incorrect.")
	case err != nil:
		return nil, err
	}
	return h.authOutput(s), nil
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

func (h *Handler) refresh(ctx context.Context, in *refreshCookieInput) (*authOutput, error) {
	s, err := h.svc.Refresh(ctx, in.RefreshToken)
	switch {
	case errors.Is(err, ErrInvalidRefreshToken):
		clear := h.refreshCookie("", -1)
		return nil, huma.ErrorWithHeaders(
			huma.Error401Unauthorized("Session has expired. Log in again."),
			http.Header{"Set-Cookie": {clear.String()}},
		)
	case err != nil:
		return nil, err
	}
	return h.authOutput(s), nil
}

func (h *Handler) logout(ctx context.Context, in *refreshCookieInput) (*struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
}, error) {
	if err := h.svc.Logout(ctx, in.RefreshToken); err != nil {
		return nil, err
	}
	return &struct {
		SetCookie http.Cookie `header:"Set-Cookie"`
	}{SetCookie: h.refreshCookie("", -1)}, nil
}

func (h *Handler) authOutput(s Session) *authOutput {
	return &authOutput{
		SetCookie: h.refreshCookie(s.RefreshToken, int(refreshTokenLifetime.Seconds())),
		Body:      toAuthResponse(s),
	}
}

func (h *Handler) refreshCookie(value string, maxAge int) http.Cookie {
	return http.Cookie{
		Name:     refreshCookieName,
		Value:    value,
		Path:     "/auth",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   h.secureCookie,
		SameSite: http.SameSiteLaxMode,
	}
}

func toAuthResponse(s Session) authResponse {
	return authResponse{AccessToken: s.AccessToken, User: toUserResponse(s.User)}
}

func toUserResponse(u User) userResponse {
	return userResponse{ID: u.ID, Name: u.Name, Email: u.Email, Role: string(u.Role), CreatedAt: u.CreatedAt}
}
