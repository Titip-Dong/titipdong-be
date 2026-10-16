package upload

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/platform/blob"
	"github.com/google/uuid"
)

type fakeRepository struct {
	mu      sync.Mutex
	uploads map[uuid.UUID]Upload
}

func (r *fakeRepository) WithTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

func (r *fakeRepository) Create(_ context.Context, u Upload) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.uploads[u.ID] = u
	return nil
}

func (r *fakeRepository) Lock(_ context.Context, id uuid.UUID) (Upload, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.uploads[id]
	if !ok {
		return Upload{}, ErrNotFound
	}
	return u, nil
}

func (r *fakeRepository) MarkAttached(_ context.Context, id uuid.UUID, size int64, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	u := r.uploads[id]
	u.Status, u.SizeBytes = StatusAttached, size
	r.uploads[id] = u
	return nil
}

func (r *fakeRepository) ListStalePending(_ context.Context, before time.Time, limit int) ([]Upload, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Upload
	for _, u := range r.uploads {
		if u.Status == StatusPending && u.CreatedAt.Before(before) && len(out) < limit {
			out = append(out, u)
		}
	}
	return out, nil
}

func (r *fakeRepository) DeletePending(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uploads[id].Status == StatusPending {
		delete(r.uploads, id)
	}
	return nil
}

type fakeStore struct {
	blobs   map[string]blob.Properties
	deleted []string
}

func (s *fakeStore) UploadURL(key string, _ time.Time) (string, error) {
	return "https://blob.test/put/" + key, nil
}

func (s *fakeStore) ReadURL(key string, _ time.Time) (string, error) {
	return "https://blob.test/get/" + key, nil
}

func (s *fakeStore) Properties(_ context.Context, key string) (blob.Properties, error) {
	p, ok := s.blobs[key]
	if !ok {
		return blob.Properties{}, blob.ErrNotFound
	}
	return p, nil
}

func (s *fakeStore) Delete(_ context.Context, key string) error {
	s.deleted = append(s.deleted, key)
	delete(s.blobs, key)
	return nil
}

var testNow = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

func testNowFunc() time.Time { return testNow }

func newTestService() (*service, *fakeRepository, *fakeStore) {
	repo := &fakeRepository{uploads: map[uuid.UUID]Upload{}}
	st := &fakeStore{blobs: map[string]blob.Properties{}}
	return newService(repo, st, testNowFunc), repo, st
}

var buyer = auth.Principal{UserID: uuid.New(), Role: auth.RoleBuyer}

func TestCreate(t *testing.T) {
	svc, repo, _ := newTestService()
	ticket, err := svc.Create(context.Background(), buyer, CreateParams{Purpose: PurposeOrderPhoto, ContentType: "image/png", SizeBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	want := "order_photo/" + ticket.Upload.ID.String() + ".png"
	if ticket.Upload.ObjectKey != want {
		t.Errorf("object key = %q, want %q", ticket.Upload.ObjectKey, want)
	}
	if ticket.UploadURL != "https://blob.test/put/"+want {
		t.Errorf("upload URL = %q", ticket.UploadURL)
	}
	if !ticket.ExpiresAt.Equal(testNow.Add(uploadURLLifetime)) {
		t.Errorf("expires at = %v", ticket.ExpiresAt)
	}
	if repo.uploads[ticket.Upload.ID].Status != StatusPending {
		t.Error("upload was not stored as pending")
	}
}

func TestCreateRejects(t *testing.T) {
	jastiper := auth.Principal{UserID: uuid.New(), Role: auth.RoleJastiper}
	for _, tc := range []struct {
		name   string
		caller auth.Principal
		p      CreateParams
		want   error
	}{
		{"wrong role for purpose", jastiper, CreateParams{PurposeOrderPhoto, "image/png", 10}, ErrForbidden},
		{"unknown purpose", buyer, CreateParams{"avatar", "image/png", 10}, &InputError{}},
		{"not an image", buyer, CreateParams{PurposeOrderPhoto, "application/pdf", 10}, &InputError{}},
		{"too large", buyer, CreateParams{PurposeOrderPhoto, "image/png", MaxSizeBytes + 1}, &InputError{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _ := newTestService()
			_, err := svc.Create(context.Background(), tc.caller, tc.p)
			var inputErr *InputError
			if errors.As(tc.want, &inputErr) {
				if !errors.As(err, &inputErr) {
					t.Fatalf("err = %v, want InputError", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestAttach(t *testing.T) {
	ctx := context.Background()
	svc, repo, st := newTestService()
	ticket, err := svc.Create(ctx, buyer, CreateParams{Purpose: PurposeOrderPhoto, ContentType: "image/jpeg", SizeBytes: 2000})
	if err != nil {
		t.Fatal(err)
	}
	id, key := ticket.Upload.ID, ticket.Upload.ObjectKey

	if _, err := svc.Attach(ctx, buyer.UserID, id, PurposeOrderPhoto); !errors.Is(err, ErrNotUploaded) {
		t.Fatalf("before upload: err = %v, want ErrNotUploaded", err)
	}
	st.blobs[key] = blob.Properties{Size: 1500, ContentType: "image/jpeg"}
	if _, err := svc.Attach(ctx, uuid.New(), id, PurposeOrderPhoto); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other owner: err = %v, want ErrNotFound", err)
	}
	if _, err := svc.Attach(ctx, buyer.UserID, id, PurposeDisputeEvidence); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other purpose: err = %v, want ErrNotFound", err)
	}

	got, err := svc.Attach(ctx, buyer.UserID, id, PurposeOrderPhoto)
	if err != nil || got != key {
		t.Fatalf("Attach = %q, %v; want %q, nil", got, err, key)
	}
	if u := repo.uploads[id]; u.Status != StatusAttached || u.SizeBytes != 1500 {
		t.Errorf("upload = %+v, want attached with the stored size", u)
	}
	if _, err := svc.Attach(ctx, buyer.UserID, id, PurposeOrderPhoto); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second attach: err = %v, want ErrNotFound", err)
	}
}

func TestAttachRejectsMismatchedBlob(t *testing.T) {
	ctx := context.Background()
	svc, _, st := newTestService()
	ticket, err := svc.Create(ctx, buyer, CreateParams{Purpose: PurposeOrderPhoto, ContentType: "image/jpeg", SizeBytes: 2000})
	if err != nil {
		t.Fatal(err)
	}
	st.blobs[ticket.Upload.ObjectKey] = blob.Properties{Size: 2000, ContentType: "text/html"}
	var inputErr *InputError
	if _, err := svc.Attach(ctx, buyer.UserID, ticket.Upload.ID, PurposeOrderPhoto); !errors.As(err, &inputErr) {
		t.Fatalf("err = %v, want InputError", err)
	}
}

func TestPurgePending(t *testing.T) {
	ctx := context.Background()
	svc, repo, st := newTestService()
	old := Upload{ID: uuid.New(), Status: StatusPending, ObjectKey: "order_photo/old.png", CreatedAt: testNow.Add(-25 * time.Hour)}
	fresh := Upload{ID: uuid.New(), Status: StatusPending, ObjectKey: "order_photo/fresh.png", CreatedAt: testNow.Add(-time.Hour)}
	attached := Upload{ID: uuid.New(), Status: StatusAttached, ObjectKey: "order_photo/kept.png", CreatedAt: testNow.Add(-48 * time.Hour)}
	for _, u := range []Upload{old, fresh, attached} {
		repo.uploads[u.ID] = u
	}

	if err := svc.PurgePending(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := repo.uploads[old.ID]; ok {
		t.Error("stale pending upload was not deleted")
	}
	if _, ok := repo.uploads[fresh.ID]; !ok {
		t.Error("fresh pending upload was deleted")
	}
	if _, ok := repo.uploads[attached.ID]; !ok {
		t.Error("attached upload was deleted")
	}
	if len(st.deleted) != 1 || st.deleted[0] != old.ObjectKey {
		t.Errorf("deleted blobs = %v, want [%s]", st.deleted, old.ObjectKey)
	}
}

func TestDisabledWithoutStore(t *testing.T) {
	svc := newService(&fakeRepository{uploads: map[uuid.UUID]Upload{}}, nil, time.Now)
	if _, err := svc.Create(context.Background(), buyer, CreateParams{PurposeOrderPhoto, "image/png", 10}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if got := svc.ReadURL("order_photo/x.png"); got != "" {
		t.Errorf("ReadURL = %q, want empty", got)
	}
}
