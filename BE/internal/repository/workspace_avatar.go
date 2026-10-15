package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type WorkspaceAvatarRepository struct {
	db *sqlx.DB
}

func NewWorkspaceAvatarRepository(db *sqlx.DB) *WorkspaceAvatarRepository {
	return &WorkspaceAvatarRepository{db: db}
}

func (r *WorkspaceAvatarRepository) Upsert(ctx context.Context, avatar *domain.WorkspaceAvatar) error {
	query := `
	INSERT INTO workspace_avatars (id, workspace_id, storage_key, filename, content_type, size)
	VALUES ($1, $2, $3, $4, $5, $6)
	ON CONFLICT (workspace_id) DO UPDATE
	SET storage_key = EXCLUDED.storage_key,
	    filename = EXCLUDED.filename,
	    content_type = EXCLUDED.content_type,
	    size = EXCLUDED.size,
	    updated_at = NOW()
	RETURNING created_at, updated_at`
	return r.db.QueryRowContext(ctx, query,
		avatar.ID, avatar.WorkspaceID, avatar.StorageKey, avatar.Filename,
		avatar.ContentType, avatar.Size,
	).Scan(&avatar.CreatedAt, &avatar.UpdatedAt)
}

func (r *WorkspaceAvatarRepository) GetByWorkspaceID(ctx context.Context, workspaceID uuid.UUID) (*domain.WorkspaceAvatar, error) {
	var avatar domain.WorkspaceAvatar
	err := r.db.GetContext(ctx, &avatar, `SELECT * FROM workspace_avatars WHERE workspace_id = $1`, workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &avatar, err
}

func (r *WorkspaceAvatarRepository) Delete(ctx context.Context, workspaceID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM workspace_avatars WHERE workspace_id = $1`, workspaceID)
	return err
}
