package dto

import (
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
)

type CreateDevBoxRequest struct {
	Name string `json:"name" validate:"required,min=1,max=100"`
}

type UpdateDevBoxRequest struct {
	Name    *string `json:"name,omitempty" validate:"omitempty,min=1,max=100"`
	Enabled *bool   `json:"enabled,omitempty"`
}

type DevBoxTokenResponse struct {
	Box   *domain.DevBox `json:"box"`
	Token string         `json:"token"`
}

type CreateDevBoxJobRequest struct {
	IssueID            string   `json:"issue_id" validate:"required,uuid"`
	Provider           string   `json:"provider" validate:"required,oneof=claude-code codex opencode"`
	RepositoryFullName string   `json:"repository_full_name,omitempty" validate:"omitempty,max=500"`
	BaseBranch         string   `json:"base_branch,omitempty" validate:"omitempty,max=255"`
	Prompt             string   `json:"prompt" validate:"max=50000"`
	TestCommand        []string `json:"test_command" validate:"max=32,dive,required,max=4096"`
	AcceptanceCriteria []string `json:"acceptance_criteria" validate:"max=50,dive,max=2000"`
	MaxRuntimeSeconds  int      `json:"max_runtime_seconds" validate:"omitempty,min=30,max=86400"`
}

type DevBoxJobListParams struct {
	PaginationParams
	IssueID string `query:"issue_id" validate:"omitempty,uuid"`
	BoxID   string `query:"box_id" validate:"omitempty,uuid"`
}

type DevBoxJobLogParams struct {
	AfterID int64 `query:"after_id" validate:"min=0"`
	Limit   int   `query:"limit" validate:"omitempty,min=1,max=500"`
}

type DevBoxHeartbeatRequest struct {
	Hostname  string                  `json:"hostname" validate:"required,max=255"`
	OS        string                  `json:"os" validate:"required,max=64"`
	Arch      string                  `json:"arch" validate:"required,max=64"`
	Version   string                  `json:"version" validate:"required,max=100"`
	Providers []domain.DevBoxProvider `json:"providers" validate:"required,max=3"`
}

type DevBoxHeartbeatResponse struct {
	Box                      *domain.DevBox `json:"box"`
	ServerTime               time.Time      `json:"server_time"`
	HeartbeatIntervalSeconds int            `json:"heartbeat_interval_seconds"`
}

type DevBoxClaimResponse struct {
	Job                      *domain.DevBoxJob `json:"job"`
	LeaseToken               string            `json:"lease_token"`
	LeaseExpiresAt           time.Time         `json:"lease_expires_at"`
	ServerTime               time.Time         `json:"server_time"`
	HeartbeatIntervalSeconds int               `json:"heartbeat_interval_seconds"`
}

type DevBoxLeaseRequest struct {
	LeaseToken string `json:"lease_token" validate:"required,max=128"`
}

type DevBoxLeaseResponse struct {
	Status          string     `json:"status"`
	CancelRequested bool       `json:"cancel_requested"`
	LeaseExpiresAt  *time.Time `json:"lease_expires_at,omitempty"`
	DeadlineAt      *time.Time `json:"deadline_at,omitempty"`
	ServerTime      time.Time  `json:"server_time"`
}

type DevBoxAppendLogRequest struct {
	LeaseToken string `json:"lease_token" validate:"required,max=128"`
	Sequence   int64  `json:"sequence" validate:"min=1"`
	Stream     string `json:"stream" validate:"required,oneof=stdout stderr system"`
	Content    string `json:"content" validate:"required,max=65536"`
}

type DevBoxAppendLogResponse struct {
	Accepted      bool `json:"accepted"`
	LogsTruncated bool `json:"logs_truncated"`
}

// DevBoxCompleteBodyLimit bounds the complete encoded request, including JSON
// escaping. The result validator permits 1 MiB of patch, 8 KiB of summary and
// 700 metadata strings of 4096 bytes: at six encoded bytes per input byte, these
// use at most 23,543,808 bytes. The assigned branch (255 database characters),
// 68-byte lease, statuses, 700 array entries, JSON envelope and two signed
// 64-bit integers bring the compact encoding bound to 23,547,734 bytes. 24 MiB
// leaves room for formatting without changing those per-field limits.
const DevBoxCompleteBodyLimit = 24 << 20

type DevBoxCompleteRequest struct {
	LeaseToken string                 `json:"lease_token" validate:"required,max=128"`
	Result     domain.DevBoxJobResult `json:"result"`
}
