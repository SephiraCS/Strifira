package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/service"
	fj "github.com/ku/ku-backend/pkg/forgejo"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ForgejoActionsToolsService shares the validated Actions surface with REST.
// ListLinkedRepos reads stored links without seeding rules or calling Forgejo.
type ForgejoActionsToolsService interface {
	ListLinkedRepos(context.Context, uuid.UUID) ([]dto.ForgejoRepoResponse, error)
	ListActionRuns(context.Context, uuid.UUID, uuid.UUID, fj.ActionRunOptions) (*dto.ListResponse[dto.ForgejoActionRunResponse], error)
	GetActionRun(context.Context, uuid.UUID, uuid.UUID, int64) (*dto.ForgejoActionRunResponse, error)
	ListActionJobs(context.Context, uuid.UUID, uuid.UUID, int64, int, int) (*dto.ListResponse[dto.ForgejoActionJobResponse], error)
	GetActionJob(context.Context, uuid.UUID, uuid.UUID, int64, int64) (*dto.ForgejoActionJobResponse, error)
	GetActionJobLog(context.Context, uuid.UUID, uuid.UUID, int64, int64, int64) (*dto.ForgejoActionLogResponse, error)
	CancelActionRun(context.Context, uuid.UUID, uuid.UUID, int64) error
}

type forgejoActionsRepoInput struct {
	Workspace string `json:"workspace" jsonschema:"workspace slug"`
	Repo      string `json:"repo" jsonschema:"active linked repository full name (owner/name) or Ku repository UUID"`
}

type forgejoActionsPageInput struct {
	Page    *int `json:"page,omitempty" jsonschema:"page number, defaults to 1, maximum 10000"`
	PerPage *int `json:"per_page,omitempty" jsonschema:"items per page, maximum 100"`
}

type forgejoActionsRunInput struct {
	forgejoActionsRepoInput
	RunID int64 `json:"run_id" jsonschema:"positive native Forgejo run ID, not the run number"`
}

type forgejoActionsJobInput struct {
	forgejoActionsRunInput
	JobID int64 `json:"job_id" jsonschema:"positive native Forgejo job ID belonging to this run"`
}

func registerForgejoActionsTools(s *mcp.Server, deps Deps) {
	readAnnotations := &mcp.ToolAnnotations{ReadOnlyHint: true}
	mcp.AddTool(s, &mcp.Tool{
		Name: "forgejo_list_action_runs", Title: "List Forgejo Actions runs",
		Description: "List native Forgejo Actions runs in an active linked repository. Requires a read-capable API key, current workspace membership, and issue:read. Defaults to 20 runs per page.",
		Annotations: readAnnotations,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		forgejoActionsRepoInput
		forgejoActionsPageInput
		Status     string `json:"status,omitempty"`
		Ref        string `json:"ref,omitempty"`
		Event      string `json:"event,omitempty"`
		HeadSHA    string `json:"head_sha,omitempty"`
		WorkflowID string `json:"workflow_id,omitempty"`
		RunNumber  *int64 `json:"run_number,omitempty" jsonschema:"positive run number filter"`
	}) (*mcp.CallToolResult, any, error) {
		page, perPage, err := forgejoActionsPage(in.forgejoActionsPageInput, 20)
		if err != nil {
			return nil, nil, err
		}
		if in.Status != "" && !slices.Contains([]string{"unknown", "waiting", "queued", "running", "success", "failure", "cancelled", "skipped", "blocked"}, in.Status) {
			return nil, nil, forgejoActionsError(service.ErrForgejoActionParams, false)
		}
		for _, value := range []string{in.Status, in.Ref, in.Event, in.HeadSHA, in.WorkflowID} {
			if len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
				return nil, nil, forgejoActionsError(service.ErrForgejoActionParams, false)
			}
		}
		var runNumber int64
		if in.RunNumber != nil {
			if *in.RunNumber <= 0 {
				return nil, nil, forgejoActionsError(service.ErrForgejoActionParams, false)
			}
			runNumber = *in.RunNumber
		}
		workspaceID, repoID, err := forgejoActionsTarget(ctx, deps, in.forgejoActionsRepoInput, false)
		if err != nil {
			return nil, nil, err
		}
		result, err := deps.ForgejoActionsSvc.ListActionRuns(ctx, workspaceID, repoID, fj.ActionRunOptions{
			Page: page, PerPage: perPage, Status: in.Status, Ref: in.Ref, Event: in.Event,
			HeadSHA: in.HeadSHA, WorkflowID: in.WorkflowID, RunNumber: runNumber,
		})
		return nil, result, forgejoActionsError(err, false)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "forgejo_get_action_run", Title: "Get Forgejo Actions run",
		Description: "Read a native Forgejo Actions run in an active linked repository. Requires read access and issue:read.", Annotations: readAnnotations,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in forgejoActionsRunInput) (*mcp.CallToolResult, any, error) {
		if in.RunID <= 0 {
			return nil, nil, forgejoActionsError(service.ErrForgejoActionParams, false)
		}
		workspaceID, repoID, err := forgejoActionsTarget(ctx, deps, in.forgejoActionsRepoInput, false)
		if err != nil {
			return nil, nil, err
		}
		result, err := deps.ForgejoActionsSvc.GetActionRun(ctx, workspaceID, repoID, in.RunID)
		return nil, result, forgejoActionsError(err, false)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "forgejo_list_action_jobs", Title: "List Forgejo Actions jobs",
		Description: "List native jobs for a Forgejo Actions run in an active linked repository. Requires read access and issue:read. Defaults to 100 jobs per page.", Annotations: readAnnotations,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		forgejoActionsRunInput
		forgejoActionsPageInput
	}) (*mcp.CallToolResult, any, error) {
		page, perPage, err := forgejoActionsPage(in.forgejoActionsPageInput, 100)
		if err != nil {
			return nil, nil, err
		}
		if in.RunID <= 0 {
			return nil, nil, forgejoActionsError(service.ErrForgejoActionParams, false)
		}
		workspaceID, repoID, err := forgejoActionsTarget(ctx, deps, in.forgejoActionsRepoInput, false)
		if err != nil {
			return nil, nil, err
		}
		result, err := deps.ForgejoActionsSvc.ListActionJobs(ctx, workspaceID, repoID, in.RunID, page, perPage)
		return nil, result, forgejoActionsError(err, false)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "forgejo_get_action_job", Title: "Get Forgejo Actions job",
		Description: "Read native job metadata after verifying that the job belongs to the selected run. Requires read access and issue:read.", Annotations: readAnnotations,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in forgejoActionsJobInput) (*mcp.CallToolResult, any, error) {
		if in.RunID <= 0 || in.JobID <= 0 {
			return nil, nil, forgejoActionsError(service.ErrForgejoActionParams, false)
		}
		workspaceID, repoID, err := forgejoActionsTarget(ctx, deps, in.forgejoActionsRepoInput, false)
		if err != nil {
			return nil, nil, err
		}
		result, err := deps.ForgejoActionsSvc.GetActionJob(ctx, workspaceID, repoID, in.RunID, in.JobID)
		return nil, result, forgejoActionsError(err, false)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "forgejo_get_action_job_logs", Title: "Read Forgejo Actions job logs",
		Description: "Read Forgejo 16 job logs after verifying run/job membership. Omit attempt for the latest attempt, or specify a positive attempt. Logs are bounded to 2 MiB with truncation metadata. Requires read access and issue:read.", Annotations: readAnnotations,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		forgejoActionsJobInput
		Attempt *int64 `json:"attempt,omitempty" jsonschema:"positive attempt; omit for the latest attempt"`
	}) (*mcp.CallToolResult, any, error) {
		if in.RunID <= 0 || in.JobID <= 0 || (in.Attempt != nil && *in.Attempt <= 0) {
			return nil, nil, forgejoActionsError(service.ErrForgejoActionParams, true)
		}
		var attempt int64
		if in.Attempt != nil {
			attempt = *in.Attempt
		}
		workspaceID, repoID, err := forgejoActionsTarget(ctx, deps, in.forgejoActionsRepoInput, false)
		if err != nil {
			return nil, nil, err
		}
		result, err := deps.ForgejoActionsSvc.GetActionJobLog(ctx, workspaceID, repoID, in.RunID, in.JobID, attempt)
		if err != nil {
			return nil, nil, forgejoActionsError(err, true)
		}
		if result != nil && len(result.Content) > fj.MaxActionLogBytes {
			bounded := *result
			bounded.Content = bounded.Content[:fj.MaxActionLogBytes]
			bounded.Truncated = true
			bounded.MaxBytes = fj.MaxActionLogBytes
			result = &bounded
		}
		return nil, result, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "forgejo_cancel_action_run", Title: "Cancel Forgejo Actions run",
		Description: "Request cancellation of an active native Forgejo Actions run. Requires an API key with write access and the workspace:manage permission, currently available to workspace owners.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in forgejoActionsRunInput) (*mcp.CallToolResult, any, error) {
		if in.RunID <= 0 {
			return nil, nil, forgejoActionsError(service.ErrForgejoActionParams, false)
		}
		workspaceID, repoID, err := forgejoActionsTarget(ctx, deps, in.forgejoActionsRepoInput, true)
		if err != nil {
			return nil, nil, err
		}
		if err := deps.ForgejoActionsSvc.CancelActionRun(ctx, workspaceID, repoID, in.RunID); err != nil {
			return nil, nil, forgejoActionsError(err, false)
		}
		return nil, map[string]string{"status": "accepted"}, nil
	})
}

func forgejoActionsPage(in forgejoActionsPageInput, defaultSize int) (int, int, error) {
	page, perPage := 1, defaultSize
	if in.Page != nil {
		page = *in.Page
	}
	if in.PerPage != nil {
		perPage = *in.PerPage
	}
	if page < 1 || page > 10000 || perPage < 1 || perPage > 100 {
		return 0, 0, forgejoActionsError(service.ErrForgejoActionParams, false)
	}
	return page, perPage, nil
}

func forgejoActionsTarget(ctx context.Context, deps Deps, in forgejoActionsRepoInput, write bool) (uuid.UUID, uuid.UUID, error) {
	id, err := authenticate(ctx)
	if err != nil {
		return uuid.Nil, uuid.Nil, errors.New("API key authentication required")
	}
	if write {
		if err := id.requireWriteScope(); err != nil {
			return uuid.Nil, uuid.Nil, errors.New("this API key is read-only; Forgejo Actions cancellation requires write scope")
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
	if deps.ForgejoActionsSvc == nil {
		return uuid.Nil, uuid.Nil, errors.New("Forgejo Actions is unavailable")
	}
	selector := strings.TrimSpace(in.Repo)
	if selector == "" || len(selector) > 512 || strings.ContainsAny(selector, "\x00\r\n") {
		return uuid.Nil, uuid.Nil, forgejoActionsError(service.ErrForgejoActionParams, false)
	}
	repos, err := deps.ForgejoActionsSvc.ListLinkedRepos(ctx, w.workspace.ID)
	if err != nil {
		return uuid.Nil, uuid.Nil, forgejoActionsError(err, false)
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
		return uuid.Nil, uuid.Nil, forgejoActionsError(service.ErrForgejoActionRepoNotFound, false)
	}
	return w.workspace.ID, matched, nil
}

// Raw service errors can contain tokens, response bodies, or request URLs.
func forgejoActionsError(err error, logs bool) error {
	if err == nil {
		return nil
	}
	message := "Forgejo could not complete the Actions request"
	switch {
	case errors.Is(err, service.ErrForgejoActionParams):
		message = "invalid Forgejo Actions parameters"
	case errors.Is(err, service.ErrForgejoActionRepoNotFound):
		message = "repository is not actively linked in this workspace"
	case errors.Is(err, service.ErrForgejoActionJobNotFound):
		message = "job was not found in this run"
	case errors.Is(err, service.ErrForgejoActionConnection):
		message = "connect this workspace to the linked repository's Forgejo instance"
	case errors.Is(err, service.ErrForgejoActionState):
		message = "the run's current state does not allow cancellation; refresh and try again"
	case errors.Is(err, context.DeadlineExceeded):
		message = "Forgejo did not respond in time"
	case errors.Is(err, context.Canceled), errors.Is(err, fj.ErrUnavailable):
		message = "Forgejo is unavailable; try again later"
	default:
		if upstream, ok := errors.AsType[*fj.APIError](err); ok {
			switch upstream.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				message = "the configured Forgejo token cannot access Actions for this repository"
			case http.StatusNotFound:
				message = "run or job was not found, or the Forgejo instance does not support this Actions API"
				if logs {
					message = "job logs are unavailable; the job logs API requires Forgejo 16 or newer"
				}
			case http.StatusConflict:
				message = "Forgejo rejected cancellation for the run's current state; refresh and try again"
			case http.StatusBadRequest, http.StatusUnprocessableEntity:
				message = "Forgejo rejected the Actions request"
			case http.StatusTooManyRequests:
				message = "Forgejo is rate limiting requests; try again later"
			}
		}
	}
	return errors.New(message)
}
