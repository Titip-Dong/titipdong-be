package trip

import (
	"encoding/base64"
	"strings"
	"time"

	"github.com/google/uuid"
)

type cursor struct {
	At time.Time
	ID uuid.UUID
}

func (c cursor) encode() string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.At.Format(time.RFC3339Nano) + "|" + c.ID.String()))
}

// decodeCursor returns the start cursor (start, nil UUID) when raw is empty.
func decodeCursor(raw string, start time.Time) (cursor, error) {
	if raw == "" {
		return cursor{At: start}, nil
	}
	invalid := &InputError{Detail: "Cursor is invalid."}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return cursor{}, invalid
	}
	at, id, ok := strings.Cut(string(b), "|")
	if !ok {
		return cursor{}, invalid
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return cursor{}, invalid
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return cursor{}, invalid
	}
	return cursor{At: t, ID: u}, nil
}
