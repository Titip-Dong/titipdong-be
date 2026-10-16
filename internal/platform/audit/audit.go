// Package audit records sensitive actions (money movements, dispute decisions,
// account changes) in the append-only audit_logs table.
package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Titip-Dong/titipdong-be/internal/db"
	"github.com/Titip-Dong/titipdong-be/internal/platform/database"
	"github.com/Titip-Dong/titipdong-be/internal/platform/logging"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Entry struct {
	// ActorID is nil for system actions.
	ActorID    *uuid.UUID
	ActorType  string
	Action     string
	TargetType string
	TargetID   string
	Metadata   map[string]any
}

type Writer struct {
	pool *pgxpool.Pool
}

func NewWriter(pool *pgxpool.Pool) *Writer {
	return &Writer{pool: pool}
}

// Write inserts e on the transaction carried by ctx, if any, so the record
// commits or rolls back with the action it describes.
func (w *Writer) Write(ctx context.Context, e Entry) error {
	if e.Metadata == nil {
		e.Metadata = map[string]any{}
	}
	metadata, err := json.Marshal(e.Metadata)
	if err != nil {
		return fmt.Errorf("encode audit metadata: %w", err)
	}
	var requestID *string
	if id := logging.RequestID(ctx); id != "" {
		requestID = &id
	}
	return db.New(database.Conn(ctx, w.pool)).InsertAuditLog(ctx, db.InsertAuditLogParams{
		ActorID:    e.ActorID,
		ActorType:  e.ActorType,
		Action:     e.Action,
		TargetType: e.TargetType,
		TargetID:   e.TargetID,
		Metadata:   metadata,
		RequestID:  requestID,
	})
}
