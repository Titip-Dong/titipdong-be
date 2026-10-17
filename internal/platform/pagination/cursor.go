// Package pagination encodes the opaque keyset cursors used by list endpoints.
package pagination

import (
	"encoding/base64"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrInvalidCursor = errors.New("invalid cursor")

type Cursor struct {
	At time.Time
	ID uuid.UUID
}

func (c Cursor) Encode() string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.At.Format(time.RFC3339Nano) + "|" + c.ID.String()))
}

// Decode returns start when raw is empty.
func Decode(raw string, start Cursor) (Cursor, error) {
	if raw == "" {
		return start, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	at, id, ok := strings.Cut(string(b), "|")
	if !ok {
		return Cursor{}, ErrInvalidCursor
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	return Cursor{At: t, ID: u}, nil
}

// Newest is the start cursor for lists ordered newest first.
var Newest = Cursor{At: time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), ID: uuid.Max}

// Page trims rows fetched with limit+1 and returns the cursor of the last kept row.
func Page[T any](rows []T, limit int, cursorOf func(T) Cursor) ([]T, string) {
	if limit < 1 || len(rows) <= limit {
		return rows, ""
	}
	rows = rows[:limit]
	return rows, cursorOf(rows[limit-1]).Encode()
}

// EncodeID is the cursor for lists keyed by a BIGINT identity, newest (highest) first.
func EncodeID(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(id, 10)))
}

// DecodeID returns math.MaxInt64 when raw is empty, so the first page starts at the newest row.
func DecodeID(raw string) (int64, error) {
	if raw == "" {
		return math.MaxInt64, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, ErrInvalidCursor
	}
	id, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil || id < 1 {
		return 0, ErrInvalidCursor
	}
	return id, nil
}
