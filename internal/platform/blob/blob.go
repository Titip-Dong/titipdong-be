// Package blob signs short-lived URLs for Azure Blob Storage (or Azurite), so
// browsers upload and download files directly without passing through the API.
package blob

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/service"
)

var ErrNotFound = errors.New("blob not found")

type Properties struct {
	Size        int64
	ContentType string
}

type Config struct {
	ConnectionString string
	Container        string
	// PublicURL replaces the scheme and host of signed URLs, for when the API
	// reaches storage at a different address than browsers do (Azurite in Docker).
	PublicURL   string
	CORSOrigins []string
}

type Store struct {
	container     *container.Client
	containerName string
	service       *service.Client
	credential    *azblob.SharedKeyCredential
	publicURL     *url.URL
}

func New(cfg Config) (*Store, error) {
	client, err := azblob.NewClientFromConnectionString(cfg.ConnectionString, nil)
	if err != nil {
		return nil, fmt.Errorf("create blob client: %w", err)
	}
	credential, err := sharedKey(cfg.ConnectionString)
	if err != nil {
		return nil, err
	}
	s := &Store{
		service:       client.ServiceClient(),
		container:     client.ServiceClient().NewContainerClient(cfg.Container),
		containerName: cfg.Container,
		credential:    credential,
	}
	if cfg.PublicURL != "" {
		if s.publicURL, err = url.Parse(cfg.PublicURL); err != nil {
			return nil, fmt.Errorf("parse blob public URL: %w", err)
		}
	}
	return s, nil
}

// Setup creates the private container if needed and allows browser uploads from origins.
func (s *Store) Setup(ctx context.Context, origins []string) error {
	if _, err := s.container.Create(ctx, nil); err != nil && !bloberror.HasCode(err, bloberror.ContainerAlreadyExists) {
		return fmt.Errorf("create blob container: %w", err)
	}
	if len(origins) == 0 {
		return nil
	}
	_, err := s.service.SetProperties(ctx, &service.SetPropertiesOptions{CORS: []*service.CORSRule{{
		AllowedOrigins:  ptr(strings.Join(origins, ",")),
		AllowedMethods:  ptr("GET,HEAD,PUT"),
		AllowedHeaders:  ptr("*"),
		ExposedHeaders:  ptr("*"),
		MaxAgeInSeconds: ptr(int32(3600)),
	}}})
	if err != nil {
		return fmt.Errorf("set blob CORS rules: %w", err)
	}
	return nil
}

// UploadURL signs a URL that can create or overwrite key until expiry.
func (s *Store) UploadURL(key string, expiry time.Time) (string, error) {
	return s.sign(key, sas.BlobPermissions{Create: true, Write: true}, expiry)
}

// ReadURL signs a URL that can read key until expiry.
func (s *Store) ReadURL(key string, expiry time.Time) (string, error) {
	return s.sign(key, sas.BlobPermissions{Read: true}, expiry)
}

func (s *Store) Properties(ctx context.Context, key string) (Properties, error) {
	res, err := s.container.NewBlobClient(key).GetProperties(ctx, nil)
	if bloberror.HasCode(err, bloberror.BlobNotFound) {
		return Properties{}, ErrNotFound
	}
	if err != nil {
		return Properties{}, err
	}
	p := Properties{}
	if res.ContentLength != nil {
		p.Size = *res.ContentLength
	}
	if res.ContentType != nil {
		p.ContentType = *res.ContentType
	}
	return p, nil
}

// Delete removes key; a missing blob is not an error.
func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.container.NewBlobClient(key).Delete(ctx, nil)
	if bloberror.HasCode(err, bloberror.BlobNotFound) {
		return nil
	}
	return err
}

// sign builds the SAS itself: blob.Client.GetSASURL signs the escaped name of
// keys that contain "/", which the service rejects.
func (s *Store) sign(key string, perms sas.BlobPermissions, expiry time.Time) (string, error) {
	qp, err := sas.BlobSignatureValues{
		ContainerName: s.containerName,
		BlobName:      key,
		Permissions:   perms.String(),
		ExpiryTime:    expiry.UTC(),
	}.SignWithSharedKey(s.credential)
	if err != nil {
		return "", fmt.Errorf("sign blob URL: %w", err)
	}
	u, err := url.Parse(s.container.NewBlobClient(key).URL())
	if err != nil {
		return "", err
	}
	u.RawQuery = qp.Encode()
	if s.publicURL != nil {
		u.Scheme, u.Host = s.publicURL.Scheme, s.publicURL.Host
	}
	return u.String(), nil
}

func sharedKey(connectionString string) (*azblob.SharedKeyCredential, error) {
	var name, key string
	for _, part := range strings.Split(connectionString, ";") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "AccountName":
			name = v
		case "AccountKey":
			key = v
		}
	}
	if name == "" || key == "" {
		return nil, errors.New("blob connection string needs AccountName and AccountKey to sign URLs")
	}
	return azblob.NewSharedKeyCredential(name, key)
}

func ptr[T any](v T) *T {
	return &v
}
