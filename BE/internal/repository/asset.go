package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/ku/ku-backend/internal/domain"
)

type AssetRepository struct {
	db *sqlx.DB
}

func NewAssetRepository(db *sqlx.DB) *AssetRepository {
	return &AssetRepository{db: db}
}

func (r *AssetRepository) Create(ctx context.Context, asset *domain.Asset) error {
	query := `
		INSERT INTO assets (id, workspace_id, storage_key, filename, content_type, size, uploaded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING created_at`
	return r.db.QueryRowContext(ctx, query,
		asset.ID, asset.WorkspaceID, asset.StorageKey, asset.Filename,
		asset.ContentType, asset.Size, asset.UploadedBy,
	).Scan(&asset.CreatedAt)
}

func (r *AssetRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Asset, error) {
	var asset domain.Asset
	err := r.db.GetContext(ctx, &asset, `SELECT * FROM assets WHERE id = $1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &asset, err
}

func (r *AssetRepository) ListByWorkspace(ctx context.Context, workspaceID uuid.UUID, search string, limit, offset int) ([]domain.AssetWithUploader, error) {
	pattern := "%" + search + "%"
	var assets []domain.AssetWithUploader
	err := r.db.SelectContext(ctx, &assets, `
		SELECT a.*, COALESCE(NULLIF(u.display_name, ''), u.name, '') AS uploader_name
		FROM assets a
		LEFT JOIN users u ON u.id = a.uploaded_by
		WHERE a.workspace_id = $1 AND ($2 = '%%' OR a.filename ILIKE $2)
		ORDER BY a.created_at DESC
		LIMIT $3 OFFSET $4`, workspaceID, pattern, limit, offset)
	return assets, err
}

func (r *AssetRepository) CountByWorkspace(ctx context.Context, workspaceID uuid.UUID, search string) (int, error) {
	pattern := "%" + search + "%"
	var count int
	err := r.db.GetContext(ctx, &count, `
		SELECT COUNT(*) FROM assets a
		WHERE a.workspace_id = $1 AND ($2 = '%%' OR a.filename ILIKE $2)`, workspaceID, pattern)
	return count, err
}

// Delete removes the asset row and returns it so the caller can drop the
// stored bytes afterwards.
func (r *AssetRepository) Delete(ctx context.Context, workspaceID, id uuid.UUID) (*domain.Asset, error) {
	var asset domain.Asset
	err := r.db.GetContext(ctx, &asset, `DELETE FROM assets WHERE id = $1 AND workspace_id = $2 RETURNING *`, id, workspaceID)
	if err != nil {
		return nil, err
	}
	return &asset, nil
}
