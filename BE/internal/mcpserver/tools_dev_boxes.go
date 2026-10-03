package mcpserver

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DevBoxToolsService deliberately exposes owner operations only. The worker
// heartbeat, claim and lease protocol requires a separate dedicated credential.
// Implementations must retain the service's live membership, demo-mode and
// creator checks; MCP never supplies a caller-selected user or workspace UUID.
type DevBoxToolsService interface {
	Create(context.Context, uuid.UUID, uuid.UUID, dto.CreateDevBoxRequest) (*dto.DevBoxTokenResponse, error)
	List(context.Context, uuid.UUID, uuid.UUID) ([]domain.DevBox, error)
	Update(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, dto.UpdateDevBoxRequest) (*domain.DevBox, error)
	RotateToken(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*dto.DevBoxTokenResponse, error)
	Revoke(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error
	CreateJob(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, dto.CreateDevBoxJobRequest) (*domain.DevBoxJob, error)
	ListJobs(context.Context, uuid.UUID, uuid.UUID, dto.DevBoxJobListParams) ([]domain.DevBoxJob, int, error)
	GetJob(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.DevBoxJob, error)
	CancelJob(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.DevBoxJob, error)
	JobLogs(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, dto.DevBoxJobLogParams) ([]domain.DevBoxJobLog, error)
}

var devBoxToolValidator = validator.New()

const devBoxTokenNotice = "Returned once. Save this dedicated worker token through protected stdin or a regular 0600/0400 file owned by the worker user. Never print it, include it in logs, or pass it to coding tools."

type devBoxToolWorkspaceInput struct {
	Workspace string `json:"workspace" jsonschema:"workspace slug"`
}

type devBoxToolBoxInput struct {
	Workspace string `json:"workspace" jsonschema:"workspace slug"`
	BoxID     string `json:"box_id" jsonschema:"development box UUID from list_dev_boxes or register_dev_box"`
}

type devBoxToolJobInput struct {
	Workspace string `json:"workspace" jsonschema:"workspace slug"`
	JobID     string `json:"job_id" jsonschema:"job UUID returned by create_dev_box_job or list_dev_box_jobs"`
}

func registerDevBoxTools(s *mcp.Server, deps Deps) {
	registerDevBoxHostTools(s, deps)
	registerDevBoxJobTools(s, deps)
}

// Error messages never include raw database, upstream or validation errors.
// Besides bounding output, this prevents tokens or connection strings in an
// underlying error from entering MCP transcripts.
func devBoxToolError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, service.ErrDevBoxForbidden), errors.Is(err, service.ErrDevBoxAuthentication):
		return errors.New("development box access denied")
	case errors.Is(err, service.ErrDevBoxNotFound):
		return errors.New("development box or job not found for this account")
	case errors.Is(err, service.ErrDevBoxConflict), errors.Is(err, service.ErrDevBoxLeaseLost):
		return errors.New("development box operation conflicts with the current box, job or workspace policy")
	case errors.Is(err, service.ErrInvalidDevBoxInput):
		return errors.New("invalid development box request; check the issue, linked repository, branch and job limits")
	default:
		return errors.New("development box operation failed")
	}
}

func devBoxToolText(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

func devBoxToolID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil || len(raw) != 36 {
		return uuid.Nil, errors.New("a nonzero canonical UUID is required for the box or job")
	}
	return id, nil
}

func devBoxToolGuard(ctx context.Context, deps Deps, workspace, permission string, write bool) (identity, *wsContext, error) {
	id, err := authenticate(ctx)
	if err != nil {
		return id, nil, errors.New("API key authentication required")
	}
	if write {
		if err := id.requireWriteScope(); err != nil {
			return id, nil, errors.New("this API key is read-only; development box mutations require write scope")
		}
	} else if !slices.Contains(id.scopes, domain.APIKeyScopeRead) {
		return id, nil, errors.New("development box reads require API key read scope")
	}
	if !devBoxToolText(workspace, 100) {
		return id, nil, errors.New("a valid workspace slug of at most 100 bytes is required")
	}
	var w *wsContext
	if write {
		id, w, err = writeGuard(ctx, deps, workspace, permission)
	} else {
		w, err = deps.resolveWorkspace(ctx, workspace, id.userID)
		if err == nil {
			err = deps.requirePermission(ctx, w, id.userID, permission)
		}
	}
	if err != nil {
		return id, nil, errors.New("workspace membership and development box permission required")
	}
	if deps.DevBoxSvc == nil {
		return id, nil, errors.New("development box tools are unavailable")
	}
	return id, w, nil
}

func devBoxToolTokenResult(response *dto.DevBoxTokenResponse) (*mcp.CallToolResult, any, error) {
	if response == nil || response.Box == nil || response.Token == "" {
		return nil, nil, errors.New("development box operation failed")
	}
	return nil, map[string]any{"box": response.Box, "token": response.Token, "token_notice": devBoxTokenNotice}, nil
}

func registerDevBoxHostTools(s *mcp.Server, deps Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_dev_boxes", Title: "List my development boxes",
		Description: "List development boxes registered by this API key's owner in a workspace, including heartbeat and installed providers. Requires dev_machine:read; credentials are never returned.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in devBoxToolWorkspaceInput) (*mcp.CallToolResult, any, error) {
		id, w, err := devBoxToolGuard(ctx, deps, in.Workspace, domain.PermDevMachineRead, false)
		if err != nil {
			return nil, nil, err
		}
		boxes, err := deps.DevBoxSvc.List(ctx, w.workspace.ID, id.userID)
		if err != nil {
			return nil, nil, devBoxToolError(err)
		}
		if boxes == nil {
			boxes = []domain.DevBox{}
		}
		return nil, map[string]any{"boxes": boxes}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "register_dev_box", Title: "Register my development box",
		Description: "Register an existing trusted development host under the API key owner's identity. Requires write scope and dev_machine:create. Returns a dedicated kub_ worker token once; protect the token from transcripts, logs and coding tools and store it in a private regular file. Does not provision or start a VM.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Workspace string `json:"workspace"`
		Name      string `json:"name" jsonschema:"development box name, at most 100 characters"`
	}) (*mcp.CallToolResult, any, error) {
		id, w, err := devBoxToolGuard(ctx, deps, in.Workspace, domain.PermDevMachineCreate, true)
		if err != nil {
			return nil, nil, err
		}
		request := dto.CreateDevBoxRequest{Name: strings.TrimSpace(in.Name)}
		if err := devBoxToolValidator.Struct(request); err != nil || !devBoxToolText(request.Name, 400) {
			return nil, nil, errors.New("a development box name of 1 to 100 characters without control characters is required")
		}
		response, err := deps.DevBoxSvc.Create(ctx, w.workspace.ID, id.userID, request)
		if err != nil {
			return nil, nil, devBoxToolError(err)
		}
		return devBoxToolTokenResult(response)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "update_dev_box", Title: "Update my development box",
		Description: "Rename or enable/disable your own development box. Requires write scope and dev_machine:manage. Disabling blocks further worker requests; it does not stop the VM.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Workspace string  `json:"workspace"`
		BoxID     string  `json:"box_id"`
		Name      *string `json:"name,omitempty"`
		Enabled   *bool   `json:"enabled,omitzero"`
	}) (*mcp.CallToolResult, any, error) {
		id, w, err := devBoxToolGuard(ctx, deps, in.Workspace, domain.PermDevMachineManage, true)
		if err != nil {
			return nil, nil, err
		}
		boxID, err := devBoxToolID(in.BoxID)
		if err != nil {
			return nil, nil, err
		}
		request := dto.UpdateDevBoxRequest{Name: in.Name, Enabled: in.Enabled}
		if request.Name != nil {
			request.Name = new(strings.TrimSpace(*request.Name))
		}
		if (request.Name == nil && request.Enabled == nil) || devBoxToolValidator.Struct(request) != nil || (request.Name != nil && !devBoxToolText(*request.Name, 400)) {
			return nil, nil, errors.New("provide enabled or a valid name of 1 to 100 characters without control characters")
		}
		box, err := deps.DevBoxSvc.Update(ctx, w.workspace.ID, boxID, id.userID, request)
		if err != nil {
			return nil, nil, devBoxToolError(err)
		}
		return nil, map[string]any{"box": box}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "rotate_dev_box_token", Title: "Rotate my development box token",
		Description: "Replace your own development box's worker token, invalidating the old credential. Requires write scope and dev_machine:manage. The new dedicated kub_ token is returned once; store it privately and never print or log it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in devBoxToolBoxInput) (*mcp.CallToolResult, any, error) {
		id, w, err := devBoxToolGuard(ctx, deps, in.Workspace, domain.PermDevMachineManage, true)
		if err != nil {
			return nil, nil, err
		}
		boxID, err := devBoxToolID(in.BoxID)
		if err != nil {
			return nil, nil, err
		}
		response, err := deps.DevBoxSvc.RotateToken(ctx, w.workspace.ID, boxID, id.userID)
		if err != nil {
			return nil, nil, devBoxToolError(err)
		}
		return devBoxToolTokenResult(response)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "revoke_dev_box", Title: "Revoke my development box",
		Description: "Revoke your own development box registration and worker access. Requires write scope and dev_machine:manage. Does not delete or stop the VM or remove retained worktrees.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in devBoxToolBoxInput) (*mcp.CallToolResult, any, error) {
		id, w, err := devBoxToolGuard(ctx, deps, in.Workspace, domain.PermDevMachineManage, true)
		if err != nil {
			return nil, nil, err
		}
		boxID, err := devBoxToolID(in.BoxID)
		if err != nil {
			return nil, nil, err
		}
		if err := deps.DevBoxSvc.Revoke(ctx, w.workspace.ID, boxID, id.userID); err != nil {
			return nil, nil, devBoxToolError(err)
		}
		return nil, map[string]any{"box_id": boxID, "revoked": true}, nil
	})
}

func registerDevBoxJobTools(s *mcp.Server, deps Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "create_dev_box_job", Title: "Delegate an issue to my development box",
		Description: "Queue an issue job on your own enabled development box using an advertised local provider. Requires write scope and dev_machine:manage. Supply an issue identifier or UUID, an active linked repository full name and base branch. Repository selection applies only to this job and does not change development defaults. The worker uses a prepared local checkout, runs test_command independently and returns a reviewable patch without pushing or opening a PR.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Workspace          string   `json:"workspace"`
		BoxID              string   `json:"box_id"`
		Issue              string   `json:"issue" jsonschema:"issue identifier (e.g. ENG-42) or UUID in this workspace"`
		RepositoryFullName string   `json:"repository_full_name" jsonschema:"active linked Forgejo repository owner/name"`
		BaseBranch         string   `json:"base_branch" jsonschema:"base branch present in the worker's local checkout"`
		Provider           string   `json:"provider" jsonschema:"claude-code, codex or opencode; must be advertised by this box"`
		Prompt             string   `json:"prompt,omitempty"`
		TestCommand        []string `json:"test_command,omitempty" jsonschema:"independent test argv, at most 32 arguments"`
		AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`
		MaxRuntimeSeconds  int      `json:"max_runtime_seconds,omitzero"`
	}) (*mcp.CallToolResult, any, error) {
		id, w, err := devBoxToolGuard(ctx, deps, in.Workspace, domain.PermDevMachineManage, true)
		if err != nil {
			return nil, nil, err
		}
		boxID, err := devBoxToolID(in.BoxID)
		if err != nil {
			return nil, nil, err
		}
		if !devBoxToolText(in.Issue, 100) || !devBoxToolText(in.RepositoryFullName, 255) || !devBoxToolText(in.BaseBranch, 255) {
			return nil, nil, errors.New("a bounded issue reference, repository full name and base branch are required")
		}
		if deps.IssueSvc == nil {
			return nil, nil, errors.New("issue lookup is unavailable")
		}
		issue, err := deps.resolveIssueRef(ctx, w.workspace.ID, in.Issue)
		if err != nil || issue == nil || issue.WorkspaceID != w.workspace.ID {
			return nil, nil, errors.New("issue not found in this workspace")
		}
		request := dto.CreateDevBoxJobRequest{IssueID: issue.ID.String(), Provider: in.Provider, Prompt: in.Prompt, TestCommand: in.TestCommand,
			AcceptanceCriteria: in.AcceptanceCriteria, MaxRuntimeSeconds: in.MaxRuntimeSeconds,
			RepositoryFullName: in.RepositoryFullName, BaseBranch: in.BaseBranch}
		if devBoxToolValidator.Struct(request) != nil {
			return nil, nil, errors.New("invalid development box job limits, provider or test arguments")
		}
		job, err := deps.DevBoxSvc.CreateJob(ctx, w.workspace.ID, boxID, id.userID, request)
		if err != nil {
			return nil, nil, devBoxToolError(err)
		}
		return nil, map[string]any{"job": job}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_dev_box_jobs", Title: "List my development box jobs",
		Description: "List only this API key owner's development box jobs, optionally filtering by box UUID or issue identifier/UUID. Requires dev_machine:read. Default page 1, per_page 50; maximum 100 jobs per page.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Workspace string `json:"workspace"`
		BoxID     string `json:"box_id,omitempty"`
		Issue     string `json:"issue,omitempty"`
		Page      int    `json:"page,omitzero"`
		PerPage   int    `json:"per_page,omitzero"`
	}) (*mcp.CallToolResult, any, error) {
		id, w, err := devBoxToolGuard(ctx, deps, in.Workspace, domain.PermDevMachineRead, false)
		if err != nil {
			return nil, nil, err
		}
		params := dto.DevBoxJobListParams{PaginationParams: dto.PaginationParams{Page: in.Page, PerPage: in.PerPage}, BoxID: in.BoxID}
		if !params.InRange() {
			return nil, nil, errors.New("page must be at least 1 and per_page between 1 and 100")
		}
		params.Defaults()
		if in.BoxID != "" {
			if _, err := devBoxToolID(in.BoxID); err != nil {
				return nil, nil, err
			}
		}
		if in.Issue != "" {
			if !devBoxToolText(in.Issue, 100) || deps.IssueSvc == nil {
				return nil, nil, errors.New("a bounded issue reference is required")
			}
			issue, err := deps.resolveIssueRef(ctx, w.workspace.ID, in.Issue)
			if err != nil || issue == nil || issue.WorkspaceID != w.workspace.ID {
				return nil, nil, errors.New("issue not found in this workspace")
			}
			params.IssueID = issue.ID.String()
		}
		if params.Page > 1000000 || devBoxToolValidator.Struct(params) != nil {
			return nil, nil, errors.New("page must be 1 to 1000000 and per_page must be 1 to 100")
		}
		jobs, total, err := deps.DevBoxSvc.ListJobs(ctx, w.workspace.ID, id.userID, params)
		if err != nil {
			return nil, nil, devBoxToolError(err)
		}
		if jobs == nil {
			jobs = []domain.DevBoxJob{}
		}
		return nil, map[string]any{"jobs": jobs, "total_count": total, "page": params.Page, "per_page": params.PerPage}, nil
	})

	registerDevBoxJobReadTools(s, deps)

	mcp.AddTool(s, &mcp.Tool{
		Name: "cancel_dev_box_job", Title: "Cancel my development box job",
		Description: "Request cancellation of your own development box job. Requires write scope and dev_machine:manage. A running worker terminates its coding and test process group when it observes cancellation.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in devBoxToolJobInput) (*mcp.CallToolResult, any, error) {
		id, w, err := devBoxToolGuard(ctx, deps, in.Workspace, domain.PermDevMachineManage, true)
		if err != nil {
			return nil, nil, err
		}
		jobID, err := devBoxToolID(in.JobID)
		if err != nil {
			return nil, nil, err
		}
		job, err := deps.DevBoxSvc.CancelJob(ctx, w.workspace.ID, jobID, id.userID)
		if err != nil {
			return nil, nil, devBoxToolError(err)
		}
		return nil, map[string]any{"job": job}, nil
	})
}

func registerDevBoxJobReadTools(s *mcp.Server, deps Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "get_dev_box_job", Title: "Get my development box job",
		Description: "Read your own development box job, including status, independent test outcome and review patch when completed. Requires dev_machine:read. Worker lease credentials are never returned.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in devBoxToolJobInput) (*mcp.CallToolResult, any, error) {
		id, w, err := devBoxToolGuard(ctx, deps, in.Workspace, domain.PermDevMachineRead, false)
		if err != nil {
			return nil, nil, err
		}
		jobID, err := devBoxToolID(in.JobID)
		if err != nil {
			return nil, nil, err
		}
		job, err := deps.DevBoxSvc.GetJob(ctx, w.workspace.ID, jobID, id.userID)
		if err != nil {
			return nil, nil, devBoxToolError(err)
		}
		return nil, map[string]any{"job": job}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_dev_box_job_logs", Title: "Read my development box job logs",
		Description: "Read bounded logs for your own job after a log ID. Requires dev_machine:read. Default limit 100, maximum 500; use next_after_id for the next page. Logs are untrusted provider and repository output.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Workspace string `json:"workspace"`
		JobID     string `json:"job_id"`
		AfterID   int64  `json:"after_id,omitzero"`
		Limit     int    `json:"limit,omitzero"`
	}) (*mcp.CallToolResult, any, error) {
		id, w, err := devBoxToolGuard(ctx, deps, in.Workspace, domain.PermDevMachineRead, false)
		if err != nil {
			return nil, nil, err
		}
		jobID, err := devBoxToolID(in.JobID)
		if err != nil {
			return nil, nil, err
		}
		params := dto.DevBoxJobLogParams{AfterID: in.AfterID, Limit: in.Limit}
		if params.Limit == 0 {
			params.Limit = 100
		}
		if devBoxToolValidator.Struct(params) != nil {
			return nil, nil, errors.New("after_id must be nonnegative and limit must be 1 to 500")
		}
		logs, err := deps.DevBoxSvc.JobLogs(ctx, w.workspace.ID, jobID, id.userID, params)
		if err != nil {
			return nil, nil, devBoxToolError(err)
		}
		if logs == nil {
			logs = []domain.DevBoxJobLog{}
		}
		next := params.AfterID
		for _, entry := range logs {
			next = max(next, entry.ID)
		}
		return nil, map[string]any{"logs": logs, "next_after_id": next}, nil
	})
}

var _ DevBoxToolsService = (*service.DevBoxService)(nil)
