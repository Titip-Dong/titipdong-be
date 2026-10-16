// Package upload issues signed upload URLs and lets other modules attach a
// finished upload to their records.
package upload

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/platform/blob"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Purpose string

const (
	PurposeOrderPhoto      Purpose = "order_photo"
	PurposeTripItemPhoto   Purpose = "trip_item_photo"
	PurposeShipmentProof   Purpose = "shipment_proof"
	PurposeDisputeEvidence Purpose = "dispute_evidence"
)

var purposeRoles = map[Purpose][]auth.Role{
	PurposeOrderPhoto:      {auth.RoleBuyer},
	PurposeTripItemPhoto:   {auth.RoleJastiper},
	PurposeShipmentProof:   {auth.RoleJastiper},
	PurposeDisputeEvidence: {auth.RoleBuyer, auth.RoleJastiper},
}

type Status string

const (
	StatusPending  Status = "pending"
	StatusAttached Status = "attached"
)

const (
	MaxSizeBytes      = 5 << 20
	uploadURLLifetime = 15 * time.Minute
	readURLLifetime   = time.Hour
	pendingRetention  = 24 * time.Hour
	purgeBatchSize    = 100
)

var extensions = map[string]string{"image/jpeg": "jpg", "image/png": "png", "image/webp": "webp"}

var (
	ErrUnavailable = errors.New("file uploads are not configured")
	ErrNotFound    = errors.New("upload not found")
	ErrForbidden   = errors.New("role cannot upload this kind of file")
	ErrNotUploaded = errors.New("file has not been uploaded")
)

type InputError struct {
	Detail string
}

func (e *InputError) Error() string {
	return "invalid input: " + e.Detail
}

type Upload struct {
	ID          uuid.UUID
	OwnerID     uuid.UUID
	Purpose     Purpose
	ObjectKey   string
	ContentType string
	SizeBytes   int64
	Status      Status
	CreatedAt   time.Time
}

type CreateParams struct {
	Purpose     Purpose
	ContentType string
	SizeBytes   int64
}

type Ticket struct {
	Upload    Upload
	UploadURL string
	ExpiresAt time.Time
}

type Service interface {
	Create(ctx context.Context, owner auth.Principal, p CreateParams) (Ticket, error)
	// Attach marks the caller's finished upload as used for purpose and returns
	// its object key. Run it inside the transaction that stores the key.
	Attach(ctx context.Context, ownerID, id uuid.UUID, purpose Purpose) (string, error)
	// ReadURL returns a short-lived download URL, or "" for an empty key.
	ReadURL(key string) string
	PurgePending(ctx context.Context) error
}

type store interface {
	UploadURL(key string, expiry time.Time) (string, error)
	ReadURL(key string, expiry time.Time) (string, error)
	Properties(ctx context.Context, key string) (blob.Properties, error)
	Delete(ctx context.Context, key string) error
}

// NewService uses st for storage; a nil st disables uploads.
func NewService(pool *pgxpool.Pool, st *blob.Store) Service {
	var s store
	if st != nil {
		s = st
	}
	return newService(newRepository(pool), s, time.Now)
}

type service struct {
	repo  repository
	store store
	now   func() time.Time
}

func newService(repo repository, st store, now func() time.Time) *service {
	return &service{repo: repo, store: st, now: now}
}

func (s *service) Create(ctx context.Context, owner auth.Principal, p CreateParams) (Ticket, error) {
	if s.store == nil {
		return Ticket{}, ErrUnavailable
	}
	roles, ok := purposeRoles[p.Purpose]
	if !ok {
		return Ticket{}, &InputError{Detail: "Purpose is not supported."}
	}
	if !slices.Contains(roles, owner.Role) {
		return Ticket{}, ErrForbidden
	}
	ext, ok := extensions[p.ContentType]
	if !ok {
		return Ticket{}, &InputError{Detail: "Only JPEG, PNG, and WebP images are allowed."}
	}
	if p.SizeBytes < 1 || p.SizeBytes > MaxSizeBytes {
		return Ticket{}, &InputError{Detail: "File must be at most 5 MB."}
	}
	id := uuid.Must(uuid.NewV7())
	u := Upload{
		ID:          id,
		OwnerID:     owner.UserID,
		Purpose:     p.Purpose,
		ObjectKey:   fmt.Sprintf("%s/%s.%s", p.Purpose, id, ext),
		ContentType: p.ContentType,
		SizeBytes:   p.SizeBytes,
		Status:      StatusPending,
		CreatedAt:   s.now(),
	}
	expires := s.now().Add(uploadURLLifetime)
	signed, err := s.store.UploadURL(u.ObjectKey, expires)
	if err != nil {
		return Ticket{}, err
	}
	if err := s.repo.Create(ctx, u); err != nil {
		return Ticket{}, err
	}
	return Ticket{Upload: u, UploadURL: signed, ExpiresAt: expires}, nil
}

func (s *service) Attach(ctx context.Context, ownerID, id uuid.UUID, purpose Purpose) (string, error) {
	if s.store == nil {
		return "", ErrUnavailable
	}
	var key string
	err := s.repo.WithTx(ctx, func(ctx context.Context) error {
		u, err := s.repo.Lock(ctx, id)
		if err != nil {
			return err
		}
		if u.OwnerID != ownerID || u.Purpose != purpose || u.Status != StatusPending {
			return ErrNotFound
		}
		props, err := s.store.Properties(ctx, u.ObjectKey)
		if errors.Is(err, blob.ErrNotFound) {
			return ErrNotUploaded
		}
		if err != nil {
			return err
		}
		if props.Size > MaxSizeBytes || props.ContentType != u.ContentType {
			return &InputError{Detail: "Uploaded file does not match the declared type or is larger than 5 MB."}
		}
		key = u.ObjectKey
		return s.repo.MarkAttached(ctx, u.ID, props.Size, s.now())
	})
	return key, err
}

func (s *service) ReadURL(key string) string {
	if key == "" || s.store == nil {
		return ""
	}
	signed, err := s.store.ReadURL(key, s.now().Add(readURLLifetime))
	if err != nil {
		slog.Error("sign read URL failed", "error", err)
		return ""
	}
	return signed
}

func (s *service) PurgePending(ctx context.Context) error {
	if s.store == nil {
		return nil
	}
	stale, err := s.repo.ListStalePending(ctx, s.now().Add(-pendingRetention), purgeBatchSize)
	if err != nil {
		return err
	}
	for _, u := range stale {
		if err := s.store.Delete(ctx, u.ObjectKey); err != nil {
			return err
		}
		if err := s.repo.DeletePending(ctx, u.ID); err != nil {
			return err
		}
	}
	return nil
}
