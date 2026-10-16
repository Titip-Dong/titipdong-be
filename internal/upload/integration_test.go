package upload

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/Titip-Dong/titipdong-be/db/migrations"
	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/platform/blob"
	"github.com/Titip-Dong/titipdong-be/internal/platform/database"
	"github.com/google/uuid"
)

// TestUploadAttachRead needs PostgreSQL (TEST_DATABASE_URL) and Azurite or Azure
// (TEST_AZURE_STORAGE_CONNECTION_STRING reachable from the test process).
func TestUploadAttachRead(t *testing.T) {
	dbURL, blobConn := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_AZURE_STORAGE_CONNECTION_STRING")
	if dbURL == "" || blobConn == "" {
		t.Skip("TEST_DATABASE_URL or TEST_AZURE_STORAGE_CONNECTION_STRING not set")
	}
	ctx := context.Background()
	pool, err := database.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	store, err := blob.New(blob.Config{ConnectionString: blobConn, Container: "test-uploads"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Setup(ctx, nil); err != nil {
		t.Fatal(err)
	}
	owner, err := auth.Provision(ctx, pool, auth.RegisterParams{
		Name: "Uploader", Email: uuid.NewString() + "@test.local", Password: "password123", Role: auth.RoleBuyer,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(pool, store)
	content := []byte("not really a jpeg")

	ticket, err := svc.Create(ctx, auth.Principal{UserID: owner.ID, Role: auth.RoleBuyer},
		CreateParams{Purpose: PurposeOrderPhoto, ContentType: "image/jpeg", SizeBytes: int64(len(content))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Attach(ctx, owner.ID, ticket.Upload.ID, PurposeOrderPhoto); !errors.Is(err, ErrNotUploaded) {
		t.Fatalf("attach before upload: err = %v, want ErrNotUploaded", err)
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, ticket.UploadURL, bytes.NewReader(content))
	req.Header.Set("x-ms-blob-type", "BlockBlob")
	req.Header.Set("Content-Type", "image/jpeg")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status = %d, want 201", res.StatusCode)
	}

	var key string
	err = database.WithTx(ctx, pool, func(ctx context.Context) error {
		key, err = svc.Attach(ctx, owner.ID, ticket.Upload.ID, PurposeOrderPhoto)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err = http.Get(svc.ReadURL(key))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	got, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !bytes.Equal(got, content) {
		t.Fatalf("GET = %d %q, want 200 with the uploaded content", res.StatusCode, got)
	}
}
