package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/middleware"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/ku/ku-backend/internal/service"
	fj "github.com/ku/ku-backend/pkg/forgejo"
	"github.com/labstack/echo/v4"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type actionsWorkspaceStub struct {
	repository.WorkspaceRepo
	workspace *domain.Workspace
	member    *domain.WorkspaceMember
}

func (s *actionsWorkspaceStub) GetBySlug(ctx context.Context, slug string) (*domain.Workspace, error) {
	if slug == s.workspace.Slug {
		return s.workspace, nil
	}
	return nil, nil
}

func (s *actionsWorkspaceStub) GetMember(ctx context.Context, workspaceID, userID uuid.UUID) (*domain.WorkspaceMember, error) {
	if s.member != nil && workspaceID == s.member.WorkspaceID && userID == s.member.UserID {
		return s.member, nil
	}
	return nil, nil
}

type actionsServiceStub struct {
	workspaceID uuid.UUID
	repoID      uuid.UUID
	repos       []dto.ForgejoRepoResponse
	run         dto.ForgejoActionRunResponse
	job         dto.ForgejoActionJobResponse
	logs        dto.ForgejoActionLogResponse
	forcedErr   error
	lookupErr   error
	lookups     int
	reads       int
	cancels     int
	options     fj.ActionRunOptions
	page        int
	perPage     int
	attempt     int64
}

var _ ForgejoActionsToolsService = (*actionsServiceStub)(nil)

func (s *actionsServiceStub) ListLinkedRepos(ctx context.Context, workspaceID uuid.UUID) ([]dto.ForgejoRepoResponse, error) {
	s.lookups++
	if workspaceID != s.workspaceID {
		return nil, errors.New("unexpected workspace")
	}
	return s.repos, s.lookupErr
}

func (s *actionsServiceStub) scope(workspaceID, repoID uuid.UUID) error {
	s.reads++
	if workspaceID != s.workspaceID || repoID != s.repoID {
		return service.ErrForgejoActionRepoNotFound
	}
	return s.forcedErr
}

func (s *actionsServiceStub) ListActionRuns(ctx context.Context, workspaceID, repoID uuid.UUID, opts fj.ActionRunOptions) (*dto.ListResponse[dto.ForgejoActionRunResponse], error) {
	if err := s.scope(workspaceID, repoID); err != nil {
		return nil, err
	}
	s.options = opts
	return &dto.ListResponse[dto.ForgejoActionRunResponse]{Data: []dto.ForgejoActionRunResponse{s.run}, TotalCount: 101, Page: opts.Page, PerPage: opts.PerPage, HasMore: true}, nil
}

func (s *actionsServiceStub) GetActionRun(ctx context.Context, workspaceID, repoID uuid.UUID, runID int64) (*dto.ForgejoActionRunResponse, error) {
	if err := s.scope(workspaceID, repoID); err != nil {
		return nil, err
	}
	if runID != s.run.ID {
		return nil, &fj.APIError{StatusCode: http.StatusNotFound}
	}
	return &s.run, nil
}

func (s *actionsServiceStub) ListActionJobs(ctx context.Context, workspaceID, repoID uuid.UUID, runID int64, page, perPage int) (*dto.ListResponse[dto.ForgejoActionJobResponse], error) {
	if err := s.scope(workspaceID, repoID); err != nil {
		return nil, err
	}
	if runID != s.run.ID {
		return nil, &fj.APIError{StatusCode: http.StatusNotFound}
	}
	s.page, s.perPage = page, perPage
	return &dto.ListResponse[dto.ForgejoActionJobResponse]{Data: []dto.ForgejoActionJobResponse{s.job}, TotalCount: 1, Page: page, PerPage: perPage}, nil
}

func (s *actionsServiceStub) GetActionJob(ctx context.Context, workspaceID, repoID uuid.UUID, runID, jobID int64) (*dto.ForgejoActionJobResponse, error) {
	if err := s.scope(workspaceID, repoID); err != nil {
		return nil, err
	}
	if runID != s.job.RunID || jobID != s.job.ID {
		return nil, service.ErrForgejoActionJobNotFound
	}
	return &s.job, nil
}

func (s *actionsServiceStub) GetActionJobLog(ctx context.Context, workspaceID, repoID uuid.UUID, runID, jobID, attempt int64) (*dto.ForgejoActionLogResponse, error) {
	if _, err := s.GetActionJob(ctx, workspaceID, repoID, runID, jobID); err != nil {
		return nil, err
	}
	s.attempt = attempt
	if attempt != 0 && attempt != s.job.Attempt {
		return nil, &fj.APIError{StatusCode: http.StatusNotFound}
	}
	return &s.logs, nil
}

func (s *actionsServiceStub) CancelActionRun(ctx context.Context, workspaceID, repoID uuid.UUID, runID int64) error {
	if err := s.scope(workspaceID, repoID); err != nil {
		return err
	}
	if runID != s.run.ID {
		return &fj.APIError{StatusCode: http.StatusNotFound}
	}
	s.cancels++
	return nil
}

type actionsMCPFixture struct {
	env        *mcpTestEnv
	workspaces *actionsWorkspaceStub
	svc        *actionsServiceStub
}

func newActionsMCPFixture(t *testing.T, role string) *actionsMCPFixture {
	t.Helper()
	user := &domain.User{ID: uuid.New(), Email: "actions@example.test", Name: "Actions"}
	workspace := &domain.Workspace{ID: uuid.New(), Slug: "actions", Name: "Actions", OwnerID: user.ID}
	workspaces := &actionsWorkspaceStub{workspace: workspace}
	if role != "" {
		workspaces.member = &domain.WorkspaceMember{WorkspaceID: workspace.ID, UserID: user.ID, Role: role}
	}
	users := &mcpUserRepo{}
	users.On("GetByID", mock.Anything, user.ID).Return(user, nil)
	keyStore := &apiKeyRepoStub{}
	keySvc := service.NewAPIKeyService(keyStore, users)
	_, fullToken, err := keySvc.Create(t.Context(), user.ID, dto.CreateAPIKeyRequest{Name: "actions-full"})
	require.NoError(t, err)
	_, readToken, err := keySvc.Create(t.Context(), user.ID, dto.CreateAPIKeyRequest{Name: "actions-read", ReadOnly: true})
	require.NoError(t, err)
	svc := &actionsServiceStub{
		workspaceID: workspace.ID, repoID: uuid.New(),
		run:  dto.ForgejoActionRunResponse{ID: 42, RunNumber: 8, Name: "ci.yaml", WorkflowID: "ci.yaml", Title: "Fix tests", Status: "waiting", Event: "pull_request", HeadBranch: "refs/pull/7/head", HeadSHA: "native-head", HTMLURL: "https://forgejo.example.test/org/project/actions/runs/8", CreatedAt: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)},
		job:  dto.ForgejoActionJobResponse{ID: 91, RunID: 42, Name: "test", Status: "failure", Conclusion: "failure", Attempt: 2},
		logs: dto.ForgejoActionLogResponse{Content: "native attempt 2 log\n", Truncated: false, MaxBytes: fj.MaxActionLogBytes},
	}
	svc.repos = []dto.ForgejoRepoResponse{{ID: svc.repoID.String(), FullName: "Org/Project", IsActive: true}}
	handler := NewHTTPHandler(Deps{Version: "test", Users: users, Workspaces: workspaces, ForgejoActionsSvc: svc})
	e := echo.New()
	e.Any("/mcp", echo.WrapHandler(handler), middleware.APIKeyAuth(keySvc))
	server := httptest.NewServer(e)
	t.Cleanup(server.Close)
	return &actionsMCPFixture{
		env:        &mcpTestEnv{server: server, user: user, workspace: workspace, fullToken: fullToken, readToken: readToken, keyStore: keyStore, users: users},
		workspaces: workspaces, svc: svc,
	}
}

func (f *actionsMCPFixture) connect(t *testing.T, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "actions-test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: f.env.server.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerClient{token}, Timeout: 10 * time.Second},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func callActionsMCP(t *testing.T, session *mcp.ClientSession, name string, extra map[string]any) *mcp.CallToolResult {
	t.Helper()
	args := map[string]any{"workspace": "actions", "repo": "org/project"}
	for key, value := range extra {
		args[key] = value
	}
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	return result
}

func assertActionsError(t *testing.T, result *mcp.CallToolResult, fragment string) {
	t.Helper()
	require.True(t, result.IsError)
	body, err := json.Marshal(result)
	require.NoError(t, err)
	assert.Contains(t, string(body), fragment)
}

func assertActionsDTO(t *testing.T, result *mcp.CallToolResult, expected any) {
	t.Helper()
	require.False(t, result.IsError)
	body, err := json.Marshal(expected)
	require.NoError(t, err)
	var expectedJSON any
	require.NoError(t, json.Unmarshal(body, &expectedJSON))
	assert.Equal(t, expectedJSON, result.StructuredContent)
}

func TestForgejoActionsMCPAdvertisesNativeTools(t *testing.T) {
	f := newActionsMCPFixture(t, domain.RoleOwner)
	session := f.connect(t, f.env.readToken)
	list, err := session.ListTools(t.Context(), nil)
	require.NoError(t, err)
	tools := make(map[string]*mcp.Tool)
	for _, tool := range list.Tools {
		tools[tool.Name] = tool
	}
	for _, name := range []string{"forgejo_list_action_runs", "forgejo_get_action_run", "forgejo_list_action_jobs", "forgejo_get_action_job", "forgejo_get_action_job_logs", "forgejo_cancel_action_run"} {
		require.Contains(t, tools, name)
		require.NotNil(t, tools[name].Annotations)
		assert.Equal(t, name != "forgejo_cancel_action_run", tools[name].Annotations.ReadOnlyHint)
		schema, ok := tools[name].InputSchema.(map[string]any)
		require.True(t, ok)
		properties, ok := schema["properties"].(map[string]any)
		require.True(t, ok)
		for _, required := range []string{"workspace", "repo"} {
			assert.Contains(t, properties, required)
			assert.Contains(t, schema["required"], required)
		}
		if name != "forgejo_list_action_runs" {
			assert.Contains(t, properties, "run_id")
			assert.Contains(t, schema["required"], "run_id")
		}
		if name == "forgejo_get_action_job" || name == "forgejo_get_action_job_logs" {
			assert.Contains(t, properties, "job_id")
			assert.Contains(t, schema["required"], "job_id")
		}
	}
	assert.NotContains(t, tools, "forgejo_rerun_action_run")
	assert.NotContains(t, tools, "forgejo_get_action_steps")
	assert.Zero(t, f.svc.lookups, "handshake and tool discovery must not fetch or change Forgejo state")
}

func TestForgejoActionsMCPReadKeyPreservesNativeDTOsAndFilters(t *testing.T) {
	f := newActionsMCPFixture(t, domain.RoleGuest)
	session := f.connect(t, f.env.readToken)
	runs := callActionsMCP(t, session, "forgejo_list_action_runs", nil)
	assertActionsDTO(t, runs, &dto.ListResponse[dto.ForgejoActionRunResponse]{Data: []dto.ForgejoActionRunResponse{f.svc.run}, TotalCount: 101, Page: 1, PerPage: 20, HasMore: true})
	assert.Equal(t, fj.ActionRunOptions{Page: 1, PerPage: 20}, f.svc.options)
	assertActionsDTO(t, callActionsMCP(t, session, "forgejo_get_action_run", map[string]any{"repo": f.svc.repoID.String(), "run_id": 42}), &f.svc.run)
	assertActionsDTO(t, callActionsMCP(t, session, "forgejo_list_action_jobs", map[string]any{"run_id": 42}), &dto.ListResponse[dto.ForgejoActionJobResponse]{Data: []dto.ForgejoActionJobResponse{f.svc.job}, TotalCount: 1, Page: 1, PerPage: 100})
	assert.Equal(t, 1, f.svc.page)
	assert.Equal(t, 100, f.svc.perPage)
	assertActionsDTO(t, callActionsMCP(t, session, "forgejo_get_action_job", map[string]any{"run_id": 42, "job_id": 91}), &f.svc.job)
	assertActionsDTO(t, callActionsMCP(t, session, "forgejo_get_action_job_logs", map[string]any{"run_id": 42, "job_id": 91}), &f.svc.logs)
	assert.Zero(t, f.svc.attempt, "omission delegates latest-attempt selection to the native API")
	assertActionsDTO(t, callActionsMCP(t, session, "forgejo_get_action_job_logs", map[string]any{"run_id": 42, "job_id": 91, "attempt": 2}), &f.svc.logs)
	assert.EqualValues(t, 2, f.svc.attempt)
	result := callActionsMCP(t, session, "forgejo_list_action_runs", map[string]any{"page": 10000, "per_page": 100, "status": "queued", "ref": "main", "event": "push", "head_sha": "sha", "workflow_id": "ci.yaml", "run_number": 8})
	require.False(t, result.IsError)
	assert.Equal(t, fj.ActionRunOptions{Page: 10000, PerPage: 100, Status: "queued", Ref: "main", Event: "push", HeadSHA: "sha", WorkflowID: "ci.yaml", RunNumber: 8}, f.svc.options)
	require.False(t, callActionsMCP(t, session, "forgejo_list_action_jobs", map[string]any{"run_id": 42, "page": 2, "per_page": 5}).IsError)
	assert.Equal(t, 2, f.svc.page)
	assert.Equal(t, 5, f.svc.perPage)
}

func TestForgejoActionsMCPCancelRequiresWriteScopeAndOwnerPermission(t *testing.T) {
	for _, role := range []string{domain.RoleOwner, domain.RoleAdmin, domain.RoleMember, domain.RoleGuest} {
		t.Run(role, func(t *testing.T) {
			f := newActionsMCPFixture(t, role)
			readSession := f.connect(t, f.env.readToken)
			assertActionsError(t, callActionsMCP(t, readSession, "forgejo_cancel_action_run", map[string]any{"run_id": 42}), "read-only")
			assert.Zero(t, f.svc.lookups)
			fullSession := f.connect(t, f.env.fullToken)
			result := callActionsMCP(t, fullSession, "forgejo_cancel_action_run", map[string]any{"run_id": 42})
			if role == domain.RoleOwner {
				assertActionsDTO(t, result, map[string]string{"status": "accepted"})
				assert.Equal(t, 1, f.svc.cancels)
			} else {
				assertActionsError(t, result, "workspace:manage")
				assert.Zero(t, f.svc.lookups)
				assert.Zero(t, f.svc.cancels)
			}
		})
	}
}

func TestForgejoActionsMCPReadsCheckCurrentMembershipAndPermission(t *testing.T) {
	for _, role := range []string{"", "unknown"} {
		t.Run(role, func(t *testing.T) {
			f := newActionsMCPFixture(t, role)
			session := f.connect(t, f.env.readToken)
			result := callActionsMCP(t, session, "forgejo_get_action_run", map[string]any{"run_id": 42})
			require.True(t, result.IsError)
			assert.Zero(t, f.svc.lookups)
			assert.Zero(t, f.svc.reads)
		})
	}
	f := newActionsMCPFixture(t, domain.RoleMember)
	session := f.connect(t, f.env.readToken)
	require.False(t, callActionsMCP(t, session, "forgejo_get_action_run", map[string]any{"run_id": 42}).IsError)
	f.workspaces.member = nil
	assertActionsError(t, callActionsMCP(t, session, "forgejo_get_action_run", map[string]any{"run_id": 42}), "workspace membership")
	assert.Equal(t, 1, f.svc.lookups, "each tool call must re-check membership before looking up a repository")
}

func TestForgejoActionsMCPReadsRequireReadScope(t *testing.T) {
	f := newActionsMCPFixture(t, domain.RoleOwner)
	for _, key := range f.env.keyStore.keysByHash {
		if key.Name == "actions-full" {
			key.Scopes = []string{domain.APIKeyScopeWrite}
		}
	}
	session := f.connect(t, f.env.fullToken)
	assertActionsError(t, callActionsMCP(t, session, "forgejo_get_action_run", map[string]any{"run_id": 42}), "lacks read access")
	assert.Zero(t, f.svc.lookups)
	assert.Zero(t, f.svc.reads)
}

func TestForgejoActionsMCPWorkspaceValidationAndErrorsNeverEchoInput(t *testing.T) {
	f := newActionsMCPFixture(t, domain.RoleOwner)
	session := f.connect(t, f.env.fullToken)
	for _, workspace := range []string{"", strings.Repeat("a", 101), strings.Repeat("é", 51), "private\nworkspace", "private\x00workspace", "private\u0085workspace"} {
		for _, tool := range []string{"forgejo_get_action_run", "forgejo_cancel_action_run"} {
			result := callActionsMCP(t, session, tool, map[string]any{"workspace": workspace, "run_id": 42})
			assertActionsError(t, result, "valid workspace slug")
			body, err := json.Marshal(result)
			require.NoError(t, err)
			assert.NotContains(t, string(body), "private")
		}
	}
	secretLikeSlug := "https://secret-token@private.internal/token"
	for _, tool := range []string{"forgejo_get_action_run", "forgejo_cancel_action_run"} {
		result := callActionsMCP(t, session, tool, map[string]any{"workspace": secretLikeSlug, "run_id": 42})
		assertActionsError(t, result, "workspace membership")
		body, err := json.Marshal(result)
		require.NoError(t, err)
		for _, fragment := range []string{secretLikeSlug, "secret-token", "private.internal"} {
			assert.NotContains(t, string(body), fragment)
		}
	}
	assert.Zero(t, f.svc.lookups)
	assert.Zero(t, f.svc.reads)
	assert.Zero(t, f.svc.cancels)
}

func TestForgejoActionsMCPRejectsUnlinkedAndInactiveRepositories(t *testing.T) {
	f := newActionsMCPFixture(t, domain.RoleOwner)
	session := f.connect(t, f.env.readToken)
	for _, selector := range []string{uuid.NewString(), "another/project"} {
		assertActionsError(t, callActionsMCP(t, session, "forgejo_get_action_run", map[string]any{"repo": selector, "run_id": 42}), "not actively linked")
	}
	f.svc.repos[0].IsActive = false
	assertActionsError(t, callActionsMCP(t, session, "forgejo_get_action_run", map[string]any{"run_id": 42}), "not actively linked")
	assert.Zero(t, f.svc.reads)
}

func TestForgejoActionsMCPWrongRunJobAndAttemptFailClosed(t *testing.T) {
	f := newActionsMCPFixture(t, domain.RoleMember)
	session := f.connect(t, f.env.readToken)
	assertActionsError(t, callActionsMCP(t, session, "forgejo_get_action_run", map[string]any{"run_id": 99}), "not found")
	assertActionsError(t, callActionsMCP(t, session, "forgejo_get_action_job", map[string]any{"run_id": 99, "job_id": 91}), "not found in this run")
	assertActionsError(t, callActionsMCP(t, session, "forgejo_get_action_job_logs", map[string]any{"run_id": 99, "job_id": 91}), "not found in this run")
	assertActionsError(t, callActionsMCP(t, session, "forgejo_get_action_job_logs", map[string]any{"run_id": 42, "job_id": 91, "attempt": 3}), "logs are unavailable")
}

func TestForgejoActionsMCPValidatesParametersBeforeServiceCalls(t *testing.T) {
	f := newActionsMCPFixture(t, domain.RoleOwner)
	session := f.connect(t, f.env.fullToken)
	cases := []struct {
		tool string
		args map[string]any
	}{
		{"forgejo_list_action_runs", map[string]any{"page": 0}},
		{"forgejo_list_action_runs", map[string]any{"page": 10001}},
		{"forgejo_list_action_runs", map[string]any{"per_page": 0}},
		{"forgejo_list_action_runs", map[string]any{"per_page": 101}},
		{"forgejo_list_action_runs", map[string]any{"status": "completed"}},
		{"forgejo_list_action_runs", map[string]any{"ref": strings.Repeat("a", 513)}},
		{"forgejo_list_action_runs", map[string]any{"workflow_id": "ci.yaml\nsecret"}},
		{"forgejo_list_action_runs", map[string]any{"run_number": 0}},
		{"forgejo_list_action_runs", map[string]any{"run_number": -1}},
		{"forgejo_get_action_run", map[string]any{"run_id": 0}},
		{"forgejo_get_action_run", map[string]any{"run_id": -1}},
		{"forgejo_list_action_jobs", map[string]any{"run_id": 42, "per_page": 101}},
		{"forgejo_get_action_job", map[string]any{"run_id": 42, "job_id": 0}},
		{"forgejo_get_action_job_logs", map[string]any{"run_id": 42, "job_id": 91, "attempt": 0}},
		{"forgejo_get_action_job_logs", map[string]any{"run_id": 42, "job_id": 91, "attempt": -1}},
		{"forgejo_cancel_action_run", map[string]any{"run_id": 0}},
	}
	for _, tc := range cases {
		assertActionsError(t, callActionsMCP(t, session, tc.tool, tc.args), "invalid Forgejo Actions parameters")
	}
	assert.Zero(t, f.svc.lookups)
	assert.Zero(t, f.svc.reads)
	assert.Zero(t, f.svc.cancels)
}

func TestForgejoActionsMCPLogsEnforceByteBoundAndPreserveTruncation(t *testing.T) {
	f := newActionsMCPFixture(t, domain.RoleGuest)
	session := f.connect(t, f.env.readToken)
	f.svc.logs.Content = strings.Repeat("x", fj.MaxActionLogBytes+17)
	result := structured(t, callActionsMCP(t, session, "forgejo_get_action_job_logs", map[string]any{"run_id": 42, "job_id": 91}))
	assert.Len(t, result["content"], fj.MaxActionLogBytes)
	assert.Equal(t, true, result["truncated"])
	assert.EqualValues(t, fj.MaxActionLogBytes, result["max_bytes"])
	assert.Len(t, f.svc.logs.Content, fj.MaxActionLogBytes+17, "bounding must not mutate the service's DTO")
}

func TestForgejoActionsMCPServiceFailuresNeverLeakCredentialsOrURLs(t *testing.T) {
	f := newActionsMCPFixture(t, domain.RoleOwner)
	session := f.connect(t, f.env.fullToken)
	secret := "disposable-private-token https://user:password@private.internal/api?token=disposable-private-token"
	for _, cause := range []error{errors.New(secret), fmt.Errorf("%s: %w", secret, &fj.APIError{StatusCode: http.StatusForbidden}), fmt.Errorf("%s: %w", secret, context.DeadlineExceeded)} {
		f.svc.forcedErr = cause
		for _, tc := range []struct {
			name string
			args map[string]any
		}{
			{"forgejo_list_action_runs", nil},
			{"forgejo_get_action_run", map[string]any{"run_id": 42}},
			{"forgejo_list_action_jobs", map[string]any{"run_id": 42}},
			{"forgejo_get_action_job", map[string]any{"run_id": 42, "job_id": 91}},
			{"forgejo_get_action_job_logs", map[string]any{"run_id": 42, "job_id": 91}},
			{"forgejo_cancel_action_run", map[string]any{"run_id": 42}},
		} {
			result := callActionsMCP(t, session, tc.name, tc.args)
			require.True(t, result.IsError)
			body, err := json.Marshal(result)
			require.NoError(t, err)
			for _, fragment := range []string{"disposable-private-token", "private.internal", "password", "https://"} {
				assert.NotContains(t, string(body), fragment)
			}
		}
	}
	f.svc.lookupErr = errors.New(secret)
	assertActionsError(t, callActionsMCP(t, session, "forgejo_get_action_run", map[string]any{"run_id": 42}), "could not complete")
}

func TestForgejoActionsMCPHTTPRejectsMissingInvalidRevokedAndExpiredKeys(t *testing.T) {
	for _, mode := range []string{"missing", "invalid", "jwt", "revoked", "expired"} {
		t.Run(mode, func(t *testing.T) {
			f := newActionsMCPFixture(t, domain.RoleOwner)
			token := f.env.fullToken
			switch mode {
			case "missing":
				token = ""
			case "invalid":
				token = "ku_invalid"
			case "jwt":
				token = "eyJhbGciOiJIUzI1NiJ9.fake.jwt"
			case "revoked", "expired":
				for _, key := range f.env.keyStore.keysByHash {
					if key.Name == "actions-full" {
						if mode == "revoked" {
							key.RevokedAt = new(time.Now())
						} else {
							key.ExpiresAt = new(time.Now().Add(-time.Minute))
						}
					}
				}
			}
			body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"forgejo_list_action_runs","arguments":{"workspace":"actions","repo":"org/project"}}}`)
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.env.server.URL+"/mcp", bytes.NewReader(body))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			response, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, http.StatusUnauthorized, response.StatusCode)
			payload, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			assert.NotContains(t, string(payload), f.env.fullToken)
			assert.Zero(t, f.svc.lookups)
			assert.Zero(t, f.svc.reads)
		})
	}
}
