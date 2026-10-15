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

type TeamRepository struct {
	db *sqlx.DB
}

func NewTeamRepository(db *sqlx.DB) *TeamRepository {
	return &TeamRepository{db: db}
}

func (r *TeamRepository) Create(ctx context.Context, team *domain.Team) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := reserveTeamKey(ctx, tx, team.ID, team.WorkspaceID, team.Key); err != nil {
		return err
	}
	query := `INSERT INTO teams (id, workspace_id, name, key, description, color, icon) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING created_at, updated_at`
	if err := tx.QueryRowContext(ctx, query, team.ID, team.WorkspaceID, team.Name, team.Key, team.Description, team.Color, team.Icon).Scan(&team.CreatedAt, &team.UpdatedAt); err != nil {
		return teamKeyError(err)
	}
	return tx.Commit()
}

// CreateWithDefaults inserts the team, its creator membership and its
// initial workflow statuses in one transaction: a team is never left
// without its creator or statuses.
func (r *TeamRepository) CreateWithDefaults(ctx context.Context, team *domain.Team, creator *domain.TeamMember, statuses []domain.TeamStatus) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := reserveTeamKey(ctx, tx, team.ID, team.WorkspaceID, team.Key); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `INSERT INTO teams (id, workspace_id, name, key, description, color, icon) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING created_at, updated_at`,
		team.ID, team.WorkspaceID, team.Name, team.Key, team.Description, team.Color, team.Icon).Scan(&team.CreatedAt, &team.UpdatedAt); err != nil {
		return teamKeyError(err)
	}
	if creator != nil {
		creator.TeamID = team.ID
		if err := tx.QueryRowContext(ctx, `INSERT INTO team_members (team_id, user_id) VALUES ($1, $2) RETURNING created_at`,
			creator.TeamID, creator.UserID).Scan(&creator.CreatedAt); err != nil {
			return err
		}
	}
	for i := range statuses {
		st := &statuses[i]
		st.TeamID = team.ID
		if err := tx.QueryRowContext(ctx, `INSERT INTO team_statuses (id, team_id, name, slug, category, color, position, is_default)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING created_at, updated_at`,
			st.ID, st.TeamID, st.Name, st.Slug, st.Category, st.Color, st.Position, st.IsDefault).Scan(&st.CreatedAt, &st.UpdatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *TeamRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Team, error) {
	var team domain.Team
	err := r.db.GetContext(ctx, &team, `SELECT * FROM teams WHERE id = $1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &team, err
}

func (r *TeamRepository) ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.Team, error) {
	var teams []domain.Team
	err := r.db.SelectContext(ctx, &teams, `SELECT * FROM teams WHERE workspace_id = $1 ORDER BY name`, workspaceID)
	return teams, err
}

func (r *TeamRepository) Update(ctx context.Context, team *domain.Team) error {
	query := `UPDATE teams SET name = $1, description = $2, color = $3, icon = $4, triage_enabled = $5, parent_auto_close_enabled = $6, sub_issue_auto_close_enabled = $7, issue_copy_prompt = $8, updated_at = NOW() WHERE id = $9 RETURNING updated_at`
	return r.db.QueryRowContext(ctx, query, team.Name, team.Description, team.Color, team.Icon, team.TriageEnabled, team.ParentAutoCloseEnabled, team.SubIssueAutoCloseEnabled, team.IssueCopyPrompt, team.ID).Scan(&team.UpdatedAt)
}

// ErrTeamKeyCollision means the requested key would reuse an existing or
// historical identifier owned by another issue or team in the workspace.
var ErrTeamKeyCollision = errors.New("another team already uses identifiers under this key")

func teamKeyError(err error) error {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23505" {
		switch pgErr.ConstraintName {
		case "teams_workspace_id_key_key", "issues_workspace_id_identifier_text_key", "issue_identifier_history_pkey":
			return ErrTeamKeyCollision
		}
	}
	return err
}

func reserveTeamKey(ctx context.Context, tx *sqlx.Tx, teamID, workspaceID uuid.UUID, key string) error {
	// Serialize key creation/renaming within a workspace. The identifier history
	// primary key also protects against aliases inserted outside these paths.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 524052))`, workspaceID); err != nil {
		return err
	}
	var collision bool
	err := tx.GetContext(ctx, &collision, `SELECT
		EXISTS(SELECT 1 FROM teams WHERE workspace_id = $2 AND key = $3 AND id <> $1)
		OR EXISTS(
			SELECT 1 FROM issue_identifier_history h
			JOIN issues owner ON owner.workspace_id = h.workspace_id AND owner.id = h.issue_id
			WHERE h.workspace_id = $2 AND split_part(h.identifier_text, '-', 1) = $3
			AND owner.team_id <> $1)
		OR EXISTS(
			SELECT 1 FROM issues mine
			JOIN issue_identifier_history h ON h.workspace_id = mine.workspace_id
				AND h.identifier_text = $3 || '-' || mine.number
			WHERE mine.workspace_id = $2 AND mine.team_id = $1 AND h.issue_id <> mine.id)`, teamID, workspaceID, key)
	if err != nil {
		return err
	}
	if collision {
		return ErrTeamKeyCollision
	}
	return nil
}

// UpdateAndRenameKey changes the team row (including its key) and rewrites
// every issue identifier from oldKey-N to newKey-N in one transaction. The
// workspace-wide identifier uniqueness is checked first; a collision fails
// the whole transaction without touching anything.
func (r *TeamRepository) UpdateAndRenameKey(ctx context.Context, team *domain.Team, newKey string) (int64, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if err := reserveTeamKey(ctx, tx, team.ID, team.WorkspaceID, newKey); err != nil {
		return 0, err
	}

	if err := tx.QueryRowContext(ctx, `UPDATE teams SET name = $1, key = $2, description = $3, color = $4, icon = $5, triage_enabled = $6, parent_auto_close_enabled = $7, sub_issue_auto_close_enabled = $8, issue_copy_prompt = $9, updated_at = NOW() WHERE id = $10 RETURNING updated_at`,
		team.Name, newKey, team.Description, team.Color, team.Icon, team.TriageEnabled, team.ParentAutoCloseEnabled, team.SubIssueAutoCloseEnabled, team.IssueCopyPrompt, team.ID).Scan(&team.UpdatedAt); err != nil {
		return 0, teamKeyError(err)
	}
	res, err := tx.ExecContext(ctx, `UPDATE issues SET identifier_text = $1 || '-' || number, updated_at = NOW() WHERE team_id = $2 AND workspace_id = $3`, newKey, team.ID, team.WorkspaceID)
	if err != nil {
		return 0, teamKeyError(err)
	}
	renamed, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return renamed, tx.Commit()
}

func (r *TeamRepository) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM teams WHERE id = $1`, id)
	return err
}

func (r *TeamRepository) AddMember(ctx context.Context, member *domain.TeamMember) error {
	query := `INSERT INTO team_members (team_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING RETURNING created_at`
	return r.db.QueryRowContext(ctx, query, member.TeamID, member.UserID).Scan(&member.CreatedAt)
}

func (r *TeamRepository) GetMember(ctx context.Context, teamID, userID uuid.UUID) (*domain.TeamMember, error) {
	var member domain.TeamMember
	err := r.db.GetContext(ctx, &member, `SELECT * FROM team_members WHERE team_id = $1 AND user_id = $2`, teamID, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &member, err
}

func (r *TeamRepository) ListMembers(ctx context.Context, teamID uuid.UUID) ([]domain.TeamMember, error) {
	var members []domain.TeamMember
	err := r.db.SelectContext(ctx, &members, `SELECT * FROM team_members WHERE team_id = $1`, teamID)
	return members, err
}

func (r *TeamRepository) RemoveMember(ctx context.Context, teamID, userID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM team_members WHERE team_id = $1 AND user_id = $2`, teamID, userID)
	return err
}
