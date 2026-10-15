package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type APIKeyRepository struct {
	db *sqlx.DB
}

func NewAPIKeyRepository(db *sqlx.DB) *APIKeyRepository {
	return &APIKeyRepository{db: db}
}

func (r *APIKeyRepository) Create(ctx context.Context, key *domain.APIKey) error {
	query := `INSERT INTO api_keys (id, user_id, name, token_hash, key_prefix, scopes, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING created_at`
	return r.db.QueryRowContext(ctx, query,
		key.ID, key.UserID, key.Name, key.TokenHash, key.KeyPrefix, key.Scopes, key.ExpiresAt,
	).Scan(&key.CreatedAt)
}

func (r *APIKeyRepository) ListByUser(ctx context.Context, userID uuid.UUID) ([]domain.APIKey, error) {
	var keys []domain.APIKey
	err := r.db.SelectContext(ctx, &keys, `SELECT * FROM api_keys WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	return keys, err
}

func (r *APIKeyRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.APIKey, error) {
	var key domain.APIKey
	err := r.db.GetContext(ctx, &key, `SELECT * FROM api_keys WHERE id = $1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &key, err
}

func (r *APIKeyRepository) GetByHash(ctx context.Context, tokenHash string) (*domain.APIKey, error) {
	var key domain.APIKey
	err := r.db.GetContext(ctx, &key, `SELECT * FROM api_keys WHERE token_hash = $1`, tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &key, err
}

func (r *APIKeyRepository) Revoke(ctx context.Context, id, userID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `UPDATE api_keys SET revoked_at = NOW() WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`, id, userID)
	return err
}

func (r *APIKeyRepository) TouchLastUsed(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at = NOW() WHERE id = $1`, id)
	return err
}
