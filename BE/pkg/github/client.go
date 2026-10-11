package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to GitHub (github.com or a GitHub Enterprise Server instance)
// using a personal access token. One client maps to one instance + token pair.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
	logClient  *http.Client
}

const (
	// defaultInstance is used when an instance URL is empty.
	defaultInstance = "https://github.com"
	apiVersion      = "2022-11-28"
	userAgent       = "ku-backend"
)

// NewClient creates a GitHub API client. baseURL is the web origin
// (e.g. https://github.com or https://github.enterprise.local); the API base
// is derived automatically (https://api.github.com for github.com, otherwise
// {instance}/api/v3 for GitHub Enterprise Server).
func NewClient(baseURL, token string) *Client {
	instance := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if instance == "" {
		instance = defaultInstance
	}
	strict := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many GitHub redirects")
			}
			nextOrigin, err := NormalizeInstanceURL(req.URL.Scheme + "://" + req.URL.Host)
			originalOrigin, originalErr := NormalizeInstanceURL(via[0].URL.Scheme + "://" + via[0].URL.Host)
			if req.URL.User != nil || err != nil || originalErr != nil || nextOrigin != originalOrigin {
				return errors.New("GitHub redirect changed origin")
			}
			return nil
		},
	}
	// Job logs live behind a 302 to a signed objects.githubusercontent.com URL;
	// only that endpoint may follow redirects off the instance origin. A
	// redirect away from https (which would drop the token onto plain text or
	// carry credentials in the URL) is still refused.
	logFollowing := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many GitHub redirects")
			}
			if req.URL.User != nil {
				return errors.New("GitHub log redirect carried credentials")
			}
			if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
				return errors.New("GitHub log redirect left https")
			}
			return nil
		},
	}
	return &Client{
		baseURL: instance, token: token,
		httpClient: strict, logClient: logFollowing,
	}
}

// NormalizeInstanceURL accepts only an HTTP(S) origin. Tokens must never be
// attached to URLs with userinfo, paths, queries, or fragments. The bare
// shorthand "github.com" is accepted for the hosted instance.
func NormalizeInstanceURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, "github.com") || strings.EqualFold(value, "www.github.com") {
		value = defaultInstance
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", errors.New("invalid GitHub instance URL; use an HTTP(S) origin")
	}
	parsed.Host = strings.ToLower(parsed.Host)
	if parsed.Scheme == "https" && parsed.Port() == "443" {
		parsed.Host = strings.TrimSuffix(parsed.Host, ":443")
	} else if parsed.Scheme == "http" && parsed.Port() == "80" {
		parsed.Host = strings.TrimSuffix(parsed.Host, ":80")
	}
	parsed.Path = ""
	return parsed.String(), nil
}

// APIError contains only the status code, never an upstream response body.
// Upstream errors can echo tokens, internal addresses, or HTML.
type APIError struct {
	StatusCode int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("GitHub API returned HTTP %d", e.StatusCode)
}

var ErrUnavailable = errors.New("GitHub is unavailable")

const maxJSONBytes = 4 << 20

type User struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	FullName  string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

type Repository struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	HTMLURL       string `json:"html_url"`
}

type Webhook struct {
	ID     int64    `json:"id"`
	URL    string   `json:"url"`
	Events []string `json:"events"`
	Config struct {
		URL string `json:"url"`
	} `json:"config"`
}

type PullRequest struct {
	ID        int64      `json:"id"`
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	State     string     `json:"state"`
	Draft     bool       `json:"draft"`
	Merged    bool       `json:"merged"`
	HTMLURL   string     `json:"html_url"`
	Head      PRBranch   `json:"head"`
	Base      PRBranch   `json:"base"`
	User      User       `json:"user"`
	MergedAt  *time.Time `json:"merged_at"`
	ClosedAt  *time.Time `json:"closed_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type PRBranch struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

// CommitStatus is a single status context on a commit.
type CommitStatus struct {
	State       string `json:"state"`
	Context     string `json:"context"`
	Description string `json:"description"`
	TargetURL   string `json:"target_url"`
}

// CombinedStatus aggregates every status context on one commit. GitHub reports
// the aggregate as a top-level "state" (success/failure/pending).
type CombinedStatus struct {
	State      string         `json:"state"`
	SHA        string         `json:"sha"`
	TotalCount int            `json:"total_count"`
	Statuses   []CommitStatus `json:"statuses"`
}

// CheckRun is one check (GitHub Actions or any check-run reporter) on a commit.
type CheckRun struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

// CheckRuns lists every check-run reported for one commit.
type CheckRuns struct {
	TotalCount int        `json:"total_count"`
	CheckRuns  []CheckRun `json:"check_runs"`
}

func (c *Client) do(method, path string, body any, result any) error {
	return c.doContext(context.Background(), method, path, body, result)
}

func (c *Client) request(ctx context.Context, method, path, accept string, body any) (*http.Response, error) {
	return c.requestWith(ctx, c.httpClient, method, path, accept, body)
}

// apiEndpoint derives the REST API root from the instance origin:
// https://api.github.com for the hosted instance, {instance}/api/v3 for
// GitHub Enterprise Server.
func (c *Client) apiEndpoint() (string, error) {
	origin, err := NormalizeInstanceURL(c.baseURL)
	if err != nil {
		return "", err
	}
	if origin == defaultInstance {
		return "https://api.github.com", nil
	}
	return origin + "/api/v3", nil
}

func (c *Client) requestWith(ctx context.Context, client *http.Client, method, path, accept string, body any) (*http.Response, error) {
	apiBase, err := c.apiEndpoint()
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, apiBase+path, reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", accept)
	request.Header.Set("X-GitHub-Api-Version", apiVersion)
	request.Header.Set("User-Agent", userAgent)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrUnavailable
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		return nil, &APIError{StatusCode: response.StatusCode}
	}
	return response, nil
}

func (c *Client) doContext(ctx context.Context, method, path string, body any, result any) error {
	_, err := c.doContextHeaders(ctx, method, path, body, result)
	return err
}

func (c *Client) doContextHeaders(ctx context.Context, method, path string, body any, result any) (http.Header, error) {
	response, err := c.request(ctx, method, path, "application/vnd.github+json", body)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if result == nil {
		return response.Header, nil
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, maxJSONBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrUnavailable
	}
	if len(encoded) > maxJSONBytes {
		return nil, errors.New("GitHub response exceeds the size limit")
	}
	if err := json.Unmarshal(encoded, result); err != nil {
		return nil, errors.New("GitHub returned an invalid response")
	}
	return response.Header, nil
}

// Discovery pagination caps: 20 pages of 100 items each.
const (
	repoPageSize   = 100
	maxRepoPages   = 20
	actionPageSize = 100
	maxActionPages = 20
)

// GetCurrentUser validates the token and returns the authenticated user.
func (c *Client) GetCurrentUser(ctx context.Context) (*User, error) {
	var user User
	if err := c.doContext(ctx, http.MethodGet, "/user", nil, &user); err != nil {
		return nil, err
	}
	return &user, nil
}

// ListRepos lists every repository visible to the token (owned, org, collaborated).
func (c *Client) ListRepos(ctx context.Context) ([]Repository, error) {
	var allRepos []Repository
	for page := 1; page <= maxRepoPages; page++ {
		var repos []Repository
		path := fmt.Sprintf("/user/repos?visibility=all&affiliation=owner,collaborator,organization_member&sort=full_name&per_page=%d&page=%d", repoPageSize, page)
		if err := c.doContext(ctx, http.MethodGet, path, nil, &repos); err != nil {
			return nil, err
		}
		allRepos = append(allRepos, repos...)
		if len(repos) < repoPageSize {
			return allRepos, nil
		}
	}
	return nil, errors.New("GitHub repository count exceeds the discovery limit")
}

// GetRepo fetches one repository by owner/name.
func (c *Client) GetRepo(owner, name string) (*Repository, error) {
	var repo Repository
	if err := c.do(http.MethodGet, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), nil, &repo); err != nil {
		return nil, err
	}
	return &repo, nil
}

// webhookEvents is the subscription ku creates on every linked repository.
var webhookEvents = []string{"push", "pull_request", "create", "status", "check_run", "workflow_run"}

// WebhookEvents returns the event subscription ku configures on linked
// repositories; webhook reconciliation compares it against live hooks.
func WebhookEvents() []string {
	return append([]string(nil), webhookEvents...)
}

// CreateWebhook registers a webhook on a repository. The secret signs
// deliveries with HMAC-SHA256 (X-Hub-Signature-256).
func (c *Client) CreateWebhook(ctx context.Context, owner, name, targetURL, secret string, events []string) (*Webhook, error) {
	if events == nil {
		events = webhookEvents
	}
	payload := map[string]any{
		"name":   "web",
		"active": true,
		"events": events,
		"config": map[string]any{
			"url":          targetURL,
			"content_type": "json",
			"secret":       secret,
			"insecure_ssl": "0",
		},
	}
	var hook Webhook
	if err := c.doContext(ctx, http.MethodPost, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/hooks", payload, &hook); err != nil {
		return nil, err
	}
	return &hook, nil
}

func (c *Client) GetWebhook(ctx context.Context, owner, name string, hookID int64) (*Webhook, error) {
	path := fmt.Sprintf("/repos/%s/%s/hooks/%d", url.PathEscape(owner), url.PathEscape(name), hookID)
	var hook Webhook
	if err := c.doContext(ctx, http.MethodGet, path, nil, &hook); err != nil {
		return nil, err
	}
	return &hook, nil
}

// DeleteWebhook removes a webhook from a repository.
func (c *Client) DeleteWebhook(ctx context.Context, owner, name string, hookID int64) error {
	return c.doContext(ctx, http.MethodDelete, fmt.Sprintf("/repos/%s/%s/hooks/%d", url.PathEscape(owner), url.PathEscape(name), hookID), nil, nil)
}

// CreatePullRequest opens a pull request.
func (c *Client) CreatePullRequest(owner, name, title, head, base, body string) (*PullRequest, error) {
	payload := map[string]any{
		"title": title,
		"head":  head,
		"base":  base,
		"body":  body,
	}
	var pr PullRequest
	if err := c.do(http.MethodPost, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/pulls", payload, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// GetCombinedStatus returns the aggregated commit status for a commit ref.
func (c *Client) GetCombinedStatus(ctx context.Context, owner, name, ref string) (*CombinedStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/status", url.PathEscape(owner), url.PathEscape(name), url.PathEscape(ref))
	var status CombinedStatus
	if _, err := c.doContextHeaders(ctx, http.MethodGet, path, nil, &status); err != nil {
		return nil, err
	}
	if status.Statuses == nil {
		status.Statuses = []CommitStatus{}
	}
	return &status, nil
}

// GetCheckRunsForRef returns every check-run reported for a commit ref.
func (c *Client) GetCheckRunsForRef(ctx context.Context, owner, name, ref string) (*CheckRuns, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs", url.PathEscape(owner), url.PathEscape(name), url.PathEscape(ref))
	var checks CheckRuns
	if _, err := c.doContextHeaders(ctx, http.MethodGet, path, nil, &checks); err != nil {
		return nil, err
	}
	if checks.CheckRuns == nil {
		checks.CheckRuns = []CheckRun{}
	}
	return &checks, nil
}
