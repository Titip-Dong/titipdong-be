package upload

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
)

const tag = "upload"

type Handler struct {
	svc Service
}

func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) RegisterRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID:   "createUpload",
		Method:        http.MethodPost,
		Path:          "/uploads",
		Summary:       "Get a signed URL to upload an image",
		Description:   "PUT the file to uploadUrl with uploadHeaders before it expires, then send the upload id to the endpoint that uses it.",
		Tags:          []string{tag},
		Security:      auth.Security(),
		DefaultStatus: http.StatusCreated,
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity,
			http.StatusServiceUnavailable},
	}, h.create)
}

type createUploadRequest struct {
	Purpose     string `json:"purpose" enum:"order_photo,trip_item_photo,shipment_proof,dispute_evidence"`
	ContentType string `json:"contentType" enum:"image/jpeg,image/png,image/webp"`
	SizeBytes   int64  `json:"sizeBytes" minimum:"1" maximum:"5242880"`
}

type uploadResponse struct {
	ID            uuid.UUID         `json:"id"`
	Purpose       string            `json:"purpose"`
	UploadURL     string            `json:"uploadUrl"`
	UploadMethod  string            `json:"uploadMethod" enum:"PUT"`
	UploadHeaders map[string]string `json:"uploadHeaders"`
	ExpiresAt     time.Time         `json:"expiresAt"`
}

func (h *Handler) create(ctx context.Context, in *struct{ Body createUploadRequest }) (*struct{ Body uploadResponse }, error) {
	p, _ := auth.PrincipalFrom(ctx)
	t, err := h.svc.Create(ctx, p, CreateParams{
		Purpose:     Purpose(in.Body.Purpose),
		ContentType: in.Body.ContentType,
		SizeBytes:   in.Body.SizeBytes,
	})
	if err != nil {
		return nil, MapError(err)
	}
	return &struct{ Body uploadResponse }{Body: uploadResponse{
		ID:           t.Upload.ID,
		Purpose:      string(t.Upload.Purpose),
		UploadURL:    t.UploadURL,
		UploadMethod: http.MethodPut,
		UploadHeaders: map[string]string{
			"x-ms-blob-type": "BlockBlob",
			"Content-Type":   t.Upload.ContentType,
		},
		ExpiresAt: t.ExpiresAt,
	}}, nil
}

// MapError converts upload errors for this handler and for modules that attach uploads.
func MapError(err error) error {
	var inputErr *InputError
	switch {
	case errors.As(err, &inputErr):
		return huma.Error422UnprocessableEntity(inputErr.Detail)
	case errors.Is(err, ErrUnavailable):
		return huma.Error503ServiceUnavailable("File uploads are not available.")
	case errors.Is(err, ErrForbidden):
		return huma.Error403Forbidden("Your role cannot upload this kind of file.")
	case errors.Is(err, ErrNotFound):
		return huma.Error422UnprocessableEntity("Upload not found.")
	case errors.Is(err, ErrNotUploaded):
		return huma.Error422UnprocessableEntity("File has not been uploaded yet.")
	}
	return err
}
