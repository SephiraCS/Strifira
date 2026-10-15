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
	"sync"
	"testing"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/middleware"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/SephiraCS/strifira-backend/internal/service"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type devBoxMCPServiceMock struct{ mock.Mock }

func (m *devBoxMCPServiceMock) Create(ctx context.Context, workspaceID, userID uuid.UUID, request dto.CreateDevBoxRequest) (*dto.DevBoxTokenResponse, error) {
	a := m.Called(ctx, workspaceID, userID, request)
	return devBoxMCPMockPointer[dto.DevBoxTokenResponse](a.Get(0)), a.Error(1)
}

func (m *devBoxMCPServiceMock) List(ctx context.Context, workspaceID, userID uuid.UUID) ([]domain.DevBox, error) {
	a := m.Called(ctx, workspaceID, userID)
	boxes, _ := a.Get(0).([]domain.DevBox)
	return boxes, a.Error(1)
}

func (m *devBoxMCPServiceMock) Update(ctx context.Context, workspaceID, boxID, userID uuid.UUID, request dto.UpdateDevBoxRequest) (*domain.DevBox, error) {
	a := m.Called(ctx, workspaceID, boxID, userID, request)
	return devBoxMCPMockPointer[domain.DevBox](a.Get(0)), a.Error(1)
}

func (m *devBoxMCPServiceMock) RotateToken(ctx context.Context, workspaceID, boxID, userID uuid.UUID) (*dto.DevBoxTokenResponse, error) {
	a := m.Called(ctx, workspaceID, boxID, userID)
	return devBoxMCPMockPointer[dto.DevBoxTokenResponse](a.Get(0)), a.Error(1)
}

func (m *devBoxMCPServiceMock) Revoke(ctx context.Context, workspaceID, boxID, userID uuid.UUID) error {
	return m.Called(ctx, workspaceID, boxID, userID).Error(0)
}

func (m *devBoxMCPServiceMock) CreateJob(ctx context.Context, workspaceID, boxID, userID uuid.UUID, request dto.CreateDevBoxJobRequest) (*domain.DevBoxJob, error) {
	a := m.Called(ctx, workspaceID, boxID, userID, request)
	return devBoxMCPMockPointer[domain.DevBoxJob](a.Get(0)), a.Error(1)
}

func (m *devBoxMCPServiceMock) ListJobs(ctx context.Context, workspaceID, userID uuid.UUID, params dto.DevBoxJobListParams) ([]domain.DevBoxJob, int, error) {
	a := m.Called(ctx, workspaceID, userID, params)
	jobs, _ := a.Get(0).([]domain.DevBoxJob)
	return jobs, a.Int(1), a.Error(2)
}

func (m *devBoxMCPServiceMock) GetJob(ctx context.Context, workspaceID, jobID, userID uuid.UUID) (*domain.DevBoxJob, error) {
	a := m.Called(ctx, workspaceID, jobID, userID)
	return devBoxMCPMockPointer[domain.DevBoxJob](a.Get(0)), a.Error(1)
}

func (m *devBoxMCPServiceMock) CancelJob(ctx context.Context, workspaceID, jobID, userID uuid.UUID) (*domain.DevBoxJob, error) {
	a := m.Called(ctx, workspaceID, jobID, userID)
	return devBoxMCPMockPointer[domain.DevBoxJob](a.Get(0)), a.Error(1)
}

func (m *devBoxMCPServiceMock) JobLogs(ctx context.Context, workspaceID, jobID, userID uuid.UUID, params dto.DevBoxJobLogParams) ([]domain.DevBoxJobLog, error) {
	a := m.Called(ctx, workspaceID, jobID, userID, params)
	logs, _ := a.Get(0).([]domain.DevBoxJobLog)
	return logs, a.Error(1)
}

func devBoxMCPMockPointer[T any](value any) *T {
	if value == nil {
		return nil
	}
	return value.(*T)
}

// Mutable membership lets one established HTTP session observe removal and
// role changes on its next call, rather than relying on cached session RBAC.
type devBoxMCPWorkspaceRepo struct {
	repository.WorkspaceRepo
	mu        sync.Mutex
	workspace *domain.Workspace
	member    *domain.WorkspaceMember
}

func (r *devBoxMCPWorkspaceRepo) GetBySlug(_ context.Context, slug string) (*domain.Workspace, error) {
	if slug == r.workspace.Slug {
		return r.workspace, nil
	}
	return nil, nil
}

func (r *devBoxMCPWorkspaceRepo) GetMember(_ context.Context, workspaceID, userID uuid.UUID) (*domain.WorkspaceMember, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.member == nil || r.member.WorkspaceID != workspaceID || r.member.UserID != userID {
		return nil, nil
	}
	return new(*r.member), nil
}

func (r *devBoxMCPWorkspaceRepo) setRole(role string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if role == "" {
		r.member = nil
		return
	}
	r.member.Role = role
}

type devBoxMCPIssueRepo struct {
	repository.IssueRepo
	issue *domain.Issue
}

func (r *devBoxMCPIssueRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Issue, error) {
	if r.issue.ID == id {
		return new(*r.issue), nil
	}
	return nil, nil
}

func (r *devBoxMCPIssueRepo) GetByIdentifier(_ context.Context, _ uuid.UUID, identifier string) (*domain.Issue, error) {
	if r.issue.Identifier == identifier {
		return new(*r.issue), nil
	}
	return nil, nil
}

type devBoxMCPTestEnv struct {
	*mcpTestEnv
	workspaceRepo *devBoxMCPWorkspaceRepo
	issueRepo     *devBoxMCPIssueRepo
}

func newDevBoxMCPTestEnv(t *testing.T, role string, buildService func(*devBoxMCPWorkspaceRepo) DevBoxToolsService) *devBoxMCPTestEnv {
	t.Helper()
	user := &domain.User{ID: uuid.New(), Email: "dev-box-owner@example.test", Name: "Owner"}
	workspace := &domain.Workspace{ID: uuid.New(), Slug: "dev-box-test", Name: "Development", OwnerID: user.ID}
	workspaces := &devBoxMCPWorkspaceRepo{workspace: workspace, member: &domain.WorkspaceMember{WorkspaceID: workspace.ID, UserID: user.ID, Role: role}}
	users := &mcpUserRepo{}
	users.On("GetByID", mock.Anything, user.ID).Return(user, nil).Maybe()
	keyStore := &apiKeyRepoStub{}
	keys := service.NewAPIKeyService(keyStore, users)
	_, fullToken, err := keys.Create(t.Context(), user.ID, dto.CreateAPIKeyRequest{Name: "dev-box-writer"})
	require.NoError(t, err)
	_, readToken, err := keys.Create(t.Context(), user.ID, dto.CreateAPIKeyRequest{Name: "dev-box-reader", ReadOnly: true})
	require.NoError(t, err)
	issues := &devBoxMCPIssueRepo{issue: &domain.Issue{ID: uuid.New(), WorkspaceID: workspace.ID, Identifier: "DEV-1"}}
	handler := NewHTTPHandler(Deps{Version: "dev-box-test", Users: users, Workspaces: workspaces,
		IssueSvc: service.NewIssueService(issues, nil, nil, nil, nil, nil), DevBoxSvc: buildService(workspaces)})
	e := echo.New()
	e.Any("/mcp", echo.WrapHandler(handler), middleware.APIKeyAuth(keys))
	server := httptest.NewServer(e)
	t.Cleanup(server.Close)
	return &devBoxMCPTestEnv{
		mcpTestEnv:    &mcpTestEnv{server: server, user: user, workspace: workspace, fullToken: fullToken, readToken: readToken, keyStore: keyStore, users: users},
		workspaceRepo: workspaces, issueRepo: issues,
	}
}

func devBoxMCPEnv(t *testing.T, svc DevBoxToolsService) *devBoxMCPTestEnv {
	t.Helper()
	return newDevBoxMCPTestEnv(t, domain.RoleOwner, func(*devBoxMCPWorkspaceRepo) DevBoxToolsService { return svc })
}

func devBoxMCPJSON(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	return string(raw)
}

func TestMCPDevBoxLifecycleAndJob(t *testing.T) {
	svc := &devBoxMCPServiceMock{}
	env := devBoxMCPEnv(t, svc)
	session := connectMCP(t, env.mcpTestEnv, env.fullToken)
	ctx, ws, user := mock.Anything, env.workspace.ID, env.user.ID
	box := &domain.DevBox{ID: uuid.New(), WorkspaceID: ws, CreatedByUserID: user, Name: "strifira-dev", Enabled: true,
		TokenHash: "private-worker-token-hash", Providers: json.RawMessage(`[{"id":"claude-code","version":"test"}]`)}
	token := "sfw_" + strings.Repeat("a", 64)
	rotated := "sfw_" + strings.Repeat("b", 64)
	svc.On("Create", ctx, ws, user, dto.CreateDevBoxRequest{Name: "strifira-dev"}).Return(&dto.DevBoxTokenResponse{Box: box, Token: token}, nil).Once()
	registration := callTool(t, session, "register_dev_box", map[string]any{"workspace": env.workspace.Slug, "name": " strifira-dev "})
	registered := structured(t, registration)
	assert.Equal(t, token, registered["token"])
	assert.Contains(t, registered["token_notice"], "Returned once")
	assert.NotContains(t, devBoxMCPJSON(t, registration), box.TokenHash)
	assert.NotContains(t, devBoxMCPJSON(t, registration), "token_hash")

	svc.On("List", ctx, ws, user).Return([]domain.DevBox{*box}, nil).Once()
	listed := callTool(t, session, "list_dev_boxes", map[string]any{"workspace": env.workspace.Slug})
	assert.Len(t, structured(t, listed)["boxes"], 1)
	assert.NotContains(t, devBoxMCPJSON(t, listed), token)
	assert.NotContains(t, devBoxMCPJSON(t, listed), box.TokenHash)

	svc.On("Update", ctx, ws, box.ID, user, dto.UpdateDevBoxRequest{Enabled: new(false)}).Return(box, nil).Once()
	updated := callTool(t, session, "update_dev_box", map[string]any{"workspace": env.workspace.Slug, "box_id": box.ID.String(), "enabled": false})
	require.False(t, updated.IsError, devBoxMCPJSON(t, updated))

	svc.On("RotateToken", ctx, ws, box.ID, user).Return(&dto.DevBoxTokenResponse{Box: box, Token: rotated}, nil).Once()
	rotation := callTool(t, session, "rotate_dev_box_token", map[string]any{"workspace": env.workspace.Slug, "box_id": box.ID.String()})
	assert.Equal(t, rotated, structured(t, rotation)["token"])
	assert.NotContains(t, devBoxMCPJSON(t, rotation), token)

	request := dto.CreateDevBoxJobRequest{IssueID: env.issueRepo.issue.ID.String(), Provider: "claude-code", Prompt: "Improve the owned repository",
		RepositoryFullName: "TerraScale/strifira", BaseBranch: "main", TestCommand: []string{"make", "test"}, AcceptanceCriteria: []string{"tests pass"}, MaxRuntimeSeconds: 300}
	job := &domain.DevBoxJob{ID: uuid.New(), WorkspaceID: ws, BoxID: box.ID, RequestedByUserID: user, IssueID: env.issueRepo.issue.ID,
		IssueIdentifier: "DEV-1", RepositoryFullName: request.RepositoryFullName, BaseBranch: request.BaseBranch, Provider: request.Provider,
		Status: domain.DevBoxJobQueued, LeaseHash: "private-job-lease-hash"}
	svc.On("CreateJob", ctx, ws, box.ID, user, request).Return(job, nil).Once()
	created := callTool(t, session, "create_dev_box_job", map[string]any{"workspace": env.workspace.Slug, "box_id": box.ID.String(), "issue": "DEV-1",
		"repository_full_name": request.RepositoryFullName, "base_branch": "main", "provider": "claude-code", "prompt": request.Prompt,
		"test_command": request.TestCommand, "acceptance_criteria": request.AcceptanceCriteria, "max_runtime_seconds": 300})
	assert.Equal(t, job.ID.String(), structured(t, created)["job"].(map[string]any)["id"])
	assert.NotContains(t, devBoxMCPJSON(t, created), job.LeaseHash)
	assert.NotContains(t, devBoxMCPJSON(t, created), "lease_hash")

	params := dto.DevBoxJobListParams{PaginationParams: dto.PaginationParams{Page: 1, PerPage: 50}, BoxID: box.ID.String(), IssueID: job.IssueID.String()}
	svc.On("ListJobs", ctx, ws, user, params).Return([]domain.DevBoxJob{*job}, 1, nil).Once()
	jobs := callTool(t, session, "list_dev_box_jobs", map[string]any{"workspace": env.workspace.Slug, "box_id": box.ID.String(), "issue": job.IssueID.String()})
	assert.Equal(t, float64(1), structured(t, jobs)["total_count"])
	assert.NotContains(t, devBoxMCPJSON(t, jobs), job.LeaseHash)

	svc.On("GetJob", ctx, ws, job.ID, user).Return(job, nil).Once()
	got := callTool(t, session, "get_dev_box_job", map[string]any{"workspace": env.workspace.Slug, "job_id": job.ID.String()})
	require.False(t, got.IsError)
	assert.NotContains(t, devBoxMCPJSON(t, got), job.LeaseHash)
	svc.On("JobLogs", ctx, ws, job.ID, user, dto.DevBoxJobLogParams{AfterID: 3, Limit: 100}).Return([]domain.DevBoxJobLog{{ID: 8, JobID: job.ID, Content: "independent tests passed"}}, nil).Once()
	logs := callTool(t, session, "get_dev_box_job_logs", map[string]any{"workspace": env.workspace.Slug, "job_id": job.ID.String(), "after_id": 3})
	assert.Equal(t, float64(8), structured(t, logs)["next_after_id"])
	svc.On("CancelJob", ctx, ws, job.ID, user).Return(job, nil).Once()
	cancelled := callTool(t, session, "cancel_dev_box_job", map[string]any{"workspace": env.workspace.Slug, "job_id": job.ID.String()})
	require.False(t, cancelled.IsError)
	svc.On("Revoke", ctx, ws, box.ID, user).Return(nil).Once()
	revocation := callTool(t, session, "revoke_dev_box", map[string]any{"workspace": env.workspace.Slug, "box_id": box.ID.String()})
	assert.Equal(t, true, structured(t, revocation)["revoked"])
	assert.NotContains(t, devBoxMCPJSON(t, revocation), rotated)
	svc.AssertExpectations(t)
}

func TestMCPDevBoxToolsAdvertised(t *testing.T) {
	env := devBoxMCPEnv(t, &devBoxMCPServiceMock{})
	session := connectMCP(t, env.mcpTestEnv, env.fullToken)
	tools, err := session.ListTools(t.Context(), nil)
	require.NoError(t, err)
	names := map[string]*mcp.Tool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = tool
	}
	for _, name := range []string{"list_dev_boxes", "register_dev_box", "update_dev_box", "rotate_dev_box_token", "revoke_dev_box",
		"create_dev_box_job", "list_dev_box_jobs", "get_dev_box_job", "get_dev_box_job_logs", "cancel_dev_box_job"} {
		require.Contains(t, names, name)
	}
	assert.Contains(t, names["register_dev_box"].Description, "token once")
	assert.Contains(t, names["create_dev_box_job"].Description, "does not change")
	assert.NotContains(t, names, "claim_dev_box_job")
	assert.NotContains(t, names, "dev_box_worker_heartbeat")
}

func TestMCPDevBoxReadKeyCannotMutate(t *testing.T) {
	svc := &devBoxMCPServiceMock{}
	env := devBoxMCPEnv(t, svc)
	session := connectMCP(t, env.mcpTestEnv, env.readToken)
	boxID, jobID := uuid.NewString(), uuid.NewString()
	for _, test := range []struct {
		name string
		args map[string]any
	}{
		{"register_dev_box", map[string]any{"name": "strifira-dev"}},
		{"update_dev_box", map[string]any{"box_id": boxID, "enabled": false}},
		{"rotate_dev_box_token", map[string]any{"box_id": boxID}},
		{"revoke_dev_box", map[string]any{"box_id": boxID}},
		{"create_dev_box_job", map[string]any{"box_id": boxID, "issue": "DEV-1", "repository_full_name": "TerraScale/strifira", "base_branch": "main", "provider": "claude-code"}},
		{"cancel_dev_box_job", map[string]any{"job_id": jobID}},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.args["workspace"] = env.workspace.Slug
			result := callTool(t, session, test.name, test.args)
			require.True(t, result.IsError)
			assert.Contains(t, devBoxMCPJSON(t, result), "read-only")
		})
	}
	assert.Empty(t, svc.Calls)
	svc.On("List", mock.Anything, env.workspace.ID, env.user.ID).Return(nil, nil).Once()
	listed := callTool(t, session, "list_dev_boxes", map[string]any{"workspace": env.workspace.Slug})
	assert.Empty(t, structured(t, listed)["boxes"])
	svc.AssertExpectations(t)
}

func TestMCPDevBoxReadsRequireReadScope(t *testing.T) {
	for _, scopes := range [][]string{nil, {domain.APIKeyScopeWrite}} {
		t.Run(fmt.Sprint(scopes), func(t *testing.T) {
			svc := &devBoxMCPServiceMock{}
			env := devBoxMCPEnv(t, svc)
			for _, key := range env.keyStore.keysByHash {
				key.Scopes = scopes
			}
			session := connectMCP(t, env.mcpTestEnv, env.fullToken)
			for _, test := range []struct {
				name string
				args map[string]any
			}{
				{"list_dev_boxes", map[string]any{}},
				{"list_dev_box_jobs", map[string]any{}},
				{"get_dev_box_job", map[string]any{"job_id": uuid.NewString()}},
				{"get_dev_box_job_logs", map[string]any{"job_id": uuid.NewString()}},
			} {
				test.args["workspace"] = env.workspace.Slug
				result := callTool(t, session, test.name, test.args)
				require.True(t, result.IsError)
				assert.Contains(t, devBoxMCPJSON(t, result), "require API key read scope")
			}
			assert.Empty(t, svc.Calls)
		})
	}
}

func TestMCPDevBoxRBACAndMembershipRefresh(t *testing.T) {
	for _, role := range []string{domain.RoleOwner, domain.RoleAdmin, domain.RoleMember, domain.RoleGuest} {
		t.Run(role, func(t *testing.T) {
			svc := &devBoxMCPServiceMock{}
			env := newDevBoxMCPTestEnv(t, role, func(*devBoxMCPWorkspaceRepo) DevBoxToolsService { return svc })
			session := connectMCP(t, env.mcpTestEnv, env.fullToken)
			if role != domain.RoleGuest {
				svc.On("List", mock.Anything, env.workspace.ID, env.user.ID).Return(nil, nil).Once()
			}
			result := callTool(t, session, "list_dev_boxes", map[string]any{"workspace": env.workspace.Slug})
			assert.Equal(t, role == domain.RoleGuest, result.IsError)
			if role == domain.RoleGuest {
				assert.Empty(t, svc.Calls)
				require.True(t, callTool(t, session, "register_dev_box", map[string]any{"workspace": env.workspace.Slug, "name": "strifira-dev"}).IsError)
				require.True(t, callTool(t, session, "cancel_dev_box_job", map[string]any{"workspace": env.workspace.Slug, "job_id": uuid.NewString()}).IsError)
				assert.Empty(t, svc.Calls)
			} else {
				env.workspaceRepo.setRole(domain.RoleGuest)
				require.True(t, callTool(t, session, "list_dev_boxes", map[string]any{"workspace": env.workspace.Slug}).IsError)
				env.workspaceRepo.setRole("")
				require.True(t, callTool(t, session, "register_dev_box", map[string]any{"workspace": env.workspace.Slug, "name": "strifira-dev"}).IsError)
				assert.Len(t, svc.Calls, 1)
			}
			svc.AssertExpectations(t)
		})
	}
}

func TestMCPDevBoxRevokedAPIKey(t *testing.T) {
	svc := &devBoxMCPServiceMock{}
	env := devBoxMCPEnv(t, svc)
	for _, key := range env.keyStore.keysByHash {
		if key.Name == "dev-box-writer" {
			require.NoError(t, env.keyStore.Revoke(t.Context(), key.ID, env.user.ID))
		}
	}
	payload := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"register_dev_box","arguments":{"workspace":"dev-box-test","name":"strifira-dev"}}}`)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, env.server.URL+"/mcp", bytes.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+env.fullToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	response, err := env.server.Client().Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.NotContains(t, string(body), env.fullToken)
	assert.Empty(t, svc.Calls)
}

// Use the real service for demo-mode and live-membership gates. Its store is
// intentionally unavailable: neither denied request may reach persistence.
func TestMCPDevBoxServiceAuthorizationPreserved(t *testing.T) {
	for _, test := range []string{"demo-restriction", "membership-lost-after-MCP-guard"} {
		t.Run(test, func(t *testing.T) {
			env := newDevBoxMCPTestEnv(t, domain.RoleOwner, func(workspaces *devBoxMCPWorkspaceRepo) DevBoxToolsService {
				allowed := func(uuid.UUID) bool { return false }
				if test != "demo-restriction" {
					allowed = func(uuid.UUID) bool {
						workspaces.setRole("")
						return true
					}
				}
				return service.NewDevBoxService(nil, workspaces, nil, allowed)
			})
			session := connectMCP(t, env.mcpTestEnv, env.fullToken)
			result := callTool(t, session, "register_dev_box", map[string]any{"workspace": env.workspace.Slug, "name": "strifira-dev"})
			require.True(t, result.IsError)
			assert.Contains(t, devBoxMCPJSON(t, result), "access denied")
		})
	}
}

func TestMCPDevBoxCreatorIsolation(t *testing.T) {
	svc := &devBoxMCPServiceMock{}
	env := newDevBoxMCPTestEnv(t, domain.RoleAdmin, func(*devBoxMCPWorkspaceRepo) DevBoxToolsService { return svc })
	session := connectMCP(t, env.mcpTestEnv, env.fullToken)
	otherBox, otherJob := uuid.New(), uuid.New()
	svc.On("RotateToken", mock.Anything, env.workspace.ID, otherBox, env.user.ID).Return(nil, service.ErrDevBoxNotFound).Once()
	svc.On("GetJob", mock.Anything, env.workspace.ID, otherJob, env.user.ID).Return(nil, service.ErrDevBoxNotFound).Once()
	for _, test := range []struct {
		name, field string
		id          uuid.UUID
	}{{"rotate_dev_box_token", "box_id", otherBox}, {"get_dev_box_job", "job_id", otherJob}} {
		result := callTool(t, session, test.name, map[string]any{"workspace": env.workspace.Slug, test.field: test.id.String()})
		require.True(t, result.IsError)
		assert.Contains(t, devBoxMCPJSON(t, result), "not found for this account")
	}
	svc.AssertExpectations(t)
}

func TestMCPDevBoxBoundedInputs(t *testing.T) {
	svc := &devBoxMCPServiceMock{}
	env := devBoxMCPEnv(t, svc)
	session := connectMCP(t, env.mcpTestEnv, env.fullToken)
	boxID, jobID := uuid.NewString(), uuid.NewString()
	jobInput := func() map[string]any {
		return map[string]any{"box_id": boxID, "issue": "DEV-1", "provider": "claude-code", "repository_full_name": "TerraScale/strifira", "base_branch": "main"}
	}
	for _, test := range []struct {
		name string
		tool string
		args map[string]any
	}{
		{"invalid-box-id", "rotate_dev_box_token", map[string]any{"box_id": "wrong"}},
		{"nil-box-id", "revoke_dev_box", map[string]any{"box_id": uuid.Nil.String()}},
		{"compact-box-id", "update_dev_box", map[string]any{"box_id": strings.ReplaceAll(boxID, "-", ""), "enabled": true}},
		{"invalid-job-id", "get_dev_box_job", map[string]any{"job_id": "wrong"}},
		{"name-too-long", "register_dev_box", map[string]any{"name": strings.Repeat("a", 101)}},
		{"name-control", "register_dev_box", map[string]any{"name": "strifira\x00dev"}},
		{"empty-update", "update_dev_box", map[string]any{"box_id": boxID}},
		{"empty-rename", "update_dev_box", map[string]any{"box_id": boxID, "name": " "}},
		{"negative-log-cursor", "get_dev_box_job_logs", map[string]any{"job_id": jobID, "after_id": -1}},
		{"excessive-log-page", "get_dev_box_job_logs", map[string]any{"job_id": jobID, "limit": 501}},
		{"negative-page", "list_dev_box_jobs", map[string]any{"page": -1}},
		{"excessive-page", "list_dev_box_jobs", map[string]any{"page": 1000001}},
		{"excessive-per-page", "list_dev_box_jobs", map[string]any{"per_page": 101}},
		{"invalid-box-filter", "list_dev_box_jobs", map[string]any{"box_id": "wrong"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.args["workspace"] = env.workspace.Slug
			require.True(t, callTool(t, session, test.tool, test.args).IsError)
		})
	}
	for _, test := range []struct {
		name, field string
		value       any
	}{
		{"missing-repo", "repository_full_name", ""},
		{"missing-branch", "base_branch", ""},
		{"oversized-issue", "issue", strings.Repeat("a", 101)},
		{"unknown-issue", "issue", "DEV-404"},
		{"unknown-provider", "provider", "other"},
		{"oversized-prompt", "prompt", strings.Repeat("a", 50001)},
		{"runtime-too-short", "max_runtime_seconds", 29},
		{"runtime-too-long", "max_runtime_seconds", 86401},
		{"too-many-test-args", "test_command", strings.Fields(strings.Repeat("test ", 33))},
		{"empty-test-arg", "test_command", []string{"test", ""}},
		{"oversized-test-arg", "test_command", []string{strings.Repeat("a", 4097)}},
		{"oversized-criterion", "acceptance_criteria", []string{strings.Repeat("a", 2001)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := jobInput()
			args["workspace"] = env.workspace.Slug
			args[test.field] = test.value
			require.True(t, callTool(t, session, "create_dev_box_job", args).IsError)
		})
	}
	assert.Empty(t, svc.Calls)
}

func TestMCPDevBoxForeignIssueRejected(t *testing.T) {
	svc := &devBoxMCPServiceMock{}
	env := devBoxMCPEnv(t, svc)
	env.issueRepo.issue.WorkspaceID = uuid.New()
	session := connectMCP(t, env.mcpTestEnv, env.fullToken)
	for _, ref := range []string{env.issueRepo.issue.Identifier, env.issueRepo.issue.ID.String()} {
		result := callTool(t, session, "create_dev_box_job", map[string]any{"workspace": env.workspace.Slug, "box_id": uuid.NewString(), "issue": ref,
			"repository_full_name": "TerraScale/strifira", "base_branch": "main", "provider": "claude-code"})
		require.True(t, result.IsError)
		assert.Contains(t, devBoxMCPJSON(t, result), "issue not found")
	}
	assert.Empty(t, svc.Calls)
}

func TestMCPDevBoxServiceErrorsRedactedAndBounded(t *testing.T) {
	secret := "sfw_" + strings.Repeat("c", 64)
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"upstream", fmt.Errorf("connection password=%s: %s", secret, strings.Repeat("x", 50000)), "operation failed"},
		{"wrapped-forbidden", fmt.Errorf("credential %s: %w", secret, service.ErrDevBoxForbidden), "access denied"},
		{"wrapped-invalid", fmt.Errorf("credential %s: %w", secret, service.ErrInvalidDevBoxInput), "invalid development box request"},
		{"wrapped-conflict", fmt.Errorf("credential %s: %w", secret, service.ErrDevBoxConflict), "conflicts"},
		{"missing", service.ErrDevBoxNotFound, "not found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc := &devBoxMCPServiceMock{}
			env := devBoxMCPEnv(t, svc)
			session := connectMCP(t, env.mcpTestEnv, env.fullToken)
			svc.On("List", mock.Anything, env.workspace.ID, env.user.ID).Return(nil, test.err).Once()
			result := callTool(t, session, "list_dev_boxes", map[string]any{"workspace": env.workspace.Slug})
			require.True(t, result.IsError)
			text := devBoxMCPJSON(t, result)
			assert.Contains(t, text, test.want)
			assert.NotContains(t, text, secret)
			assert.Less(t, len(text), 1000)
			svc.AssertExpectations(t)
		})
	}
	assert.Nil(t, devBoxToolError(nil))
	assert.EqualError(t, devBoxToolError(errors.New("private failure")), "development box operation failed")
}
