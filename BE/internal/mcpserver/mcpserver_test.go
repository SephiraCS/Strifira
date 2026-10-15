package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// --- Mocks ---

type mcpUserRepo struct {
	mock.Mock
}

func (m *mcpUserRepo) Create(ctx context.Context, user *domain.User) error {
	args := m.Called(ctx, user)
	return args.Error(0)
}

func (m *mcpUserRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.User), args.Error(1)
}

func (m *mcpUserRepo) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	args := m.Called(ctx, email)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.User), args.Error(1)
}

func (m *mcpUserRepo) Update(ctx context.Context, user *domain.User) error {
	args := m.Called(ctx, user)
	return args.Error(0)
}

func (m *mcpUserRepo) ListByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]domain.User, error) {
	args := m.Called(ctx, ids)
	if users, ok := args.Get(0).(map[uuid.UUID]domain.User); ok {
		return users, args.Error(1)
	}
	return map[uuid.UUID]domain.User{}, args.Error(1)
}

type mcpWorkspaceRepo struct {
	mock.Mock
}

func (m *mcpWorkspaceRepo) Create(ctx context.Context, ws *domain.Workspace) error {
	args := m.Called(ctx, ws)
	return args.Error(0)
}

func (m *mcpWorkspaceRepo) CreateWithMemberAndLabels(ctx context.Context, ws *domain.Workspace, member *domain.WorkspaceMember, labels []domain.Label) error {
	args := m.Called(ctx, ws, member, labels)
	return args.Error(0)
}

func (m *mcpWorkspaceRepo) GetBySlug(ctx context.Context, slug string) (*domain.Workspace, error) {
	args := m.Called(ctx, slug)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Workspace), args.Error(1)
}

func (m *mcpWorkspaceRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Workspace, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Workspace), args.Error(1)
}

func (m *mcpWorkspaceRepo) Update(ctx context.Context, ws *domain.Workspace) error {
	args := m.Called(ctx, ws)
	return args.Error(0)
}

func (m *mcpWorkspaceRepo) Delete(ctx context.Context, id uuid.UUID) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *mcpWorkspaceRepo) ListByUser(ctx context.Context, userID uuid.UUID) ([]domain.Workspace, error) {
	args := m.Called(ctx, userID)
	if ws, ok := args.Get(0).([]domain.Workspace); ok {
		return ws, args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *mcpWorkspaceRepo) AddMember(ctx context.Context, member *domain.WorkspaceMember) error {
	args := m.Called(ctx, member)
	return args.Error(0)
}

func (m *mcpWorkspaceRepo) GetMember(ctx context.Context, workspaceID, userID uuid.UUID) (*domain.WorkspaceMember, error) {
	args := m.Called(ctx, workspaceID, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.WorkspaceMember), args.Error(1)
}

func (m *mcpWorkspaceRepo) ListMembers(ctx context.Context, workspaceID uuid.UUID) ([]domain.WorkspaceMember, error) {
	args := m.Called(ctx, workspaceID)
	if members, ok := args.Get(0).([]domain.WorkspaceMember); ok {
		return members, args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *mcpWorkspaceRepo) ListMembersWithUsers(ctx context.Context, workspaceID uuid.UUID) ([]domain.WorkspaceMemberWithUser, error) {
	args := m.Called(ctx, workspaceID)
	if members, ok := args.Get(0).([]domain.WorkspaceMemberWithUser); ok {
		return members, args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *mcpWorkspaceRepo) UpdateMemberRole(ctx context.Context, workspaceID, userID uuid.UUID, role string) error {
	args := m.Called(ctx, workspaceID, userID, role)
	return args.Error(0)
}

func (m *mcpWorkspaceRepo) RemoveMember(ctx context.Context, workspaceID, userID uuid.UUID) error {
	args := m.Called(ctx, workspaceID, userID)
	return args.Error(0)
}

func (m *mcpWorkspaceRepo) CountMembersByRole(ctx context.Context, workspaceID uuid.UUID, role string) (int, error) {
	args := m.Called(ctx, workspaceID, role)
	return args.Int(0), args.Error(1)
}

// apiKeyRepoStub is a tiny in-memory APIKeyRepo so the real middleware and
// service run against the test's created keys.
type apiKeyRepoStub struct {
	keysByHash map[string]*domain.APIKey
}

func (s *apiKeyRepoStub) Create(ctx context.Context, key *domain.APIKey) error {
	if s.keysByHash == nil {
		s.keysByHash = map[string]*domain.APIKey{}
	}
	stored := *key
	s.keysByHash[key.TokenHash] = &stored
	return nil
}

func (s *apiKeyRepoStub) ListByUser(ctx context.Context, userID uuid.UUID) ([]domain.APIKey, error) {
	return nil, nil
}

func (s *apiKeyRepoStub) GetByID(ctx context.Context, id uuid.UUID) (*domain.APIKey, error) {
	return nil, nil
}

func (s *apiKeyRepoStub) GetByHash(ctx context.Context, tokenHash string) (*domain.APIKey, error) {
	return s.keysByHash[tokenHash], nil
}

func (s *apiKeyRepoStub) Revoke(ctx context.Context, id, userID uuid.UUID) error {
	for _, key := range s.keysByHash {
		if key.ID == id {
			now := time.Now()
			key.RevokedAt = &now
		}
	}
	return nil
}

func (s *apiKeyRepoStub) TouchLastUsed(ctx context.Context, id uuid.UUID) error {
	return nil
}

// stubIssueRepo embeds the IssueRepo interface and only implements the
// lookup the tests exercise; unresolved methods panic loudly if reached.
type stubIssueRepo struct {
	repository.IssueRepo
}

func (stubIssueRepo) GetByIdentifier(ctx context.Context, workspaceID uuid.UUID, identifier string) (*domain.Issue, error) {
	return nil, nil
}

// --- Test fixture ---

type mcpTestEnv struct {
	server     *httptest.Server
	user       *domain.User
	workspace  *domain.Workspace
	fullToken  string
	readToken  string
	keyStore   *apiKeyRepoStub
	users      *mcpUserRepo
	workspaces *mcpWorkspaceRepo
}

func newMCPTestEnv(t *testing.T) *mcpTestEnv {
	t.Helper()
	ctx := context.Background()

	user := &domain.User{ID: uuid.New(), Email: "owner@example.test", Name: "Owner", DisplayName: "Owner", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	ws := &domain.Workspace{ID: uuid.New(), Name: "Acme", Slug: "acme", OwnerID: user.ID, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	member := &domain.WorkspaceMember{WorkspaceID: ws.ID, UserID: user.ID, Role: domain.RoleOwner, CreatedAt: time.Now()}

	users := &mcpUserRepo{}
	users.On("GetByID", mock.Anything, user.ID).Return(user, nil).Maybe()
	users.On("ListByIDs", mock.Anything, mock.Anything).Return(map[uuid.UUID]domain.User{}, nil).Maybe()

	workspaces := &mcpWorkspaceRepo{}
	workspaces.On("GetBySlug", mock.Anything, "acme").Return(ws, nil).Maybe()
	workspaces.On("GetBySlug", mock.Anything, mock.Anything).Return(nil, nil).Maybe()
	workspaces.On("GetMember", mock.Anything, ws.ID, user.ID).Return(member, nil).Maybe()
	workspaces.On("GetMember", mock.Anything, mock.Anything, mock.Anything).Return(nil, nil).Maybe()
	workspaces.On("ListByUser", mock.Anything, user.ID).Return([]domain.Workspace{*ws}, nil).Maybe()
	workspaces.On("ListMembersWithUsers", mock.Anything, ws.ID).Return([]domain.WorkspaceMemberWithUser{{
		WorkspaceID: ws.ID, UserID: user.ID, Role: domain.RoleOwner,
		Email: user.Email, Name: user.Name, DisplayName: user.DisplayName, CreatedAt: member.CreatedAt,
	}}, nil).Maybe()

	keyStore := &apiKeyRepoStub{}
	apiKeySvc := service.NewAPIKeyService(keyStore, users)
	_, fullToken, err := apiKeySvc.Create(ctx, user.ID, dto.CreateAPIKeyRequest{Name: "full"})
	require.NoError(t, err)
	_, readToken, err := apiKeySvc.Create(ctx, user.ID, dto.CreateAPIKeyRequest{Name: "reader", ReadOnly: true})
	require.NoError(t, err)

	handler := NewHTTPHandler(Deps{
		Version:     "test",
		Users:       users,
		Workspaces:  workspaces,
		IssueSvc:    service.NewIssueService(stubIssueRepo{}, nil, nil, nil, nil, nil),
		CommentSvc:  service.NewCommentService(nil, nil, nil, nil),
		RelationSvc: service.NewIssueRelationService(nil, nil),
	})

	e := echo.New()
	e.Any("/mcp", echo.WrapHandler(handler), middleware.APIKeyAuth(apiKeySvc))
	server := httptest.NewServer(e)
	t.Cleanup(server.Close)

	return &mcpTestEnv{
		server: server, user: user, workspace: ws,
		fullToken: fullToken, readToken: readToken,
		keyStore: keyStore, users: users, workspaces: workspaces,
	}
}

// bearerClient injects the API key into every request.
type bearerClient struct{ token string }

func (b bearerClient) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func connectMCP(t *testing.T, env *mcpTestEnv, token string) *mcp.ClientSession {
	t.Helper()
	transport := &mcp.StreamableClientTransport{
		Endpoint:   env.server.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerClient{token}, Timeout: 10 * time.Second},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "strifira-test", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), transport, nil)
	require.NoError(t, err, "initialize over streamable HTTP must succeed with a valid key")
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	return res
}

func structured(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	require.False(t, res.IsError, "tool %s should not error", "call")
	require.NotNil(t, res.StructuredContent)
	m, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok, "structured content should be an object, got %T", res.StructuredContent)
	return m
}

// --- Tests ---

func TestMCPToolRegistrationAndHandshake(t *testing.T) {
	env := newMCPTestEnv(t)
	session := connectMCP(t, env, env.fullToken)

	res, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)

	want := []string{
		// identity + catalog
		"get_me", "list_workspaces", "get_workspace", "list_members",
		"list_teams", "list_team_states", "list_projects", "list_labels",
		// issues + comments
		"list_issues", "get_issue", "create_issue", "update_issue",
		"delete_issue", "list_comments", "create_comment",
		// workspace + members
		"create_workspace", "update_workspace", "invite_member",
		"update_member_role", "remove_member",
		// teams, workflow states, projects, labels
		"create_team", "update_team", "delete_team",
		"create_team_state", "update_team_state", "delete_team_state",
		"create_project", "update_project", "delete_project",
		"create_label", "update_label", "delete_label",
		// issue extras
		"duplicate_issue", "list_issue_history", "list_relations",
		"create_relation", "delete_relation", "resolve_comment", "reopen_comment",
		// cycles, templates, notifications, analytics
		"list_cycles", "list_issue_templates", "list_notifications",
		"mark_notifications_read", "workspace_analytics",
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	for _, name := range want {
		assert.True(t, got[name], "tool %s must be advertised", name)
	}
}

func TestMCPGetMe(t *testing.T) {
	env := newMCPTestEnv(t)
	session := connectMCP(t, env, env.fullToken)

	out := structured(t, callTool(t, session, "get_me", map[string]any{}))
	userObj, ok := out["user"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, env.user.Email, userObj["email"])
	assert.Equal(t, env.user.ID.String(), userObj["id"])
	scopes, ok := out["scopes"].([]any)
	require.True(t, ok)
	assert.Contains(t, scopes, "write")
}

func TestMCPListAndGetWorkspace(t *testing.T) {
	env := newMCPTestEnv(t)
	session := connectMCP(t, env, env.fullToken)

	list, ok := callTool(t, session, "list_workspaces", map[string]any{}).StructuredContent.([]any)
	require.True(t, ok, "list_workspaces returns an array")
	require.Len(t, list, 1)
	first, _ := list[0].(map[string]any)
	assert.Equal(t, "acme", first["slug"])
	assert.Equal(t, domain.RoleOwner, first["role"])

	out := structured(t, callTool(t, session, "get_workspace", map[string]any{"workspace": "acme"}))
	assert.Equal(t, "acme", out["slug"])

	// Non-member workspaces read as errors, not leaks
	res := callTool(t, session, "get_workspace", map[string]any{"workspace": "nope"})
	assert.True(t, res.IsError, "unknown workspace must surface as a tool error")
}

func TestMCPListMembers(t *testing.T) {
	env := newMCPTestEnv(t)
	session := connectMCP(t, env, env.fullToken)

	list, ok := callTool(t, session, "list_members", map[string]any{"workspace": "acme"}).StructuredContent.([]any)
	require.True(t, ok)
	require.Len(t, list, 1)
	member, _ := list[0].(map[string]any)
	assert.Equal(t, domain.RoleOwner, member["role"])
	assert.Equal(t, env.user.Email, member["email"])
}

func TestMCPReadOnlyKeyCannotWrite(t *testing.T) {
	env := newMCPTestEnv(t)
	session := connectMCP(t, env, env.readToken)

	// get_me still works and reports the read-only scope
	out := structured(t, callTool(t, session, "get_me", map[string]any{}))
	scopes, _ := out["scopes"].([]any)
	assert.NotContains(t, scopes, "write")

	// Every write tool refuses before touching the issue service
	for _, tool := range []string{"create_issue", "update_issue", "delete_issue", "create_comment"} {
		args := map[string]any{"workspace": "acme"}
		switch tool {
		case "create_issue":
			args["title"] = "x"
			args["team_key"] = "ENG"
		case "update_issue", "delete_issue":
			args["identifier"] = "ENG-1"
		case "create_comment":
			args["identifier"] = "ENG-1"
			args["body"] = "x"
		}
		res := callTool(t, session, tool, args)
		assert.True(t, res.IsError, "%s must fail for a read-only key", tool)
		text := ""
		if len(res.Content) > 0 {
			if tc, ok := res.Content[0].(*mcp.TextContent); ok {
				text = tc.Text
			}
		}
		assert.Contains(t, text, "read-only", "%s error should explain the scope refusal", tool)
	}
}

func TestMCPAuthFailures(t *testing.T) {
	env := newMCPTestEnv(t)

	post := func(auth string) int {
		body := map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "initialize",
			"params": map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{},
				"clientInfo":      map[string]any{"name": "t", "version": "0"},
			},
		}
		buf, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, env.server.URL+"/mcp", bytes.NewReader(buf))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		return resp.StatusCode
	}

	assert.Equal(t, http.StatusUnauthorized, post(""), "missing key must 401")
	assert.Equal(t, http.StatusUnauthorized, post("Bearer strifira_bogus"), "unknown key must 401")
	assert.Equal(t, http.StatusOK, post("Bearer "+env.fullToken), "valid key passes the gate")

	// A revoked key stops authenticating immediately (stateless mode
	// re-authenticates every request).
	session := connectMCP(t, env, env.fullToken)
	for _, key := range env.keyStore.keysByHash {
		now := time.Now()
		key.RevokedAt = &now
	}
	_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_me", Arguments: map[string]any{}})
	require.Error(t, err, "revoked key must fail the next request")
}

func TestMCPPublicHostHeaderAccepted(t *testing.T) {
	env := newMCPTestEnv(t)

	// Behind the in-container proxy the connection local address is
	// loopback while the Host header is the public domain; the SDK's
	// DNS-rebinding guard must stay disabled for that combination.
	body := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "t", "version": "0"},
		},
	}
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, env.server.URL+"/mcp", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+env.fullToken)
	req.Host = "strifira.mariogk.com"

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "public Host header must pass the rebinding guard")
}

func TestMCPGetIssueNotFound(t *testing.T) {
	env := newMCPTestEnv(t)
	session := connectMCP(t, env, env.fullToken)

	// The stub issue repo resolves nothing, so a missing issue must surface
	// as a clean tool error rather than a transport failure.
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get_issue", Arguments: map[string]any{"workspace": "acme", "identifier": "ENG-404"},
	})
	require.NoError(t, err)
	assert.True(t, res.IsError)
}
