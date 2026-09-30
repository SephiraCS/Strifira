package forgejo

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

// These models follow Forgejo's ActionRun/ActionRunJob schemas. In particular,
// run numbers are index_in_repo, refs are prettyref, and jobs have attempts.
type ActionRun struct {
	ID          int64      `json:"id"`
	Title       string     `json:"title"`
	WorkflowID  string     `json:"workflow_id"`
	Index       int64      `json:"index_in_repo"`
	PrettyRef   string     `json:"prettyref"`
	CommitSHA   string     `json:"commit_sha"`
	Event       string     `json:"event"`
	Status      string     `json:"status"`
	Started     *time.Time `json:"started"`
	Stopped     *time.Time `json:"stopped"`
	Created     time.Time  `json:"created"`
	Updated     time.Time  `json:"updated"`
	HTMLURL     string     `json:"html_url"`
	TriggerUser *User      `json:"trigger_user"`
}

type ActionRunList struct {
	Runs       []ActionRun `json:"workflow_runs"`
	TotalCount int64       `json:"total_count"`
}

type ActionJob struct {
	ID      int64  `json:"id"`
	RunID   int64  `json:"run_id"`
	Attempt int64  `json:"attempt"`
	Name    string `json:"name"`
	Status  string `json:"status"`
}

type ActionRunOptions struct {
	Page       int
	PerPage    int
	Status     string
	Ref        string
	Event      string
	HeadSHA    string
	WorkflowID string
	RunNumber  int64
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
			return "", errors.New("invalid Forgejo repository name")
		}
	}
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/actions", nil
}

func (c *Client) ListActionRuns(ctx context.Context, owner, name string, opts ActionRunOptions) (*ActionRunList, error) {
	path, err := actionRepoPath(owner, name)
	if err != nil {
		return nil, err
	}
	query := url.Values{"page": {strconv.Itoa(opts.Page)}, "limit": {strconv.Itoa(opts.PerPage)}}
	for key, value := range map[string]string{
		"status": opts.Status, "ref": opts.Ref, "event": opts.Event,
		"head_sha": opts.HeadSHA, "workflow_id": opts.WorkflowID,
	} {
		if value != "" {
			query.Set(key, value)
		}
	}
	if opts.RunNumber > 0 {
		query.Set("run_number", strconv.FormatInt(opts.RunNumber, 10))
	}
	var runs ActionRunList
	if err := c.doContext(ctx, http.MethodGet, path+"/runs?"+query.Encode(), nil, &runs); err != nil {
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

func (c *Client) ListActionJobs(ctx context.Context, owner, name string, runID int64) ([]ActionJob, error) {
	path, err := actionRepoPath(owner, name)
	if err != nil {
		return nil, err
	}
	var jobs []ActionJob
	if err := c.doContext(ctx, http.MethodGet, fmt.Sprintf("%s/runs/%d/jobs", path, runID), nil, &jobs); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (c *Client) GetActionJobLog(ctx context.Context, owner, name string, jobID, attempt int64) (*ActionLog, error) {
	path, err := actionRepoPath(owner, name)
	if err != nil {
		return nil, err
	}
	path = fmt.Sprintf("%s/jobs/%d/logs", path, jobID)
	if attempt > 0 {
		path += "?attempt=" + strconv.FormatInt(attempt, 10)
	}
	resp, err := c.request(ctx, http.MethodGet, path, "text/plain", nil)
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

func (c *Client) CancelActionRun(ctx context.Context, owner, name string, runID int64) error {
	path, err := actionRepoPath(owner, name)
	if err != nil {
		return err
	}
	return c.doContext(ctx, http.MethodPost, fmt.Sprintf("%s/runs/%d/cancel", path, runID), nil, nil)
}
