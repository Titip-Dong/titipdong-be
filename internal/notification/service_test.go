package notification

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/platform/pagination"
	"github.com/google/uuid"
)

type fakeRepository struct {
	mu     sync.Mutex
	rows   map[uuid.UUID]Notification
	admins []uuid.UUID
	now    func() time.Time
}

func newFakeRepository(now func() time.Time) *fakeRepository {
	return &fakeRepository{rows: map[uuid.UUID]Notification{}, now: now}
}

func (r *fakeRepository) Insert(_ context.Context, n Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	n.CreatedAt = r.now()
	r.rows[n.ID] = n
	return nil
}

func (r *fakeRepository) AdminIDs(context.Context) ([]uuid.UUID, error) {
	return r.admins, nil
}

func (r *fakeRepository) List(_ context.Context, userID uuid.UUID, unreadOnly bool, before pagination.Cursor, limit int) ([]Notification, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Notification
	for _, n := range r.rows {
		earlier := n.CreatedAt.Before(before.At) || (n.CreatedAt.Equal(before.At) && n.ID.String() < before.ID.String())
		if n.UserID == userID && earlier && (!unreadOnly || n.ReadAt == nil) {
			out = append(out, n)
		}
	}
	slices.SortFunc(out, func(a, b Notification) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), strings.Compare(b.ID.String(), a.ID.String()))
	})
	return out[:min(len(out), limit)], nil
}

func (r *fakeRepository) UnreadCount(_ context.Context, userID uuid.UUID) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var count int64
	for _, n := range r.rows {
		if n.UserID == userID && n.ReadAt == nil {
			count++
		}
	}
	return count, nil
}

func (r *fakeRepository) MarkRead(_ context.Context, userID, id uuid.UUID, at time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.rows[id]
	if !ok || n.UserID != userID {
		return false, nil
	}
	if n.ReadAt == nil {
		n.ReadAt = &at
		r.rows[id] = n
	}
	return true, nil
}

func (r *fakeRepository) MarkAllRead(_ context.Context, userID uuid.UUID, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, n := range r.rows {
		if n.UserID == userID && n.ReadAt == nil {
			n.ReadAt = &at
			r.rows[id] = n
		}
	}
	return nil
}

func newTestService() (*service, *fakeRepository) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	repo := newFakeRepository(func() time.Time { return now })
	return newService(repo, func() time.Time { return now }), repo
}

func TestNotifyDeduplicatesRecipients(t *testing.T) {
	svc, repo := newTestService()
	ctx := context.Background()
	buyer, jastiper := uuid.New(), uuid.New()

	err := svc.Notify(ctx, Event{
		Type:       TypeOrderCancelled,
		Recipients: []uuid.UUID{buyer, jastiper, buyer, uuid.Nil},
		Data:       map[string]any{"orderCode": "TD-ABC123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(repo.rows))
	}
	for _, n := range repo.rows {
		if n.Type != TypeOrderCancelled || n.Data["orderCode"] != "TD-ABC123" {
			t.Errorf("row = %+v", n)
		}
	}
	if err := svc.Notify(ctx, Event{Recipients: []uuid.UUID{buyer}}); err == nil {
		t.Error("Notify without a type succeeded")
	}
}

func TestListUnreadAndMarkRead(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	me, other := uuid.New(), uuid.New()
	for range 3 {
		if err := svc.Notify(ctx, Event{Type: TypeOrderApproved, Recipients: []uuid.UUID{me}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Notify(ctx, Event{Type: TypeOrderApproved, Recipients: []uuid.UUID{other}}); err != nil {
		t.Fatal(err)
	}

	first, err := svc.List(ctx, me, ListParams{Limit: 2})
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %+v, err = %v", first, err)
	}
	second, err := svc.List(ctx, me, ListParams{Limit: 2, Cursor: first.NextCursor})
	if err != nil || len(second.Items) != 1 || second.NextCursor != "" {
		t.Fatalf("second page = %+v, err = %v", second, err)
	}

	id := first.Items[0].ID
	if err := svc.MarkRead(ctx, other, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("MarkRead by another user err = %v, want ErrNotFound", err)
	}
	if err := svc.MarkRead(ctx, me, id); err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkRead(ctx, me, id); err != nil {
		t.Errorf("MarkRead twice err = %v", err)
	}
	if n, _ := svc.UnreadCount(ctx, me); n != 2 {
		t.Errorf("unread = %d, want 2", n)
	}
	unread, _ := svc.List(ctx, me, ListParams{UnreadOnly: true, Limit: 20})
	if len(unread.Items) != 2 {
		t.Errorf("unread items = %d, want 2", len(unread.Items))
	}

	if err := svc.MarkAllRead(ctx, me); err != nil {
		t.Fatal(err)
	}
	if n, _ := svc.UnreadCount(ctx, me); n != 0 {
		t.Errorf("unread after read-all = %d, want 0", n)
	}
	if n, _ := svc.UnreadCount(ctx, other); n != 1 {
		t.Errorf("other user's unread = %d, want 1", n)
	}

	if _, err := svc.List(ctx, me, ListParams{Limit: 20, Cursor: "garbage"}); !errors.Is(err, pagination.ErrInvalidCursor) {
		t.Errorf("bad cursor err = %v", err)
	}
}
