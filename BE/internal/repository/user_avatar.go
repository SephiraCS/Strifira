package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type UserAvatarRepository struct {
	db *sqlx.DB
}

func NewUserAvatarRepository(db *sqlx.DB) *UserAvatarRepository {
	return &UserAvatarRepository{db: db}
}

func (r *UserAvatarRepository) Upsert(ctx context.Context, avatar *domain.UserAvatar) error {
	query := `
	INSERT INTO user_avatars (id, user_id, storage_key, filename, content_type, size)
	VALUES ($1, $2, $3, $4, $5, $6)
	ON CONFLICT (user_id) DO UPDATE
	SET storage_key = EXCLUDED.storage_key,
	    filename = EXCLUDED.filename,
	    content_type = EXCLUDED.content_type,
	    size = EXCLUDED.size,
	    updated_at = NOW()
	RETURNING created_at, updated_at`
	return r.db.QueryRowContext(ctx, query,
		avatar.ID, avatar.UserID, avatar.StorageKey, avatar.Filename,
		avatar.ContentType, avatar.Size,
	).Scan(&avatar.CreatedAt, &avatar.UpdatedAt)
}

func (r *UserAvatarRepository) GetByUserID(ctx context.Context, userID uuid.UUID) (*domain.UserAvatar, error) {
	var avatar domain.UserAvatar
	err := r.db.GetContext(ctx, &avatar, `SELECT * FROM user_avatars WHERE user_id = $1`, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &avatar, err
}

func (r *UserAvatarRepository) Delete(ctx context.Context, userID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM user_avatars WHERE user_id = $1`, userID)
	return err
}
