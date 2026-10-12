package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// DocumentUpdateRepository stores the append-only Yjs update log per
// document. The server treats updates as opaque bytes; only their order
// matters (replay order == id order).
type DocumentUpdateRepository struct {
	db *sqlx.DB
}

func NewDocumentUpdateRepository(db *sqlx.DB) *DocumentUpdateRepository {
	return &DocumentUpdateRepository{db: db}
}

func (r *DocumentUpdateRepository) AppendUpdates(ctx context.Context, documentID uuid.UUID, updates [][]byte) error {
	if len(updates) == 0 {
		return nil
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, u := range updates {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO document_updates (document_id, update) VALUES ($1, $2)`, documentID, u); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *DocumentUpdateRepository) ListUpdates(ctx context.Context, documentID uuid.UUID) ([][]byte, error) {
	var updates [][]byte
	err := r.db.SelectContext(ctx, &updates,
		`SELECT update FROM document_updates WHERE document_id = $1 ORDER BY id ASC`, documentID)
	return updates, err
}

func (r *DocumentUpdateRepository) CountUpdates(ctx context.Context, documentID uuid.UUID) (int, error) {
	var count int
	err := r.db.GetContext(ctx, &count,
		`SELECT COUNT(*) FROM document_updates WHERE document_id = $1`, documentID)
	return count, err
}

// DeleteByDocument wipes the document's update log. Used when a revision
// restore makes the stored HTML authoritative again: the stale Yjs state
// must go so editors re-seed from the restored content.
func (r *DocumentUpdateRepository) DeleteByDocument(ctx context.Context, documentID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM document_updates WHERE document_id = $1`, documentID)
	return err
}

// Compact replaces every stored update with a single full-state snapshot.
// Buffered updates that arrived after the snapshot was taken are re-appended
// so nothing is lost.
func (r *DocumentUpdateRepository) Compact(ctx context.Context, documentID uuid.UUID, snapshot []byte, tail [][]byte) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM document_updates WHERE document_id = $1`, documentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO document_updates (document_id, update) VALUES ($1, $2)`, documentID, snapshot); err != nil {
		return err
	}
	for _, u := range tail {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO document_updates (document_id, update) VALUES ($1, $2)`, documentID, u); err != nil {
			return err
		}
	}
	return tx.Commit()
}
