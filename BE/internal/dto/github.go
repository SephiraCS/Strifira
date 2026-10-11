package dto

import "time"

// --- Requests ---

type ConnectGitHubRequest struct {
	InstanceURL  string  `json:"instance_url" validate:"required,url"`
	Token        string  `json:"token" validate:"required,min=20"`
	Organization *string `json:"organization" validate:"omitempty,max=100"`
}

type LinkGitHubReposRequest struct {
	GitHubRepoIDs []int64 `json:"github_repo_ids" validate:"required,min=1"`
}

type UpdateAutoTransitionsRequest struct {
	Transitions []AutoTransitionRule `json:"transitions" validate:"required"`
}

type UpdateGitHubUserMappingsRequest struct {
	Mappings []GitHubUserMapping `json:"mappings"`
}

type GitHubUserMapping struct {
	GitHubLogin string `json:"github_login"`
	UserID      string `json:"user_id"`
}

type AutoTransitionRule struct {
	Event            string  `json:"event" validate:"required"`
	TargetStatus     string  `json:"target_status"`
	TargetStatusID   *string `json:"target_status_id"`
	TargetAssigneeID *string `json:"target_assignee_id"`
	AssignGitHubUser bool    `json:"assign_github_user"`
	IsActive         bool    `json:"is_active"`
}

// --- Responses ---

type GitHubStatusResponse struct {
	Configured   bool                   `json:"configured"`
	Connected    bool                   `json:"connected"`
	Disabled     bool                   `json:"disabled"`
	GlobalConfig bool                   `json:"global_config"`
	ManagedBy    string                 `json:"managed_by"`
	InstanceURL  string                 `json:"instance_url,omitempty"`
	Organization *string                `json:"organization,omitempty"`
	Account      *GitHubAccountResponse `json:"account,omitempty"`
	Repos        []GitHubRepoResponse   `json:"repos"`
}

type GitHubAccountResponse struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

type GitHubRepoResponse struct {
	ID            string `json:"id"`
	GitHubRepoID  int64  `json:"github_repo_id"`
	FullName      string `json:"full_name"`
	InstanceURL   string `json:"instance_url"`
	DefaultBranch string `json:"default_branch"`
	IsActive      bool   `json:"is_active"`
}

type GitHubAvailableRepoResponse struct {
	GitHubRepoID  int64  `json:"github_repo_id"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Linked        bool   `json:"linked"`
}

type GitHubPullRequestResponse struct {
	ID              string     `json:"id"`
	Number          int        `json:"number"`
	Title           string     `json:"title"`
	State           string     `json:"state"`
	ChecksGreen     bool       `json:"checks_green"`
	AuthorLogin     string     `json:"author_login"`
	AuthorAvatarURL string     `json:"author_avatar_url"`
	HTMLURL         string     `json:"html_url"`
	HeadBranch      string     `json:"head_branch"`
	HeadSHA         string     `json:"head_sha"`
	BaseBranch      string     `json:"base_branch"`
	Additions       int        `json:"additions"`
	Deletions       int        `json:"deletions"`
	RepoID          string     `json:"repo_id"`
	RepoFullName    string     `json:"repo_full_name"`
	MergedAt        *time.Time `json:"merged_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type GitHubBranchResponse struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	HTMLURL      string    `json:"html_url"`
	RepoID       string    `json:"repo_id"`
	RepoFullName string    `json:"repo_full_name"`
	CreatedAt    time.Time `json:"created_at"`
}

type GitHubCommitResponse struct {
	ID              string    `json:"id"`
	SHA             string    `json:"sha"`
	ShortSHA        string    `json:"short_sha"`
	Message         string    `json:"message"`
	AuthorLogin     string    `json:"author_login"`
	AuthorAvatarURL string    `json:"author_avatar_url"`
	HTMLURL         string    `json:"html_url"`
	RepoFullName    string    `json:"repo_full_name"`
	CommittedAt     time.Time `json:"committed_at"`
}

type GitHubIssueActivityResponse struct {
	PullRequests []GitHubPullRequestResponse `json:"pull_requests"`
	Branches     []GitHubBranchResponse      `json:"branches"`
	Commits      []GitHubCommitResponse      `json:"commits"`
}

type GitHubAutoTransitionResponse struct {
	Event            string  `json:"event"`
	TargetStatus     string  `json:"target_status"`
	TargetStatusID   *string `json:"target_status_id"`
	TargetAssigneeID *string `json:"target_assignee_id"`
	AssignGitHubUser bool    `json:"assign_github_user"`
	IsActive         bool    `json:"is_active"`
}
