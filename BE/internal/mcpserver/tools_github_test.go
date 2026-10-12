package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/middleware"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/ku/ku-backend/internal/service"
	"github.com/ku/ku-backend/pkg/crypto"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// githubToolsEnv provisions a throwaway migrated database plus an MCP endpoint
// whose GitHub tools run against the real service over that database.
type githubToolsEnv struct {
	env      *mcpTestEnv
	db       *sqlx.DB
	upstream *atomic.Int64
}

func newGitHubToolsEnv(t *testing.T) *githubToolsEnv {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not configured")
	}
	ctx := context.Background()
	admin, err := sql.Open("pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	require.NoError(t, admin.PingContext(ctx))

	databaseName := "ku_mcp_github_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.ExecContext(ctx, `CREATE DATABASE "`+databaseName+`"`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE "`+databaseName+`" WITH (FORCE)`)
	})

	parsed, err := url.Parse(databaseURL)
	require.NoError(t, err)
	parsed.Path = "/" + databaseName
	parsed.RawPath = ""
	testURL := parsed.String()

	migrator, err := migrate.New("file://../../migrations", testURL)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = migrator.Close() })
	require.NoError(t, migrator.Up())

	db, err := sqlx.Connect("pgx", testURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// Any live GitHub traffic would land here; the unlink mapping must not
	// need it, so every request is counted and refused.
	var upstream atomic.Int64
	instance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstream.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(instance.Close)

	key := crypto.DeriveKey("mcp-github-tools-fixture")
	encryptedToken, err := crypto.Encrypt("stored-token", key)
	require.NoError(t, err)

	user := &domain.User{ID: uuid.New(), Email: "gh-tools@example.test", Name: "GH Tools"}
	workspace := &domain.Workspace{ID: uuid.New(), Slug: "gh-tools", Name: "GH Tools", OwnerID: user.ID}
	_, err = db.ExecContext(ctx, `INSERT INTO users (id,email,name,password_hash) VALUES ($1,$2,$3,'x')`, user.ID, user.Email, user.Name)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO workspaces (id,name,slug,owner_id) VALUES ($1,$2,$3,$4)`, workspace.ID, workspace.Name, workspace.Slug, user.ID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO workspace_members (workspace_id,user_id,role) VALUES ($1,$2,'owner')`, workspace.ID, user.ID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO github_connections (id,workspace_id,instance_url,token,account_login) VALUES ($1,$2,$3,$4,'octo')`,
		uuid.New(), workspace.ID, instance.URL, encryptedToken)
	require.NoError(t, err)
	encryptedSecret, err := crypto.Encrypt("fixture-webhook-secret", key)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO github_repos (id,workspace_id,github_repo_id,full_name,instance_url,webhook_secret) VALUES ($1,$2,$3,'org/project',$4,$5)`,
		uuid.New(), workspace.ID, int64(52), instance.URL, encryptedSecret)
	require.NoError(t, err)

	users := &mcpUserRepo{}
	users.On("GetByID", mock.Anything, user.ID).Return(user, nil)
	keyStore := &apiKeyRepoStub{}
	keySvc := service.NewAPIKeyService(keyStore, users)
	_, fullToken, err := keySvc.Create(ctx, user.ID, dto.CreateAPIKeyRequest{Name: "gh-tools"})
	require.NoError(t, err)

	workspaces := &actionsWorkspaceStub{workspace: workspace, member: &domain.WorkspaceMember{WorkspaceID: workspace.ID, UserID: user.ID, Role: domain.RoleOwner}}
	githubSvc := service.NewGitHubService(repository.NewGitHubRepository(db), nil, nil, nil, key, nil, "", "", nil)
	handler := NewHTTPHandler(Deps{Version: "test", Users: users, Workspaces: workspaces, GitHubSvc: githubSvc})
	e := echo.New()
	e.Any("/mcp", echo.WrapHandler(handler), middleware.APIKeyAuth(keySvc))
	server := httptest.NewServer(e)
	t.Cleanup(server.Close)

	return &githubToolsEnv{
		env:      &mcpTestEnv{server: server, user: user, workspace: workspace, fullToken: fullToken, keyStore: keyStore, users: users},
		db:       db,
		upstream: &upstream,
	}
}

func TestMCPGitHubUnlinkRepoUsesOnlyDatabaseRows(t *testing.T) {
	env := newGitHubToolsEnv(t)
	session := connectMCP(t, env.env, env.env.fullToken)

	// The full name matches case-insensitively, as before.
	res := callTool(t, session, "github_unlink_repo", map[string]any{"workspace": "gh-tools", "repo": "Org/Project"})
	out := structured(t, res)
	assert.Equal(t, "org/project", out["unlinked"])

	var linked int
	require.NoError(t, env.db.Get(&linked, `SELECT count(*) FROM github_repos`))
	assert.Zero(t, linked, "the linked repository row must be deleted")
	assert.Zero(t, env.upstream.Load(), "resolving the full name must not validate the token against GitHub")

	// Unlinking the same repository again is a clean tool error.
	res = callTool(t, session, "github_unlink_repo", map[string]any{"workspace": "gh-tools", "repo": "org/project"})
	assert.True(t, res.IsError)
	encoded, err := json.Marshal(res)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "not linked")
	assert.Zero(t, env.upstream.Load())
}
