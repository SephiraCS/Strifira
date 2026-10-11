package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const (
	DevBoxJobQueued    = "queued"
	DevBoxJobLeased    = "leased"
	DevBoxJobRunning   = "running"
	DevBoxJobSucceeded = "succeeded"
	DevBoxJobFailed    = "failed"
	DevBoxJobCancelled = "cancelled"
	DevBoxJobTimeout   = "timeout"
)

type DevBoxProvider struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type DevBox struct {
	ID              uuid.UUID       `json:"id" db:"id"`
	WorkspaceID     uuid.UUID       `json:"workspace_id" db:"workspace_id"`
	CreatedByUserID uuid.UUID       `json:"created_by_user_id" db:"created_by_user_id"`
	Name            string          `json:"name" db:"name"`
	Enabled         bool            `json:"enabled" db:"enabled"`
	TokenHash       string          `json:"-" db:"token_hash"`
	TokenPrefix     string          `json:"token_prefix" db:"token_prefix"`
	Hostname        string          `json:"hostname" db:"hostname"`
	OS              string          `json:"os" db:"os"`
	Arch            string          `json:"arch" db:"arch"`
	Version         string          `json:"version" db:"version"`
	Providers       json.RawMessage `json:"providers" db:"providers"`
	LastSeenAt      *time.Time      `json:"last_seen_at,omitempty" db:"last_seen_at"`
	RevokedAt       *time.Time      `json:"revoked_at,omitempty" db:"revoked_at"`
	CreatedAt       time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at" db:"updated_at"`
	Online          bool            `json:"online" db:"-"`
}

type DevBoxJobResult struct {
	Status       string   `json:"status"`
	ExitCode     int      `json:"exit_code"`
	Summary      string   `json:"summary"`
	ChangedFiles []string `json:"changed_files"`
	Branch       string   `json:"branch"`
	Patch        string   `json:"patch"`
	TestStatus   string   `json:"test_status"`
	TestExitCode *int     `json:"test_exit_code,omitempty"`
	TestsRun     []string `json:"tests_run"`
	RiskNotes    []string `json:"risk_notes"`
}

type DevBoxJob struct {
	ID                 uuid.UUID        `json:"id" db:"id"`
	WorkspaceID        uuid.UUID        `json:"workspace_id" db:"workspace_id"`
	BoxID              uuid.UUID        `json:"box_id" db:"box_id"`
	RequestedByUserID  uuid.UUID        `json:"requested_by_user_id" db:"requested_by_user_id"`
	IssueID            uuid.UUID        `json:"issue_id" db:"issue_id"`
	IssueIdentifier    string           `json:"issue_identifier" db:"issue_identifier"`
	GitHubRepoID       uuid.UUID        `json:"github_repo_id" db:"github_repo_id"`
	Provider           string           `json:"provider" db:"provider"`
	Prompt             string           `json:"prompt" db:"prompt"`
	RepositoryURL      string           `json:"repository_url" db:"repository_url"`
	RepositoryFullName string           `json:"repository_full_name" db:"repository_full_name"`
	BaseBranch         string           `json:"base_branch" db:"base_branch"`
	WorkingBranch      string           `json:"working_branch" db:"working_branch"`
	TestCommand        json.RawMessage  `json:"test_command" db:"test_command"`
	AcceptanceCriteria json.RawMessage  `json:"acceptance_criteria" db:"acceptance_criteria"`
	MaxRuntimeSeconds  int              `json:"max_runtime_seconds" db:"max_runtime_seconds"`
	Status             string           `json:"status" db:"status"`
	CancelRequested    bool             `json:"cancel_requested" db:"cancel_requested"`
	LeaseHash          string           `json:"-" db:"lease_hash"`
	LeaseExpiresAt     *time.Time       `json:"lease_expires_at,omitempty" db:"lease_expires_at"`
	DeadlineAt         *time.Time       `json:"deadline_at,omitempty" db:"deadline_at"`
	StartedAt          *time.Time       `json:"started_at,omitempty" db:"started_at"`
	CompletedAt        *time.Time       `json:"completed_at,omitempty" db:"completed_at"`
	CreatedAt          time.Time        `json:"created_at" db:"created_at"`
	UpdatedAt          time.Time        `json:"updated_at" db:"updated_at"`
	Result             *json.RawMessage `json:"result,omitempty" db:"result"`
	LogsBytes          int64            `json:"logs_bytes" db:"logs_bytes"`
	LogsTruncated      bool             `json:"logs_truncated" db:"logs_truncated"`
}

type DevBoxJobLog struct {
	ID        int64     `json:"id" db:"id"`
	JobID     uuid.UUID `json:"job_id" db:"job_id"`
	Sequence  int64     `json:"sequence" db:"sequence"`
	Stream    string    `json:"stream" db:"stream"`
	Content   string    `json:"content" db:"content"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

func DevBoxJobTerminal(status string) bool {
	switch status {
	case DevBoxJobSucceeded, DevBoxJobFailed, DevBoxJobCancelled, DevBoxJobTimeout:
		return true
	default:
		return false
	}
}
