package repository

import (
	"context"
	"database/sql"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/ku/ku-backend/internal/domain"
)

type DocumentRepository struct {
	db *sqlx.DB
}

func NewDocumentRepository(db *sqlx.DB) *DocumentRepository {
	return &DocumentRepository{db: db}
}

func (r *DocumentRepository) Create(ctx context.Context, doc *domain.Document) error {
	query := `INSERT INTO documents (id, workspace_id, parent_id, title, icon, content_html, content_text, position, is_template, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING created_at, updated_at`
	return r.db.QueryRowContext(ctx, query,
		doc.ID, doc.WorkspaceID, doc.ParentID, doc.Title, doc.Icon, doc.ContentHTML, doc.ContentText,
		doc.Position, doc.IsTemplate, doc.CreatedBy,
	).Scan(&doc.CreatedAt, &doc.UpdatedAt)
}

func (r *DocumentRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Document, error) {
	var doc domain.Document
	err := r.db.GetContext(ctx, &doc, `SELECT * FROM documents WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &doc, nil
}

func (r *DocumentRepository) ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.Document, error) {
	var docs []domain.Document
	err := r.db.SelectContext(ctx, &docs, `
		SELECT * FROM documents
		WHERE workspace_id = $1 AND deleted_at IS NULL
		ORDER BY position ASC, created_at ASC`, workspaceID)
	return docs, err
}

func (r *DocumentRepository) ListDeleted(ctx context.Context, workspaceID uuid.UUID) ([]domain.Document, error) {
	var docs []domain.Document
	err := r.db.SelectContext(ctx, &docs, `
		SELECT * FROM documents
		WHERE workspace_id = $1 AND deleted_at IS NOT NULL
		ORDER BY deleted_at DESC`, workspaceID)
	return docs, err
}

func (r *DocumentRepository) Update(ctx context.Context, doc *domain.Document) error {
	query := `UPDATE documents SET
		parent_id = $2, title = $3, icon = $4, content_html = $5, content_text = $6,
		position = $7, is_template = $8, updated_by = $9, updated_at = NOW()
		WHERE id = $1
		RETURNING updated_at`
	return r.db.QueryRowContext(ctx, query,
		doc.ID, doc.ParentID, doc.Title, doc.Icon, doc.ContentHTML, doc.ContentText,
		doc.Position, doc.IsTemplate, doc.UpdatedBy,
	).Scan(&doc.UpdatedAt)
}

// MarkDeleted soft-deletes the document and its whole subtree.
func (r *DocumentRepository) MarkDeleted(ctx context.Context, workspaceID, id, userID uuid.UUID) error {
	query := `
		WITH RECURSIVE subtree AS (
			SELECT id FROM documents WHERE id = $2 AND workspace_id = $1
			UNION ALL
			SELECT d.id FROM documents d JOIN subtree s ON d.parent_id = s.id
		)
		UPDATE documents SET deleted_at = NOW(), deleted_by = $3
		WHERE id IN (SELECT id FROM subtree) AND deleted_at IS NULL`
	_, err := r.db.ExecContext(ctx, query, workspaceID, id, userID)
	return err
}

func (r *DocumentRepository) Restore(ctx context.Context, workspaceID, id uuid.UUID) (*domain.Document, error) {
	query := `
		UPDATE documents SET deleted_at = NULL, deleted_by = NULL
		WHERE id = $2 AND workspace_id = $1 AND deleted_at IS NOT NULL
		RETURNING id`
	if _, err := r.db.ExecContext(ctx, query, workspaceID, id); err != nil {
		return nil, err
	}
	// A restored page whose parent is still deleted (or gone) moves to the
	// root so it can never dangle under invisible ancestors.
	fixup := `
		UPDATE documents SET parent_id = NULL
		WHERE id = $2 AND workspace_id = $1
		  AND parent_id IS NOT NULL
		  AND NOT EXISTS (
			SELECT 1 FROM documents p WHERE p.id = documents.parent_id AND p.deleted_at IS NULL
		  )`
	if _, err := r.db.ExecContext(ctx, fixup, workspaceID, id); err != nil {
		return nil, err
	}
	return r.GetByID(ctx, id)
}

func (r *DocumentRepository) Delete(ctx context.Context, workspaceID, id uuid.UUID) error {
	// CASCADE removes descendants, updates, revisions, links, comments, tags.
	_, err := r.db.ExecContext(ctx, `DELETE FROM documents WHERE id = $2 AND workspace_id = $1`, workspaceID, id)
	return err
}

func (r *DocumentRepository) Search(ctx context.Context, workspaceID uuid.UUID, q string, limit int) ([]domain.Document, error) {
	pattern := "%" + q + "%"
	var docs []domain.Document
	err := r.db.SelectContext(ctx, &docs, `
		SELECT * FROM documents
		WHERE workspace_id = $1 AND deleted_at IS NULL AND is_template = FALSE
		  AND (title ILIKE $2 OR content_text ILIKE $2)
		ORDER BY updated_at DESC
		LIMIT $3`, workspaceID, pattern, limit)
	return docs, err
}

func (r *DocumentRepository) GetByTitle(ctx context.Context, workspaceID uuid.UUID, title string) (*domain.Document, error) {
	var doc domain.Document
	err := r.db.GetContext(ctx, &doc, `
		SELECT * FROM documents
		WHERE workspace_id = $1 AND deleted_at IS NULL AND LOWER(title) = LOWER($2)
		ORDER BY created_at ASC LIMIT 1`, workspaceID, title)
	if err != nil {
		return nil, err
	}
	return &doc, nil
}

// Duplicate copies a document's content, tags and Yjs update log to a new id
// in one transaction so the clone opens with the exact same editing state.
func (r *DocumentRepository) Duplicate(ctx context.Context, src *domain.Document, dst *domain.Document) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	query := `INSERT INTO documents (id, workspace_id, parent_id, title, icon, content_html, content_text, position, is_template, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING created_at, updated_at`
	if err := tx.QueryRowContext(ctx, query,
		dst.ID, dst.WorkspaceID, dst.ParentID, dst.Title, dst.Icon, dst.ContentHTML, dst.ContentText,
		dst.Position, dst.IsTemplate, dst.CreatedBy,
	).Scan(&dst.CreatedAt, &dst.UpdatedAt); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO document_updates (document_id, update) SELECT $2, update FROM document_updates WHERE document_id = $1 ORDER BY id`,
		src.ID, dst.ID); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO document_tag_links (document_id, tag_id) SELECT $2, tag_id FROM document_tag_links WHERE document_id = $1`,
		src.ID, dst.ID); err != nil {
		return err
	}

	return tx.Commit()
}

func (r *DocumentRepository) CreateRevision(ctx context.Context, rev *domain.DocumentRevision) error {
	query := `INSERT INTO document_revisions (id, document_id, title, icon, content_html, content_text, edited_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING created_at`
	return r.db.QueryRowContext(ctx, query,
		rev.ID, rev.DocumentID, rev.Title, rev.Icon, rev.ContentHTML, rev.ContentText, rev.EditedBy,
	).Scan(&rev.CreatedAt)
}

func (r *DocumentRepository) ListRevisions(ctx context.Context, documentID uuid.UUID, limit int) ([]domain.DocumentRevision, error) {
	var revs []domain.DocumentRevision
	err := r.db.SelectContext(ctx, &revs, `
		SELECT * FROM document_revisions WHERE document_id = $1
		ORDER BY created_at DESC LIMIT $2`, documentID, limit)
	return revs, err
}

func (r *DocumentRepository) GetRevisionByID(ctx context.Context, id uuid.UUID) (*domain.DocumentRevision, error) {
	var rev domain.DocumentRevision
	err := r.db.GetContext(ctx, &rev, `SELECT * FROM document_revisions WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &rev, nil
}

func (r *DocumentRepository) LastRevisionAt(ctx context.Context, documentID uuid.UUID) (sql.NullTime, error) {
	var at sql.NullTime
	err := r.db.GetContext(ctx, &at, `
		SELECT MAX(created_at) FROM document_revisions WHERE document_id = $1`, documentID)
	return at, err
}

// ReplaceLinks swaps the resolved outgoing wiki links of a document.
func (r *DocumentRepository) ReplaceLinks(ctx context.Context, documentID uuid.UUID, targetIDs []uuid.UUID) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM document_links WHERE source_id = $1`, documentID); err != nil {
		return err
	}
	for _, target := range targetIDs {
		if target == documentID {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO document_links (source_id, target_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			documentID, target); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *DocumentRepository) ListBacklinks(ctx context.Context, documentID uuid.UUID) ([]domain.Document, error) {
	var docs []domain.Document
	err := r.db.SelectContext(ctx, &docs, `
		SELECT d.* FROM document_links l
		JOIN documents d ON d.id = l.source_id
		WHERE l.target_id = $1 AND d.deleted_at IS NULL
		ORDER BY d.updated_at DESC`, documentID)
	return docs, err
}

func (r *DocumentRepository) ListLinksByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.DocumentLink, error) {
	var links []domain.DocumentLink
	err := r.db.SelectContext(ctx, &links, `
		SELECT l.source_id, l.target_id, l.created_at FROM document_links l
		JOIN documents s ON s.id = l.source_id AND s.workspace_id = $1 AND s.deleted_at IS NULL
		JOIN documents t ON t.id = l.target_id AND t.deleted_at IS NULL
		ORDER BY l.created_at ASC`, workspaceID)
	return links, err
}

func (r *DocumentRepository) ListTags(ctx context.Context, workspaceID uuid.UUID) ([]domain.DocumentTag, error) {
	var tags []domain.DocumentTag
	err := r.db.SelectContext(ctx, &tags,
		`SELECT * FROM document_tags WHERE workspace_id = $1 ORDER BY name ASC`, workspaceID)
	return tags, err
}

func (r *DocumentRepository) CreateTag(ctx context.Context, tag *domain.DocumentTag) error {
	query := `INSERT INTO document_tags (id, workspace_id, name, color, created_by)
		VALUES ($1, $2, $3, $4, $5) RETURNING created_at`
	err := r.db.QueryRowContext(ctx, query, tag.ID, tag.WorkspaceID, tag.Name, tag.Color, tag.CreatedBy).Scan(&tag.CreatedAt)
	if err != nil {
		return err
	}
	return nil
}

func (r *DocumentRepository) GetTagByName(ctx context.Context, workspaceID uuid.UUID, name string) (*domain.DocumentTag, error) {
	var tag domain.DocumentTag
	err := r.db.GetContext(ctx, &tag,
		`SELECT * FROM document_tags WHERE workspace_id = $1 AND LOWER(name) = LOWER($2)`, workspaceID, name)
	if err != nil {
		return nil, err
	}
	return &tag, nil
}

func (r *DocumentRepository) DeleteTag(ctx context.Context, workspaceID, tagID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM document_tags WHERE id = $2 AND workspace_id = $1`, workspaceID, tagID)
	return err
}

func (r *DocumentRepository) SetDocumentTags(ctx context.Context, documentID uuid.UUID, tagIDs []uuid.UUID) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM document_tag_links WHERE document_id = $1`, documentID); err != nil {
		return err
	}
	for _, tagID := range tagIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO document_tag_links (document_id, tag_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			documentID, tagID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *DocumentRepository) ListTagsForDocument(ctx context.Context, documentID uuid.UUID) ([]domain.DocumentTag, error) {
	var tags []domain.DocumentTag
	err := r.db.SelectContext(ctx, &tags, `
		SELECT t.* FROM document_tags t
		JOIN document_tag_links l ON l.tag_id = t.id
		WHERE l.document_id = $1 ORDER BY t.name ASC`, documentID)
	return tags, err
}

// ListTagsForDocuments returns tag names per document for list/tree payloads.
func (r *DocumentRepository) ListTagsForDocuments(ctx context.Context, documentIDs []uuid.UUID) (map[uuid.UUID][]string, error) {
	result := make(map[uuid.UUID][]string)
	if len(documentIDs) == 0 {
		return result, nil
	}
	query, args, err := sqlx.In(`
		SELECT l.document_id, t.name FROM document_tag_links l
		JOIN document_tags t ON t.id = l.tag_id
		WHERE l.document_id IN (?) ORDER BY t.name ASC`, documentIDs)
	if err != nil {
		return nil, err
	}
	query = r.db.Rebind(query)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var docID uuid.UUID
		var name string
		if err := rows.Scan(&docID, &name); err != nil {
			return nil, err
		}
		result[docID] = append(result[docID], name)
	}
	return result, rows.Err()
}

func (r *DocumentRepository) CreateComment(ctx context.Context, comment *domain.DocumentComment) error {
	query := `INSERT INTO document_comments (id, document_id, user_id, body, parent_id)
		VALUES ($1, $2, $3, $4, $5) RETURNING created_at, updated_at`
	return r.db.QueryRowContext(ctx, query,
		comment.ID, comment.DocumentID, comment.UserID, comment.Body, comment.ParentID,
	).Scan(&comment.CreatedAt, &comment.UpdatedAt)
}

func (r *DocumentRepository) ListComments(ctx context.Context, documentID uuid.UUID) ([]domain.DocumentComment, error) {
	var comments []domain.DocumentComment
	err := r.db.SelectContext(ctx, &comments,
		`SELECT * FROM document_comments WHERE document_id = $1 AND parent_id IS NULL ORDER BY created_at ASC`, documentID)
	return comments, err
}

func (r *DocumentRepository) ListCommentReplies(ctx context.Context, parentID uuid.UUID) ([]domain.DocumentComment, error) {
	var comments []domain.DocumentComment
	err := r.db.SelectContext(ctx, &comments,
		`SELECT * FROM document_comments WHERE parent_id = $1 ORDER BY created_at ASC`, parentID)
	return comments, err
}

func (r *DocumentRepository) GetCommentByID(ctx context.Context, id uuid.UUID) (*domain.DocumentComment, error) {
	var comment domain.DocumentComment
	err := r.db.GetContext(ctx, &comment, `SELECT * FROM document_comments WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &comment, nil
}

func (r *DocumentRepository) ResolveComment(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `UPDATE document_comments SET resolved_at = NOW() WHERE id = $1`, id)
	return err
}

func (r *DocumentRepository) ReopenComment(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `UPDATE document_comments SET resolved_at = NULL WHERE id = $1`, id)
	return err
}
