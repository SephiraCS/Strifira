package handler_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
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
	echomw "github.com/labstack/echo/v4/middleware"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/handler"
	"github.com/SephiraCS/strifira-backend/internal/metrics"
	mw "github.com/SephiraCS/strifira-backend/internal/middleware"
	"github.com/SephiraCS/strifira-backend/internal/realtime"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/SephiraCS/strifira-backend/internal/service"
	"github.com/SephiraCS/strifira-backend/pkg/crypto"
	jwtpkg "github.com/SephiraCS/strifira-backend/pkg/jwt"
)

const (
	obsJWTSecret   = "observability-test-secret-at-least-32-characters"
	obsFrontendURL = "https://app.example.test"
	obsGitHubURL   = "https://github.example.test"
)

// obsEnv is the API wired the same way cmd/server wires it (request ID,
// metrics, recovery, logging, auth and workspace membership middleware) over
// a disposable, fully migrated database. Requests go through a real HTTP
// listener.
type obsEnv struct {
	hub       *realtime.Hub
	db        *sqlx.DB
	server    *httptest.Server
	token     string
	userID    uuid.UUID
	workspace *domain.Workspace
	team      *domain.Team
	label     *domain.Label
	cycle     *domain.Cycle
	repoFull  string
	hookKey   []byte
}

// obsOptions vary the server wiring between tests.
type obsOptions struct {
	// development admits loopback WebSocket origins.
	development bool
	// frontendURLs are extra configured frontend origins.
	frontendURLs []string
}

func setupObservability(t *testing.T) *obsEnv {
	t.Helper()
	return setupObservabilityWith(t, obsOptions{})
}

func setupObservabilityWith(t *testing.T, opts obsOptions) *obsEnv {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = os.Getenv("DATABASE_URL")
	}
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not configured")
	}
	ctx := context.Background()

	admin, err := sql.Open("pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	name := "strifira_handler_obs_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.ExecContext(ctx, `CREATE DATABASE "`+name+`"`)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), `DROP DATABASE "`+name+`" WITH (FORCE)`) })

	parsed, err := url.Parse(databaseURL)
	require.NoError(t, err)
	parsed.Path = "/" + name
	testURL := parsed.String()

	migrator, err := migrate.New("file://../../migrations", testURL)
	require.NoError(t, err)
	require.NoError(t, migrator.Up())
	_, _ = migrator.Close()

	db, err := sqlx.Connect("pgx", testURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	hub := realtime.NewHub()
	userRepo := repository.NewUserRepository(db)
	workspaceRepo := repository.NewWorkspaceRepository(db)
	teamRepo := repository.NewTeamRepository(db)
	issueRepo := repository.NewIssueRepository(db)
	teamStatusRepo := repository.NewTeamStatusRepository(db)
	projectRepo := repository.NewProjectRepository(db)
	cycleRepo := repository.NewCycleRepository(db)
	notifSvc := service.NewNotificationService(repository.NewNotificationRepository(db))
	authSvc := service.NewAuthService(userRepo, repository.NewRefreshTokenRepository(db), obsJWTSecret)
	workspaceSvc := service.NewWorkspaceService(workspaceRepo, userRepo)
	teamSvc := service.NewTeamService(teamRepo, teamStatusRepo, hub)
	issueSvc := service.NewIssueService(issueRepo, teamRepo, teamStatusRepo, repository.NewIssueHistoryRepository(db), hub, notifSvc, projectRepo)
	commentSvc := service.NewCommentService(repository.NewCommentRepository(db), issueRepo, hub, notifSvc)
	relationSvc := service.NewIssueRelationService(repository.NewIssueRelationRepository(db), issueRepo)
	cycleSvc := service.NewCycleService(cycleRepo, teamRepo, hub, notifSvc)
	labelSvc := service.NewLabelService(repository.NewLabelRepository(db))
	githubRepo := repository.NewGitHubRepository(db)
	hookKey := crypto.DeriveKey(obsJWTSecret + ":forgejo") // label predates the GitHub migration; must not change
	githubSvc := service.NewGitHubService(githubRepo, issueRepo, teamRepo, teamStatusRepo, hookKey, hub, obsFrontendURL, "", nil)

	user, _, _, err := authSvc.Register(ctx, dto.RegisterRequest{Email: "obs@example.test", Password: "Correct-horse-battery-9", Name: "Obs"})
	require.NoError(t, err)
	ws, err := workspaceSvc.Create(ctx, user.ID, dto.CreateWorkspaceRequest{Name: "Observability", Slug: "obs"})
	require.NoError(t, err)
	team, err := teamSvc.Create(ctx, ws.ID, user.ID, dto.CreateTeamRequest{Name: "Core", Key: "OBS"})
	require.NoError(t, err)
	label, err := labelSvc.Create(ctx, ws.ID, dto.CreateLabelRequest{Name: "obs-label", Color: "#ff0000"})
	require.NoError(t, err)
	start := time.Now().AddDate(0, 0, -3).Format("2006-01-02")
	end := time.Now().AddDate(0, 0, 4).Format("2006-01-02")
	cycle, err := cycleSvc.Create(ctx, ws.ID, team.ID, dto.CreateCycleRequest{Name: "Sprint", StartDate: start, EndDate: end})
	require.NoError(t, err)

	secret := "observability-webhook-secret"
	encrypted, err := crypto.Encrypt(secret, hookKey)
	require.NoError(t, err)
	require.NoError(t, githubRepo.CreateRepo(ctx, &domain.GitHubRepoModel{
		ID: uuid.New(), WorkspaceID: ws.ID, GitHubRepoID: 7, FullName: "org/obs",
		InstanceURL: obsGitHubURL, DefaultBranch: "main", WebhookSecret: encrypted, IsActive: true,
	}))

	issueH := handler.NewIssueHandler(issueSvc, commentSvc, userRepo, teamStatusRepo, projectRepo, cycleRepo, relationSvc)
	cycleH := handler.NewCycleHandler(cycleSvc)
	wsH := handler.NewWSHandler(hub, handler.WebSocketOriginPatterns(append([]string{obsFrontendURL}, opts.frontendURLs...), opts.development))
	githubH := handler.NewGitHubHandler(githubSvc)

	e := echo.New()
	e.HideBanner = true
	e.Use(echomw.RequestID())
	e.Use(metrics.Middleware())
	e.Use(mw.Recovery())
	e.Use(mw.Logging())
	e.GET("/metrics", metrics.Handler())
	e.GET("/api/test/panic", func(echo.Context) error { panic("handler exploded") })
	e.POST("/api/github/webhook", githubH.HandleWebhook)
	api := e.Group("/api", mw.Auth(obsJWTSecret))
	wsGroup := api.Group("/workspaces/:slug", mw.WorkspaceMembership(workspaceRepo))
	wsGroup.GET("/issues", issueH.List)
	wsGroup.POST("/issues", issueH.Create, mw.RequirePermission("issue:create"))
	wsGroup.GET("/issues/:identifier/sub-issues", issueH.ListSubIssues)
	wsGroup.POST("/issues/:identifier/sub-issues/bulk", issueH.BulkCreateSubIssues, mw.RequirePermission("issue:create"))
	wsGroup.POST("/teams/:teamId/cycles/:cycleId/complete", cycleH.Complete)
	wsGroup.GET("/ws", wsH.Handle)

	server := httptest.NewServer(e)
	t.Cleanup(server.Close)

	token, err := jwtpkg.GenerateAccessToken(user.ID, obsJWTSecret)
	require.NoError(t, err)
	return &obsEnv{hub: hub, db: db, server: server, token: token, userID: user.ID, workspace: ws, team: team,
		label: label, cycle: cycle, repoFull: "org/obs", hookKey: []byte(secret)}
}

func (env *obsEnv) do(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			reader = strings.NewReader(b)
		default:
			raw, err := json.Marshal(b)
			require.NoError(t, err)
			reader = bytes.NewReader(raw)
		}
	}
	req, err := http.NewRequest(method, env.server.URL+path, reader)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+env.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, raw
}

func (env *obsEnv) createIssue(t *testing.T, title string, parentID *string) dto.IssueResponse {
	t.Helper()
	req := dto.CreateIssueRequest{
		Title: title, TeamID: env.team.ID.String(), ParentID: parentID,
		AssigneeIDs: []string{env.userID.String()}, LabelIDs: []string{env.label.ID.String()},
	}
	status, raw := env.do(t, http.MethodPost, "/api/workspaces/obs/issues", req)
	require.Equal(t, http.StatusCreated, status, string(raw))
	var issue dto.IssueResponse
	require.NoError(t, json.Unmarshal(raw, &issue))
	return issue
}

func (env *obsEnv) metricsText(t *testing.T) string {
	t.Helper()
	resp, err := http.Get(env.server.URL + "/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(raw)
}

func TestSubIssueEndpointsReturnFullyEnrichedIssues(t *testing.T) {
	env := setupObservability(t)
	parent := env.createIssue(t, "Parent", nil)
	child := env.createIssue(t, "Existing child", &parent.ID)
	env.createIssue(t, "Grandchild", &child.ID)

	status, raw := env.do(t, http.MethodPost, "/api/workspaces/obs/issues/"+parent.Identifier+"/sub-issues/bulk", dto.BulkCreateSubIssueRequest{
		Issues: []dto.CreateSubIssueRequest{
			{Title: "Bulk one", AssigneeIDs: []string{env.userID.String()}, LabelIDs: []string{env.label.ID.String()}},
			{Title: "Bulk two"},
		},
	})
	require.Equal(t, http.StatusCreated, status, string(raw))
	var created []dto.IssueResponse
	require.NoError(t, json.Unmarshal(raw, &created))
	require.Len(t, created, 2)
	require.Equal(t, "Bulk one", created[0].Title)
	require.NotNil(t, created[0].Parent)
	require.Equal(t, parent.Identifier, created[0].Parent.Identifier)
	require.Len(t, created[0].Labels, 1)
	require.Equal(t, "obs-label", created[0].Labels[0].Name)
	require.Len(t, created[0].Assignees, 1)
	require.Equal(t, env.userID.String(), created[0].Assignees[0].ID)
	require.NotNil(t, created[0].Creator)
	require.NotNil(t, created[0].StatusInfo)
	require.Empty(t, created[1].Labels)

	status, raw = env.do(t, http.MethodGet, "/api/workspaces/obs/issues/"+parent.Identifier+"/sub-issues", nil)
	require.Equal(t, http.StatusOK, status, string(raw))
	var subIssues []dto.IssueResponse
	require.NoError(t, json.Unmarshal(raw, &subIssues))
	require.Len(t, subIssues, 3)
	byTitle := make(map[string]dto.IssueResponse, len(subIssues))
	for _, issue := range subIssues {
		require.NotNil(t, issue.Parent, issue.Title)
		require.Equal(t, parent.ID, issue.Parent.ID)
		require.NotNil(t, issue.StatusInfo, issue.Title)
		require.NotNil(t, issue.Creator, issue.Title)
		require.Equal(t, env.userID.String(), issue.Creator.ID)
		byTitle[issue.Title] = issue
	}

	existing := byTitle["Existing child"]
	require.Len(t, existing.Labels, 1)
	require.Len(t, existing.Assignees, 1)
	require.NotNil(t, existing.SubIssueCount, "nested sub-issue counts must still be reported")
	require.Equal(t, 1, *existing.SubIssueCount)
	require.Equal(t, 0, *existing.SubIssueDone)
	require.Nil(t, byTitle["Bulk two"].SubIssueCount)

	status, raw = env.do(t, http.MethodGet, "/api/workspaces/obs/issues/OBS-999/sub-issues", nil)
	require.Equal(t, http.StatusBadRequest, status, string(raw))
	require.NotContains(t, string(raw), "sql")
}

// Bulk sub-issue creation commits before the response is enriched; an
// enrichment failure must not turn the committed creation into an error
// (a client retry would duplicate the sub-issues).
func TestBulkSubIssuesSurviveEnrichmentFailure(t *testing.T) {
	env := setupObservability(t)
	parent := env.createIssue(t, "Parent", nil)

	// Relation summaries are only read during enrichment.
	_, err := env.db.Exec(`ALTER TABLE issue_relations RENAME TO issue_relations_away`)
	require.NoError(t, err)
	status, raw := env.do(t, http.MethodPost, "/api/workspaces/obs/issues/"+parent.Identifier+"/sub-issues/bulk", dto.BulkCreateSubIssueRequest{
		Issues: []dto.CreateSubIssueRequest{{Title: "Kept one"}, {Title: "Kept two"}},
	})
	_, err = env.db.Exec(`ALTER TABLE issue_relations_away RENAME TO issue_relations`)
	require.NoError(t, err)

	require.Equal(t, http.StatusCreated, status, string(raw))
	var created []dto.IssueResponse
	require.NoError(t, json.Unmarshal(raw, &created))
	require.Len(t, created, 2)
	require.Equal(t, "Kept one", created[0].Title)
	require.NotEmpty(t, created[0].Identifier)
	var stored int
	require.NoError(t, env.db.Get(&stored, `SELECT count(*) FROM issues WHERE parent_id = $1`, parent.ID))
	require.Equal(t, 2, stored)
}

func TestCycleCompleteRejectsMalformedBody(t *testing.T) {
	env := setupObservability(t)
	path := "/api/workspaces/obs/teams/" + env.team.ID.String() + "/cycles/" + env.cycle.ID.String() + "/complete"

	status, raw := env.do(t, http.MethodPost, path, `{"carry_over": "maybe"`)
	require.Equal(t, http.StatusBadRequest, status, string(raw))
	require.Contains(t, string(raw), "Invalid request body")

	var completedAt sql.NullTime
	require.NoError(t, env.db.Get(&completedAt, `SELECT completed_at FROM cycles WHERE id = $1`, env.cycle.ID))
	require.False(t, completedAt.Valid, "a rejected request must not complete the cycle")

	// An empty body is still accepted: the request fields are optional.
	req, err := http.NewRequest(http.MethodPost, env.server.URL+path, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+env.token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// The completion body is optional regardless of framing: an empty chunked
// body (unknown length) completes the cycle, while truncated JSON is still
// rejected. No body-limit middleware runs here, so the handler itself must
// tell the cases apart.
func TestCycleCompleteAcceptsEmptyChunkedBody(t *testing.T) {
	env := setupObservability(t)
	path := "/api/workspaces/obs/teams/" + env.team.ID.String() + "/cycles/" + env.cycle.ID.String() + "/complete"
	send := func(body string) (int, string) {
		req, err := http.NewRequest(http.MethodPost, env.server.URL+path, io.MultiReader(strings.NewReader(body)))
		require.NoError(t, err)
		req.ContentLength = -1 // unknown length: sent with Transfer-Encoding: chunked
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+env.token)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}

	status, raw := send(`{"retrospective": "went well", "carry_over": tr`)
	require.Equal(t, http.StatusBadRequest, status, raw)
	var completedAt sql.NullTime
	require.NoError(t, env.db.Get(&completedAt, `SELECT completed_at FROM cycles WHERE id = $1`, env.cycle.ID))
	require.False(t, completedAt.Valid, "truncated JSON must not complete the cycle")

	status, raw = send("  \n")
	require.Equal(t, http.StatusOK, status, raw)
	require.NoError(t, env.db.Get(&completedAt, `SELECT completed_at FROM cycles WHERE id = $1`, env.cycle.ID))
	require.True(t, completedAt.Valid)
}

func TestWebSocketRejectsForeignOrigins(t *testing.T) {
	env := setupObservability(t)
	wsURL := "ws" + strings.TrimPrefix(env.server.URL, "http") + "/api/workspaces/obs/ws"
	dial := func(origin string) (*websocket.Conn, *http.Response, error) {
		header := http.Header{}
		header.Set("Authorization", "Bearer "+env.token)
		if origin != "" {
			header.Set("Origin", origin)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: header})
	}

	_, resp, err := dial("https://evil.example.test")
	require.Error(t, err, "a cross-site origin must not get a cookie-authenticated socket")
	require.NotNil(t, resp)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	_, resp, err = dial("http://localhost:5173")
	require.Error(t, err, "loopback origins are only trusted in development")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	conn, _, err := dial(obsFrontendURL)
	require.NoError(t, err, "the configured frontend origin must connect")
	conn.Close(websocket.StatusNormalClosure, "")

	conn, _, err = dial(env.server.URL)
	require.NoError(t, err, "same-origin connections must connect")
	conn.Close(websocket.StatusNormalClosure, "")

	require.Contains(t, handler.WebSocketOriginPatterns([]string{obsFrontendURL}, true), "localhost:*")
}

func TestMetricsExposeRequestsAndGitHubWebhookOutcomes(t *testing.T) {
	env := setupObservability(t)
	before := env.metricsText(t)

	parent := env.createIssue(t, "Metrics parent", nil)
	status, _ := env.do(t, http.MethodGet, "/api/workspaces/obs/issues/"+parent.Identifier+"/sub-issues", nil)
	require.Equal(t, http.StatusOK, status)

	deliver := func(event string, payload map[string]any, sign bool) int {
		payload["repository"] = map[string]any{"full_name": env.repoFull, "html_url": obsGitHubURL + "/" + env.repoFull}
		body, err := json.Marshal(payload)
		require.NoError(t, err)
		req, err := http.NewRequest(http.MethodPost, env.server.URL+"/api/github/webhook", bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("X-GitHub-Event", event)
		req.Header.Set("X-GitHub-Delivery", uuid.NewString())
		signature := "sha256=" + strings.Repeat("0", 64)
		if sign {
			mac := hmac.New(sha256.New, env.hookKey)
			mac.Write(body)
			signature = "sha256=" + hex.EncodeToString(mac.Sum(nil))
		}
		req.Header.Set("X-Hub-Signature-256", signature)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	require.Equal(t, http.StatusUnauthorized, deliver("push", map[string]any{"ref": "refs/heads/main"}, false))
	require.Equal(t, http.StatusOK, deliver("issues", map[string]any{"action": "opened"}, true))
	require.Equal(t, http.StatusInternalServerError, deliver("push", map[string]any{"commits": "not-a-list"}, true))
	// Client-chosen event names never become labels of their own.
	require.Equal(t, http.StatusUnauthorized, deliver("attacker_chosen_event", map[string]any{}, false))

	// Client-chosen methods and unknown paths collapse to single labels.
	rawRequest := func(method, path string) int {
		req, err := http.NewRequest(method, env.server.URL+path, nil)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	rawRequest("BREWCOFFEE", "/api/github/webhook")
	require.Equal(t, http.StatusNotFound, rawRequest(http.MethodGet, "/no/such/route-"+uuid.NewString()))

	// A panicking handler is recovered and still counted, once, as a 500.
	require.Equal(t, http.StatusInternalServerError, rawRequest(http.MethodGet, "/api/test/panic"))

	after := env.metricsText(t)
	require.Contains(t, after, `strifira_http_requests_total{method="GET",route="/api/workspaces/:slug/issues/:identifier/sub-issues",status="200"}`)
	require.Contains(t, after, `strifira_http_request_duration_seconds_bucket{method="POST",route="/api/workspaces/:slug/issues",le="+Inf"}`)
	require.NotContains(t, after, parent.Identifier, "route labels must use templates, not concrete paths")
	require.NotContains(t, after, `route="/metrics"`)
	require.NotContains(t, after, "attacker_chosen_event")
	require.NotContains(t, after, "BREWCOFFEE")
	require.Contains(t, after, `method="other"`)
	require.NotContains(t, after, "/no/such/route")
	require.Contains(t, after, `route="unmatched",status="404"`)

	for _, sample := range []string{
		`strifira_github_webhook_deliveries_total{event="push",result="invalid_signature"}`,
		`strifira_github_webhook_deliveries_total{event="issues",result="processed"}`,
		`strifira_github_webhook_deliveries_total{event="push",result="error"}`,
		`strifira_github_webhook_deliveries_total{event="other",result="invalid_signature"}`,
		`strifira_http_requests_total{method="GET",route="/api/test/panic",status="500"}`,
	} {
		require.Equal(t, counterValue(before, sample)+1, counterValue(after, sample), sample)
	}
}

// counterValue reads one sample from the Prometheus text format, 0 if absent.
func counterValue(text, sample string) float64 {
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(line, sample+" "); ok {
			var value float64
			_ = json.Unmarshal([]byte(strings.TrimSpace(rest)), &value)
			return value
		}
	}
	return 0
}

// wsDial opens the realtime socket of the obs workspace with origin.
func (env *obsEnv) wsDial(t *testing.T, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(env.server.URL, "http") + "/api/workspaces/obs/ws"
	header := http.Header{}
	header.Set("Authorization", "Bearer "+env.token)
	if origin != "" {
		header.Set("Origin", origin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: header})
}

// IPv6 hosts contain glob metacharacters ("[", "]"); both the development
// loopback rule and configured IPv6 frontends must match them literally.
func TestWebSocketAcceptsIPv6LoopbackAndFrontendOrigins(t *testing.T) {
	env := setupObservabilityWith(t, obsOptions{development: true, frontendURLs: []string{"http://[fd00::7]:8080"}})

	for _, origin := range []string{"http://[::1]:5173", "http://[fd00::7]:8080", "http://localhost:5173"} {
		conn, _, err := env.wsDial(t, origin)
		require.NoError(t, err, origin)
		conn.Close(websocket.StatusNormalClosure, "")
	}
	for _, origin := range []string{"http://[fd00::8]:8080", "http://:8080", "https://evil.example.test"} {
		_, resp, err := env.wsDial(t, origin)
		require.Error(t, err, origin)
		require.NotNil(t, resp, origin)
		require.Equal(t, http.StatusForbidden, resp.StatusCode, origin)
	}
}

// Hijacked WebSockets are not drained by the HTTP server's Shutdown; the
// hub closes them with StatusGoingAway and waits for their cleanup.
func TestHubShutdownClosesLiveWebSockets(t *testing.T) {
	env := setupObservability(t)
	conn, _, err := env.wsDial(t, obsFrontendURL)
	require.NoError(t, err)
	readErr := make(chan error, 1)
	go func() {
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				readErr <- err
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, env.hub.Shutdown(ctx), "every handler must unregister before the deadline")

	select {
	case err := <-readErr:
		require.Equal(t, websocket.StatusGoingAway, websocket.CloseStatus(err))
	case <-time.After(5 * time.Second):
		t.Fatal("the client was not disconnected")
	}

	// Upgrades after shutdown started are refused before hijacking.
	_, resp, err := env.wsDial(t, obsFrontendURL)
	require.Error(t, err)
	require.NotNil(t, resp)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

// An upgrade admitted before Shutdown (between admission and registration,
// i.e. mid-hijack) holds Shutdown open until it settles, so it cannot slip
// past the drain.
func TestHubShutdownWaitsForAdmittedUpgrades(t *testing.T) {
	env := setupObservability(t)
	upgraded, admitted := env.hub.BeginUpgrade()
	require.True(t, admitted)

	result := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		result <- env.hub.Shutdown(ctx)
	}()
	select {
	case err := <-result:
		t.Fatalf("Shutdown returned (%v) while an upgrade was still pending", err)
	case <-time.After(300 * time.Millisecond):
	}

	// No new upgrade is admitted meanwhile.
	_, again := env.hub.BeginUpgrade()
	require.False(t, again)

	upgraded()
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not finish once the upgrade settled")
	}
}
