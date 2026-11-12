package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func TestProjectRename_PreservesIdentityAndLinks(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("project-rename")
	teamID := env.createTeam(owner, "Engineering", "ENG")
	project := env.mustJSON(http.MethodPost, owner.path("/projects"), owner.Token,
		map[string]any{"name": "Launch", "team_id": teamID, "description": "Keep this description"}, http.StatusCreated)
	id := project["id"].(string)
	_, err := env.db.Exec(`UPDATE projects SET lead_id=$1, start_date='2026-01-01', target_date='2026-12-31', sort_order=42 WHERE id=$2`, owner.ID, id)
	require.NoError(t, err)
	_, err = env.db.Exec(`INSERT INTO project_members (project_id,user_id) VALUES ($1,$2)`, id, owner.ID)
	require.NoError(t, err)
	issue := env.mustJSON(http.MethodPost, owner.path("/issues"), owner.Token,
		map[string]any{"title": "Linked issue", "team_id": teamID, "project_id": id}, http.StatusCreated)
	doc := env.mustJSON(http.MethodPost, owner.path("/docs"), owner.Token,
		map[string]any{"title": "Linked document"}, http.StatusCreated)
	linked := env.do(http.MethodPost, owner.path("/docs/"+doc["id"].(string)+"/issues"), owner.Token,
		map[string]any{"identifier": issue["identifier"]})
	require.Equal(t, http.StatusOK, linked.Code, linked.Body.String())
	env.mustJSON(http.MethodPost, owner.path("/shared-links"), owner.Token,
		map[string]any{"scope": "project", "scope_id": id}, http.StatusCreated)

	// Compare complete rows, including resource IDs, content and timestamps.
	snapshot := func() string {
		var value string
		require.NoError(t, env.db.Get(&value, `SELECT jsonb_build_object(
			'project', (SELECT to_jsonb(p)-'name'-'updated_at' FROM projects p WHERE id=$1),
			'issues', (SELECT jsonb_agg(to_jsonb(i)) FROM issues i WHERE project_id=$1),
			'members', (SELECT jsonb_agg(to_jsonb(m)) FROM project_members m WHERE project_id=$1),
			'documents', (SELECT jsonb_agg(to_jsonb(d)) FROM documents d WHERE workspace_id=$2),
			'document_links', (SELECT jsonb_agg(to_jsonb(l)) FROM document_issue_links l),
			'shared_links', (SELECT jsonb_agg(to_jsonb(s)) FROM shared_links s WHERE workspace_id=$2)
		)::text`, id, owner.WorkspaceID))
		return value
	}
	before := snapshot()
	updated := env.mustJSON(http.MethodPatch, owner.path("/projects/"+id), owner.Token,
		map[string]any{"name": "  Portal renovado  "}, http.StatusOK)
	require.Equal(t, id, updated["id"])
	require.Equal(t, "Portal renovado", updated["name"])
	require.JSONEq(t, before, snapshot())
	fetched := env.mustJSON(http.MethodGet, owner.path("/projects/"+id), owner.Token, nil, http.StatusOK)
	require.Equal(t, "Portal renovado", fetched["name"])
	require.EqualValues(t, 1, fetched["progress"].(map[string]any)["total"])

	// The product permits equal names; renaming must keep that behavior.
	env.mustJSON(http.MethodPost, owner.path("/projects"), owner.Token,
		map[string]any{"name": "Portal renovado"}, http.StatusCreated)
	env.mustJSON(http.MethodPatch, owner.path("/projects/"+id), owner.Token,
		map[string]any{"name": "Portal renovado"}, http.StatusOK)
	require.Equal(t, 2, env.count(`SELECT count(*) FROM projects WHERE workspace_id=$1`, owner.WorkspaceID))
}

func TestProjectRename_MCPValidatesNames(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("project-mcp")
	project := env.mustJSON(http.MethodPost, owner.path("/projects"), owner.Token, map[string]any{"name": "Original"}, http.StatusCreated)
	_, key, err := env.apiKeySvc.Create(t.Context(), owner.ID, dto.CreateAPIKeyRequest{Name: "projects"})
	require.NoError(t, err)
	server := httptest.NewServer(env.server)
	t.Cleanup(server.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "project-rename", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:   server.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{key}, Timeout: 10 * time.Second},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	for _, name := range []string{" ", "<b> </b>", strings.Repeat("a", 201)} {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "update_project",
			Arguments: map[string]any{"workspace": owner.Slug, "project": project["id"], "name": name}})
		require.NoError(t, err)
		require.True(t, result.IsError)
	}
	fetched := env.mustJSON(http.MethodGet, owner.path("/projects/"+project["id"].(string)), owner.Token, nil, http.StatusOK)
	require.Equal(t, "Original", fetched["name"])
}

func TestProjectRename_NameValidation(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("project-names")
	project := env.mustJSON(http.MethodPost, owner.path("/projects"), owner.Token, map[string]any{"name": "Original"}, http.StatusCreated)
	id := project["id"].(string)
	for _, name := range []string{"", " \t\n", "\u2003\u00a0", "<b> </b>", strings.Repeat("a", 201), strings.Repeat("界", 201)} {
		t.Run(name, func(t *testing.T) {
			for _, method := range []string{http.MethodPatch, http.MethodPost} {
				path := owner.path("/projects")
				if method == http.MethodPatch {
					path += "/" + id
				}
				env.mustJSON(method, path, owner.Token, map[string]any{"name": name}, http.StatusBadRequest)
			}
			var stored string
			require.NoError(t, env.db.Get(&stored, `SELECT name FROM projects WHERE id=$1`, id))
			require.Equal(t, "Original", stored)
		})
	}
	for _, name := range []string{strings.Repeat("a", 200), strings.Repeat("界", 200), strings.Repeat("🚀", 200), "<b>Portal</b>"} {
		updated := env.mustJSON(http.MethodPatch, owner.path("/projects/"+id), owner.Token, map[string]any{"name": name}, http.StatusOK)
		require.NotEmpty(t, updated["name"])
	}
}

func TestProjectRename_PermissionsAndWorkspaceIsolation(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("project-owner")
	project := env.mustJSON(http.MethodPost, owner.path("/projects"), owner.Token, map[string]any{"name": "Original"}, http.StatusCreated)
	id := project["id"].(string)
	path := owner.path("/projects/" + id)
	env.mustJSON(http.MethodPatch, path, "", map[string]any{"name": "Unauthorized"}, http.StatusUnauthorized)
	for _, role := range []string{"owner", "admin", "member", "guest"} {
		user := env.seedOwner("rename-" + role)
		_, err := env.db.Exec(`INSERT INTO workspace_members (workspace_id,user_id,role) VALUES ($1,$2,$3)`, owner.WorkspaceID, user.ID, role)
		require.NoError(t, err)
		want := http.StatusOK
		if role == "guest" {
			want = http.StatusForbidden
		}
		env.mustJSON(http.MethodPatch, path, user.Token, map[string]any{"name": role}, want)
		// The same authenticated user belongs to both workspaces. A valid
		// project ID must never let the URL's workspace boundary be crossed.
		for _, method := range []string{http.MethodPatch, http.MethodGet} {
			env.mustJSON(method, user.path("/projects/"+id), user.Token, map[string]any{"name": "Cross-workspace"}, http.StatusNotFound)
		}
	}
	outsider := env.seedOwner("rename-outsider")
	env.mustJSON(http.MethodPatch, path, outsider.Token, map[string]any{"name": "Outsider"}, http.StatusForbidden)
	env.mustJSON(http.MethodPatch, owner.path("/projects/"+uuid.NewString()), owner.Token, map[string]any{"name": "Missing"}, http.StatusNotFound)
	fetched := env.mustJSON(http.MethodGet, path, owner.Token, nil, http.StatusOK)
	require.Equal(t, "member", fetched["name"])
}
