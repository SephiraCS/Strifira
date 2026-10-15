package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/service"
	gh "github.com/SephiraCS/strifira-backend/pkg/github"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// GitHubActionsToolsService shares the validated Actions surface with REST.
// ListLinkedRepos reads stored links without seeding rules or calling GitHub.
type GitHubActionsToolsService interface {
	ListLinkedRepos(context.Context, uuid.UUID) ([]dto.GitHubRepoResponse, error)
	ListActionRuns(context.Context, uuid.UUID, uuid.UUID, gh.ActionRunOptions) (*dto.ListResponse[dto.GitHubActionRunResponse], error)
	GetActionRun(context.Context, uuid.UUID, uuid.UUID, int64) (*dto.GitHubActionRunResponse, error)
	ListActionJobs(context.Context, uuid.UUID, uuid.UUID, int64, int, int) (*dto.ListResponse[dto.GitHubActionJobResponse], error)
	GetActionJob(context.Context, uuid.UUID, uuid.UUID, int64, int64) (*dto.GitHubActionJobResponse, error)
	GetActionJobLog(context.Context, uuid.UUID, uuid.UUID, int64, int64, int64) (*dto.GitHubActionLogResponse, error)
	CancelActionRun(context.Context, uuid.UUID, uuid.UUID, int64) error
}

type githubActionsRepoInput struct {
	Workspace string `json:"workspace" jsonschema:"workspace slug"`
	Repo      string `json:"repo" jsonschema:"active linked repository full name (owner/name) or Strifira repository UUID"`
}

type githubActionsPageInput struct {
	Page    *int `json:"page,omitempty" jsonschema:"page number, defaults to 1, maximum 10000"`
	PerPage *int `json:"per_page,omitempty" jsonschema:"items per page, maximum 100"`
}

type githubActionsRunInput struct {
	githubActionsRepoInput
	RunID int64 `json:"run_id" jsonschema:"positive native GitHub run ID, not the run number"`
}

type githubActionsJobInput struct {
	githubActionsRunInput
	JobID int64 `json:"job_id" jsonschema:"positive native GitHub job ID belonging to this run"`
}

func registerGitHubActionsTools(s *mcp.Server, deps Deps) {
	readAnnotations := &mcp.ToolAnnotations{ReadOnlyHint: true}
	mcp.AddTool(s, &mcp.Tool{
		Name: "github_list_action_runs", Title: "List GitHub Actions runs",
		Description: "List native GitHub Actions runs in an active linked repository. Requires a read-capable API key, current workspace membership, and issue:read. Defaults to 20 runs per page.",
		Annotations: readAnnotations,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		githubActionsRepoInput
		githubActionsPageInput
		Status     string `json:"status,omitempty"`
		Ref        string `json:"ref,omitempty"`
		Event      string `json:"event,omitempty"`
		HeadSHA    string `json:"head_sha,omitempty"`
		WorkflowID string `json:"workflow_id,omitempty"`
	}) (*mcp.CallToolResult, any, error) {
		page, perPage, err := githubActionsPage(in.githubActionsPageInput, 20)
		if err != nil {
			return nil, nil, err
		}
		if in.Status != "" && !slices.Contains(service.GitHubActionStatusFilters, in.Status) {
			return nil, nil, githubActionsError(service.ErrGitHubActionParams, false)
		}
		for _, value := range []string{in.Status, in.Ref, in.Event, in.HeadSHA, in.WorkflowID} {
			if len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
				return nil, nil, githubActionsError(service.ErrGitHubActionParams, false)
			}
		}
		workspaceID, repoID, err := githubActionsTarget(ctx, deps, in.githubActionsRepoInput, false)
		if err != nil {
			return nil, nil, err
		}
		result, err := deps.GitHubActionsSvc.ListActionRuns(ctx, workspaceID, repoID, gh.ActionRunOptions{
			Page: page, PerPage: perPage, Status: in.Status, Ref: in.Ref, Event: in.Event,
			HeadSHA: in.HeadSHA, WorkflowID: in.WorkflowID,
		})
		return nil, result, githubActionsError(err, false)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "github_get_action_run", Title: "Get GitHub Actions run",
		Description: "Read a native GitHub Actions run in an active linked repository. Requires read access and issue:read.", Annotations: readAnnotations,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in githubActionsRunInput) (*mcp.CallToolResult, any, error) {
		if in.RunID <= 0 {
			return nil, nil, githubActionsError(service.ErrGitHubActionParams, false)
		}
		workspaceID, repoID, err := githubActionsTarget(ctx, deps, in.githubActionsRepoInput, false)
		if err != nil {
			return nil, nil, err
		}
		result, err := deps.GitHubActionsSvc.GetActionRun(ctx, workspaceID, repoID, in.RunID)
		return nil, result, githubActionsError(err, false)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "github_list_action_jobs", Title: "List GitHub Actions jobs",
		Description: "List native jobs for a GitHub Actions run in an active linked repository. Requires read access and issue:read. Defaults to 100 jobs per page.", Annotations: readAnnotations,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		githubActionsRunInput
		githubActionsPageInput
	}) (*mcp.CallToolResult, any, error) {
		page, perPage, err := githubActionsPage(in.githubActionsPageInput, 100)
		if err != nil {
			return nil, nil, err
		}
		if in.RunID <= 0 {
			return nil, nil, githubActionsError(service.ErrGitHubActionParams, false)
		}
		workspaceID, repoID, err := githubActionsTarget(ctx, deps, in.githubActionsRepoInput, false)
		if err != nil {
			return nil, nil, err
		}
		result, err := deps.GitHubActionsSvc.ListActionJobs(ctx, workspaceID, repoID, in.RunID, page, perPage)
		return nil, result, githubActionsError(err, false)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "github_get_action_job", Title: "Get GitHub Actions job",
		Description: "Read native job metadata after verifying that the job belongs to the selected run. Requires read access and issue:read.", Annotations: readAnnotations,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in githubActionsJobInput) (*mcp.CallToolResult, any, error) {
		if in.RunID <= 0 || in.JobID <= 0 {
			return nil, nil, githubActionsError(service.ErrGitHubActionParams, false)
		}
		workspaceID, repoID, err := githubActionsTarget(ctx, deps, in.githubActionsRepoInput, false)
		if err != nil {
			return nil, nil, err
		}
		result, err := deps.GitHubActionsSvc.GetActionJob(ctx, workspaceID, repoID, in.RunID, in.JobID)
		return nil, result, githubActionsError(err, false)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "github_get_action_job_logs", Title: "Read GitHub Actions job logs",
		Description: "Read GitHub 16 job logs after verifying run/job membership. Omit attempt for the latest attempt, or specify a positive attempt. Logs are bounded to 2 MiB with truncation metadata. Requires read access and issue:read.", Annotations: readAnnotations,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		githubActionsJobInput
		Attempt *int64 `json:"attempt,omitempty" jsonschema:"positive attempt; omit for the latest attempt"`
	}) (*mcp.CallToolResult, any, error) {
		if in.RunID <= 0 || in.JobID <= 0 || (in.Attempt != nil && *in.Attempt <= 0) {
			return nil, nil, githubActionsError(service.ErrGitHubActionParams, true)
		}
		var attempt int64
		if in.Attempt != nil {
			attempt = *in.Attempt
		}
		workspaceID, repoID, err := githubActionsTarget(ctx, deps, in.githubActionsRepoInput, false)
		if err != nil {
			return nil, nil, err
		}
		result, err := deps.GitHubActionsSvc.GetActionJobLog(ctx, workspaceID, repoID, in.RunID, in.JobID, attempt)
		if err != nil {
			return nil, nil, githubActionsError(err, true)
		}
		if result != nil && len(result.Content) > gh.MaxActionLogBytes {
			bounded := *result
			bounded.Content = bounded.Content[:gh.MaxActionLogBytes]
			bounded.Truncated = true
			bounded.MaxBytes = gh.MaxActionLogBytes
			result = &bounded
		}
		return nil, result, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "github_cancel_action_run", Title: "Cancel GitHub Actions run",
		Description: "Request cancellation of an active native GitHub Actions run. Requires an API key with write access and the workspace:manage permission, currently available to workspace owners.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in githubActionsRunInput) (*mcp.CallToolResult, any, error) {
		if in.RunID <= 0 {
			return nil, nil, githubActionsError(service.ErrGitHubActionParams, false)
		}
		workspaceID, repoID, err := githubActionsTarget(ctx, deps, in.githubActionsRepoInput, true)
		if err != nil {
			return nil, nil, err
		}
		if err := deps.GitHubActionsSvc.CancelActionRun(ctx, workspaceID, repoID, in.RunID); err != nil {
			return nil, nil, githubActionsError(err, false)
		}
		return nil, map[string]string{"status": "accepted"}, nil
	})
}

func githubActionsPage(in githubActionsPageInput, defaultSize int) (int, int, error) {
	page, perPage := 1, defaultSize
	if in.Page != nil {
		page = *in.Page
	}
	if in.PerPage != nil {
		perPage = *in.PerPage
	}
	if page < 1 || page > 10000 || perPage < 1 || perPage > 100 {
		return 0, 0, githubActionsError(service.ErrGitHubActionParams, false)
	}
	return page, perPage, nil
}

func githubActionsTarget(ctx context.Context, deps Deps, in githubActionsRepoInput, write bool) (uuid.UUID, uuid.UUID, error) {
	id, err := authenticate(ctx)
	if err != nil {
		return uuid.Nil, uuid.Nil, errors.New("API key authentication required")
	}
	if write {
		if err := id.requireWriteScope(); err != nil {
			return uuid.Nil, uuid.Nil, errors.New("this API key is read-only; GitHub Actions cancellation requires write scope")
		}
	} else if !slices.Contains(id.scopes, domain.APIKeyScopeRead) {
		return uuid.Nil, uuid.Nil, errors.New("this API key lacks read access")
	}
	if strings.TrimSpace(in.Workspace) == "" || len(in.Workspace) > 100 || !utf8.ValidString(in.Workspace) || strings.ContainsFunc(in.Workspace, unicode.IsControl) {
		return uuid.Nil, uuid.Nil, errors.New("a valid workspace slug of at most 100 bytes is required")
	}
	var w *wsContext
	if write {
		_, w, err = writeGuard(ctx, deps, in.Workspace, domain.PermWorkspaceManage)
	} else {
		w, err = deps.resolveWorkspace(ctx, in.Workspace, id.userID)
		if err == nil {
			err = deps.requirePermission(ctx, w, id.userID, domain.PermIssueRead)
		}
	}
	if err != nil {
		if write {
			return uuid.Nil, uuid.Nil, errors.New("workspace membership and workspace:manage permission required")
		}
		return uuid.Nil, uuid.Nil, errors.New("workspace membership and issue:read permission required")
	}
	if deps.GitHubActionsSvc == nil {
		return uuid.Nil, uuid.Nil, errors.New("GitHub Actions is unavailable")
	}
	selector := strings.TrimSpace(in.Repo)
	if selector == "" || len(selector) > 512 || strings.ContainsAny(selector, "\x00\r\n") {
		return uuid.Nil, uuid.Nil, githubActionsError(service.ErrGitHubActionParams, false)
	}
	repos, err := deps.GitHubActionsSvc.ListLinkedRepos(ctx, w.workspace.ID)
	if err != nil {
		return uuid.Nil, uuid.Nil, githubActionsError(err, false)
	}
	selectedID, parseErr := uuid.Parse(selector)
	var matched uuid.UUID
	for _, repo := range repos {
		if !repo.IsActive {
			continue
		}
		repoID, err := uuid.Parse(repo.ID)
		if err != nil || repoID == uuid.Nil {
			continue
		}
		if (parseErr == nil && repoID == selectedID) || strings.EqualFold(repo.FullName, selector) {
			if matched != uuid.Nil && matched != repoID {
				return uuid.Nil, uuid.Nil, errors.New("repository name is ambiguous; use the linked repository UUID")
			}
			matched = repoID
		}
	}
	if matched == uuid.Nil {
		return uuid.Nil, uuid.Nil, githubActionsError(service.ErrGitHubActionRepoNotFound, false)
	}
	return w.workspace.ID, matched, nil
}

// Raw service errors can contain tokens, response bodies, or request URLs.
func githubActionsError(err error, logs bool) error {
	if err == nil {
		return nil
	}
	message := "GitHub could not complete the Actions request"
	switch {
	case errors.Is(err, service.ErrGitHubActionParams):
		message = "invalid GitHub Actions parameters"
	case errors.Is(err, service.ErrGitHubActionRepoNotFound):
		message = "repository is not actively linked in this workspace"
	case errors.Is(err, service.ErrGitHubActionJobNotFound):
		message = "job was not found in this run"
	case errors.Is(err, service.ErrGitHubActionConnection):
		message = "connect this workspace to the linked repository's GitHub instance"
	case errors.Is(err, service.ErrGitHubActionState):
		message = "the run's current state does not allow cancellation; refresh and try again"
	case errors.Is(err, context.DeadlineExceeded):
		message = "GitHub did not respond in time"
	case errors.Is(err, context.Canceled), errors.Is(err, gh.ErrUnavailable):
		message = "GitHub is unavailable; try again later"
	default:
		if upstream, ok := errors.AsType[*gh.APIError](err); ok {
			switch upstream.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				message = "the configured GitHub token cannot access Actions for this repository"
			case http.StatusNotFound:
				message = "run or job was not found, or the GitHub instance does not support this Actions API"
				if logs {
					message = "job logs are unavailable; the job logs API requires GitHub 16 or newer"
				}
			case http.StatusConflict:
				message = "GitHub rejected cancellation for the run's current state; refresh and try again"
			case http.StatusBadRequest, http.StatusUnprocessableEntity:
				message = "GitHub rejected the Actions request"
			case http.StatusTooManyRequests:
				message = "GitHub is rate limiting requests; try again later"
			}
		}
	}
	return errors.New(message)
}
