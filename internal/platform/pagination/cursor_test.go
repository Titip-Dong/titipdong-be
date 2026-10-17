package pagination

import (
	"errors"
	"testing"
)

func TestIDCursor(t *testing.T) {
	id, err := DecodeID(EncodeID(42))
	if err != nil || id != 42 {
		t.Fatalf("round trip = %d, %v", id, err)
	}
	for _, raw := range []string{"!!", EncodeID(0), "bm90LWEtbnVtYmVy"} {
		if _, err := DecodeID(raw); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("DecodeID(%q) error = %v, want ErrInvalidCursor", raw, err)
		}
	}
}
