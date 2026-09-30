package dto

import "time"

type ForgejoActionRunResponse struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Title       string     `json:"title"`
	RunNumber   int64      `json:"run_number"`
	Event       string     `json:"event"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	WorkflowID  string     `json:"workflow_id"`
	HeadBranch  string     `json:"head_branch"`
	HeadSHA     string     `json:"head_sha"`
	HTMLURL     string     `json:"html_url"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

type ForgejoActionJobResponse struct {
	ID          int64                       `json:"id"`
	RunID       int64                       `json:"run_id"`
	Name        string                      `json:"name"`
	Status      string                      `json:"status"`
	Conclusion  string                      `json:"conclusion"`
	HTMLURL     string                      `json:"html_url"`
	Attempt     int64                       `json:"attempt"`
	StartedAt   *time.Time                  `json:"started_at"`
	CompletedAt *time.Time                  `json:"completed_at"`
	Steps       []ForgejoActionStepResponse `json:"steps"`
}

type ForgejoActionStepResponse struct {
	Number      int64      `json:"number"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

type ForgejoActionLogResponse struct {
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	MaxBytes  int    `json:"max_bytes"`
}
