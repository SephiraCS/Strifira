package domain

import (
	"time"

	"github.com/google/uuid"
)

// ForgejoConnection holds the per-workspace Forgejo instance URL and access
// token (encrypted). A row overrides the global env configuration. The
// optional organization restricts repo discovery and linking to that owner.
type ForgejoConnection struct {
	ID               uuid.UUID `json:"id" db:"id"`
	WorkspaceID      uuid.UUID `json:"workspace_id" db:"workspace_id"`
	InstanceURL      string    `json:"instance_url" db:"instance_url"`
	Organization     *string   `json:"organization,omitempty" db:"organization"`
	Token            string    `json:"-" db:"token"`
	AccountLogin     string    `json:"account_login" db:"account_login"`
	AccountAvatarURL *string   `json:"account_avatar_url" db:"account_avatar_url"`
	CreatedAt        time.Time `json:"created_at" db:"created_at"`
	UpdatedAt        time.Time `json:"updated_at" db:"updated_at"`
}

// ForgejoWorkspaceDisable marks a workspace with the integration paused
// entirely; the marker wins over both the per-workspace connection row and
// the global env configuration. The connection row itself is retained.
type ForgejoWorkspaceDisable struct {
	WorkspaceID uuid.UUID `json:"workspace_id" db:"workspace_id"`
	DisabledAt  time.Time `json:"disabled_at" db:"disabled_at"`
}

// ForgejoRepoModel is a repository linked to a workspace. The webhook fields
// track the webhook ku itself created on the repository so unlinking can
// remove it again.
type ForgejoRepoModel struct {
	ID            uuid.UUID `json:"id" db:"id"`
	WorkspaceID   uuid.UUID `json:"workspace_id" db:"workspace_id"`
	ForgejoRepoID int64     `json:"forgejo_repo_id" db:"forgejo_repo_id"`
	FullName      string    `json:"full_name" db:"full_name"`
	InstanceURL   string    `json:"instance_url" db:"instance_url"`
	DefaultBranch string    `json:"default_branch" db:"default_branch"`
	WebhookID     *int64    `json:"-" db:"webhook_id"`
	WebhookSecret string    `json:"-" db:"webhook_secret"`
	IsActive      bool      `json:"is_active" db:"is_active"`
	CreatedAt     time.Time `json:"created_at" db:"created_at"`
}

type ForgejoPullRequest struct {
	ID              uuid.UUID  `json:"id" db:"id"`
	WorkspaceID     uuid.UUID  `json:"workspace_id" db:"workspace_id"`
	ForgejoRepoID   uuid.UUID  `json:"forgejo_repo_id" db:"forgejo_repo_id"`
	Number          int        `json:"number" db:"number"`
	Title           string     `json:"title" db:"title"`
	Body            string     `json:"-" db:"body"`
	State           string     `json:"state" db:"state"`
	ChecksGreen     bool       `json:"checks_green" db:"checks_green"`
	AuthorLogin     string     `json:"author_login" db:"author_login"`
	AuthorAvatarURL *string    `json:"author_avatar_url" db:"author_avatar_url"`
	HTMLURL         string     `json:"html_url" db:"html_url"`
	HeadBranch      *string    `json:"head_branch" db:"head_branch"`
	HeadSHA         *string    `json:"head_sha" db:"head_sha"`
	BaseBranch      *string    `json:"base_branch" db:"base_branch"`
	Additions       int        `json:"additions" db:"additions"`
	Deletions       int        `json:"deletions" db:"deletions"`
	MergedAt        *time.Time `json:"merged_at" db:"merged_at"`
	ClosedAt        *time.Time `json:"closed_at" db:"closed_at"`
	CreatedAt       time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at" db:"updated_at"`
}

type ForgejoBranch struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	WorkspaceID   uuid.UUID  `json:"workspace_id" db:"workspace_id"`
	IssueID       *uuid.UUID `json:"issue_id" db:"issue_id"`
	ForgejoRepoID uuid.UUID  `json:"forgejo_repo_id" db:"forgejo_repo_id"`
	Name          string     `json:"name" db:"name"`
	HTMLURL       *string    `json:"html_url" db:"html_url"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
}

type ForgejoCommit struct {
	ID              uuid.UUID  `json:"id" db:"id"`
	WorkspaceID     uuid.UUID  `json:"workspace_id" db:"workspace_id"`
	IssueID         *uuid.UUID `json:"issue_id" db:"issue_id"`
	ForgejoRepoID   uuid.UUID  `json:"forgejo_repo_id" db:"forgejo_repo_id"`
	SHA             string     `json:"sha" db:"sha"`
	Message         string     `json:"message" db:"message"`
	AuthorLogin     *string    `json:"author_login" db:"author_login"`
	AuthorAvatarURL *string    `json:"author_avatar_url" db:"author_avatar_url"`
	HTMLURL         string     `json:"html_url" db:"html_url"`
	CommittedAt     time.Time  `json:"committed_at" db:"committed_at"`
	CreatedAt       time.Time  `json:"created_at" db:"created_at"`
}

// ForgejoAutoTransition maps a Forgejo lifecycle event to a target issue
// status. Events: branch_created, pr_opened, pr_checks_green, pr_merged.
type ForgejoAutoTransition struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	WorkspaceID       uuid.UUID  `json:"workspace_id" db:"workspace_id"`
	Event             string     `json:"event" db:"event"`
	TargetStatus      string     `json:"target_status" db:"target_status"`
	TargetStatusID    *uuid.UUID `json:"target_status_id" db:"target_status_id"`
	TargetAssigneeID  *uuid.UUID `json:"target_assignee_id" db:"target_assignee_id"`
	AssignForgejoUser bool       `json:"assign_forgejo_user" db:"assign_forgejo_user"`
	IsActive          bool       `json:"is_active" db:"is_active"`
}

type ForgejoUserMapping struct {
	WorkspaceID  uuid.UUID `json:"-" db:"workspace_id"`
	ForgejoLogin string    `json:"forgejo_login" db:"forgejo_login"`
	UserID       uuid.UUID `json:"user_id" db:"user_id"`
}
