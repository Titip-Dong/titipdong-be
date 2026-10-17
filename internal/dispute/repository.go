package dispute

import (
	"context"
	"errors"

	"github.com/Titip-Dong/titipdong-be/internal/db"
	"github.com/Titip-Dong/titipdong-be/internal/platform/database"
	"github.com/Titip-Dong/titipdong-be/internal/platform/pagination"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type repository interface {
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error
	// Create returns ErrAlreadyDisputed when the order already has a dispute.
	Create(ctx context.Context, d Dispute) error
	Dispute(ctx context.Context, id uuid.UUID) (Dispute, error)
	ByOrder(ctx context.Context, orderID uuid.UUID) (Dispute, error)
	Lock(ctx context.Context, id uuid.UUID) (Dispute, error)
	// Resolve returns ErrResolved when the dispute was no longer open.
	Resolve(ctx context.Context, d Dispute) error
	List(ctx context.Context, status Status, after pagination.Cursor, limit int) ([]Dispute, error)
	InsertEvidence(ctx context.Context, e Evidence) error
	Evidence(ctx context.Context, disputeID uuid.UUID) ([]Evidence, error)
	InsertMessage(ctx context.Context, disputeID, authorID uuid.UUID, body string) error
	Messages(ctx context.Context, disputeID uuid.UUID) ([]Message, error)
}

type pgRepository struct {
	pool *pgxpool.Pool
}

func newRepository(pool *pgxpool.Pool) *pgRepository {
	return &pgRepository{pool: pool}
}

func (r *pgRepository) q(ctx context.Context) *db.Queries {
	return db.New(database.Conn(ctx, r.pool))
}

func (r *pgRepository) WithTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return database.WithTx(ctx, r.pool, fn)
}

func (r *pgRepository) Create(ctx context.Context, d Dispute) error {
	err := r.q(ctx).CreateDispute(ctx, db.CreateDisputeParams{
		ID:          d.ID,
		OrderID:     d.OrderID,
		OpenedBy:    d.OpenedBy,
		Category:    string(d.Category),
		Description: d.Description,
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrAlreadyDisputed
	}
	return err
}

func (r *pgRepository) Dispute(ctx context.Context, id uuid.UUID) (Dispute, error) {
	row, err := r.q(ctx).GetDispute(ctx, id)
	if err != nil {
		return Dispute{}, notFound(err)
	}
	return toDispute(row.Dispute, row.OrderCode, row.BuyerID, row.JastiperID), nil
}

func (r *pgRepository) ByOrder(ctx context.Context, orderID uuid.UUID) (Dispute, error) {
	row, err := r.q(ctx).GetDisputeByOrder(ctx, orderID)
	if err != nil {
		return Dispute{}, notFound(err)
	}
	return toDispute(row.Dispute, row.OrderCode, row.BuyerID, row.JastiperID), nil
}

func (r *pgRepository) Lock(ctx context.Context, id uuid.UUID) (Dispute, error) {
	row, err := r.q(ctx).LockDispute(ctx, id)
	if err != nil {
		return Dispute{}, notFound(err)
	}
	return toDispute(row.Dispute, row.OrderCode, row.BuyerID, row.JastiperID), nil
}

func (r *pgRepository) Resolve(ctx context.Context, d Dispute) error {
	n, err := r.q(ctx).ResolveDispute(ctx, db.ResolveDisputeParams{
		Decision:     string(d.Decision),
		DecisionNote: d.DecisionNote,
		DecidedBy:    *d.DecidedBy,
		DecidedAt:    *d.DecidedAt,
		ID:           d.ID,
	})
	if err == nil && n == 0 {
		return ErrResolved
	}
	return err
}

func (r *pgRepository) List(ctx context.Context, status Status, after pagination.Cursor, limit int) ([]Dispute, error) {
	rows, err := r.q(ctx).ListDisputes(ctx, db.ListDisputesParams{
		Status:         string(status),
		AfterCreatedAt: after.At,
		AfterID:        after.ID,
		RowLimit:       int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Dispute, len(rows))
	for i, row := range rows {
		out[i] = toDispute(row.Dispute, row.OrderCode, row.BuyerID, row.JastiperID)
	}
	return out, nil
}

func (r *pgRepository) InsertEvidence(ctx context.Context, e Evidence) error {
	var note *string
	if e.Note != "" {
		note = &e.Note
	}
	return r.q(ctx).InsertDisputeEvidence(ctx, db.InsertDisputeEvidenceParams{
		ID:         e.ID,
		DisputeID:  e.DisputeID,
		UploadedBy: e.UploadedBy,
		FileKey:    e.FileKey,
		Note:       note,
	})
}

func (r *pgRepository) Evidence(ctx context.Context, disputeID uuid.UUID) ([]Evidence, error) {
	rows, err := r.q(ctx).ListDisputeEvidence(ctx, disputeID)
	if err != nil {
		return nil, err
	}
	out := make([]Evidence, len(rows))
	for i, row := range rows {
		out[i] = Evidence{
			ID:           row.ID,
			DisputeID:    row.DisputeID,
			UploadedBy:   row.UploadedBy,
			UploaderName: row.UploaderName,
			UploaderRole: row.UploaderRole,
			FileKey:      row.FileKey,
			Note:         deref(row.Note),
			CreatedAt:    row.CreatedAt,
		}
	}
	return out, nil
}

func (r *pgRepository) InsertMessage(ctx context.Context, disputeID, authorID uuid.UUID, body string) error {
	return r.q(ctx).InsertDisputeMessage(ctx, db.InsertDisputeMessageParams{DisputeID: disputeID, AuthorID: authorID, Body: body})
}

func (r *pgRepository) Messages(ctx context.Context, disputeID uuid.UUID) ([]Message, error) {
	rows, err := r.q(ctx).ListDisputeMessages(ctx, disputeID)
	if err != nil {
		return nil, err
	}
	out := make([]Message, len(rows))
	for i, row := range rows {
		out[i] = Message{
			ID:         row.ID,
			AuthorID:   row.AuthorID,
			AuthorName: row.AuthorName,
			AuthorRole: row.AuthorRole,
			Body:       row.Body,
			CreatedAt:  row.CreatedAt,
		}
	}
	return out, nil
}

func toDispute(d db.Dispute, orderCode string, buyerID, jastiperID uuid.UUID) Dispute {
	out := Dispute{
		ID:           d.ID,
		OrderID:      d.OrderID,
		OrderCode:    orderCode,
		BuyerID:      buyerID,
		JastiperID:   jastiperID,
		OpenedBy:     d.OpenedBy,
		Category:     Category(d.Category),
		Description:  d.Description,
		Status:       Status(d.Status),
		DecisionNote: deref(d.DecisionNote),
		DecidedBy:    d.DecidedBy,
		DecidedAt:    d.DecidedAt,
		CreatedAt:    d.CreatedAt,
	}
	if d.Decision != nil {
		out.Decision = Decision(*d.Decision)
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
