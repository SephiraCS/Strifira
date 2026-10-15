package repository

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type GitHubRepository struct {
	db *sqlx.DB
}

func (r *GitHubRepository) ListActiveReposWithHooks(ctx context.Context) ([]domain.GitHubRepoModel, error) {
	var repos []domain.GitHubRepoModel
	err := r.db.SelectContext(ctx, &repos, `SELECT * FROM github_repos WHERE is_active = true AND webhook_id IS NOT NULL`)
	return repos, err
}

func (r *GitHubRepository) UpdateWebhookID(ctx context.Context, repoID uuid.UUID, hookID int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE github_repos SET webhook_id = $1 WHERE id = $2`, hookID, repoID)
	return err
}

func NewGitHubRepository(db *sqlx.DB) *GitHubRepository {
	return &GitHubRepository{db: db}
}

// --- Connections ---

func (r *GitHubRepository) UpsertConnection(ctx context.Context, conn *domain.GitHubConnection) error {
	query := `INSERT INTO github_connections (id, workspace_id, instance_url, organization, token, account_login, account_avatar_url)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (workspace_id) DO UPDATE SET
			instance_url=EXCLUDED.instance_url, organization=EXCLUDED.organization,
			token=EXCLUDED.token,
			account_login=EXCLUDED.account_login, account_avatar_url=EXCLUDED.account_avatar_url,
			updated_at=NOW()
		RETURNING created_at, updated_at`
	return r.db.QueryRowContext(ctx, query,
		conn.ID, conn.WorkspaceID, conn.InstanceURL, conn.Organization, conn.Token, conn.AccountLogin, conn.AccountAvatarURL,
	).Scan(&conn.CreatedAt, &conn.UpdatedAt)
}

func (r *GitHubRepository) GetConnectionByWorkspace(ctx context.Context, workspaceID uuid.UUID) (*domain.GitHubConnection, error) {
	var conn domain.GitHubConnection
	err := r.db.GetContext(ctx, &conn, `SELECT * FROM github_connections WHERE workspace_id = $1`, workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &conn, err
}

func (r *GitHubRepository) DeleteConnection(ctx context.Context, workspaceID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM github_connections WHERE workspace_id = $1`, workspaceID)
	return err
}

// --- Workspace disables ---

// GetWorkspaceDisable returns the workspace's disable marker (nil when the
// integration is not paused).
func (r *GitHubRepository) GetWorkspaceDisable(ctx context.Context, workspaceID uuid.UUID) (*domain.GitHubWorkspaceDisable, error) {
	var disable domain.GitHubWorkspaceDisable
	err := r.db.GetContext(ctx, &disable, `SELECT * FROM github_workspace_disables WHERE workspace_id = $1`, workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &disable, err
}

func (r *GitHubRepository) SetWorkspaceDisable(ctx context.Context, workspaceID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO github_workspace_disables (workspace_id) VALUES ($1)
		ON CONFLICT (workspace_id) DO UPDATE SET disabled_at = NOW()`, workspaceID)
	return err
}

func (r *GitHubRepository) ClearWorkspaceDisable(ctx context.Context, workspaceID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM github_workspace_disables WHERE workspace_id = $1`, workspaceID)
	return err
}

// --- Repos ---

func (r *GitHubRepository) CreateRepo(ctx context.Context, repo *domain.GitHubRepoModel) error {
	query := `INSERT INTO github_repos (id, workspace_id, github_repo_id, full_name, instance_url, default_branch, webhook_id, webhook_secret, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (workspace_id, github_repo_id) DO UPDATE SET
			full_name=EXCLUDED.full_name, instance_url=EXCLUDED.instance_url,
			default_branch=EXCLUDED.default_branch, webhook_id=EXCLUDED.webhook_id,
			webhook_secret=EXCLUDED.webhook_secret, is_active=EXCLUDED.is_active
		RETURNING created_at`
	return r.db.QueryRowContext(ctx, query,
		repo.ID, repo.WorkspaceID, repo.GitHubRepoID, repo.FullName, repo.InstanceURL,
		repo.DefaultBranch, repo.WebhookID, repo.WebhookSecret, repo.IsActive,
	).Scan(&repo.CreatedAt)
}

func (r *GitHubRepository) ListReposByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.GitHubRepoModel, error) {
	var repos []domain.GitHubRepoModel
	err := r.db.SelectContext(ctx, &repos, `SELECT * FROM github_repos WHERE workspace_id = $1 AND is_active = true ORDER BY full_name`, workspaceID)
	return repos, err
}

func (r *GitHubRepository) GetRepoByID(ctx context.Context, id uuid.UUID) (*domain.GitHubRepoModel, error) {
	var repo domain.GitHubRepoModel
	err := r.db.GetContext(ctx, &repo, `SELECT * FROM github_repos WHERE id = $1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &repo, err
}

func (r *GitHubRepository) GetRepoByGitHubID(ctx context.Context, workspaceID uuid.UUID, githubRepoID int64) (*domain.GitHubRepoModel, error) {
	var repo domain.GitHubRepoModel
	err := r.db.GetContext(ctx, &repo, `SELECT * FROM github_repos WHERE workspace_id = $1 AND github_repo_id = $2`, workspaceID, githubRepoID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &repo, err
}

// GetRepoByFullName resolves a webhook delivery to its linked repo row.
// GitHub webhook payloads carry the repository html_url, which pins the
// instance; full_name alone could collide across instances.
func (r *GitHubRepository) GetRepoByFullName(ctx context.Context, instanceURL, fullName string) (*domain.GitHubRepoModel, error) {
	var repo domain.GitHubRepoModel
	err := r.db.GetContext(ctx, &repo, `
		SELECT * FROM github_repos
		WHERE LOWER(full_name) = LOWER($1) AND LOWER(instance_url) = LOWER($2) AND is_active = true`,
		fullName, instanceURL)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &repo, err
}

func (r *GitHubRepository) DeleteRepo(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM github_repos WHERE id = $1`, id)
	return err
}

func (r *GitHubRepository) DeleteReposByWorkspace(ctx context.Context, workspaceID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM github_repos WHERE workspace_id = $1`, workspaceID)
	return err
}

// --- Pull Requests ---

func (r *GitHubRepository) UpsertPullRequest(ctx context.Context, pr *domain.GitHubPullRequest) error {
	query := `INSERT INTO github_pull_requests (id, workspace_id, github_repo_id, number, title, state, checks_green, author_login, author_avatar_url, html_url, head_branch, head_sha, base_branch, additions, deletions, merged_at, closed_at, body)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		ON CONFLICT (github_repo_id, number) DO UPDATE SET
			title = EXCLUDED.title, body = EXCLUDED.body, state = EXCLUDED.state,
			checks_green = CASE WHEN github_pull_requests.head_sha IS NOT DISTINCT FROM COALESCE(EXCLUDED.head_sha, github_pull_requests.head_sha)
				AND NOT (github_pull_requests.state = 'draft' AND EXCLUDED.state = 'open')
				THEN github_pull_requests.checks_green ELSE false END,
			author_login = EXCLUDED.author_login, author_avatar_url = EXCLUDED.author_avatar_url,
			additions = EXCLUDED.additions, deletions = EXCLUDED.deletions,
			head_branch = COALESCE(EXCLUDED.head_branch, github_pull_requests.head_branch),
			head_sha = COALESCE(EXCLUDED.head_sha, github_pull_requests.head_sha),
			base_branch = EXCLUDED.base_branch,
			merged_at = EXCLUDED.merged_at, closed_at = EXCLUDED.closed_at, updated_at = NOW()
		WHERE github_pull_requests.state <> 'merged' OR EXCLUDED.state = 'merged'
		RETURNING id, created_at, updated_at, checks_green`
	err := r.db.QueryRowContext(ctx, query,
		pr.ID, pr.WorkspaceID, pr.GitHubRepoID, pr.Number, pr.Title, pr.State, pr.ChecksGreen,
		pr.AuthorLogin, pr.AuthorAvatarURL, pr.HTMLURL, pr.HeadBranch, pr.HeadSHA, pr.BaseBranch,
		pr.Additions, pr.Deletions, pr.MergedAt, pr.ClosedAt, pr.Body,
	).Scan(&pr.ID, &pr.CreatedAt, &pr.UpdatedAt, &pr.ChecksGreen)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrGitHubStalePullRequest
	}
	return err
}

func (r *GitHubRepository) UpdatePullRequestChecks(ctx context.Context, pr *domain.GitHubPullRequest, checksGreen bool) error {
	result, err := r.db.ExecContext(ctx, `UPDATE github_pull_requests SET checks_green = $1, updated_at = NOW()
		WHERE id = $2 AND head_sha IS NOT DISTINCT FROM $3 AND state = $4 AND updated_at = $5 AND state IN ('open', 'draft')`,
		checksGreen, pr.ID, pr.HeadSHA, pr.State, pr.UpdatedAt)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return ErrGitHubStalePullRequest
	}
	return err
}

// ListReposWithOpenPRs lists linked repos that have at least one open PR —
// the status poller's work list.
func (r *GitHubRepository) ListReposWithOpenPRs(ctx context.Context) ([]domain.GitHubRepoModel, error) {
	var repos []domain.GitHubRepoModel
	err := r.db.SelectContext(ctx, &repos, `
		SELECT r.* FROM github_repos r
		WHERE r.is_active AND EXISTS (
			SELECT 1 FROM github_pull_requests p
			WHERE p.github_repo_id = r.id AND p.state IN ('open', 'draft'))
		ORDER BY r.full_name`)
	return repos, err
}

// ListOpenPRsByRepo lists a repo's open PRs (with head sha) for status polling.
func (r *GitHubRepository) ListOpenPRsByRepo(ctx context.Context, repoID uuid.UUID) ([]domain.GitHubPullRequest, error) {
	var prs []domain.GitHubPullRequest
	err := r.db.SelectContext(ctx, &prs, `
		SELECT * FROM github_pull_requests
		WHERE github_repo_id = $1 AND state IN ('open', 'draft')`, repoID)
	return prs, err
}

// ListOpenPRsByHeadSHA finds open PRs whose head commit matches — used to
// react to commit-status (CI) events.
func (r *GitHubRepository) ListOpenPRsByHeadSHA(ctx context.Context, repoID uuid.UUID, sha string) ([]domain.GitHubPullRequest, error) {
	var prs []domain.GitHubPullRequest
	err := r.db.SelectContext(ctx, &prs, `
		SELECT * FROM github_pull_requests
		WHERE github_repo_id = $1 AND head_sha = $2 AND state IN ('open', 'draft')`,
		repoID, sha)
	return prs, err
}

func (r *GitHubRepository) GetPRByRepoAndNumber(ctx context.Context, repoID uuid.UUID, number int) (*domain.GitHubPullRequest, error) {
	var pr domain.GitHubPullRequest
	err := r.db.GetContext(ctx, &pr, `SELECT * FROM github_pull_requests WHERE github_repo_id = $1 AND number = $2`, repoID, number)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &pr, err
}

// --- PR ↔ Issue links ---

func (r *GitHubRepository) ReplacePRIssueLinks(ctx context.Context, pr *domain.GitHubPullRequest, issueIDs []uuid.UUID) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockCurrentPullRequest(ctx, tx, pr); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM github_pull_request_issues WHERE pr_id = $1`, pr.ID); err != nil {
		return err
	}
	for _, issueID := range issueIDs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO github_pull_request_issues (pr_id, issue_id) VALUES ($1, $2)
			ON CONFLICT DO NOTHING`, pr.ID, issueID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListIssueIDsForPR returns the issue ids linked to a PR row.
func (r *GitHubRepository) ListIssueIDsForPR(ctx context.Context, prID uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := r.db.SelectContext(ctx, &ids, `SELECT issue_id FROM github_pull_request_issues WHERE pr_id = $1 ORDER BY created_at`, prID)
	return ids, err
}

// --- Branches ---

func (r *GitHubRepository) UpsertBranch(ctx context.Context, b *domain.GitHubBranch) error {
	query := `INSERT INTO github_branches (id, workspace_id, issue_id, github_repo_id, name, html_url)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (github_repo_id, name) DO UPDATE SET
			issue_id = COALESCE(EXCLUDED.issue_id, github_branches.issue_id)
		RETURNING created_at`
	return r.db.QueryRowContext(ctx, query,
		b.ID, b.WorkspaceID, b.IssueID, b.GitHubRepoID, b.Name, b.HTMLURL,
	).Scan(&b.CreatedAt)
}

// --- Commits ---

func (r *GitHubRepository) UpsertCommit(ctx context.Context, c *domain.GitHubCommit) error {
	query := `INSERT INTO github_commits (id, workspace_id, issue_id, github_repo_id, sha, message, author_login, author_avatar_url, html_url, committed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (github_repo_id, sha) DO UPDATE SET
			issue_id = COALESCE(EXCLUDED.issue_id, github_commits.issue_id),
			message = EXCLUDED.message, html_url = EXCLUDED.html_url
		RETURNING created_at`
	return r.db.QueryRowContext(ctx, query,
		c.ID, c.WorkspaceID, c.IssueID, c.GitHubRepoID, c.SHA,
		c.Message, c.AuthorLogin, c.AuthorAvatarURL, c.HTMLURL, c.CommittedAt,
	).Scan(&c.CreatedAt)
}

// --- Auto Transitions ---

func (r *GitHubRepository) UpsertAutoTransition(ctx context.Context, t *domain.GitHubAutoTransition) error {
	query := `INSERT INTO github_auto_transitions (id, workspace_id, event, target_status, target_status_id, target_assignee_id, assign_github_user, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (workspace_id, event) DO UPDATE SET
			target_status = EXCLUDED.target_status, target_status_id = EXCLUDED.target_status_id,
			target_assignee_id = EXCLUDED.target_assignee_id, assign_github_user = EXCLUDED.assign_github_user,
			is_active = EXCLUDED.is_active`
	_, err := r.db.ExecContext(ctx, query, t.ID, t.WorkspaceID, t.Event, t.TargetStatus, t.TargetStatusID, t.TargetAssigneeID, t.AssignGitHubUser, t.IsActive)
	return err
}

func (r *GitHubRepository) IsWorkspaceMember(ctx context.Context, workspaceID, userID uuid.UUID) (bool, error) {
	var exists bool
	err := r.db.GetContext(ctx, &exists, `SELECT EXISTS (SELECT 1 FROM workspace_members WHERE workspace_id = $1 AND user_id = $2)`, workspaceID, userID)
	return exists, err
}

// MatchWorkspaceMemberByName uses an explicit mapping first, then an exact Strifira
// username match. Ambiguity in the fallback leaves assignment alone.
func (r *GitHubRepository) MatchWorkspaceMemberByName(ctx context.Context, workspaceID uuid.UUID, login string) (*uuid.UUID, error) {
	var mapped uuid.UUID
	err := r.db.GetContext(ctx, &mapped, `SELECT user_id FROM github_user_mappings
		WHERE workspace_id = $1 AND github_login = lower($2)`, workspaceID, login)
	if err == nil {
		return &mapped, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var ids []uuid.UUID
	err = r.db.SelectContext(ctx, &ids, `SELECT wm.user_id FROM workspace_members wm
		JOIN users u ON u.id = wm.user_id
		WHERE wm.workspace_id = $1 AND lower(u.name) = lower($2) LIMIT 2`, workspaceID, login)
	if err != nil || len(ids) != 1 {
		return nil, err
	}
	return &ids[0], nil
}

func (r *GitHubRepository) ListUserMappings(ctx context.Context, workspaceID uuid.UUID) ([]domain.GitHubUserMapping, error) {
	var mappings []domain.GitHubUserMapping
	err := r.db.SelectContext(ctx, &mappings, `SELECT workspace_id, github_login, user_id
		FROM github_user_mappings WHERE workspace_id = $1 ORDER BY github_login`, workspaceID)
	return mappings, err
}

func (r *GitHubRepository) ReplaceUserMappings(ctx context.Context, workspaceID uuid.UUID, mappings []domain.GitHubUserMapping) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM github_user_mappings WHERE workspace_id = $1`, workspaceID); err != nil {
		return err
	}
	for _, mapping := range mappings {
		if _, err := tx.ExecContext(ctx, `INSERT INTO github_user_mappings (workspace_id, github_login, user_id)
			VALUES ($1, $2, $3)`, workspaceID, mapping.GitHubLogin, mapping.UserID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ApplyIssueAutomation changes status and, when a target user is given, adds
// them to the assignee set in one transaction so the legacy primary assignee
// stays in sync. Existing assignees are never removed; the primary is only
// filled in when unset.
var ErrGitHubAutomationConflict = errors.New("issue changed while applying GitHub automation")
var ErrGitHubStalePullRequest = errors.New("pull request changed while applying GitHub automation")

// Hold the PR row until the guarded mutation commits. A synchronized or closed
// webhook cannot replace its head/state between this check and an issue move.
func lockCurrentPullRequest(ctx context.Context, tx *sqlx.Tx, expected *domain.GitHubPullRequest) error {
	if expected == nil {
		return nil
	}
	var current domain.GitHubPullRequest
	err := tx.GetContext(ctx, &current, `SELECT id, head_sha, state, updated_at FROM github_pull_requests WHERE id=$1 FOR UPDATE`, expected.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrGitHubStalePullRequest
	}
	if err != nil {
		return err
	}
	if !current.UpdatedAt.Equal(expected.UpdatedAt) || current.State != expected.State || (current.HeadSHA == nil) != (expected.HeadSHA == nil) ||
		(current.HeadSHA != nil && *current.HeadSHA != *expected.HeadSHA) {
		return ErrGitHubStalePullRequest
	}
	return nil
}

func (r *GitHubRepository) ApplyIssueAutomation(ctx context.Context, issue *domain.Issue, addTarget *uuid.UUID, expectedPR *domain.GitHubPullRequest) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockCurrentPullRequest(ctx, tx, expectedPR); err != nil {
		return err
	}
	var previous domain.Issue
	if err := tx.GetContext(ctx, &previous, `SELECT id, status, status_id, updated_at, creator_id FROM issues WHERE id=$1 AND workspace_id=$2 FOR UPDATE`, issue.ID, issue.WorkspaceID); err != nil {
		return err
	}
	if !previous.UpdatedAt.Equal(issue.UpdatedAt) {
		return ErrGitHubAutomationConflict
	}
	var oldAssignees []uuid.UUID
	if addTarget != nil {
		if err := tx.SelectContext(ctx, &oldAssignees, `SELECT user_id FROM issue_assignees WHERE issue_id=$1 ORDER BY user_id`, issue.ID); err != nil {
			return err
		}
	}
	if err := tx.QueryRowContext(ctx, `UPDATE issues SET status=$1, status_id=$2,
  assignee_id=CASE WHEN $3::uuid IS NOT NULL THEN COALESCE(assignee_id,$3::uuid) ELSE assignee_id END, updated_at=NOW()
  WHERE id=$4 AND workspace_id=$5 AND ($2::uuid IS NULL OR EXISTS(SELECT 1 FROM team_statuses ts WHERE ts.id=$2 AND ts.team_id=issues.team_id)) RETURNING updated_at`,
		issue.Status, issue.StatusID, addTarget, issue.ID, issue.WorkspaceID).Scan(&issue.UpdatedAt); err != nil {
		return err
	}
	history := func(field, oldValue, newValue string) error {
		if previous.CreatorID == uuid.Nil {
			return nil
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO issue_history(id,issue_id,user_id,field,old_value,new_value) VALUES($1,$2,$3,$4,$5,$6)`, uuid.New(), issue.ID, previous.CreatorID, field, oldValue, newValue)
		return err
	}
	if previous.Status != issue.Status || (previous.StatusID == nil) != (issue.StatusID == nil) || (previous.StatusID != nil && issue.StatusID != nil && *previous.StatusID != *issue.StatusID) {
		if err := history("status", string(previous.Status), string(issue.Status)); err != nil {
			return err
		}
	}
	if addTarget != nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO issue_assignees(issue_id,user_id) VALUES($1,$2) ON CONFLICT(issue_id,user_id) DO NOTHING`, issue.ID, *addTarget); err != nil {
			return err
		}
		if !slices.Contains(oldAssignees, *addTarget) {
			serialize := func(ids []uuid.UUID) string {
				values := make([]string, 0, len(ids))
				for _, id := range ids {
					values = append(values, id.String())
				}
				slices.Sort(values)
				return strings.Join(values, ", ")
			}
			if err := history("assignees", serialize(oldAssignees), serialize(append(oldAssignees, *addTarget))); err != nil {
				return err
			}
		}
		if issue.AssigneeID == nil {
			issue.AssigneeID = addTarget
		}
	}
	return tx.Commit()
}

func (r *GitHubRepository) ListAutoTransitions(ctx context.Context, workspaceID uuid.UUID) ([]domain.GitHubAutoTransition, error) {
	var transitions []domain.GitHubAutoTransition
	err := r.db.SelectContext(ctx, &transitions, `SELECT * FROM github_auto_transitions WHERE workspace_id = $1`, workspaceID)
	return transitions, err
}

func (r *GitHubRepository) GetAutoTransitionByEvent(ctx context.Context, workspaceID uuid.UUID, event string) (*domain.GitHubAutoTransition, error) {
	var t domain.GitHubAutoTransition
	err := r.db.GetContext(ctx, &t, `SELECT * FROM github_auto_transitions WHERE workspace_id = $1 AND event = $2`, workspaceID, event)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &t, err
}

// --- Queries joining repos for display ---

type PRWithRepo struct {
	domain.GitHubPullRequest
	RepoFullName string `db:"repo_full_name"`
}

type BranchWithRepo struct {
	domain.GitHubBranch
	RepoFullName string `db:"repo_full_name"`
}

type CommitWithRepo struct {
	domain.GitHubCommit
	RepoFullName string `db:"repo_full_name"`
}

func (r *GitHubRepository) ListPRsWithRepoByIssue(ctx context.Context, issueID uuid.UUID) ([]PRWithRepo, error) {
	var prs []PRWithRepo
	query := `SELECT p.*, r.full_name AS repo_full_name
		FROM github_pull_requests p
		JOIN github_repos r ON r.id = p.github_repo_id
		JOIN github_pull_request_issues li ON li.pr_id = p.id
		WHERE li.issue_id = $1
		ORDER BY p.created_at DESC`
	err := r.db.SelectContext(ctx, &prs, query, issueID)
	return prs, err
}

func (r *GitHubRepository) ListBranchesWithRepoByIssue(ctx context.Context, issueID uuid.UUID) ([]BranchWithRepo, error) {
	var branches []BranchWithRepo
	query := `SELECT b.*, r.full_name AS repo_full_name FROM github_branches b JOIN github_repos r ON r.id = b.github_repo_id WHERE b.issue_id = $1 ORDER BY b.created_at DESC`
	err := r.db.SelectContext(ctx, &branches, query, issueID)
	return branches, err
}

func (r *GitHubRepository) ListCommitsWithRepoByIssue(ctx context.Context, issueID uuid.UUID) ([]CommitWithRepo, error) {
	var commits []CommitWithRepo
	query := `SELECT c.*, r.full_name AS repo_full_name FROM github_commits c JOIN github_repos r ON r.id = c.github_repo_id WHERE c.issue_id = $1 ORDER BY c.committed_at DESC LIMIT 50`
	err := r.db.SelectContext(ctx, &commits, query, issueID)
	return commits, err
}
