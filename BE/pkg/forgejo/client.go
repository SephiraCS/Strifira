package forgejo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

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
	ID   int64  `json:"id"`
	URL  string `json:"url"`
	Type string `json:"type"`
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
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, c.baseURL+"/api/v1"+path, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "token "+c.token)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode >= 400 {
		responseBody, _ := io.ReadAll(response.Body)
		return fmt.Errorf("Forgejo API error %d: %s", response.StatusCode, responseBody)
	}
	if result == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(result)
}

// GetCurrentUser validates the token and returns the authenticated user.
func (c *Client) GetCurrentUser() (*User, error) {
	var user User
	if err := c.do(http.MethodGet, "/user", nil, &user); err != nil {
		return nil, err
	}
	return &user, nil
}

// ListRepos lists every repository visible to the token (owned, org, collaborated).
func (c *Client) ListRepos() ([]Repository, error) {
	var allRepos []Repository
	page := 1
	for {
		var repos []Repository
		path := fmt.Sprintf("/user/repos?limit=50&page=%d", page)
		if err := c.do(http.MethodGet, path, nil, &repos); err != nil {
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
func (c *Client) CreateWebhook(owner, name, targetURL, secret string, events []string) (*Webhook, error) {
	if events == nil {
		events = []string{"push", "pull_request", "create", "status"}
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
	if err := c.do(http.MethodPost, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/hooks", payload, &hook); err != nil {
		return nil, err
	}
	return &hook, nil
}

// DeleteWebhook removes a webhook from a repository.
func (c *Client) DeleteWebhook(owner, name string, hookID int64) error {
	return c.do(http.MethodDelete, fmt.Sprintf("/repos/%s/%s/hooks/%d", url.PathEscape(owner), url.PathEscape(name), hookID), nil, nil)
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
func (c *Client) GetCombinedStatus(owner, name, ref string) (*CombinedStatus, error) {
	var status CombinedStatus
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/status", url.PathEscape(owner), url.PathEscape(name), url.PathEscape(ref))
	if err := c.do(http.MethodGet, path, nil, &status); err != nil {
		return nil, err
	}
	return &status, nil
}
