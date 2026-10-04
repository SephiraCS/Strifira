package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ku/ku-backend/internal/dto"
)

// crossWorkspaceFixture: owner A manages workspace A; owner B owns workspace
// B with a team ENG and two issues. Every test below proves A cannot touch
// B's rows through A's workspace routes.
type crossWorkspaceFixture struct {
	env      *integrationEnv
	a, b     integrationUser
	aTeam    string
	bTeam    string
	bIssue1  string
	bIssue2  string
	aIssue   string
	aProject string
	bProject string
}

func newCrossWorkspaceFixture(t *testing.T) *crossWorkspaceFixture {
	env := newIntegrationEnv(t)
	f := &crossWorkspaceFixture{env: env, a: env.seedOwner("alpha"), b: env.seedOwner("bravo")}
	f.aTeam = env.createTeam(f.a, "Alpha", "ALP")
	f.bTeam = env.createTeam(f.b, "Bravo", "BRV")
	f.aIssue = env.createIssue(f.a, f.aTeam, "alpha issue")
	f.bIssue1 = env.createIssue(f.b, f.bTeam, "bravo one")
	f.bIssue2 = env.createIssue(f.b, f.bTeam, "bravo two")
	f.aProject = uuid.NewString()
	f.bProject = uuid.NewString()
	_, err := env.db.Exec(`INSERT INTO projects (id,workspace_id,name) VALUES ($1,$2,'Alpha project'),($3,$4,'Bravo project')`,
		f.aProject, f.a.WorkspaceID, f.bProject, f.b.WorkspaceID)
	require.NoError(t, err)
	return f
}

func TestCrossWorkspace_WebhookUpdateDeleteReturnNotFound(t *testing.T) {
	f := newCrossWorkspaceFixture(t)
	env := f.env
	hookID := uuid.New()
	_, err := env.db.Exec(`INSERT INTO webhooks (id,workspace_id,url,secret,events,is_active) VALUES ($1,$2,'https://bravo.example.test/hook','x',$3,true)`,
		hookID, f.b.WorkspaceID, pq.StringArray{"issue.created"})
	require.NoError(t, err)

	rec := env.do(http.MethodPatch, f.a.path("/webhooks/"+hookID.String()), f.a.Token, map[string]any{"is_active": false})
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	rec = env.do(http.MethodDelete, f.a.path("/webhooks/"+hookID.String()), f.a.Token, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	var active bool
	require.NoError(t, env.db.Get(&active, `SELECT is_active FROM webhooks WHERE id=$1`, hookID))
	assert.True(t, active, "foreign webhook must be untouched")

	// The owner of B can still manage it through B's workspace.
	env.mustJSON(http.MethodPatch, f.b.path("/webhooks/"+hookID.String()), f.b.Token, map[string]any{"is_active": false}, http.StatusOK)
	env.mustJSON(http.MethodDelete, f.b.path("/webhooks/"+hookID.String()), f.b.Token, nil, http.StatusOK)
	assert.Zero(t, env.count(`SELECT count(*) FROM webhooks WHERE id=$1`, hookID))
}

func TestCrossWorkspace_RelationDeleteReturnsNotFound(t *testing.T) {
	f := newCrossWorkspaceFixture(t)
	env := f.env
	rel := env.mustJSON(http.MethodPost, f.b.path("/issues/"+f.bIssue1+"/relations"), f.b.Token,
		map[string]any{"related_identifier": f.bIssue2, "type": "blocking"}, http.StatusCreated)
	relID := rel["id"].(string)

	rec := env.do(http.MethodDelete, f.a.path("/issues/"+f.aIssue+"/relations/"+relID), f.a.Token, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Equal(t, 2, env.count(`SELECT count(*) FROM issue_relations`), "relation and its inverse must survive")

	// Deleting from the owning workspace removes both rows.
	env.mustJSON(http.MethodDelete, f.b.path("/issues/"+f.bIssue1+"/relations/"+relID), f.b.Token, nil, http.StatusOK)
	assert.Zero(t, env.count(`SELECT count(*) FROM issue_relations`))
}

func TestCrossWorkspace_TemplateGetUpdateDeleteReturnNotFound(t *testing.T) {
	f := newCrossWorkspaceFixture(t)
	env := f.env
	tmpl := env.mustJSON(http.MethodPost, f.b.path("/issue-templates"), f.b.Token, map[string]any{"title": "Bravo template"}, http.StatusCreated)
	id := tmpl["id"].(string)

	assert.Equal(t, http.StatusNotFound, env.do(http.MethodGet, f.a.path("/issue-templates/"+id), f.a.Token, nil).Code)
	assert.Equal(t, http.StatusNotFound, env.do(http.MethodPatch, f.a.path("/issue-templates/"+id), f.a.Token, map[string]any{"title": "hijacked"}).Code)
	assert.Equal(t, http.StatusNotFound, env.do(http.MethodDelete, f.a.path("/issue-templates/"+id), f.a.Token, nil).Code)

	var title string
	require.NoError(t, env.db.Get(&title, `SELECT title FROM issue_templates WHERE id=$1`, id))
	assert.Equal(t, "Bravo template", title)

	env.mustJSON(http.MethodPatch, f.b.path("/issue-templates/"+id), f.b.Token, map[string]any{"title": "Renamed"}, http.StatusOK)
	env.mustJSON(http.MethodDelete, f.b.path("/issue-templates/"+id), f.b.Token, nil, http.StatusOK)
	assert.Zero(t, env.count(`SELECT count(*) FROM issue_templates WHERE id=$1`, id))
}

func TestCrossWorkspace_CycleRoutesReturnNotFound(t *testing.T) {
	f := newCrossWorkspaceFixture(t)
	env := f.env
	cycle := env.mustJSON(http.MethodPost, f.b.path("/teams/"+f.bTeam+"/cycles"), f.b.Token,
		map[string]any{"name": "Sprint 1", "start_date": "2026-01-01", "end_date": "2026-01-14"}, http.StatusCreated)
	id := cycle["id"].(string)

	// Through B's team id in A's workspace...
	for _, path := range []string{
		"/teams/" + f.bTeam + "/cycles/" + id,
		// ...and through A's own team id (cycle belongs to another team).
		"/teams/" + f.aTeam + "/cycles/" + id,
	} {
		assert.Equal(t, http.StatusNotFound, env.do(http.MethodGet, f.a.path(path), f.a.Token, nil).Code, path)
		assert.Equal(t, http.StatusNotFound, env.do(http.MethodPatch, f.a.path(path), f.a.Token, map[string]any{"name": "hijacked"}).Code, path)
		assert.Equal(t, http.StatusNotFound, env.do(http.MethodPost, f.a.path(path+"/complete"), f.a.Token, map[string]any{}).Code, path)
		assert.Equal(t, http.StatusNotFound, env.do(http.MethodDelete, f.a.path(path), f.a.Token, nil).Code, path)
	}
	// Listing or creating cycles of B's team from A's workspace is refused.
	assert.Equal(t, http.StatusNotFound, env.do(http.MethodGet, f.a.path("/teams/"+f.bTeam+"/cycles"), f.a.Token, nil).Code)
	assert.Equal(t, http.StatusNotFound, env.do(http.MethodPost, f.a.path("/teams/"+f.bTeam+"/cycles"), f.a.Token,
		map[string]any{"name": "Sprint 2", "start_date": "2026-02-01", "end_date": "2026-02-14"}).Code)

	var name, status string
	require.NoError(t, env.db.QueryRow(`SELECT name, status FROM cycles WHERE id=$1`, id).Scan(&name, &status))
	assert.Equal(t, "Sprint 1", name)
	assert.Equal(t, "upcoming", status)
	assert.Equal(t, 1, env.count(`SELECT count(*) FROM cycles`))

	env.mustJSON(http.MethodPatch, f.b.path("/teams/"+f.bTeam+"/cycles/"+id), f.b.Token, map[string]any{"name": "Renamed"}, http.StatusOK)
	env.mustJSON(http.MethodDelete, f.b.path("/teams/"+f.bTeam+"/cycles/"+id), f.b.Token, nil, http.StatusOK)
	assert.Zero(t, env.count(`SELECT count(*) FROM cycles`))
}

func TestCrossWorkspace_TeamStatusRoutesReturnNotFound(t *testing.T) {
	f := newCrossWorkspaceFixture(t)
	env := f.env
	var bStatus string
	require.NoError(t, env.db.Get(&bStatus, `SELECT id FROM team_statuses WHERE team_id=$1 AND slug='todo'`, f.bTeam))
	bCustom := env.mustJSON(http.MethodPost, f.b.path("/teams/"+f.bTeam+"/statuses"), f.b.Token,
		map[string]any{"name": "QA", "category": "started"}, http.StatusCreated)["id"].(string)

	// B's team through A's workspace.
	assert.Equal(t, http.StatusNotFound, env.do(http.MethodGet, f.a.path("/teams/"+f.bTeam+"/statuses"), f.a.Token, nil).Code)
	assert.Equal(t, http.StatusNotFound, env.do(http.MethodPost, f.a.path("/teams/"+f.bTeam+"/statuses"), f.a.Token,
		map[string]any{"name": "Injected", "category": "started"}).Code)
	for _, teamID := range []string{f.bTeam, f.aTeam} {
		path := f.a.path("/teams/" + teamID + "/statuses/" + bCustom)
		assert.Equal(t, http.StatusNotFound, env.do(http.MethodPatch, path, f.a.Token, map[string]any{"name": "hijacked"}).Code, teamID)
		assert.Equal(t, http.StatusNotFound, env.do(http.MethodDelete, path, f.a.Token, nil).Code, teamID)
	}
	var name string
	require.NoError(t, env.db.Get(&name, `SELECT name FROM team_statuses WHERE id=$1`, bCustom))
	assert.Equal(t, "QA", name)
	assert.Zero(t, env.count(`SELECT count(*) FROM team_statuses WHERE name='Injected'`))

	// A's own status cannot be attached to B's project, on create or update.
	assert.Equal(t, http.StatusNotFound, env.do(http.MethodPost, f.a.path("/teams/"+f.aTeam+"/statuses"), f.a.Token,
		map[string]any{"name": "Leaky", "category": "started", "project_ids": []string{f.bProject}}).Code)
	assert.Zero(t, env.count(`SELECT count(*) FROM team_statuses WHERE name='Leaky'`))
	aCustom := env.mustJSON(http.MethodPost, f.a.path("/teams/"+f.aTeam+"/statuses"), f.a.Token,
		map[string]any{"name": "Review", "category": "started", "project_ids": []string{f.aProject}}, http.StatusCreated)["id"].(string)
	assert.Equal(t, http.StatusNotFound, env.do(http.MethodPatch, f.a.path("/teams/"+f.aTeam+"/statuses/"+aCustom), f.a.Token,
		map[string]any{"project_ids": []string{f.bProject}}).Code)
	assert.Zero(t, env.count(`SELECT count(*) FROM project_status_visibility WHERE project_id=$1`, f.bProject),
		"no visibility rows may be written for a foreign project")
	assert.Equal(t, 1, env.count(`SELECT count(*) FROM project_status_visibility WHERE project_id=$1 AND status_id=$2`, f.aProject, aCustom))

	// B still manages its own statuses.
	env.mustJSON(http.MethodPatch, f.b.path("/teams/"+f.bTeam+"/statuses/"+bCustom), f.b.Token, map[string]any{"name": "QA2"}, http.StatusOK)
	env.mustJSON(http.MethodDelete, f.b.path("/teams/"+f.bTeam+"/statuses/"+bCustom), f.b.Token, nil, http.StatusOK)
	assert.Equal(t, 1, env.count(`SELECT count(*) FROM team_statuses WHERE id=$1`, bStatus))
}

func TestCrossWorkspace_SharedLinkUpdateDeleteReturnNotFound(t *testing.T) {
	f := newCrossWorkspaceFixture(t)
	env := f.env
	link := env.mustJSON(http.MethodPost, f.b.path("/shared-links"), f.b.Token, map[string]any{"scope": "workspace"}, http.StatusCreated)
	id := link["id"].(string)

	assert.Equal(t, http.StatusNotFound, env.do(http.MethodPatch, f.a.path("/shared-links/"+id), f.a.Token, map[string]any{"is_active": false}).Code)
	assert.Equal(t, http.StatusNotFound, env.do(http.MethodDelete, f.a.path("/shared-links/"+id), f.a.Token, nil).Code)
	var active bool
	require.NoError(t, env.db.Get(&active, `SELECT is_active FROM shared_links WHERE id=$1`, id))
	assert.True(t, active)

	// A link scoped to another workspace's team cannot be created either.
	rec := env.do(http.MethodPost, f.a.path("/shared-links"), f.a.Token, map[string]any{"scope": "team", "scope_id": f.bTeam})
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	env.mustJSON(http.MethodPatch, f.b.path("/shared-links/"+id), f.b.Token, map[string]any{"is_active": false}, http.StatusOK)
	env.mustJSON(http.MethodDelete, f.b.path("/shared-links/"+id), f.b.Token, nil, http.StatusOK)
}

// bearerTransport adds an API key to every MCP request.
type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// The MCP tools take bare relation/state IDs plus a workspace slug; IDs of
// another workspace must be refused there too.
func TestCrossWorkspace_MCPRelationAndStateToolsRefuseForeignIDs(t *testing.T) {
	f := newCrossWorkspaceFixture(t)
	env := f.env
	rel := env.mustJSON(http.MethodPost, f.b.path("/issues/"+f.bIssue1+"/relations"), f.b.Token,
		map[string]any{"related_identifier": f.bIssue2, "type": "blocking"}, http.StatusCreated)
	bState := env.mustJSON(http.MethodPost, f.b.path("/teams/"+f.bTeam+"/statuses"), f.b.Token,
		map[string]any{"name": "QA", "category": "started"}, http.StatusCreated)["id"].(string)

	_, key, err := env.apiKeySvc.Create(t.Context(), f.a.ID, dto.CreateAPIKeyRequest{Name: "alpha"})
	require.NoError(t, err)
	server := httptest.NewServer(env.server)
	t.Cleanup(server.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "cross-workspace", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:   server.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{key}, Timeout: 10 * time.Second},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
		require.NoError(t, err)
		return res
	}

	assert.True(t, call("delete_relation", map[string]any{"workspace": f.a.Slug, "relation_id": rel["id"]}).IsError)
	assert.Equal(t, 2, env.count(`SELECT count(*) FROM issue_relations`))
	assert.True(t, call("update_team_state", map[string]any{"workspace": f.a.Slug, "state_id": bState, "name": "hijacked"}).IsError)
	assert.True(t, call("delete_team_state", map[string]any{"workspace": f.a.Slug, "state_id": bState}).IsError)
	var name string
	require.NoError(t, env.db.Get(&name, `SELECT name FROM team_statuses WHERE id=$1`, bState))
	assert.Equal(t, "QA", name)

	// The same tools still work on the caller's own workspace rows.
	aState := env.mustJSON(http.MethodPost, f.a.path("/teams/"+f.aTeam+"/statuses"), f.a.Token,
		map[string]any{"name": "Review", "category": "started"}, http.StatusCreated)["id"].(string)
	assert.False(t, call("update_team_state", map[string]any{"workspace": f.a.Slug, "state_id": aState, "name": "Reviewed"}).IsError)
	assert.False(t, call("delete_team_state", map[string]any{"workspace": f.a.Slug, "state_id": aState}).IsError)
	aRel := env.mustJSON(http.MethodPost, f.a.path("/issues/"+f.aIssue+"/relations"), f.a.Token,
		map[string]any{"related_identifier": env.createIssue(f.a, f.aTeam, "alpha two"), "type": "blocking"}, http.StatusCreated)
	assert.False(t, call("delete_relation", map[string]any{"workspace": f.a.Slug, "relation_id": aRel["id"]}).IsError)
	assert.Equal(t, 2, env.count(`SELECT count(*) FROM issue_relations`), "only B's pair remains")
}
