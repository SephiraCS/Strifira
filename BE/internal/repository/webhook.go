package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type WebhookRepository struct {
	db *sqlx.DB
}

func NewWebhookRepository(db *sqlx.DB) *WebhookRepository {
	return &WebhookRepository{db: db}
}

func (r *WebhookRepository) Create(ctx context.Context, w *domain.Webhook) error {
	query := `INSERT INTO webhooks (id, workspace_id, url, secret, events, is_active) VALUES ($1, $2, $3, $4, $5, $6) RETURNING created_at, updated_at`
	return r.db.QueryRowContext(ctx, query, w.ID, w.WorkspaceID, w.URL, w.Secret, w.Events, w.IsActive).Scan(&w.CreatedAt, &w.UpdatedAt)
}

// GetByID returns the webhook only when it belongs to workspaceID; a foreign
// or missing row yields (nil, nil).
func (r *WebhookRepository) GetByID(ctx context.Context, workspaceID, id uuid.UUID) (*domain.Webhook, error) {
	var w domain.Webhook
	err := r.db.GetContext(ctx, &w, `SELECT * FROM webhooks WHERE id = $1 AND workspace_id = $2`, id, workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &w, err
}

func (r *WebhookRepository) ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.Webhook, error) {
	var webhooks []domain.Webhook
	err := r.db.SelectContext(ctx, &webhooks, `SELECT * FROM webhooks WHERE workspace_id = $1 ORDER BY created_at DESC`, workspaceID)
	return webhooks, err
}

func (r *WebhookRepository) Update(ctx context.Context, w *domain.Webhook) error {
	query := `UPDATE webhooks SET url = $1, events = $2, is_active = $3, updated_at = NOW() WHERE id = $4 AND workspace_id = $5 RETURNING updated_at`
	return r.db.QueryRowContext(ctx, query, w.URL, w.Events, w.IsActive, w.ID, w.WorkspaceID).Scan(&w.UpdatedAt)
}

// Delete removes the webhook when it belongs to workspaceID and reports
// whether a row was deleted.
func (r *WebhookRepository) Delete(ctx context.Context, workspaceID, id uuid.UUID) (bool, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM webhooks WHERE id = $1 AND workspace_id = $2`, id, workspaceID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
