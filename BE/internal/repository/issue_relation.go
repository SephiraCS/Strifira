package repository

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
	"github.com/ku/ku-backend/internal/domain"
)

type IssueRelationRepository struct {
	db *sqlx.DB
}

func NewIssueRelationRepository(db *sqlx.DB) *IssueRelationRepository {
	return &IssueRelationRepository{db: db}
}

func (r *IssueRelationRepository) Create(ctx context.Context, rel *domain.IssueRelation) error {
	query := `INSERT INTO issue_relations (id, issue_id, related_issue_id, type) VALUES ($1, $2, $3, $4) RETURNING created_at`
	return r.db.QueryRowContext(ctx, query, rel.ID, rel.IssueID, rel.RelatedIssueID, rel.Type).Scan(&rel.CreatedAt)
}

func (r *IssueRelationRepository) ListByIssue(ctx context.Context, issueID uuid.UUID) ([]domain.IssueRelation, error) {
	var relations []domain.IssueRelation
	query := `SELECT * FROM issue_relations WHERE issue_id = $1 OR related_issue_id = $1 ORDER BY created_at DESC`
	err := r.db.SelectContext(ctx, &relations, query, issueID)
	return relations, err
}

func (r *IssueRelationRepository) ListByIssues(ctx context.Context, issueIDs []uuid.UUID) ([]domain.IssueRelation, error) {
	if len(issueIDs) == 0 {
		return []domain.IssueRelation{}, nil
	}

	query, args, err := sqlx.In(`SELECT * FROM issue_relations WHERE issue_id IN (?) OR related_issue_id IN (?) ORDER BY created_at DESC`, issueIDs, issueIDs)
	if err != nil {
		return nil, err
	}
	query = r.db.Rebind(query)

	var relations []domain.IssueRelation
	err = r.db.SelectContext(ctx, &relations, query, args...)
	return relations, err
}

func (r *IssueRelationRepository) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM issue_relations WHERE id = $1`, id)
	return err
}

func (r *IssueRelationRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.IssueRelation, error) {
	var rel domain.IssueRelation
	err := r.db.GetContext(ctx, &rel, `SELECT * FROM issue_relations WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	return &rel, nil
}

func (r *IssueRelationRepository) DeleteByIssues(ctx context.Context, issueID, relatedIssueID uuid.UUID, relType domain.IssueRelationType) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM issue_relations WHERE issue_id = $1 AND related_issue_id = $2 AND type = $3`, issueID, relatedIssueID, relType)
	return err
}

// ErrRelationExists means the relation (or its inverse) is already stored.
var ErrRelationExists = errors.New("this relation already exists")

// relationWriteError reports a violation of the relation uniqueness
// constraint as ErrRelationExists.
func relationWriteError(err error) error {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23505" &&
		pgErr.ConstraintName == "issue_relations_issue_id_related_issue_id_type_key" {
		return ErrRelationExists
	}
	return err
}

// CreateWithInverse inserts rel and, when inverse is non-nil, its inverse row
// in one transaction so a relation is never left half-created. A duplicate
// relation reports ErrRelationExists.
//
// The two rows are inserted in a fixed key order, whichever direction was
// requested: concurrent "A blocking B" and "B blocked_by A" requests then
// contend on the same first row, so the loser waits and gets a unique
// violation (ErrRelationExists) instead of the two deadlocking.
func (r *IssueRelationRepository) CreateWithInverse(ctx context.Context, rel, inverse *domain.IssueRelation) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows := []*domain.IssueRelation{rel}
	if inverse != nil {
		rows = append(rows, inverse)
		slices.SortFunc(rows, compareRelationKeys)
	}
	query := `INSERT INTO issue_relations (id, issue_id, related_issue_id, type) VALUES ($1, $2, $3, $4) RETURNING created_at`
	for _, row := range rows {
		if err := tx.QueryRowContext(ctx, query, row.ID, row.IssueID, row.RelatedIssueID, row.Type).Scan(&row.CreatedAt); err != nil {
			return relationWriteError(err)
		}
	}
	return tx.Commit()
}

// compareRelationKeys orders relations by their unique key
// (issue_id, related_issue_id, type).
func compareRelationKeys(a, b *domain.IssueRelation) int {
	if c := bytes.Compare(a.IssueID[:], b.IssueID[:]); c != 0 {
		return c
	}
	if c := bytes.Compare(a.RelatedIssueID[:], b.RelatedIssueID[:]); c != 0 {
		return c
	}
	return strings.Compare(string(a.Type), string(b.Type))
}

// DeleteInWorkspace deletes the relation (and its inverse row, for
// asymmetric types) in one transaction, but only when the relation's issue
// belongs to workspaceID. It reports whether the relation was found.
//
// Both rows are locked in id order before either is deleted, so concurrent
// deletes of the two directions of one pair serialise instead of each
// locking its own row and deadlocking on the other.
func (r *IssueRelationRepository) DeleteInWorkspace(ctx context.Context, workspaceID, id uuid.UUID) (bool, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var rel domain.IssueRelation
	err = tx.GetContext(ctx, &rel, `SELECT r.* FROM issue_relations r JOIN issues i ON i.id = r.issue_id
		WHERE r.id = $1 AND i.workspace_id = $2`, id, workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// The pair: the relation itself plus, for asymmetric types, its inverse.
	const pair = `id = $1 OR (issue_id = $2 AND related_issue_id = $3 AND type = $4 AND type <> $5)`
	args := []any{id, rel.RelatedIssueID, rel.IssueID, rel.Type.Inverse(), rel.Type}
	var locked []uuid.UUID
	if err := tx.SelectContext(ctx, &locked, `SELECT id FROM issue_relations WHERE `+pair+` ORDER BY id FOR UPDATE`, args...); err != nil {
		return false, err
	}
	// A concurrent delete may have removed the relation while we waited.
	if !slices.Contains(locked, id) {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM issue_relations WHERE `+pair, args...); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
