package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type RefreshToken struct {
	ID        uuid.UUID  `db:"id"`
	UserID    uuid.UUID  `db:"user_id"`
	TokenHash string     `db:"token_hash"`
	ExpiresAt time.Time  `db:"expires_at"`
	CreatedAt time.Time  `db:"created_at"`
	RotatedAt *time.Time `db:"rotated_at"`
}

type RefreshTokenRepository struct {
	db *sqlx.DB
}

func NewRefreshTokenRepository(db *sqlx.DB) *RefreshTokenRepository {
	return &RefreshTokenRepository{db: db}
}

func (r *RefreshTokenRepository) Create(ctx context.Context, rt *RefreshToken) error {
	query := `INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at) VALUES ($1, $2, $3, $4) RETURNING created_at`
	return r.db.QueryRowContext(ctx, query, rt.ID, rt.UserID, rt.TokenHash, rt.ExpiresAt).Scan(&rt.CreatedAt)
}

func (r *RefreshTokenRepository) GetByHash(ctx context.Context, hash string) (*RefreshToken, error) {
	var rt RefreshToken
	err := r.db.GetContext(ctx, &rt, `SELECT * FROM refresh_tokens WHERE token_hash = $1 AND expires_at > NOW()`, hash)
	if err != nil {
		return nil, err
	}
	return &rt, nil
}

func (r *RefreshTokenRepository) RotateByHash(ctx context.Context, hash string, grace time.Duration) (bool, error) {
	// One atomic statement decides the race: the first rotation stamps
	// rotated_at, later rotations within grace still match the WHERE clause
	// (each context racing the same cookie gets its own refreshed session),
	// and replays after grace no longer match.
	query := `
		UPDATE refresh_tokens
		SET rotated_at = NOW()
		WHERE token_hash = $1
		  AND expires_at > NOW()
		  AND (rotated_at IS NULL OR rotated_at >= NOW() - $2::interval)
		RETURNING id`
	var id uuid.UUID
	err := r.db.QueryRowContext(ctx, query, hash, grace.Seconds()).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (r *RefreshTokenRepository) DeleteByHash(ctx context.Context, hash string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM refresh_tokens WHERE token_hash = $1`, hash)
	return err
}

func (r *RefreshTokenRepository) DeleteByUser(ctx context.Context, userID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM refresh_tokens WHERE user_id = $1`, userID)
	return err
}

func (r *RefreshTokenRepository) DeleteExpired(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM refresh_tokens WHERE expires_at < NOW()`)
	return err
}
