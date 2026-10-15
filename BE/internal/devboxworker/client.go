package devboxworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/dto"
)

type ProviderInfo struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type Heartbeat struct {
	Hostname  string         `json:"hostname"`
	OS        string         `json:"os"`
	Arch      string         `json:"arch"`
	Version   string         `json:"version"`
	Providers []ProviderInfo `json:"providers"`
}

type Job struct {
	ID                 string   `json:"id"`
	Provider           string   `json:"provider"`
	Prompt             string   `json:"prompt"`
	RepositoryURL      string   `json:"repository_url"`
	RepositoryFullName string   `json:"repository_full_name"`
	BaseBranch         string   `json:"base_branch"`
	WorkingBranch      string   `json:"working_branch"`
	TestCommand        []string `json:"test_command"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	MaxRuntimeSeconds  int      `json:"max_runtime_seconds"`
}

type Lease struct {
	Job                      Job       `json:"job"`
	Token                    string    `json:"lease_token"`
	ExpiresAt                time.Time `json:"lease_expires_at"`
	ServerTime               time.Time `json:"server_time"`
	HeartbeatIntervalSeconds int       `json:"heartbeat_interval_seconds"`
}

type LeaseState struct {
	Status          string    `json:"status"`
	CancelRequested bool      `json:"cancel_requested"`
	ExpiresAt       time.Time `json:"lease_expires_at"`
	DeadlineAt      time.Time `json:"deadline_at"`
	ServerTime      time.Time `json:"server_time"`
}

type Result struct {
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

type LogEntry struct {
	LeaseToken string `json:"lease_token"`
	Sequence   int64  `json:"sequence"`
	Stream     string `json:"stream"`
	Content    string `json:"content"`
}

type LogResponse struct {
	Accepted  bool `json:"accepted"`
	Truncated bool `json:"logs_truncated"`
}

// API separates transport from execution for deterministic lease and cancellation tests.
type API interface {
	Heartbeat(context.Context, Heartbeat) error
	Claim(context.Context) (*Lease, error)
	Start(context.Context, string, string) (LeaseState, error)
	Renew(context.Context, string, string) (LeaseState, error)
	Log(context.Context, string, LogEntry) (LogResponse, error)
	Complete(context.Context, string, string, Result) error
}

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("worker API returned HTTP %d", e.Status) }

// transportError distinguishes retryable network failures from request, TLS,
// protocol, and redirect errors, which another identical request cannot fix.
type transportError struct{ err error }

func (e *transportError) Error() string { return e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

func networkFailure(err error) bool {
	if value, ok := errors.AsType[*url.Error](err); ok {
		err = value.Err
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if _, ok := errors.AsType[*net.AddrError](err); ok {
		return false
	}
	if _, ok := errors.AsType[net.InvalidAddrError](err); ok {
		return false
	}
	if _, ok := errors.AsType[*net.InvalidAddrError](err); ok {
		return false
	}
	if value, ok := errors.AsType[*net.DNSError](err); ok {
		return !value.IsNotFound && (value.IsTimeout || value.IsTemporary)
	}
	_, network := errors.AsType[net.Error](err)
	return network || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func NewClient(config Config, token string) *Client {
	return &Client{baseURL: strings.TrimRight(config.ServerURL, "/") + "/api/dev-box-worker", token: token, http: &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("worker API redirects are prohibited") },
	}}
}

func (c *Client) call(ctx context.Context, path string, body, response any, maxBodyBytes int) (bool, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return false, fmt.Errorf("encode worker request: %w", err)
	}
	// A trusted tool can still accidentally echo a token; never publish it in logs/artifacts.
	encoded = bytes.ReplaceAll(encoded, []byte(c.token), []byte("[REDACTED]"))
	if maxBodyBytes > 0 && len(encoded) > maxBodyBytes {
		return false, fmt.Errorf("worker request exceeds the %d-byte encoded body limit", maxBodyBytes)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return false, fmt.Errorf("create worker request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		if resp == nil && networkFailure(err) {
			err = &transportError{err: err}
		}
		return false, fmt.Errorf("request worker API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, &HTTPError{Status: resp.StatusCode}
	}
	if response != nil {
		data, err := io.ReadAll(io.LimitReader(resp.Body, 2*maxOutputBytes+1))
		if err != nil || len(data) > 2*maxOutputBytes {
			return false, errors.New("worker API response exceeds limit or could not be read")
		}
		if err := json.Unmarshal(data, response); err != nil {
			return false, fmt.Errorf("decode worker response: %w", err)
		}
	}
	return true, nil
}

func (c *Client) Heartbeat(ctx context.Context, body Heartbeat) error {
	_, err := c.call(ctx, "/heartbeat", body, nil, 0)
	return err
}
func (c *Client) Claim(ctx context.Context) (*Lease, error) {
	var lease Lease
	ok, err := c.call(ctx, "/claim", struct{}{}, &lease, 0)
	if err != nil || !ok {
		return nil, err
	}
	return &lease, nil
}
func (c *Client) leaseCall(ctx context.Context, jobID, token, action string) (LeaseState, error) {
	var state LeaseState
	_, err := c.call(ctx, "/jobs/"+jobID+"/"+action, map[string]string{"lease_token": token}, &state, 0)
	return state, err
}
func (c *Client) Start(ctx context.Context, id, token string) (LeaseState, error) {
	return c.leaseCall(ctx, id, token, "start")
}
func (c *Client) Renew(ctx context.Context, id, token string) (LeaseState, error) {
	return c.leaseCall(ctx, id, token, "renew")
}
func (c *Client) Log(ctx context.Context, id string, body LogEntry) (LogResponse, error) {
	var result LogResponse
	_, err := c.call(ctx, "/jobs/"+id+"/logs", body, &result, 0)
	return result, err
}
func (c *Client) Complete(ctx context.Context, id, token string, result Result) error {
	_, err := c.call(ctx, "/jobs/"+id+"/complete", struct {
		LeaseToken string `json:"lease_token"`
		Result     Result `json:"result"`
	}{token, result}, nil, dto.DevBoxCompleteBodyLimit)
	return err
}
