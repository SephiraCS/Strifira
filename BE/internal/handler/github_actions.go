package handler

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/middleware"
	"github.com/SephiraCS/strifira-backend/internal/service"
	gh "github.com/SephiraCS/strifira-backend/pkg/github"
	"github.com/SephiraCS/strifira-backend/pkg/response"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

type githubActionsService interface {
	ListActionRuns(context.Context, uuid.UUID, uuid.UUID, gh.ActionRunOptions) (*dto.ListResponse[dto.GitHubActionRunResponse], error)
	GetActionRun(context.Context, uuid.UUID, uuid.UUID, int64) (*dto.GitHubActionRunResponse, error)
	ListActionJobs(context.Context, uuid.UUID, uuid.UUID, int64, int, int) (*dto.ListResponse[dto.GitHubActionJobResponse], error)
	GetActionJob(context.Context, uuid.UUID, uuid.UUID, int64, int64) (*dto.GitHubActionJobResponse, error)
	GetActionJobLog(context.Context, uuid.UUID, uuid.UUID, int64, int64, int64) (*dto.GitHubActionLogResponse, error)
	CancelActionRun(context.Context, uuid.UUID, uuid.UUID, int64) error
}

type GitHubActionsHandler struct {
	svc githubActionsService
}

func NewGitHubActionsHandler(svc githubActionsService) *GitHubActionsHandler {
	return &GitHubActionsHandler{svc: svc}
}

// actionScope passes the authenticated workspace's ID to every service call;
// repoId is the UUID of a Strifira link, never a caller-selected upstream repository.
func actionScope(c echo.Context) (uuid.UUID, uuid.UUID, error) {
	ws := middleware.GetWorkspace(c)
	if ws == nil || ws.ID == uuid.Nil {
		return uuid.Nil, uuid.Nil, echo.ErrForbidden
	}
	repoID, err := uuid.Parse(c.Param("repoId"))
	if err != nil || repoID == uuid.Nil {
		return uuid.Nil, uuid.Nil, service.ErrGitHubActionParams
	}
	return ws.ID, repoID, nil
}

func actionID(c echo.Context, param string) (int64, error) {
	id, err := strconv.ParseInt(c.Param(param), 10, 64)
	if err != nil || id <= 0 {
		return 0, service.ErrGitHubActionParams
	}
	return id, nil
}

func actionQueryInt(query url.Values, name string, defaultValue, maximum int64) (int64, error) {
	values, present := query[name]
	if !present {
		return defaultValue, nil
	}
	if len(values) != 1 {
		return 0, service.ErrGitHubActionParams
	}
	value, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil || value < 1 || value > maximum {
		return 0, service.ErrGitHubActionParams
	}
	return value, nil
}

func actionPagination(query url.Values, defaultSize int64) (int, int, error) {
	page, err := actionQueryInt(query, "page", 1, 10000)
	if err != nil {
		return 0, 0, err
	}
	perPage, err := actionQueryInt(query, "per_page", defaultSize, 100)
	return int(page), int(perPage), err
}

func actionRunOptions(query url.Values) (gh.ActionRunOptions, error) {
	page, perPage, err := actionPagination(query, 20)
	if err != nil {
		return gh.ActionRunOptions{}, err
	}
	for _, name := range []string{"status", "ref", "event", "head_sha", "workflow_id"} {
		values := query[name]
		if len(values) > 1 || len(query.Get(name)) > 512 || strings.ContainsAny(query.Get(name), "\x00\r\n") {
			return gh.ActionRunOptions{}, service.ErrGitHubActionParams
		}
	}
	status := query.Get("status")
	if status != "" && !slices.Contains(service.GitHubActionStatusFilters, status) {
		return gh.ActionRunOptions{}, service.ErrGitHubActionParams
	}
	return gh.ActionRunOptions{
		Page: page, PerPage: perPage, Status: status, Ref: query.Get("ref"), Event: query.Get("event"),
		HeadSHA: query.Get("head_sha"), WorkflowID: query.Get("workflow_id"),
	}, nil
}

func (h *GitHubActionsHandler) ListRuns(c echo.Context) error {
	workspaceID, repoID, err := actionScope(c)
	if err != nil {
		return githubActionError(c, err, false)
	}
	opts, err := actionRunOptions(c.QueryParams())
	if err != nil {
		return githubActionError(c, err, false)
	}
	result, err := h.svc.ListActionRuns(c.Request().Context(), workspaceID, repoID, opts)
	if err != nil {
		return githubActionError(c, err, false)
	}
	return response.Success(c, http.StatusOK, result)
}

func (h *GitHubActionsHandler) GetRun(c echo.Context) error {
	workspaceID, repoID, err := actionScope(c)
	if err != nil {
		return githubActionError(c, err, false)
	}
	runID, err := actionID(c, "runId")
	if err != nil {
		return githubActionError(c, err, false)
	}
	result, err := h.svc.GetActionRun(c.Request().Context(), workspaceID, repoID, runID)
	if err != nil {
		return githubActionError(c, err, false)
	}
	return response.Success(c, http.StatusOK, result)
}

func (h *GitHubActionsHandler) ListJobs(c echo.Context) error {
	workspaceID, repoID, err := actionScope(c)
	if err != nil {
		return githubActionError(c, err, false)
	}
	runID, err := actionID(c, "runId")
	if err != nil {
		return githubActionError(c, err, false)
	}
	page, perPage, err := actionPagination(c.QueryParams(), 100)
	if err != nil {
		return githubActionError(c, err, false)
	}
	result, err := h.svc.ListActionJobs(c.Request().Context(), workspaceID, repoID, runID, page, perPage)
	if err != nil {
		return githubActionError(c, err, false)
	}
	return response.Success(c, http.StatusOK, result)
}

func (h *GitHubActionsHandler) GetJob(c echo.Context) error {
	workspaceID, repoID, err := actionScope(c)
	if err != nil {
		return githubActionError(c, err, false)
	}
	runID, err := actionID(c, "runId")
	if err != nil {
		return githubActionError(c, err, false)
	}
	jobID, err := actionID(c, "jobId")
	if err != nil {
		return githubActionError(c, err, false)
	}
	result, err := h.svc.GetActionJob(c.Request().Context(), workspaceID, repoID, runID, jobID)
	if err != nil {
		return githubActionError(c, err, false)
	}
	return response.Success(c, http.StatusOK, result)
}

func (h *GitHubActionsHandler) GetJobLogs(c echo.Context) error {
	workspaceID, repoID, err := actionScope(c)
	if err != nil {
		return githubActionError(c, err, true)
	}
	runID, err := actionID(c, "runId")
	if err != nil {
		return githubActionError(c, err, true)
	}
	jobID, err := actionID(c, "jobId")
	if err != nil {
		return githubActionError(c, err, true)
	}
	attempt, err := actionQueryInt(c.QueryParams(), "attempt", 0, 1<<63-1)
	if err != nil {
		return githubActionError(c, err, true)
	}
	result, err := h.svc.GetActionJobLog(c.Request().Context(), workspaceID, repoID, runID, jobID, attempt)
	if err != nil {
		return githubActionError(c, err, true)
	}
	return response.Success(c, http.StatusOK, result)
}

func (h *GitHubActionsHandler) Cancel(c echo.Context) error {
	workspaceID, repoID, err := actionScope(c)
	if err != nil {
		return githubActionError(c, err, false)
	}
	runID, err := actionID(c, "runId")
	if err != nil {
		return githubActionError(c, err, false)
	}
	err = h.svc.CancelActionRun(c.Request().Context(), workspaceID, repoID, runID)
	if err != nil {
		return githubActionError(c, err, false)
	}
	return response.Success(c, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// Only fixed messages leave this boundary. Store errors, upstream bodies, and
// request URLs may contain credentials or private infrastructure details.
func githubActionError(c echo.Context, err error, logs bool) error {
	switch {
	case errors.Is(err, echo.ErrForbidden):
		return response.Forbidden(c)
	case errors.Is(err, service.ErrGitHubActionParams):
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid GitHub Actions parameters")
	case errors.Is(err, service.ErrGitHubActionRepoNotFound):
		return response.Error(c, http.StatusNotFound, "NOT_FOUND", "Repository is not linked in this workspace")
	case errors.Is(err, service.ErrGitHubActionJobNotFound):
		return response.Error(c, http.StatusNotFound, "ACTION_NOT_FOUND", "Job was not found in this run")
	case errors.Is(err, service.ErrGitHubActionConnection):
		return response.Error(c, http.StatusConflict, "GITHUB_CONNECTION_UNAVAILABLE", "Connect this workspace to the linked repository's GitHub instance")
	case errors.Is(err, service.ErrGitHubActionState):
		return response.Error(c, http.StatusConflict, "ACTION_STATE_CONFLICT", "The run's current state does not allow this action; refresh and try again")
	case errors.Is(err, context.DeadlineExceeded):
		return response.Error(c, http.StatusGatewayTimeout, "GITHUB_TIMEOUT", "GitHub did not respond in time")
	case errors.Is(err, context.Canceled), errors.Is(err, gh.ErrUnavailable):
		return response.Error(c, http.StatusBadGateway, "GITHUB_UNAVAILABLE", "GitHub is unavailable; try again later")
	case errors.Is(err, gh.ErrRateLimited):
		return response.Error(c, http.StatusServiceUnavailable, "GITHUB_RATE_LIMITED", "GitHub API rate limit is exhausted; strifira backs off and retries automatically; try again later")
	}
	if upstream, ok := errors.AsType[*gh.APIError](err); ok {
		switch upstream.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return response.Error(c, http.StatusBadGateway, "GITHUB_ACCESS_DENIED", "The configured GitHub token cannot access Actions for this repository")
		case http.StatusNotFound:
			if logs {
				return response.Error(c, http.StatusNotFound, "ACTION_LOGS_UNAVAILABLE", "Job logs are unavailable; GitHub expires the logs of completed runs after a retention period")
			}
			return response.Error(c, http.StatusNotFound, "ACTION_NOT_FOUND", "Run or job was not found, or this GitHub instance does not support the requested Actions API")
		case http.StatusConflict:
			return response.Error(c, http.StatusConflict, "ACTION_STATE_CONFLICT", "GitHub rejected this action for the run's current state; refresh and try again")
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			return response.Error(c, http.StatusBadRequest, "GITHUB_ACTION_REJECTED", "GitHub rejected the Actions request")
		case http.StatusTooManyRequests:
			return response.Error(c, http.StatusServiceUnavailable, "GITHUB_RATE_LIMITED", "GitHub is rate limiting requests; try again later")
		default:
			return response.Error(c, http.StatusBadGateway, "GITHUB_UNAVAILABLE", "GitHub could not complete the Actions request")
		}
	}
	return response.InternalErrorErr(c, err)
}
