package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/ku/ku-backend/internal/domain"
)

type ForgejoRepository struct {
	db *sqlx.DB
}

func NewForgejoRepository(db *sqlx.DB) *ForgejoRepository {
	return &ForgejoRepository{db: db}
}

// --- Connections ---

func (r *ForgejoRepository) UpsertConnection(ctx context.Context, conn *domain.ForgejoConnection) error {
	query := `INSERT INTO forgejo_connections (id, workspace_id, instance_url, token, account_login, account_avatar_url)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (workspace_id) DO UPDATE SET
			instance_url=EXCLUDED.instance_url, token=EXCLUDED.token,
			account_login=EXCLUDED.account_login, account_avatar_url=EXCLUDED.account_avatar_url,
			updated_at=NOW()
		RETURNING created_at, updated_at`
	return r.db.QueryRowContext(ctx, query,
		conn.ID, conn.WorkspaceID, conn.InstanceURL, conn.Token, conn.AccountLogin, conn.AccountAvatarURL,
	).Scan(&conn.CreatedAt, &conn.UpdatedAt)
}

func (r *ForgejoRepository) GetConnectionByWorkspace(ctx context.Context, workspaceID uuid.UUID) (*domain.ForgejoConnection, error) {
	var conn domain.ForgejoConnection
	err := r.db.GetContext(ctx, &conn, `SELECT * FROM forgejo_connections WHERE workspace_id = $1`, workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &conn, err
}

func (r *ForgejoRepository) DeleteConnection(ctx context.Context, workspaceID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM forgejo_connections WHERE workspace_id = $1`, workspaceID)
	return err
}

// --- Repos ---

func (r *ForgejoRepository) CreateRepo(ctx context.Context, repo *domain.ForgejoRepoModel) error {
	query := `INSERT INTO forgejo_repos (id, workspace_id, forgejo_repo_id, full_name, instance_url, default_branch, webhook_id, webhook_secret, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (workspace_id, forgejo_repo_id) DO UPDATE SET
			full_name=EXCLUDED.full_name, instance_url=EXCLUDED.instance_url,
			default_branch=EXCLUDED.default_branch, webhook_id=EXCLUDED.webhook_id,
			webhook_secret=EXCLUDED.webhook_secret, is_active=EXCLUDED.is_active
		RETURNING created_at`
	return r.db.QueryRowContext(ctx, query,
		repo.ID, repo.WorkspaceID, repo.ForgejoRepoID, repo.FullName, repo.InstanceURL,
		repo.DefaultBranch, repo.WebhookID, repo.WebhookSecret, repo.IsActive,
	).Scan(&repo.CreatedAt)
}

func (r *ForgejoRepository) ListReposByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.ForgejoRepoModel, error) {
	var repos []domain.ForgejoRepoModel
	err := r.db.SelectContext(ctx, &repos, `SELECT * FROM forgejo_repos WHERE workspace_id = $1 AND is_active = true ORDER BY full_name`, workspaceID)
	return repos, err
}

func (r *ForgejoRepository) GetRepoByID(ctx context.Context, id uuid.UUID) (*domain.ForgejoRepoModel, error) {
	var repo domain.ForgejoRepoModel
	err := r.db.GetContext(ctx, &repo, `SELECT * FROM forgejo_repos WHERE id = $1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &repo, err
}

func (r *ForgejoRepository) GetRepoByForgejoID(ctx context.Context, workspaceID uuid.UUID, forgejoRepoID int64) (*domain.ForgejoRepoModel, error) {
	var repo domain.ForgejoRepoModel
	err := r.db.GetContext(ctx, &repo, `SELECT * FROM forgejo_repos WHERE workspace_id = $1 AND forgejo_repo_id = $2`, workspaceID, forgejoRepoID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &repo, err
}

// GetRepoByFullName resolves a webhook delivery to its linked repo row.
// Forgejo webhook payloads carry the repository html_url, which pins the
// instance; full_name alone could collide across instances.
func (r *ForgejoRepository) GetRepoByFullName(ctx context.Context, instanceURL, fullName string) (*domain.ForgejoRepoModel, error) {
	var repo domain.ForgejoRepoModel
	err := r.db.GetContext(ctx, &repo, `
		SELECT * FROM forgejo_repos
		WHERE LOWER(full_name) = LOWER($1) AND LOWER(instance_url) = LOWER($2) AND is_active = true`,
		fullName, instanceURL)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &repo, err
}

func (r *ForgejoRepository) DeleteRepo(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM forgejo_repos WHERE id = $1`, id)
	return err
}

func (r *ForgejoRepository) DeleteReposByWorkspace(ctx context.Context, workspaceID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM forgejo_repos WHERE workspace_id = $1`, workspaceID)
	return err
}

// --- Pull Requests ---

func (r *ForgejoRepository) UpsertPullRequest(ctx context.Context, pr *domain.ForgejoPullRequest) error {
	query := `INSERT INTO forgejo_pull_requests (id, workspace_id, forgejo_repo_id, number, title, state, checks_green, author_login, author_avatar_url, html_url, head_branch, head_sha, base_branch, additions, deletions, merged_at, closed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT (forgejo_repo_id, number) DO UPDATE SET
			title = EXCLUDED.title, state = EXCLUDED.state, checks_green = EXCLUDED.checks_green,
			author_login = EXCLUDED.author_login, author_avatar_url = EXCLUDED.author_avatar_url,
			additions = EXCLUDED.additions, deletions = EXCLUDED.deletions,
			head_branch = COALESCE(EXCLUDED.head_branch, forgejo_pull_requests.head_branch),
			head_sha = COALESCE(EXCLUDED.head_sha, forgejo_pull_requests.head_sha),
			base_branch = EXCLUDED.base_branch,
			merged_at = EXCLUDED.merged_at, closed_at = EXCLUDED.closed_at, updated_at = NOW()
		RETURNING id, created_at, updated_at`
	return r.db.QueryRowContext(ctx, query,
		pr.ID, pr.WorkspaceID, pr.ForgejoRepoID, pr.Number, pr.Title, pr.State, pr.ChecksGreen,
		pr.AuthorLogin, pr.AuthorAvatarURL, pr.HTMLURL, pr.HeadBranch, pr.HeadSHA, pr.BaseBranch,
		pr.Additions, pr.Deletions, pr.MergedAt, pr.ClosedAt,
	).Scan(&pr.ID, &pr.CreatedAt, &pr.UpdatedAt)
}

func (r *ForgejoRepository) UpdatePullRequestChecks(ctx context.Context, repoID uuid.UUID, number int, checksGreen bool) error {
	_, err := r.db.ExecContext(ctx, `UPDATE forgejo_pull_requests SET checks_green = $1, updated_at = NOW() WHERE forgejo_repo_id = $2 AND number = $3`, checksGreen, repoID, number)
	return err
}

// ListReposWithOpenPRs lists linked repos that have at least one open PR —
// the status poller's work list.
func (r *ForgejoRepository) ListReposWithOpenPRs(ctx context.Context) ([]domain.ForgejoRepoModel, error) {
	var repos []domain.ForgejoRepoModel
	err := r.db.SelectContext(ctx, &repos, `
		SELECT r.* FROM forgejo_repos r
		WHERE r.is_active AND EXISTS (
			SELECT 1 FROM forgejo_pull_requests p
			WHERE p.forgejo_repo_id = r.id AND p.state IN ('open', 'draft'))
		ORDER BY r.full_name`)
	return repos, err
}

// ListOpenPRsByRepo lists a repo's open PRs (with head sha) for status polling.
func (r *ForgejoRepository) ListOpenPRsByRepo(ctx context.Context, repoID uuid.UUID) ([]domain.ForgejoPullRequest, error) {
	var prs []domain.ForgejoPullRequest
	err := r.db.SelectContext(ctx, &prs, `
		SELECT * FROM forgejo_pull_requests
		WHERE forgejo_repo_id = $1 AND state IN ('open', 'draft')`, repoID)
	return prs, err
}

// ListOpenPRsByHeadSHA finds open PRs whose head commit matches — used to
// react to commit-status (CI) events.
func (r *ForgejoRepository) ListOpenPRsByHeadSHA(ctx context.Context, repoID uuid.UUID, sha string) ([]domain.ForgejoPullRequest, error) {
	var prs []domain.ForgejoPullRequest
	err := r.db.SelectContext(ctx, &prs, `
		SELECT * FROM forgejo_pull_requests
		WHERE forgejo_repo_id = $1 AND head_sha = $2 AND state IN ('open', 'draft')`,
		repoID, sha)
	return prs, err
}

func (r *ForgejoRepository) GetPRByRepoAndNumber(ctx context.Context, repoID uuid.UUID, number int) (*domain.ForgejoPullRequest, error) {
	var pr domain.ForgejoPullRequest
	err := r.db.GetContext(ctx, &pr, `SELECT * FROM forgejo_pull_requests WHERE forgejo_repo_id = $1 AND number = $2`, repoID, number)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &pr, err
}

// --- PR ↔ Issue links ---

func (r *ForgejoRepository) ReplacePRIssueLinks(ctx context.Context, prID uuid.UUID, issueIDs []uuid.UUID) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM forgejo_pull_request_issues WHERE pr_id = $1`, prID); err != nil {
		return err
	}
	for _, issueID := range issueIDs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO forgejo_pull_request_issues (pr_id, issue_id) VALUES ($1, $2)
			ON CONFLICT DO NOTHING`, prID, issueID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListIssueIDsForPR returns the issue ids linked to a PR row.
func (r *ForgejoRepository) ListIssueIDsForPR(ctx context.Context, prID uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := r.db.SelectContext(ctx, &ids, `SELECT issue_id FROM forgejo_pull_request_issues WHERE pr_id = $1 ORDER BY created_at`, prID)
	return ids, err
}

// --- Branches ---

func (r *ForgejoRepository) UpsertBranch(ctx context.Context, b *domain.ForgejoBranch) error {
	query := `INSERT INTO forgejo_branches (id, workspace_id, issue_id, forgejo_repo_id, name, html_url)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (forgejo_repo_id, name) DO UPDATE SET
			issue_id = COALESCE(EXCLUDED.issue_id, forgejo_branches.issue_id)
		RETURNING created_at`
	return r.db.QueryRowContext(ctx, query,
		b.ID, b.WorkspaceID, b.IssueID, b.ForgejoRepoID, b.Name, b.HTMLURL,
	).Scan(&b.CreatedAt)
}

// --- Commits ---

func (r *ForgejoRepository) UpsertCommit(ctx context.Context, c *domain.ForgejoCommit) error {
	query := `INSERT INTO forgejo_commits (id, workspace_id, issue_id, forgejo_repo_id, sha, message, author_login, author_avatar_url, html_url, committed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (forgejo_repo_id, sha) DO UPDATE SET
			issue_id = COALESCE(EXCLUDED.issue_id, forgejo_commits.issue_id),
			message = EXCLUDED.message, html_url = EXCLUDED.html_url
		RETURNING created_at`
	return r.db.QueryRowContext(ctx, query,
		c.ID, c.WorkspaceID, c.IssueID, c.ForgejoRepoID, c.SHA,
		c.Message, c.AuthorLogin, c.AuthorAvatarURL, c.HTMLURL, c.CommittedAt,
	).Scan(&c.CreatedAt)
}

// --- Auto Transitions ---

func (r *ForgejoRepository) UpsertAutoTransition(ctx context.Context, t *domain.ForgejoAutoTransition) error {
	query := `INSERT INTO forgejo_auto_transitions (id, workspace_id, event, target_status, target_status_id, is_active)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (workspace_id, event) DO UPDATE SET
			target_status = EXCLUDED.target_status, target_status_id = EXCLUDED.target_status_id, is_active = EXCLUDED.is_active`
	_, err := r.db.ExecContext(ctx, query, t.ID, t.WorkspaceID, t.Event, t.TargetStatus, t.TargetStatusID, t.IsActive)
	return err
}

func (r *ForgejoRepository) ListAutoTransitions(ctx context.Context, workspaceID uuid.UUID) ([]domain.ForgejoAutoTransition, error) {
	var transitions []domain.ForgejoAutoTransition
	err := r.db.SelectContext(ctx, &transitions, `SELECT * FROM forgejo_auto_transitions WHERE workspace_id = $1`, workspaceID)
	return transitions, err
}

func (r *ForgejoRepository) GetAutoTransitionByEvent(ctx context.Context, workspaceID uuid.UUID, event string) (*domain.ForgejoAutoTransition, error) {
	var t domain.ForgejoAutoTransition
	err := r.db.GetContext(ctx, &t, `SELECT * FROM forgejo_auto_transitions WHERE workspace_id = $1 AND event = $2 AND is_active = true`, workspaceID, event)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &t, err
}

// --- Queries joining repos for display ---

type PRWithRepo struct {
	domain.ForgejoPullRequest
	RepoFullName string `db:"repo_full_name"`
}

type BranchWithRepo struct {
	domain.ForgejoBranch
	RepoFullName string `db:"repo_full_name"`
}

type CommitWithRepo struct {
	domain.ForgejoCommit
	RepoFullName string `db:"repo_full_name"`
}

func (r *ForgejoRepository) ListPRsWithRepoByIssue(ctx context.Context, issueID uuid.UUID) ([]PRWithRepo, error) {
	var prs []PRWithRepo
	query := `SELECT p.*, r.full_name AS repo_full_name
		FROM forgejo_pull_requests p
		JOIN forgejo_repos r ON r.id = p.forgejo_repo_id
		JOIN forgejo_pull_request_issues li ON li.pr_id = p.id
		WHERE li.issue_id = $1
		ORDER BY p.created_at DESC`
	err := r.db.SelectContext(ctx, &prs, query, issueID)
	return prs, err
}

func (r *ForgejoRepository) ListBranchesWithRepoByIssue(ctx context.Context, issueID uuid.UUID) ([]BranchWithRepo, error) {
	var branches []BranchWithRepo
	query := `SELECT b.*, r.full_name AS repo_full_name FROM forgejo_branches b JOIN forgejo_repos r ON r.id = b.forgejo_repo_id WHERE b.issue_id = $1 ORDER BY b.created_at DESC`
	err := r.db.SelectContext(ctx, &branches, query, issueID)
	return branches, err
}

func (r *ForgejoRepository) ListCommitsWithRepoByIssue(ctx context.Context, issueID uuid.UUID) ([]CommitWithRepo, error) {
	var commits []CommitWithRepo
	query := `SELECT c.*, r.full_name AS repo_full_name FROM forgejo_commits c JOIN forgejo_repos r ON r.id = c.forgejo_repo_id WHERE c.issue_id = $1 ORDER BY c.committed_at DESC LIMIT 50`
	err := r.db.SelectContext(ctx, &commits, query, issueID)
	return commits, err
}
