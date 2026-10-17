package trip

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/platform/pagination"
	"github.com/Titip-Dong/titipdong-be/internal/upload"
	"github.com/google/uuid"
)

type fakeRepository struct {
	mu     sync.Mutex
	trips  map[uuid.UUID]Trip
	items  map[uuid.UUID]Item
	active map[uuid.UUID]int64
	now    func() time.Time
}

func newFakeRepository(now func() time.Time) *fakeRepository {
	return &fakeRepository{trips: map[uuid.UUID]Trip{}, items: map[uuid.UUID]Item{}, active: map[uuid.UUID]int64{}, now: now}
}

func (r *fakeRepository) CreateItem(_ context.Context, it Item) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[it.ID] = it
	return nil
}

func (r *fakeRepository) Items(_ context.Context, tripID uuid.UUID) ([]Item, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Item
	for _, it := range r.items {
		if it.TripID == tripID {
			out = append(out, it)
		}
	}
	slices.SortFunc(out, func(a, b Item) int { return cmp.Compare(a.Position, b.Position) })
	return out, nil
}

func (r *fakeRepository) Item(_ context.Context, tripID, itemID uuid.UUID) (Item, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	it, ok := r.items[itemID]
	if !ok || it.TripID != tripID {
		return Item{}, ErrItemNotFound
	}
	return it, nil
}

func (r *fakeRepository) ItemStats(ctx context.Context, tripID uuid.UUID) (int, int, error) {
	items, _ := r.Items(ctx, tripID)
	next := 0
	for _, it := range items {
		next = max(next, it.Position+1)
	}
	return len(items), next, nil
}

func (r *fakeRepository) UpdateItem(ctx context.Context, it Item) error {
	return r.CreateItem(ctx, it)
}

func (r *fakeRepository) DeleteItem(_ context.Context, tripID, itemID uuid.UUID) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	it, ok := r.items[itemID]
	if !ok || it.TripID != tripID {
		return false, nil
	}
	delete(r.items, itemID)
	return true, nil
}

func (r *fakeRepository) WithTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

func (r *fakeRepository) Lock(ctx context.Context, id uuid.UUID) (Trip, error) {
	return r.TripByID(ctx, id)
}

func (r *fakeRepository) AdjustReserved(_ context.Context, id uuid.UUID, units int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.trips[id]
	t.ReservedCapacity += units
	if t.ReservedCapacity < 0 || t.ReservedCapacity > t.Capacity {
		return errors.New("reserved_capacity check violated")
	}
	r.trips[id] = t
	return nil
}

func (r *fakeRepository) ActiveOrders(_ context.Context, tripID uuid.UUID) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active[tripID], nil
}

func (r *fakeRepository) CancelTrip(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.trips[id]
	t.Status = StatusCancelled
	r.trips[id] = t
	return nil
}

func (r *fakeRepository) UpdateTrip(_ context.Context, t Trip) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trips[t.ID] = t
	return nil
}

func (r *fakeRepository) CloseExpired(_ context.Context, now time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int64
	for id, t := range r.trips {
		if t.Status == StatusOpen && !t.RequestDeadline.After(now) {
			t.Status = StatusClosed
			r.trips[id] = t
			n++
		}
	}
	return n, nil
}

// CompleteReturned ignores orders; the integration test covers the active-order rule.
func (r *fakeRepository) CompleteReturned(_ context.Context, today time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int64
	for id, t := range r.trips {
		if t.Status == StatusClosed && t.ReturnsOn.Before(today) {
			t.Status = StatusCompleted
			r.trips[id] = t
			n++
		}
	}
	return n, nil
}

func (r *fakeRepository) CreateTrip(_ context.Context, t Trip) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.trips {
		if existing.Slug == t.Slug {
			return ErrSlugTaken
		}
	}
	t.JastiperName = "Jastiper"
	t.CreatedAt = r.now()
	r.trips[t.ID] = t
	return nil
}

func (r *fakeRepository) TripByID(_ context.Context, id uuid.UUID) (Trip, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.trips[id]
	if !ok {
		return Trip{}, ErrNotFound
	}
	return t, nil
}

func (r *fakeRepository) TripBySlug(_ context.Context, slug string) (Trip, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.trips {
		if t.Slug == slug {
			return t, nil
		}
	}
	return Trip{}, ErrNotFound
}

func (r *fakeRepository) ListOpen(_ context.Context, f openFilter) ([]Trip, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Trip
	for _, t := range r.trips {
		after := t.DepartsOn.After(f.After.At) || (t.DepartsOn.Equal(f.After.At) && t.ID.String() > f.After.ID.String())
		inRange := (f.From == nil || !t.DepartsOn.Before(*f.From)) && (f.To == nil || !t.DepartsOn.After(*f.To))
		if t.Status == StatusOpen && t.RequestDeadline.After(f.Now) && t.RemainingCapacity() > 0 && after && inRange &&
			strings.HasPrefix(strings.ToLower(t.DestinationCity), strings.ToLower(f.City)) {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(a, b Trip) int {
		return cmp.Or(a.DepartsOn.Compare(b.DepartsOn), strings.Compare(a.ID.String(), b.ID.String()))
	})
	return out[:min(len(out), f.Limit)], nil
}

func (r *fakeRepository) ListByJastiper(_ context.Context, jastiperID uuid.UUID, before pagination.Cursor, limit int) ([]Trip, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Trip
	for _, t := range r.trips {
		earlier := t.CreatedAt.Before(before.At) || (t.CreatedAt.Equal(before.At) && t.ID.String() < before.ID.String())
		if t.JastiperID == jastiperID && earlier {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(a, b Trip) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), strings.Compare(b.ID.String(), a.ID.String()))
	})
	return out[:min(len(out), limit)], nil
}

func (r *fakeRepository) CloseTrip(_ context.Context, id uuid.UUID) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.trips[id]
	if t.Status != StatusOpen {
		return false, nil
	}
	t.Status = StatusClosed
	r.trips[id] = t
	return true, nil
}

var testNow = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

func newTestService() *service {
	now := func() time.Time { return testNow }
	return newService(newFakeRepository(now), now)
}

func validParams() CreateParams {
	return CreateParams{
		Title:              "  Jastip Tokyo Oktober!  ",
		DestinationCity:    "Tokyo",
		DestinationCountry: "jp",
		DepartsOn:          time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC),
		ReturnsOn:          time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC),
		RequestDeadline:    time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC),
		Capacity:           5,
	}
}

func TestCreateNormalizesAndGeneratesSlug(t *testing.T) {
	svc := newTestService()
	trip, err := svc.Create(t.Context(), uuid.New(), validParams())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if trip.Title != "Jastip Tokyo Oktober!" || trip.DestinationCountry != "JP" || trip.Status != StatusOpen {
		t.Errorf("trip = %+v, want trimmed title, upper-case country, open", trip)
	}
	if !strings.HasPrefix(trip.Slug, "jastip-tokyo-oktober-") || len(trip.Slug) != len("jastip-tokyo-oktober-")+6 {
		t.Errorf("slug = %q, want jastip-tokyo-oktober-<6 chars>", trip.Slug)
	}
}

func TestCreateValidation(t *testing.T) {
	tests := map[string]func(p *CreateParams){
		"blank title":            func(p *CreateParams) { p.Title = "  " },
		"returns before departs": func(p *CreateParams) { p.ReturnsOn = p.DepartsOn.AddDate(0, 0, -1) },
		"deadline in the past":   func(p *CreateParams) { p.RequestDeadline = testNow.Add(-time.Minute) },
		"deadline after return":  func(p *CreateParams) { p.RequestDeadline = p.ReturnsOn.AddDate(0, 0, 1) },
		"zero capacity":          func(p *CreateParams) { p.Capacity = 0 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p := validParams()
			mutate(&p)
			var inputErr *InputError
			if _, err := newTestService().Create(t.Context(), uuid.New(), p); !errors.As(err, &inputErr) {
				t.Errorf("Create() error = %v, want *InputError", err)
			}
		})
	}
}

func TestListFiltersAndPaginates(t *testing.T) {
	svc := newTestService()
	jastiperID := uuid.New()
	for i, city := range []string{"Tokyo", "Osaka", "Tokyo", "Tokyo"} {
		p := validParams()
		p.DestinationCity = city
		p.DepartsOn = p.DepartsOn.AddDate(0, 0, i)
		p.ReturnsOn = p.ReturnsOn.AddDate(0, 0, i)
		if _, err := svc.Create(t.Context(), jastiperID, p); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}
	closed, _ := svc.Create(t.Context(), jastiperID, validParams())
	if _, err := svc.Close(t.Context(), jastiperID, closed.ID); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	first, err := svc.List(t.Context(), ListParams{City: "tok", Limit: 2})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %d items, cursor %q; want 2 and a cursor", len(first.Items), first.NextCursor)
	}
	second, err := svc.List(t.Context(), ListParams{City: "tok", Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("List(cursor) error = %v", err)
	}
	if len(second.Items) != 1 || second.NextCursor != "" {
		t.Fatalf("second page = %d items, cursor %q; want 1 and no cursor", len(second.Items), second.NextCursor)
	}
	if !first.Items[1].DepartsOn.Before(second.Items[0].DepartsOn) {
		t.Error("pages are not ordered by departure date")
	}

	if _, err := svc.List(t.Context(), ListParams{Limit: 2, Cursor: "garbage"}); !errors.Is(err, pagination.ErrInvalidCursor) {
		t.Errorf("List(bad cursor) error = %v, want ErrInvalidCursor", err)
	}
}

func TestListByJastiperIncludesClosed(t *testing.T) {
	svc := newTestService()
	jastiperID := uuid.New()
	trip, _ := svc.Create(t.Context(), jastiperID, validParams())
	_, _ = svc.Close(t.Context(), jastiperID, trip.ID)
	_, _ = svc.Create(t.Context(), uuid.New(), validParams())

	page, err := svc.ListByJastiper(t.Context(), jastiperID, 20, "")
	if err != nil {
		t.Fatalf("ListByJastiper() error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Status != StatusClosed {
		t.Errorf("items = %+v, want only the caller's closed trip", page.Items)
	}
}

func TestClose(t *testing.T) {
	svc := newTestService()
	owner := uuid.New()
	trip, _ := svc.Create(t.Context(), owner, validParams())

	if _, err := svc.Close(t.Context(), uuid.New(), trip.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Close(other jastiper) error = %v, want ErrNotFound", err)
	}
	closed, err := svc.Close(t.Context(), owner, trip.ID)
	if err != nil || closed.Status != StatusClosed || closed.AcceptsRequests(testNow) {
		t.Errorf("Close() = %+v, %v; want closed", closed, err)
	}
	if _, err := svc.Close(t.Context(), owner, trip.ID); !errors.Is(err, ErrNotOpen) {
		t.Errorf("Close(again) error = %v, want ErrNotOpen", err)
	}
}

func TestSlugify(t *testing.T) {
	tests := map[string]string{
		"Jastip Tokyo Oktober!": "jastip-tokyo-oktober",
		"  ---  ":               "trip",
		"Seoul & Busan 2026":    "seoul-busan-2026",
	}
	for in, want := range tests {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReserveAndRelease(t *testing.T) {
	svc := newTestService()
	ctx := t.Context()
	jastiperID := uuid.New()
	tr, err := svc.Create(ctx, jastiperID, validParams())
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.Reserve(ctx, tr.ID, 3); err != nil {
		t.Fatalf("Reserve(3) error = %v", err)
	}
	if err := svc.Reserve(ctx, tr.ID, 3); !errors.Is(err, ErrCapacityFull) {
		t.Errorf("Reserve(3 more of 5) error = %v, want ErrCapacityFull", err)
	}
	if err := svc.Reserve(ctx, tr.ID, 2); err != nil {
		t.Fatalf("Reserve(last 2) error = %v", err)
	}
	got, _ := svc.ByID(ctx, tr.ID)
	if got.RemainingCapacity() != 0 || got.AcceptsRequests(testNow) {
		t.Errorf("full trip: remaining %d, accepts %v", got.RemainingCapacity(), got.AcceptsRequests(testNow))
	}
	if page, _ := svc.List(ctx, ListParams{Limit: 20}); len(page.Items) != 0 {
		t.Errorf("full trip listed: %+v", page.Items)
	}

	if err := svc.Release(ctx, tr.ID, 4); err != nil {
		t.Fatalf("Release(4) error = %v", err)
	}
	if got, _ := svc.ByID(ctx, tr.ID); got.RemainingCapacity() != 4 {
		t.Errorf("remaining after release = %d, want 4", got.RemainingCapacity())
	}
	if err := svc.Release(ctx, tr.ID, 2); err != nil {
		t.Errorf("Release(more than reserved) error = %v, want nil", err)
	}
	if got, _ := svc.ByID(ctx, tr.ID); got.ReservedCapacity != 0 {
		t.Errorf("reserved after over-release = %d, want 0", got.ReservedCapacity)
	}

	if _, err := svc.Close(ctx, jastiperID, tr.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Reserve(ctx, tr.ID, 1); !errors.Is(err, ErrTripClosed) {
		t.Errorf("Reserve(closed) error = %v, want ErrTripClosed", err)
	}
	if err := svc.Release(ctx, tr.ID, 1); err != nil {
		t.Errorf("Release(closed) error = %v, want nil", err)
	}
	if err := svc.Reserve(ctx, uuid.New(), 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("Reserve(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestCloseExpired(t *testing.T) {
	now := testNow
	repo := newFakeRepository(func() time.Time { return now })
	svc := newService(repo, func() time.Time { return now })
	ctx := t.Context()
	tr, err := svc.Create(ctx, uuid.New(), validParams())
	if err != nil {
		t.Fatal(err)
	}

	now = tr.RequestDeadline.Add(-time.Minute)
	_ = svc.CloseExpired(ctx)
	if got, _ := svc.ByID(ctx, tr.ID); got.Status != StatusOpen {
		t.Fatalf("before the deadline status = %s, want open", got.Status)
	}
	now = tr.RequestDeadline
	_ = svc.CloseExpired(ctx)
	if got, _ := svc.ByID(ctx, tr.ID); got.Status != StatusClosed {
		t.Fatalf("at the deadline status = %s, want closed", got.Status)
	}
	now = tr.ReturnsOn.Add(23 * time.Hour)
	_ = svc.CloseExpired(ctx)
	if got, _ := svc.ByID(ctx, tr.ID); got.Status != StatusClosed {
		t.Fatalf("on the return date status = %s, want closed", got.Status)
	}
	now = tr.ReturnsOn.AddDate(0, 0, 1)
	if err := svc.CloseExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.ByID(ctx, tr.ID); got.Status != StatusCompleted {
		t.Errorf("the day after returning status = %s, want completed", got.Status)
	}
}

type fakeUploads struct{}

func (fakeUploads) Attach(_ context.Context, _, id uuid.UUID, purpose upload.Purpose) (string, error) {
	return string(purpose) + "/" + id.String() + ".jpg", nil
}

func (fakeUploads) ReadURL(key string) string {
	if key == "" {
		return ""
	}
	return "https://blob.test/" + key
}

func TestCatalog(t *testing.T) {
	svc := newTestService()
	svc.uploads = fakeUploads{}
	ctx := t.Context()
	jastiperID := uuid.New()
	tr, err := svc.Create(ctx, jastiperID, validParams())
	if err != nil {
		t.Fatal(err)
	}
	ptr := func(s string) *string { return &s }
	price := int64(45_000)
	photo := uuid.New()

	first, err := svc.AddItem(ctx, jastiperID, tr.ID, ItemParams{Name: ptr(" Tokyo Banana "), ReferencePrice: &price, PhotoUpload: &photo})
	if err != nil || first.Name != "Tokyo Banana" || first.Position != 0 || first.PhotoKey == "" {
		t.Fatalf("AddItem() = %+v, %v", first, err)
	}
	second, err := svc.AddItem(ctx, jastiperID, tr.ID, ItemParams{Name: ptr("Royce")})
	if err != nil || second.Position != 1 {
		t.Fatalf("AddItem(second) = %+v, %v", second, err)
	}
	zero := 0
	if _, err := svc.UpdateItem(ctx, jastiperID, tr.ID, second.ID, ItemParams{Position: &zero, Description: ptr("Cokelat")}); err != nil {
		t.Fatal(err)
	}
	two := 2
	if _, err := svc.UpdateItem(ctx, jastiperID, tr.ID, first.ID, ItemParams{Position: &two}); err != nil {
		t.Fatal(err)
	}
	items, _ := svc.Items(ctx, tr.ID)
	if len(items) != 2 || items[0].ID != second.ID || items[0].Description != "Cokelat" || items[1].Name != "Tokyo Banana" {
		t.Errorf("items after reorder = %+v", items)
	}

	var inputErr *InputError
	if _, err := svc.AddItem(ctx, jastiperID, tr.ID, ItemParams{}); !errors.As(err, &inputErr) {
		t.Errorf("AddItem(no name) error = %v, want *InputError", err)
	}
	if _, err := svc.UpdateItem(ctx, jastiperID, tr.ID, first.ID, ItemParams{Name: ptr("  ")}); !errors.As(err, &inputErr) {
		t.Errorf("UpdateItem(blank name) error = %v, want *InputError", err)
	}
	if _, err := svc.AddItem(ctx, uuid.New(), tr.ID, ItemParams{Name: ptr("X")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("AddItem(other jastiper) error = %v, want ErrNotFound", err)
	}
	if err := svc.DeleteItem(ctx, jastiperID, tr.ID, uuid.New()); !errors.Is(err, ErrItemNotFound) {
		t.Errorf("DeleteItem(unknown) error = %v, want ErrItemNotFound", err)
	}
	if err := svc.DeleteItem(ctx, jastiperID, tr.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if items, _ := svc.Items(ctx, tr.ID); len(items) != 1 {
		t.Errorf("items after delete = %d, want 1", len(items))
	}

	for i := range maxItemsPerTrip - 1 {
		if _, err := svc.AddItem(ctx, jastiperID, tr.ID, ItemParams{Name: ptr(fmt.Sprint("Item ", i))}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.AddItem(ctx, jastiperID, tr.ID, ItemParams{Name: ptr("One too many")}); !errors.As(err, &inputErr) {
		t.Errorf("AddItem(51st) error = %v, want *InputError", err)
	}

	repo := svc.repo.(*fakeRepository)
	locked := repo.trips[tr.ID]
	locked.Status = StatusCompleted
	repo.trips[tr.ID] = locked
	if err := svc.DeleteItem(ctx, jastiperID, tr.ID, second.ID); !errors.Is(err, ErrCatalogLocked) {
		t.Errorf("DeleteItem(completed trip) error = %v, want ErrCatalogLocked", err)
	}

	noUploads := newTestService()
	tr2, _ := noUploads.Create(ctx, jastiperID, validParams())
	if _, err := noUploads.AddItem(ctx, jastiperID, tr2.ID, ItemParams{Name: ptr("X"), PhotoUpload: &photo}); !errors.Is(err, upload.ErrUnavailable) {
		t.Errorf("AddItem(photo, uploads disabled) error = %v, want upload.ErrUnavailable", err)
	}
}

func TestListFiltersByDepartureDate(t *testing.T) {
	svc := newTestService()
	ctx := t.Context()
	for i := range 3 {
		p := validParams()
		p.DepartsOn = p.DepartsOn.AddDate(0, 0, i) // Oct 10, 11, 12
		if _, err := svc.Create(ctx, uuid.New(), p); err != nil {
			t.Fatal(err)
		}
	}
	day := func(d int) *time.Time { v := time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC); return &v }
	page, err := svc.List(ctx, ListParams{From: day(11), To: day(12), Limit: 20})
	if err != nil || len(page.Items) != 2 || !page.Items[0].DepartsOn.Equal(*day(11)) {
		t.Fatalf("List(11..12) = %+v, %v; want the trips departing on the 11th and 12th", page.Items, err)
	}
	if page, _ := svc.List(ctx, ListParams{To: day(10), Limit: 20}); len(page.Items) != 1 {
		t.Errorf("List(..10) = %d items, want 1", len(page.Items))
	}
	var inputErr *InputError
	if _, err := svc.List(ctx, ListParams{From: day(12), To: day(11), Limit: 20}); !errors.As(err, &inputErr) {
		t.Errorf("List(to before from) error = %v, want *InputError", err)
	}
}

func TestUpdate(t *testing.T) {
	svc := newTestService()
	ctx := t.Context()
	jastiperID := uuid.New()
	tr, err := svc.Create(ctx, jastiperID, validParams())
	if err != nil {
		t.Fatal(err)
	}
	ptr := func(s string) *string { return &s }
	capacity := 8
	got, err := svc.Update(ctx, jastiperID, tr.ID, UpdateParams{Title: ptr(" Jastip Osaka "), DestinationCountry: ptr("jp"), Capacity: &capacity})
	if err != nil || got.Title != "Jastip Osaka" || got.Capacity != 8 || got.Slug != tr.Slug || got.DestinationCity != "Tokyo" {
		t.Fatalf("Update() = %+v, %v; want the new title and capacity, the old slug and city", got, err)
	}

	if err := svc.Reserve(ctx, tr.ID, 3); err != nil {
		t.Fatal(err)
	}
	two := 2
	var inputErr *InputError
	if _, err := svc.Update(ctx, jastiperID, tr.ID, UpdateParams{Capacity: &two}); !errors.As(err, &inputErr) {
		t.Errorf("Update(capacity below reserved) error = %v, want *InputError", err)
	}
	early := tr.DepartsOn.AddDate(0, 0, -1)
	if _, err := svc.Update(ctx, jastiperID, tr.ID, UpdateParams{ReturnsOn: &early}); !errors.As(err, &inputErr) {
		t.Errorf("Update(returns before departs) error = %v, want *InputError", err)
	}
	if _, err := svc.Update(ctx, jastiperID, tr.ID, UpdateParams{Title: ptr("  ")}); !errors.As(err, &inputErr) {
		t.Errorf("Update(blank title) error = %v, want *InputError", err)
	}
	if _, err := svc.Update(ctx, uuid.New(), tr.ID, UpdateParams{Title: ptr("X")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update(other jastiper) error = %v, want ErrNotFound", err)
	}
	if _, err := svc.Close(ctx, jastiperID, tr.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(ctx, jastiperID, tr.ID, UpdateParams{Title: ptr("X")}); !errors.Is(err, ErrNotOpen) {
		t.Errorf("Update(closed) error = %v, want ErrNotOpen", err)
	}
}

func TestCancel(t *testing.T) {
	svc := newTestService()
	repo := svc.repo.(*fakeRepository)
	ctx := t.Context()
	jastiperID := uuid.New()
	busy, _ := svc.Create(ctx, jastiperID, validParams())
	repo.active[busy.ID] = 1
	if _, err := svc.Cancel(ctx, jastiperID, busy.ID); !errors.Is(err, ErrActiveOrders) {
		t.Errorf("Cancel(active orders) error = %v, want ErrActiveOrders", err)
	}

	idle, _ := svc.Create(ctx, jastiperID, validParams())
	if _, err := svc.Cancel(ctx, uuid.New(), idle.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Cancel(other jastiper) error = %v, want ErrNotFound", err)
	}
	if _, err := svc.Close(ctx, jastiperID, idle.ID); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Cancel(ctx, jastiperID, idle.ID)
	if err != nil || got.Status != StatusCancelled {
		t.Fatalf("Cancel(closed, idle) = %s, %v; want cancelled", got.Status, err)
	}
	if _, err := svc.Cancel(ctx, jastiperID, idle.ID); !errors.Is(err, ErrNotCancelable) {
		t.Errorf("Cancel(again) error = %v, want ErrNotCancelable", err)
	}
	if err := svc.Reserve(ctx, idle.ID, 1); !errors.Is(err, ErrTripClosed) {
		t.Errorf("Reserve(cancelled) error = %v, want ErrTripClosed", err)
	}
}
