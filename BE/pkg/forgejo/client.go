package forgejo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client talks to a Forgejo (Gitea-compatible v1) instance using a personal
// access token. One client maps to one instance + token pair.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewClient creates a Forgejo API client. baseURL is the instance origin
// (e.g. https://git.terrascale.tech) without a trailing slash or /api suffix.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return errors.New("too many Forgejo redirects")
				}
				nextOrigin, err := NormalizeInstanceURL(req.URL.Scheme + "://" + req.URL.Host)
				originalOrigin, originalErr := NormalizeInstanceURL(via[0].URL.Scheme + "://" + via[0].URL.Host)
				if req.URL.User != nil || err != nil || originalErr != nil || nextOrigin != originalOrigin {
					return errors.New("Forgejo redirect changed origin")
				}
				return nil
			},
		},
	}
}

// NormalizeInstanceURL accepts only an HTTP(S) origin. Tokens must never be
// attached to URLs with userinfo, paths, queries, or fragments.
func NormalizeInstanceURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", errors.New("invalid Forgejo instance URL; use an HTTP(S) origin")
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
	return fmt.Sprintf("Forgejo API returned HTTP %d", e.StatusCode)
}

var ErrUnavailable = errors.New("Forgejo is unavailable")

const maxJSONBytes = 4 << 20

type User struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	FullName  string `json:"full_name"`
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
	Type   string   `json:"type"`
	Events []string `json:"events"`
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

// CombinedStatus aggregates every status context on one commit.
type CombinedStatus struct {
	State      string         `json:"state"`
	SHA        string         `json:"sha"`
	TotalCount int            `json:"total_count"`
	Statuses   []CommitStatus `json:"statuses"`
}

// AllChecksPassed reports whether the combined status represents completed,
// successful checks (at least one status, all success).
func (c *CombinedStatus) AllChecksPassed() bool {
	return c.TotalCount > 0 && c.State == "success"
}

func (c *Client) do(method, path string, body any, result any) error {
	return c.doContext(context.Background(), method, path, body, result)
}

func (c *Client) request(ctx context.Context, method, path, accept string, body any) (*http.Response, error) {
	baseURL, err := NormalizeInstanceURL(c.baseURL)
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
	request, err := http.NewRequestWithContext(ctx, method, baseURL+"/api/v1"+path, reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "token "+c.token)
	request.Header.Set("Accept", accept)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := c.httpClient.Do(request)
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
	response, err := c.request(ctx, method, path, "application/json", body)
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
		return nil, errors.New("Forgejo response exceeds the size limit")
	}
	if err := json.Unmarshal(encoded, result); err != nil {
		return nil, errors.New("Forgejo returned an invalid response")
	}
	return response.Header, nil
}

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
	page := 1
	for {
		var repos []Repository
		path := fmt.Sprintf("/user/repos?limit=50&page=%d", page)
		if err := c.doContext(ctx, http.MethodGet, path, nil, &repos); err != nil {
			return nil, err
		}
		allRepos = append(allRepos, repos...)
		if len(repos) < 50 {
			return allRepos, nil
		}
		page++
	}
}

// GetRepo fetches one repository by owner/name.
func (c *Client) GetRepo(owner, name string) (*Repository, error) {
	var repo Repository
	if err := c.do(http.MethodGet, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), nil, &repo); err != nil {
		return nil, err
	}
	return &repo, nil
}

// CreateWebhook registers a webhook on a repository. The secret signs
// deliveries with HMAC-SHA256 (X-Forgejo-Signature).
func (c *Client) CreateWebhook(ctx context.Context, owner, name, targetURL, secret string, events []string) (*Webhook, error) {
	if events == nil {
		events = []string{"push", "pull_request", "create", "action_run_success", "action_run_failure", "action_run_recover"}
	}
	payload := map[string]any{
		"type":   "forgejo",
		"active": true,
		"events": events,
		"config": map[string]any{
			"url":          targetURL,
			"content_type": "json",
			"secret":       secret,
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
	// Forgejo computes the combined state for one page only. Its JSON count
	// is also page-local; the true number of contexts is X-Total-Count.
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result := &CombinedStatus{SHA: ref, State: "pending", Statuses: []CommitStatus{}}
	seen := make(map[string]bool)
	for page := 1; page <= 100; page++ {
		var status CombinedStatus
		path := fmt.Sprintf("/repos/%s/%s/commits/%s/status?limit=100&page=%d", url.PathEscape(owner), url.PathEscape(name), url.PathEscape(ref), page)
		headers, err := c.doContextHeaders(ctx, http.MethodGet, path, nil, &status)
		if err != nil {
			return nil, err
		}
		for _, check := range status.Statuses {
			if !seen[check.Context] {
				seen[check.Context] = true
				result.Statuses = append(result.Statuses, check)
			}
		}
		total := len(result.Statuses)
		if count := headers.Get("X-Total-Count"); count != "" {
			total, err = strconv.Atoi(count)
			if err != nil || total < 0 {
				return nil, errors.New("Forgejo returned an invalid status count")
			}
			if len(result.Statuses) < total && len(status.Statuses) == 0 {
				return nil, errors.New("Forgejo returned incomplete commit statuses")
			}
		} else if len(status.Statuses) == 100 {
			total++ // No header: continue until an incomplete page.
		}
		if len(result.Statuses) >= total {
			result.TotalCount = len(result.Statuses)
			passed, pending := false, false
			for _, check := range result.Statuses {
				if check.State == "failure" || check.State == "error" {
					result.State = "failure"
					return result, nil
				}
				switch check.State {
				case "success":
					passed = true
				case "skipped":
					// Forgejo treats a conditionally skipped context as neutral.
				default:
					pending = true
				}
			}
			if passed && !pending {
				result.State = "success"
			}
			return result, nil
		}
	}
	return nil, errors.New("Forgejo commit status count exceeds the limit")
}
