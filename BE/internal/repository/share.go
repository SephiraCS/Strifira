package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
)

type ScreenShareRepository struct {
	db *sqlx.DB
}

func NewScreenShareRepository(db *sqlx.DB) *ScreenShareRepository {
	return &ScreenShareRepository{db: db}
}

var _ ScreenShareRepo = (*ScreenShareRepository)(nil)

var ErrScreenShareAlreadyActive = errors.New("presenter already has an active share")

func (r *ScreenShareRepository) Create(ctx context.Context, share *domain.ScreenShare) error {
	query := `INSERT INTO screen_shares (id, workspace_id, started_by, status, presenter_token) VALUES ($1, $2, $3, $4, $5) RETURNING created_at`
	err := r.db.QueryRowContext(ctx, query, share.ID, share.WorkspaceID, share.StartedBy, share.Status, share.PresenterToken).Scan(&share.CreatedAt)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_screen_shares_active_presenter" {
		return ErrScreenShareAlreadyActive
	}
	return err
}

func (r *ScreenShareRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.ScreenShare, error) {
	var share domain.ScreenShare
	err := r.db.GetContext(ctx, &share, `SELECT * FROM screen_shares WHERE id = $1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &share, err
}

func (r *ScreenShareRepository) ListActiveByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.ScreenShare, error) {
	var shares []domain.ScreenShare
	err := r.db.SelectContext(ctx, &shares, `SELECT * FROM screen_shares WHERE workspace_id = $1 AND status = 'active' ORDER BY created_at`, workspaceID)
	return shares, err
}

func (r *ScreenShareRepository) ListRecentEndedByWorkspace(ctx context.Context, workspaceID uuid.UUID, limit int) ([]domain.ScreenShare, error) {
	var shares []domain.ScreenShare
	err := r.db.SelectContext(ctx, &shares, `SELECT * FROM screen_shares WHERE workspace_id = $1 AND status = 'ended' ORDER BY ended_at DESC NULLS LAST LIMIT $2`, workspaceID, limit)
	return shares, err
}

func (r *ScreenShareRepository) GetActiveByPresenter(ctx context.Context, workspaceID, userID uuid.UUID) (*domain.ScreenShare, error) {
	var share domain.ScreenShare
	err := r.db.GetContext(ctx, &share, `SELECT * FROM screen_shares WHERE workspace_id = $1 AND started_by = $2 AND status = 'active' ORDER BY created_at DESC LIMIT 1`, workspaceID, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &share, err
}

func (r *ScreenShareRepository) MarkEnded(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `UPDATE screen_shares SET status = 'ended', ended_at = NOW() WHERE id = $1 AND status = 'active'`, id)
	return err
}

func (r *ScreenShareRepository) TouchPresenterSeen(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `UPDATE screen_shares SET last_presenter_seen_at = NOW() WHERE id = $1 AND status = 'active'`, id)
	return err
}
