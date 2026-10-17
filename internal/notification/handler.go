package notification

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

const tag = "notification"

type Handler struct {
	svc Service
}

func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) RegisterRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "listNotifications",
		Method:      http.MethodGet,
		Path:        "/notifications",
		Summary:     "List the caller's notifications, newest first",
		Tags:        []string{tag},
		Security:    auth.Security(),
		Errors:      []int{http.StatusUnauthorized, http.StatusUnprocessableEntity},
	}, h.list)

	huma.Register(api, huma.Operation{
		OperationID: "getUnreadCount",
		Method:      http.MethodGet,
		Path:        "/notifications/unread-count",
		Summary:     "Count the caller's unread notifications",
		Tags:        []string{tag},
		Security:    auth.Security(),
		Errors:      []int{http.StatusUnauthorized},
	}, h.unreadCount)

	huma.Register(api, huma.Operation{
		OperationID:   "markNotificationRead",
		Method:        http.MethodPost,
		Path:          "/notifications/{id}/read",
		Summary:       "Mark a notification as read",
		Tags:          []string{tag},
		Security:      auth.Security(),
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusUnauthorized, http.StatusNotFound},
	}, h.markRead)

	huma.Register(api, huma.Operation{
		OperationID:   "markAllNotificationsRead",
		Method:        http.MethodPost,
		Path:          "/notifications/read-all",
		Summary:       "Mark all of the caller's notifications as read",
		Tags:          []string{tag},
		Security:      auth.Security(),
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusUnauthorized},
	}, h.markAllRead)
}

type notificationResponse struct {
	ID        uuid.UUID      `json:"id"`
	Type      string         `json:"type"`
	Data      map[string]any `json:"data"`
	ReadAt    *time.Time     `json:"readAt,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
}

type notificationPage struct {
	Items      []notificationResponse `json:"items"`
	NextCursor string                 `json:"nextCursor,omitempty"`
}

type unreadCountResponse struct {
	Count int64 `json:"count"`
}

func (h *Handler) list(ctx context.Context, in *struct {
	Unread bool   `query:"unread" doc:"Only unread notifications"`
	Limit  int    `query:"limit" default:"20" minimum:"1" maximum:"100"`
	Cursor string `query:"cursor"`
}) (*struct{ Body notificationPage }, error) {
	p, _ := auth.PrincipalFrom(ctx)
	page, err := h.svc.List(ctx, p.UserID, ListParams{UnreadOnly: in.Unread, Limit: in.Limit, Cursor: in.Cursor})
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]notificationResponse, len(page.Items))
	for i, n := range page.Items {
		items[i] = notificationResponse{ID: n.ID, Type: n.Type, Data: n.Data, ReadAt: n.ReadAt, CreatedAt: n.CreatedAt}
	}
	return &struct{ Body notificationPage }{Body: notificationPage{Items: items, NextCursor: page.NextCursor}}, nil
}

func (h *Handler) unreadCount(ctx context.Context, _ *struct{}) (*struct{ Body unreadCountResponse }, error) {
	p, _ := auth.PrincipalFrom(ctx)
	n, err := h.svc.UnreadCount(ctx, p.UserID)
	if err != nil {
		return nil, mapError(err)
	}
	return &struct{ Body unreadCountResponse }{Body: unreadCountResponse{Count: n}}, nil
}

func (h *Handler) markRead(ctx context.Context, in *struct {
	ID uuid.UUID `path:"id"`
}) (*struct{}, error) {
	p, _ := auth.PrincipalFrom(ctx)
	if err := h.svc.MarkRead(ctx, p.UserID, in.ID); err != nil {
		return nil, mapError(err)
	}
	return &struct{}{}, nil
}

func (h *Handler) markAllRead(ctx context.Context, _ *struct{}) (*struct{}, error) {
	p, _ := auth.PrincipalFrom(ctx)
	if err := h.svc.MarkAllRead(ctx, p.UserID); err != nil {
		return nil, mapError(err)
	}
	return &struct{}{}, nil
}

func mapError(err error) error {
	switch {
	case errors.Is(err, pagination.ErrInvalidCursor):
		return huma.Error422UnprocessableEntity("Cursor is invalid.")
	case errors.Is(err, ErrNotFound):
		return huma.Error404NotFound("Notification not found.")
	}
	return err
}
