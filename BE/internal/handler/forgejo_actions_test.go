package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/middleware"
	"github.com/ku/ku-backend/internal/service"
	fj "github.com/ku/ku-backend/pkg/forgejo"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

type forgejoActionsStub struct {
	calls       int
	operation   string
	workspaceID uuid.UUID
	repoID      uuid.UUID
	resourceID  int64
	runID       int64
	attempt     int64
	opts        fj.ActionRunOptions
	page        int
	perPage     int
	err         error
}

func (s *forgejoActionsStub) record(operation string, workspaceID, repoID uuid.UUID, resourceID int64) {
	s.calls++
	s.operation, s.workspaceID, s.repoID, s.resourceID = operation, workspaceID, repoID, resourceID
}

func (s *forgejoActionsStub) ListActionRuns(_ context.Context, ws, repo uuid.UUID, opts fj.ActionRunOptions) (*dto.ListResponse[dto.ForgejoActionRunResponse], error) {
	s.record("list runs", ws, repo, 0)
	s.opts = opts
	return &dto.ListResponse[dto.ForgejoActionRunResponse]{
		Data:       []dto.ForgejoActionRunResponse{{ID: 81, Title: "Test", Status: "running"}},
		TotalCount: 21, Page: opts.Page, PerPage: opts.PerPage, HasMore: true,
	}, s.err
}

func (s *forgejoActionsStub) GetActionRun(_ context.Context, ws, repo uuid.UUID, id int64) (*dto.ForgejoActionRunResponse, error) {
	s.record("get run", ws, repo, id)
	return &dto.ForgejoActionRunResponse{ID: id, Status: "failure"}, s.err
}

func (s *forgejoActionsStub) ListActionJobs(_ context.Context, ws, repo uuid.UUID, id int64, page, perPage int) (*dto.ListResponse[dto.ForgejoActionJobResponse], error) {
	s.record("list jobs", ws, repo, id)
	s.page, s.perPage = page, perPage
	return &dto.ListResponse[dto.ForgejoActionJobResponse]{
		Data:       []dto.ForgejoActionJobResponse{{ID: 91, RunID: id, Status: "running"}},
		TotalCount: 1, Page: page, PerPage: perPage,
	}, s.err
}

func (s *forgejoActionsStub) GetActionJob(_ context.Context, ws, repo uuid.UUID, runID, id int64) (*dto.ForgejoActionJobResponse, error) {
	s.record("get job", ws, repo, id)
	s.runID = runID
	return &dto.ForgejoActionJobResponse{ID: id, RunID: runID, Status: "running"}, s.err
}

func (s *forgejoActionsStub) GetActionJobLog(_ context.Context, ws, repo uuid.UUID, runID, id, attempt int64) (*dto.ForgejoActionLogResponse, error) {
	s.record("logs", ws, repo, id)
	s.runID, s.attempt = runID, attempt
	return &dto.ForgejoActionLogResponse{Content: "test output\n", Truncated: true, MaxBytes: fj.MaxActionLogBytes}, s.err
}

func (s *forgejoActionsStub) CancelActionRun(_ context.Context, ws, repo uuid.UUID, id int64) error {
	s.record("cancel", ws, repo, id)
	return s.err
}

// Read the real server registrations so RBAC tests cover the middleware
// configured for production, including missing permissions or omitted routes.
func forgejoActionsRouter(t *testing.T, stub *forgejoActionsStub, workspaceID uuid.UUID, role string) *echo.Echo {
	t.Helper()
	source, err := os.ReadFile("../../cmd/server/main.go")
	require.NoError(t, err)
	registrations := regexp.MustCompile(`ws\.(GET|POST)\("([^"]+)", forgejoActionsH\.(\w+), mw.RequirePermission\("([^"]+)"\)\)`).FindAllStringSubmatch(string(source), -1)
	require.Len(t, registrations, 6, "all Actions routes must have explicit permission middleware")
	h := NewForgejoActionsHandler(stub)
	handlers := map[string]echo.HandlerFunc{
		"ListRuns": h.ListRuns, "GetRun": h.GetRun, "ListJobs": h.ListJobs,
		"GetJob": h.GetJob, "GetJobLogs": h.GetJobLogs, "Cancel": h.Cancel,
	}
	e := echo.New()
	ws := e.Group("/api/workspaces/:slug", func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("workspace", &domain.Workspace{ID: workspaceID})
			c.Set("workspace_role", role)
			return next(c)
		}
	})
	for _, registration := range registrations {
		method, path, name, permission := registration[1], registration[2], registration[3], registration[4]
		if method == http.MethodGet {
			require.Equal(t, "issue:read", permission)
		} else {
			require.Equal(t, "workspace:manage", permission)
		}
		require.Contains(t, handlers, name)
		ws.Add(method, path, handlers[name], middleware.RequirePermission(permission))
	}
	return e
}

func requestForgejoActions(t *testing.T, stub *forgejoActionsStub, ws uuid.UUID, role, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	e := forgejoActionsRouter(t, stub, ws, role)
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(method, path, nil).WithContext(t.Context()))
	return recorder
}

func TestForgejoActionsRoutesScopeAndPermissions(t *testing.T) {
	workspaceID, repoID := uuid.New(), uuid.New()
	base := "/api/workspaces/test/forgejo/repos/" + repoID.String() + "/actions"
	for _, test := range []struct {
		method, path, operation string
		id                      int64
	}{
		{http.MethodGet, "/runs", "list runs", 0},
		{http.MethodGet, "/runs/81", "get run", 81},
		{http.MethodGet, "/runs/81/jobs", "list jobs", 81},
		{http.MethodGet, "/runs/81/jobs/91", "get job", 91},
		{http.MethodGet, "/runs/81/jobs/91/logs", "logs", 91},
		{http.MethodPost, "/runs/81/cancel", "cancel", 81},
	} {
		for _, role := range []string{domain.RoleOwner, domain.RoleAdmin, domain.RoleMember, domain.RoleGuest, "", "unknown"} {
			t.Run(test.operation+"/"+role, func(t *testing.T) {
				stub := &forgejoActionsStub{}
				recorder := requestForgejoActions(t, stub, workspaceID, role, test.method, base+test.path)
				allowed := role == domain.RoleOwner || (test.method == http.MethodGet && (role == domain.RoleAdmin || role == domain.RoleMember || role == domain.RoleGuest))
				if !allowed {
					require.Equal(t, http.StatusForbidden, recorder.Code)
					require.Zero(t, stub.calls, "forbidden request must not contact Forgejo")
					return
				}
				status := http.StatusOK
				if test.method == http.MethodPost {
					status = http.StatusAccepted
					require.JSONEq(t, `{"status":"accepted"}`, recorder.Body.String())
				}
				require.Equal(t, status, recorder.Code)
				require.Equal(t, 1, stub.calls)
				require.Equal(t, workspaceID, stub.workspaceID)
				require.Equal(t, repoID, stub.repoID)
				require.Equal(t, test.operation, stub.operation)
				require.Equal(t, test.id, stub.resourceID)
				if test.operation == "get job" || test.operation == "logs" {
					require.Equal(t, int64(81), stub.runID)
				}
			})
		}
	}
}

func TestForgejoActionsRejectInvalidInputsBeforeService(t *testing.T) {
	repoID := uuid.New().String()
	base := "/api/workspaces/test/forgejo/repos/" + repoID + "/actions"
	for _, test := range []struct{ method, path string }{
		{http.MethodGet, strings.Replace(base, repoID, "bad-id", 1) + "/runs"},
		{http.MethodGet, strings.Replace(base, repoID, uuid.Nil.String(), 1) + "/runs"},
		{http.MethodGet, base + "/runs/0"},
		{http.MethodGet, base + "/runs/-1/jobs"},
		{http.MethodGet, base + "/runs/0/jobs/91"},
		{http.MethodGet, base + "/runs/abc/jobs/91/logs"},
		{http.MethodGet, base + "/runs/81/jobs/abc"},
		{http.MethodGet, base + "/runs/81/jobs/9223372036854775808"},
		{http.MethodGet, base + "/runs/81/jobs/0/logs"},
		{http.MethodGet, base + "/runs/81/jobs/91/logs?attempt=-1"},
		{http.MethodGet, base + "/runs/81/jobs/91/logs?attempt=0"},
		{http.MethodGet, base + "/runs/81/jobs/91/logs?attempt=abc"},
		{http.MethodGet, base + "/runs/81/jobs/91/logs?attempt=1&attempt=2"},
		{http.MethodGet, base + "/runs?page=0"},
		{http.MethodGet, base + "/runs?page=10001"},
		{http.MethodGet, base + "/runs?page=abc"},
		{http.MethodGet, base + "/runs?page="},
		{http.MethodGet, base + "/runs?page=1&page=2"},
		{http.MethodGet, base + "/runs?per_page=101"},
		{http.MethodGet, base + "/runs?per_page=-1"},
		{http.MethodGet, base + "/runs?status=completed"},
		{http.MethodGet, base + "/runs?status=waiting&status=running"},
		{http.MethodGet, base + "/runs?ref=main%0A"},
		{http.MethodGet, base + "/runs?event=push%00"},
		{http.MethodGet, base + "/runs?workflow_id=" + strings.Repeat("a", 513)},
		{http.MethodGet, base + "/runs?run_number=0"},
		{http.MethodGet, base + "/runs?run_number=9223372036854775808"},
		{http.MethodGet, base + "/runs/81/jobs?per_page=0"},
		{http.MethodPost, base + "/runs/abc/cancel"},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			stub := &forgejoActionsStub{}
			recorder := requestForgejoActions(t, stub, uuid.New(), domain.RoleOwner, test.method, test.path)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Contains(t, recorder.Body.String(), `"code":"BAD_REQUEST"`)
			require.Zero(t, stub.calls)
		})
	}
}

func TestForgejoActionsFiltersPaginationAndLogs(t *testing.T) {
	base := "/api/workspaces/test/forgejo/repos/" + uuid.New().String() + "/actions"
	stub := &forgejoActionsStub{}
	recorder := requestForgejoActions(t, stub, uuid.New(), domain.RoleGuest, http.MethodGet, base+"/runs?page=2&per_page=10&status=queued&ref=feature%2Ftest&event=push&head_sha=abc&workflow_id=ci.yml&run_number=5")
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, fj.ActionRunOptions{Page: 2, PerPage: 10, Status: "queued", Ref: "feature/test", Event: "push", HeadSHA: "abc", WorkflowID: "ci.yml", RunNumber: 5}, stub.opts)
	var list dto.ListResponse[dto.ForgejoActionRunResponse]
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &list))
	require.Equal(t, 21, list.TotalCount)
	require.Equal(t, 2, list.Page)
	require.Equal(t, 10, list.PerPage)
	require.True(t, list.HasMore)
	require.Equal(t, int64(81), list.Data[0].ID)

	recorder = requestForgejoActions(t, stub, uuid.New(), domain.RoleGuest, http.MethodGet, base+"/runs")
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, 1, stub.opts.Page)
	require.Equal(t, 20, stub.opts.PerPage)

	recorder = requestForgejoActions(t, stub, uuid.New(), domain.RoleGuest, http.MethodGet, base+"/runs/81/jobs")
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, 1, stub.page)
	require.Equal(t, 100, stub.perPage)

	recorder = requestForgejoActions(t, stub, uuid.New(), domain.RoleGuest, http.MethodGet, base+"/runs/81/jobs?page=2&per_page=10")
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, 2, stub.page)
	require.Equal(t, 10, stub.perPage)

	recorder = requestForgejoActions(t, stub, uuid.New(), domain.RoleGuest, http.MethodGet, base+"/runs/81/jobs/91/logs?attempt=3")
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, int64(3), stub.attempt)
	var logs dto.ForgejoActionLogResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &logs))
	require.Equal(t, "test output\n", logs.Content)
	require.True(t, logs.Truncated)
	require.Equal(t, fj.MaxActionLogBytes, logs.MaxBytes)
}

func TestForgejoActionsErrorsAreClassifiedAndSanitized(t *testing.T) {
	base := "/api/workspaces/test/forgejo/repos/" + uuid.New().String() + "/actions"
	for _, test := range []struct {
		name, code string
		err        error
		status     int
	}{
		{"repository", "NOT_FOUND", service.ErrForgejoActionRepoNotFound, http.StatusNotFound},
		{"job outside run", "ACTION_NOT_FOUND", service.ErrForgejoActionJobNotFound, http.StatusNotFound},
		{"connection", "FORGEJO_CONNECTION_UNAVAILABLE", service.ErrForgejoActionConnection, http.StatusConflict},
		{"parameters", "BAD_REQUEST", service.ErrForgejoActionParams, http.StatusBadRequest},
		{"state", "ACTION_STATE_CONFLICT", service.ErrForgejoActionState, http.StatusConflict},
		{"deadline", "FORGEJO_TIMEOUT", context.DeadlineExceeded, http.StatusGatewayTimeout},
		{"cancelled", "FORGEJO_UNAVAILABLE", context.Canceled, http.StatusBadGateway},
		{"unavailable", "FORGEJO_UNAVAILABLE", fj.ErrUnavailable, http.StatusBadGateway},
		{"unauthorized", "FORGEJO_ACCESS_DENIED", &fj.APIError{StatusCode: 401}, http.StatusBadGateway},
		{"forbidden", "FORGEJO_ACCESS_DENIED", &fj.APIError{StatusCode: 403}, http.StatusBadGateway},
		{"not found", "ACTION_NOT_FOUND", &fj.APIError{StatusCode: 404}, http.StatusNotFound},
		{"conflict", "ACTION_STATE_CONFLICT", &fj.APIError{StatusCode: 409}, http.StatusConflict},
		{"rejected", "FORGEJO_ACTION_REJECTED", &fj.APIError{StatusCode: 400}, http.StatusBadRequest},
		{"unprocessable", "FORGEJO_ACTION_REJECTED", &fj.APIError{StatusCode: 422}, http.StatusBadRequest},
		{"rate limit", "FORGEJO_RATE_LIMITED", &fj.APIError{StatusCode: 429}, http.StatusServiceUnavailable},
		{"upstream", "FORGEJO_UNAVAILABLE", &fj.APIError{StatusCode: 500}, http.StatusBadGateway},
		{"store", "INTERNAL_ERROR", errors.New("private database error"), http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := &forgejoActionsStub{err: fmt.Errorf("secret token upstream body: %w", test.err)}
			recorder := requestForgejoActions(t, stub, uuid.New(), domain.RoleOwner, http.MethodGet, base+"/runs")
			require.Equal(t, test.status, recorder.Code)
			var result dto.ErrorResponse
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
			require.Equal(t, test.code, result.Error.Code)
			require.NotContains(t, recorder.Body.String(), "secret")
			require.NotContains(t, recorder.Body.String(), "private database")
		})
	}
	stub := &forgejoActionsStub{err: &fj.APIError{StatusCode: 404}}
	recorder := requestForgejoActions(t, stub, uuid.New(), domain.RoleGuest, http.MethodGet, base+"/runs/81/jobs/91/logs")
	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.Contains(t, recorder.Body.String(), "ACTION_LOGS_UNAVAILABLE")
	require.Contains(t, recorder.Body.String(), "Forgejo 16 or newer")
}

func TestForgejoActionsMissingWorkspaceIsForbidden(t *testing.T) {
	stub := &forgejoActionsStub{}
	e := echo.New()
	recorder := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/runs", nil).WithContext(t.Context()), recorder)
	require.NoError(t, NewForgejoActionsHandler(stub).ListRuns(c))
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Zero(t, stub.calls)
}

func TestForgejoActionsRemovedRoutesReturnNotFound(t *testing.T) {
	base := "/api/workspaces/test/forgejo/repos/" + uuid.New().String() + "/actions"
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/jobs/91"}, {http.MethodGet, "/jobs/91/logs"}, {http.MethodPost, "/runs/81/rerun"},
	} {
		stub := &forgejoActionsStub{}
		recorder := requestForgejoActions(t, stub, uuid.New(), domain.RoleOwner, tc.method, base+tc.path)
		require.Equal(t, http.StatusNotFound, recorder.Code)
		require.Zero(t, stub.calls)
	}
}

func TestForgejoActionsMembershipFailureOnJobAndLogs(t *testing.T) {
	base := "/api/workspaces/test/forgejo/repos/" + uuid.New().String() + "/actions/runs/81/jobs/91"
	for _, suffix := range []string{"", "/logs"} {
		stub := &forgejoActionsStub{err: service.ErrForgejoActionJobNotFound}
		recorder := requestForgejoActions(t, stub, uuid.New(), domain.RoleGuest, http.MethodGet, base+suffix)
		require.Equal(t, http.StatusNotFound, recorder.Code)
		require.Contains(t, recorder.Body.String(), "ACTION_NOT_FOUND")
		require.NotContains(t, recorder.Body.String(), "ACTION_LOGS_UNAVAILABLE")
	}
}
