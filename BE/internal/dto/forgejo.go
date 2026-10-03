package dto

import "time"

// --- Requests ---

type ConnectForgejoRequest struct {
	InstanceURL string `json:"instance_url" validate:"required,url"`
	Token       string `json:"token" validate:"required,min=20"`
}

type LinkForgejoReposRequest struct {
	ForgejoRepoIDs []int64 `json:"forgejo_repo_ids" validate:"required,min=1"`
}

type UpdateAutoTransitionsRequest struct {
	Transitions []AutoTransitionRule `json:"transitions" validate:"required"`
}

type UpdateForgejoUserMappingsRequest struct {
	Mappings []ForgejoUserMapping `json:"mappings"`
}

type ForgejoUserMapping struct {
	ForgejoLogin string `json:"forgejo_login"`
	UserID       string `json:"user_id"`
}

type AutoTransitionRule struct {
	Event             string  `json:"event" validate:"required"`
	TargetStatus      string  `json:"target_status"`
	TargetStatusID    *string `json:"target_status_id"`
	TargetAssigneeID  *string `json:"target_assignee_id"`
	AssignForgejoUser bool    `json:"assign_forgejo_user"`
	IsActive          bool    `json:"is_active"`
}

// --- Responses ---

type ForgejoStatusResponse struct {
	Configured   bool                    `json:"configured"`
	Connected    bool                    `json:"connected"`
	GlobalConfig bool                    `json:"global_config"`
	InstanceURL  string                  `json:"instance_url,omitempty"`
	Account      *ForgejoAccountResponse `json:"account,omitempty"`
	Repos        []ForgejoRepoResponse   `json:"repos"`
}

type ForgejoAccountResponse struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

type ForgejoRepoResponse struct {
	ID            string `json:"id"`
	ForgejoRepoID int64  `json:"forgejo_repo_id"`
	FullName      string `json:"full_name"`
	InstanceURL   string `json:"instance_url"`
	DefaultBranch string `json:"default_branch"`
	IsActive      bool   `json:"is_active"`
}

type ForgejoAvailableRepoResponse struct {
	ForgejoRepoID int64  `json:"forgejo_repo_id"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Linked        bool   `json:"linked"`
}

type ForgejoPullRequestResponse struct {
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

type ForgejoBranchResponse struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	HTMLURL      string    `json:"html_url"`
	RepoID       string    `json:"repo_id"`
	RepoFullName string    `json:"repo_full_name"`
	CreatedAt    time.Time `json:"created_at"`
}

type ForgejoCommitResponse struct {
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

type ForgejoIssueActivityResponse struct {
	PullRequests []ForgejoPullRequestResponse `json:"pull_requests"`
	Branches     []ForgejoBranchResponse      `json:"branches"`
	Commits      []ForgejoCommitResponse      `json:"commits"`
}

type ForgejoAutoTransitionResponse struct {
	Event             string  `json:"event"`
	TargetStatus      string  `json:"target_status"`
	TargetStatusID    *string `json:"target_status_id"`
	TargetAssigneeID  *string `json:"target_assignee_id"`
	AssignForgejoUser bool    `json:"assign_forgejo_user"`
	IsActive          bool    `json:"is_active"`
}
