package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// These models follow GitHub's workflow run / workflow job schemas. Runs carry
// a status (queued, in_progress, completed) plus a separate conclusion once
// completed; jobs belong to a run attempt.
type ActionRun struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	Title        string     `json:"display_title"`
	RunNumber    int64      `json:"run_number"`
	WorkflowID   string     `json:"path"`
	Event        string     `json:"event"`
	Status       string     `json:"status"`
	Conclusion   string     `json:"conclusion"`
	HeadBranch   string     `json:"head_branch"`
	HeadSHA      string     `json:"head_sha"`
	HTMLURL      string     `json:"html_url"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	RunStartedAt *time.Time `json:"run_started_at"`
}

type ActionRunList struct {
	Runs       []ActionRun `json:"workflow_runs"`
	TotalCount int64       `json:"total_count"`
}

type ActionJob struct {
	ID         int64  `json:"id"`
	RunID      int64  `json:"run_id"`
	Attempt    int64  `json:"run_attempt"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

type ActionJobList struct {
	Jobs       []ActionJob `json:"jobs"`
	TotalCount int64       `json:"total_count"`
}

type ActionRunOptions struct {
	Page       int
	PerPage    int
	Status     string
	Ref        string
	Event      string
	HeadSHA    string
	WorkflowID string
}

// MaxActionLogBytes bounds memory and browser rendering for each log request.
const MaxActionLogBytes = 2 << 20

type ActionLog struct {
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	MaxBytes  int    `json:"max_bytes"`
}

func actionRepoPath(owner, name string) (string, error) {
	for _, part := range []string{owner, name} {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "/\\?#\x00\r\n") {
			return "", errors.New("invalid GitHub repository name")
		}
	}
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/actions", nil
}

// actionRunQuery translates ku's filter vocabulary onto GitHub's query
// parameters. GitHub filters runs by status (queued, in_progress, completed);
// branch names replace full refs.
func actionRunQuery(opts ActionRunOptions) (url.Values, error) {
	query := url.Values{"page": {strconv.Itoa(opts.Page)}, "per_page": {strconv.Itoa(opts.PerPage)}}
	status := opts.Status
	switch status {
	case "":
	case "waiting", "queued":
		status = "queued"
	case "running", "in_progress":
		status = "in_progress"
	case "completed":
	default:
		return nil, errors.New("invalid GitHub Actions status filter")
	}
	if status != "" {
		query.Set("status", status)
	}
	if opts.Ref != "" {
		query.Set("branch", strings.TrimPrefix(opts.Ref, "refs/heads/"))
	}
	if opts.Event != "" {
		query.Set("event", opts.Event)
	}
	if opts.HeadSHA != "" {
		query.Set("head_sha", opts.HeadSHA)
	}
	return query, nil
}

// ListActionRuns lists workflow runs. When a workflow ID is given, GitHub's
// workflow-scoped endpoint is used.
func (c *Client) ListActionRuns(ctx context.Context, owner, name string, opts ActionRunOptions) (*ActionRunList, error) {
	base, err := actionRepoPath(owner, name)
	if err != nil {
		return nil, err
	}
	path := base + "/runs"
	if workflow := strings.TrimSpace(opts.WorkflowID); workflow != "" {
		path = base + "/workflows/" + url.PathEscape(workflow) + "/runs"
	}
	query, err := actionRunQuery(opts)
	if err != nil {
		return nil, err
	}
	var runs ActionRunList
	if err := c.doContext(ctx, http.MethodGet, path+"?"+query.Encode(), nil, &runs); err != nil {
		return nil, err
	}
	return &runs, nil
}

func (c *Client) GetActionRun(ctx context.Context, owner, name string, runID int64) (*ActionRun, error) {
	path, err := actionRepoPath(owner, name)
	if err != nil {
		return nil, err
	}
	var run ActionRun
	if err := c.doContext(ctx, http.MethodGet, fmt.Sprintf("%s/runs/%d", path, runID), nil, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

// ListActionJobs reads one page of a run's jobs. per_page is capped at 100 so
// callers fetch exactly the page they render; GitHub reports the full
// total_count alongside every page.
func (c *Client) ListActionJobs(ctx context.Context, owner, name string, runID int64, page, perPage int) (*ActionJobList, error) {
	path, err := actionRepoPath(owner, name)
	if err != nil {
		return nil, err
	}
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > actionPageSize {
		perPage = actionPageSize
	}
	var jobs ActionJobList
	listPath := fmt.Sprintf("%s/runs/%d/jobs?per_page=%d&page=%d", path, runID, perPage, page)
	if err := c.doContext(ctx, http.MethodGet, listPath, nil, &jobs); err != nil {
		return nil, err
	}
	if jobs.Jobs == nil {
		jobs.Jobs = []ActionJob{}
	}
	return &jobs, nil
}

// GetActionJob reads one job by ID. Its run_id scopes the job to a run without
// listing every job in the run.
func (c *Client) GetActionJob(ctx context.Context, owner, name string, jobID int64) (*ActionJob, error) {
	path, err := actionRepoPath(owner, name)
	if err != nil {
		return nil, err
	}
	var job ActionJob
	if err := c.doContext(ctx, http.MethodGet, fmt.Sprintf("%s/jobs/%d", path, jobID), nil, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

// GetActionJobLog downloads a job log. GitHub answers with a 302 to a signed
// https URL that must be followed; expired logs return 404.
func (c *Client) GetActionJobLog(ctx context.Context, owner, name string, jobID int64) (*ActionLog, error) {
	path, err := actionRepoPath(owner, name)
	if err != nil {
		return nil, err
	}
	resp, err := c.requestWith(ctx, c.logClient, http.MethodGet, fmt.Sprintf("%s/jobs/%d/logs", path, jobID), "application/vnd.github+json", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	content, err := io.ReadAll(io.LimitReader(resp.Body, MaxActionLogBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrUnavailable
	}
	return &ActionLog{
		Content:   string(content[:min(len(content), MaxActionLogBytes)]),
		Truncated: len(content) > MaxActionLogBytes, MaxBytes: MaxActionLogBytes,
	}, nil
}

// CancelActionRun requests cancellation of a run. GitHub answers 202 when
// accepted and 409 when the run already finished.
func (c *Client) CancelActionRun(ctx context.Context, owner, name string, runID int64) error {
	path, err := actionRepoPath(owner, name)
	if err != nil {
		return err
	}
	return c.doContext(ctx, http.MethodPost, fmt.Sprintf("%s/runs/%d/cancel", path, runID), nil, nil)
}
