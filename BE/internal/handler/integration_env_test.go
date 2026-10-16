package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/SephiraCS/strifira-backend/internal/bus"
	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/mcpserver"
	mw "github.com/SephiraCS/strifira-backend/internal/middleware"
	"github.com/SephiraCS/strifira-backend/internal/realtime"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/SephiraCS/strifira-backend/internal/service"
	jwtpkg "github.com/SephiraCS/strifira-backend/pkg/jwt"
	"github.com/SephiraCS/strifira-backend/pkg/validate"
)

// testKVGetter resolves the integration throttle bucket through a getter,
// mirroring the production wiring where the bus provisions lazily.
func testKVGetter(t *testing.T) func() jetstream.KeyValue {
	kv := bus.NewTestKV(t, "login-throttle-test", 2*integrationLoginLockout)
	return func() jetstream.KeyValue { return kv }
}

// integrationJWTSecret signs tokens for the HTTP integration environment.
const integrationJWTSecret = "integration-secret-that-is-at-least-32-characters"

// Login throttle settings for the integration server: small so tests can
// reach the lockout quickly.
const (
	integrationLoginMaxFailures = 3
	integrationLoginLockout     = time.Minute
)

// integrationEnv is a real HTTP stack (Echo router, auth/workspace/permission
// middleware, handlers, services, repositories) over a freshly migrated
// Postgres database. Routes mirror cmd/server/main.go.
type integrationEnv struct {
	t          *testing.T
	db         *sqlx.DB
	server     *echo.Echo
	webhookSvc *service.WebhookService
	apiKeySvc  *service.APIKeyService
}

func integrationDatabaseURL(t *testing.T) string {
	t.Helper()
	if value := os.Getenv("TEST_DATABASE_URL"); value != "" {
		return value
	}
	if value := os.Getenv("DATABASE_URL"); value != "" {
		return value
	}
	t.Skip("TEST_DATABASE_URL is not configured")
	return ""
}

func newIntegrationEnv(t *testing.T) *integrationEnv {
	t.Helper()
	baseURL := integrationDatabaseURL(t)
	ctx := context.Background()

	admin, err := sql.Open("pgx", baseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	require.NoError(t, admin.PingContext(ctx))

	name := "strifira_handler_it_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.ExecContext(ctx, `CREATE DATABASE "`+name+`"`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE "`+name+`" WITH (FORCE)`)
	})

	parsed, err := url.Parse(baseURL)
	require.NoError(t, err)
	parsed.Path = "/" + name
	parsed.RawPath = ""
	testURL := parsed.String()

	migrator, err := migrate.New("file://../../migrations", testURL)
	require.NoError(t, err)
	require.NoError(t, migrator.Up())
	_, _ = migrator.Close()

	db, err := sqlx.Connect("pgx", testURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	env := &integrationEnv{t: t, db: db}
	env.server = env.buildServer()
	return env
}

func (env *integrationEnv) buildServer() *echo.Echo {
	db := env.db
	hub := realtime.NewHub()

	userRepo := repository.NewUserRepository(db)
	refreshRepo := repository.NewRefreshTokenRepository(db)
	workspaceRepo := repository.NewWorkspaceRepository(db)
	teamRepo := repository.NewTeamRepository(db)
	issueRepo := repository.NewIssueRepository(db)
	commentRepo := repository.NewCommentRepository(db)
	projectRepo := repository.NewProjectRepository(db)
	notifRepo := repository.NewNotificationRepository(db)
	historyRepo := repository.NewIssueHistoryRepository(db)
	relationRepo := repository.NewIssueRelationRepository(db)
	templateRepo := repository.NewIssueTemplateRepository(db)
	viewRepo := repository.NewViewRepository(db)
	cycleRepo := repository.NewCycleRepository(db)
	teamStatusRepo := repository.NewTeamStatusRepository(db)
	visibilityRepo := repository.NewProjectStatusVisibilityRepository(db)

	authSvc := service.NewAuthService(userRepo, refreshRepo, integrationJWTSecret)
	teamSvc := service.NewTeamService(teamRepo, teamStatusRepo, hub)
	notifSvc := service.NewNotificationService(notifRepo)
	issueSvc := service.NewIssueService(issueRepo, teamRepo, teamStatusRepo, historyRepo, hub, notifSvc, projectRepo)
	commentSvc := service.NewCommentService(commentRepo, issueRepo, hub, notifSvc)
	relationSvc := service.NewIssueRelationService(relationRepo, issueRepo)
	templateSvc := service.NewIssueTemplateService(templateRepo)
	cycleSvc := service.NewCycleService(cycleRepo, teamRepo, hub, notifSvc)
	teamStatusSvc := service.NewTeamStatusService(teamStatusRepo, visibilityRepo, teamRepo, projectRepo)
	// Receivers in these tests are loopback httptest servers, which the
	// production delivery guard refuses; everything else stays blocked.
	webhookSvc := service.NewWebhookService(repository.NewWebhookRepository(db), integrationJWTSecret).
		WithAllowedAddresses(func(ip net.IP) bool { return ip.IsLoopback() || !validate.IsBlockedIP(ip) })
	env.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = webhookSvc.Close(ctx)
	})
	projectSvc := service.NewProjectService(projectRepo, hub)
	hub.OnBroadcast = webhookSvc.HubListener()
	env.webhookSvc = webhookSvc
	sharedLinkSvc := service.NewSharedLinkService(repository.NewSharedLinkRepository(db), workspaceRepo, teamRepo, projectRepo, viewRepo, issueRepo, userRepo, teamStatusRepo, repository.NewDocumentRepository(db), integrationJWTSecret)

	authH := NewAuthHandler(authSvc, false, nil).
		WithLoginThrottle(mw.NewLoginThrottle(testKVGetter(env.t), integrationLoginMaxFailures, integrationLoginLockout))
	teamH := NewTeamHandler(teamSvc)
	issueH := NewIssueHandler(issueSvc, commentSvc, userRepo, teamStatusRepo, projectRepo, cycleRepo, relationSvc)
	relationH := NewIssueRelationHandler(relationSvc)
	templateH := NewIssueTemplateHandler(templateSvc)
	cycleH := NewCycleHandler(cycleSvc)
	teamStatusH := NewTeamStatusHandler(teamStatusSvc)
	webhookH := NewWebhookHandler(webhookSvc)
	projectH := NewProjectHandler(projectSvc)
	sharedLinkH := NewSharedLinkHandler(sharedLinkSvc, "http://strifira.test")
	githubH := NewGitHubHandler(service.NewGitHubService(repository.NewGitHubRepository(db), issueRepo, teamRepo, teamStatusRepo, make([]byte, 32), hub, "http://strifira.test", "", nil))

	env.apiKeySvc = service.NewAPIKeyService(repository.NewAPIKeyRepository(db), userRepo)
	mcpHandler := mcpserver.NewHTTPHandler(mcpserver.Deps{
		Version:      "integration",
		Users:        userRepo,
		Workspaces:   workspaceRepo,
		Teams:        teamRepo,
		TeamStatuses: teamStatusRepo,
		Projects:     projectRepo,
		ProjectSvc:   projectSvc,
		IssueSvc:     issueSvc,
		CommentSvc:   commentSvc,
		RelationSvc:  relationSvc,
		TeamSvc:      teamSvc,
		StatusSvc:    teamStatusSvc,
		NotifSvc:     notifSvc,
	})

	e := echo.New()
	e.HideBanner = true
	e.Use(mw.Recovery())
	e.Use(mw.BodyLimit())

	auth := e.Group("/api/auth")
	auth.POST("/login", authH.Login)
	e.GET("/api/public/share/:token/issues", sharedLinkH.ListPublicIssues)
	e.POST("/api/github/webhook", githubH.HandleWebhook)
	// Stand-in for the workspace import routes, which enforce their own
	// 1 GiB limit and are exempt from the global body limit.
	drain := func(c echo.Context) error {
		n, err := io.Copy(io.Discard, c.Request().Body)
		if err != nil {
			return c.NoContent(http.StatusRequestEntityTooLarge)
		}
		return c.JSON(http.StatusOK, map[string]int64{"bytes": n})
	}
	e.POST("/api/workspaces/import", drain)
	e.POST("/api/test/echo-body", drain)

	e.Any("/mcp", echo.WrapHandler(mcpHandler), mw.APIKeyAuth(env.apiKeySvc))

	api := e.Group("/api", mw.Auth(integrationJWTSecret))
	ws := api.Group("/workspaces/:slug", mw.WorkspaceMembership(workspaceRepo))
	ws.POST("/teams", teamH.Create, mw.RequirePermission("team:manage"))
	ws.GET("/teams/:teamId/statuses", teamStatusH.List)
	ws.POST("/teams/:teamId/statuses", teamStatusH.Create, mw.RequirePermission("team:manage"))
	ws.PATCH("/teams/:teamId/statuses/:statusId", teamStatusH.Update, mw.RequirePermission("team:manage"))
	ws.DELETE("/teams/:teamId/statuses/:statusId", teamStatusH.Delete, mw.RequirePermission("team:manage"))
	ws.GET("/teams/:teamId/cycles", cycleH.List)
	ws.POST("/teams/:teamId/cycles", cycleH.Create)
	ws.GET("/teams/:teamId/cycles/:cycleId", cycleH.Get)
	ws.PATCH("/teams/:teamId/cycles/:cycleId", cycleH.Update)
	ws.POST("/teams/:teamId/cycles/:cycleId/complete", cycleH.Complete)
	ws.DELETE("/teams/:teamId/cycles/:cycleId", cycleH.Delete)
	ws.POST("/issues", issueH.Create, mw.RequirePermission("issue:create"))
	ws.PATCH("/issues/:identifier", issueH.Update, mw.RequirePermission("issue:update"))
	ws.DELETE("/issues/:identifier", issueH.Delete, mw.RequirePermission("issue:delete_own"))
	ws.POST("/issues/:identifier/comments", issueH.CreateComment, mw.RequirePermission("issue:create"))
	ws.POST("/issues/:identifier/relations", relationH.Create, mw.RequirePermission("issue:update"))
	ws.GET("/issues/:identifier/relations", relationH.List)
	ws.DELETE("/issues/:identifier/relations/:relationId", relationH.Delete, mw.RequirePermission("issue:update"))
	ws.POST("/issue-templates", templateH.Create, mw.RequirePermission("issue:create"))
	ws.GET("/issue-templates/:id", templateH.Get)
	ws.PATCH("/issue-templates/:id", templateH.Update, mw.RequirePermission("issue:create"))
	ws.DELETE("/issue-templates/:id", templateH.Delete, mw.RequirePermission("issue:create"))
	ws.PATCH("/projects/:id", projectH.Update, mw.RequirePermission("project:manage"))
	ws.POST("/webhooks", webhookH.Create, mw.RequirePermission("workspace:manage"))
	ws.GET("/webhooks", webhookH.List, mw.RequirePermission("workspace:manage"))
	ws.PATCH("/webhooks/:id", webhookH.Update, mw.RequirePermission("workspace:manage"))
	ws.DELETE("/webhooks/:id", webhookH.Delete, mw.RequirePermission("workspace:manage"))
	ws.POST("/shared-links", sharedLinkH.Create)
	ws.PATCH("/shared-links/:id", sharedLinkH.Update)
	ws.DELETE("/shared-links/:id", sharedLinkH.Delete)

	// Docs (wiki) — mirrors the docs route group in cmd/server/main.go.
	documentRepo := repository.NewDocumentRepository(db)
	documentUpdateRepo := repository.NewDocumentUpdateRepository(db)
	docSyncSvc := service.NewDocSyncService(documentRepo, documentUpdateRepo)
	documentSvc := service.NewDocumentService(documentRepo, hub, notifSvc, docSyncSvc)
	docSyncH := NewDocSyncHandler(docSyncSvc, documentSvc, WebSocketOriginPatterns(nil, true))
	documentH := NewDocumentHandler(documentSvc, userRepo)
	ws.GET("/docs", documentH.List)
	ws.POST("/docs", documentH.Create, mw.RequirePermission(domain.PermDocCreate))
	ws.GET("/docs/search", documentH.Search)
	ws.GET("/docs/graph", documentH.Graph)
	ws.GET("/docs/trash", documentH.ListTrash)
	ws.GET("/docs/:docId", documentH.Get)
	ws.PATCH("/docs/:docId", documentH.Update, mw.RequirePermission(domain.PermDocUpdate))
	ws.DELETE("/docs/:docId", documentH.Delete, mw.RequirePermission(domain.PermDocDelete))
	ws.POST("/docs/:docId/restore", documentH.Restore, mw.RequirePermission(domain.PermDocDelete))
	ws.DELETE("/docs/:docId/purge", documentH.Purge, mw.RequirePermission(domain.PermDocDelete))
	ws.POST("/docs/:docId/move", documentH.Move, mw.RequirePermission(domain.PermDocUpdate))
	ws.POST("/docs/:docId/duplicate", documentH.Duplicate, mw.RequirePermission(domain.PermDocCreate))
	ws.GET("/docs/:docId/revisions", documentH.ListRevisions)
	ws.POST("/docs/:docId/revisions/:revisionId/restore", documentH.RestoreRevision, mw.RequirePermission(domain.PermDocUpdate))
	ws.GET("/docs/:docId/backlinks", documentH.Backlinks)
	ws.PUT("/docs/:docId/tags", documentH.SetTags, mw.RequirePermission(domain.PermDocUpdate))
	ws.GET("/docs/:docId/comments", documentH.ListComments)
	ws.POST("/docs/:docId/comments", documentH.CreateComment, mw.RequirePermission(domain.PermDocUpdate))
	ws.POST("/docs/:docId/comments/:commentId/resolve", documentH.ResolveComment, mw.RequirePermission(domain.PermDocUpdate))
	ws.POST("/docs/:docId/comments/:commentId/reopen", documentH.ReopenComment, mw.RequirePermission(domain.PermDocUpdate))
	ws.GET("/doc-tags", documentH.ListTags)
	ws.POST("/doc-tags", documentH.CreateTag, mw.RequirePermission(domain.PermDocUpdate))
	ws.DELETE("/doc-tags/:tagId", documentH.DeleteTag, mw.RequirePermission(domain.PermDocUpdate))
	ws.GET("/docs/:docId/collab", docSyncH.Handle)
	return e
}

// integrationUser is a workspace owner with a bearer token.
type integrationUser struct {
	ID       uuid.UUID
	Email    string
	Password string
	Token    string
	// Workspace owned by the user.
	WorkspaceID uuid.UUID
	Slug        string
}

// seedOwner creates a user with a real bcrypt password who owns a fresh
// workspace.
func (env *integrationEnv) seedOwner(label string) integrationUser {
	env.t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:10]
	u := integrationUser{
		ID:          uuid.New(),
		Email:       label + "-" + suffix + "@example.test",
		Password:    "Secret-Pass-123",
		WorkspaceID: uuid.New(),
		Slug:        label + "-" + suffix,
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(u.Password), bcrypt.MinCost)
	require.NoError(env.t, err)
	_, err = env.db.Exec(`INSERT INTO users (id,email,name,password_hash) VALUES ($1,$2,$3,$4)`, u.ID, u.Email, label, string(hash))
	require.NoError(env.t, err)
	_, err = env.db.Exec(`INSERT INTO workspaces (id,name,slug,owner_id) VALUES ($1,$2,$3,$4)`, u.WorkspaceID, label+" workspace", u.Slug, u.ID)
	require.NoError(env.t, err)
	_, err = env.db.Exec(`INSERT INTO workspace_members (workspace_id,user_id,role) VALUES ($1,$2,$3)`, u.WorkspaceID, u.ID, domain.RoleOwner)
	require.NoError(env.t, err)
	u.Token, err = jwtpkg.GenerateAccessToken(u.ID, integrationJWTSecret)
	require.NoError(env.t, err)
	return u
}

// do sends a request through the full Echo stack.
func (env *integrationEnv) do(method, path, token string, body any) *httptest.ResponseRecorder {
	env.t.Helper()
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		reader = bytes.NewReader(b)
	case string:
		reader = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		require.NoError(env.t, err)
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	if token != "" {
		req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, req)
	return rec
}

// mustJSON performs a request, asserts the status and decodes the body.
func (env *integrationEnv) mustJSON(method, path, token string, body any, wantStatus int) map[string]any {
	env.t.Helper()
	rec := env.do(method, path, token, body)
	require.Equalf(env.t, wantStatus, rec.Code, "%s %s: %s", method, path, rec.Body.String())
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		require.NoError(env.t, json.Unmarshal(rec.Body.Bytes(), &out))
	}
	return out
}

func (u integrationUser) path(suffix string) string {
	return "/api/workspaces/" + u.Slug + suffix
}

// createTeam creates a team through the API and returns its id.
func (env *integrationEnv) createTeam(u integrationUser, name, key string) string {
	env.t.Helper()
	team := env.mustJSON(http.MethodPost, u.path("/teams"), u.Token, map[string]any{"name": name, "key": key}, http.StatusCreated)
	return team["id"].(string)
}

// createIssue creates an issue through the API and returns its identifier.
func (env *integrationEnv) createIssue(u integrationUser, teamID, title string) string {
	env.t.Helper()
	issue := env.mustJSON(http.MethodPost, u.path("/issues"), u.Token, map[string]any{"title": title, "team_id": teamID}, http.StatusCreated)
	return issue["identifier"].(string)
}

func (env *integrationEnv) count(query string, args ...any) int {
	env.t.Helper()
	var n int
	require.NoError(env.t, env.db.Get(&n, query, args...))
	return n
}
