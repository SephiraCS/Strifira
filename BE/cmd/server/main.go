package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	log "github.com/sirupsen/logrus"

	"github.com/ku/ku-backend/internal/agent"
	"github.com/ku/ku-backend/internal/apidocs"
	"github.com/ku/ku-backend/internal/config"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/handler"
	"github.com/ku/ku-backend/internal/mcpserver"
	mw "github.com/ku/ku-backend/internal/middleware"
	"github.com/ku/ku-backend/internal/realtime"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/ku/ku-backend/internal/service"
	"github.com/ku/ku-backend/pkg/crypto"
	"github.com/ku/ku-backend/pkg/storage"
)

func main() {
	log.SetFormatter(&log.TextFormatter{FullTimestamp: true})

	// Handle CLI subcommands
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		runMigrate(os.Args[2:])
		return
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Database
	db, err := sqlx.Connect("pgx", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)

	// Realtime hub
	hub := realtime.NewHub()

	// Repositories
	userRepo := repository.NewUserRepository(db)
	refreshRepo := repository.NewRefreshTokenRepository(db)
	workspaceRepo := repository.NewWorkspaceRepository(db)
	teamRepo := repository.NewTeamRepository(db)
	issueRepo := repository.NewIssueRepository(db)
	labelRepo := repository.NewLabelRepository(db)
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
	favRepo := repository.NewFavoriteRepository(db)
	prefsRepo := repository.NewUserPreferencesRepository(db)
	assetRepo := repository.NewAssetRepository(db)
	aiSettingsRepo := repository.NewAISettingsRepository(db)

	// Dev Machine control-plane store
	devMachineRepo := repository.NewDevMachineRepository(db)

	// Screen sharing: peer-to-peer WebRTC signaled over the workspace hub
	shareRepo := repository.NewScreenShareRepository(db)

	// Services
	authSvc := service.NewAuthService(userRepo, refreshRepo, cfg.JWTSecret)
	workspaceSvc := service.NewWorkspaceService(workspaceRepo, userRepo)
	teamSvc := service.NewTeamService(teamRepo, teamStatusRepo, hub)
	notifSvc := service.NewNotificationService(notifRepo)
	issueSvc := service.NewIssueService(issueRepo, teamRepo, teamStatusRepo, historyRepo, hub, notifSvc, projectRepo)
	labelSvc := service.NewLabelService(labelRepo)
	commentSvc := service.NewCommentService(commentRepo, issueRepo, hub, notifSvc)
	projectSvc := service.NewProjectService(projectRepo)
	relationSvc := service.NewIssueRelationService(relationRepo, issueRepo)
	templateSvc := service.NewIssueTemplateService(templateRepo)
	viewSvc := service.NewViewService(viewRepo, hub)
	cycleSvc := service.NewCycleService(cycleRepo, teamRepo, hub, notifSvc)
	teamStatusSvc := service.NewTeamStatusService(teamStatusRepo, visibilityRepo)
	favSvc := service.NewFavoriteService(favRepo)
	prefsSvc := service.NewPreferencesService(prefsRepo)
	aiSettingsSvc := service.NewAISettingsService(aiSettingsRepo, workspaceRepo, issueRepo, crypto.DeriveKey(cfg.JWTSecret+":ai"))

	// Dev Machine agent registry
	devMachineAgentReg := agent.NewRegistry(
		agent.NewClaudeCodeProvider(cfg.DevMachine.ClaudeCodeImage),
		agent.NewOpenCodeProvider(cfg.DevMachine.OpenCodeImage),
		agent.NewCodexProvider(cfg.DevMachine.CodexImage),
		agent.NewCustomCLIProvider(cfg.DevMachine.CustomImage),
	)

	var devMachineEncKey []byte
	if cfg.DevMachine.EncryptionKey != "" {
		devMachineEncKey = crypto.DeriveKey(cfg.DevMachine.EncryptionKey)
	}
	devMachineSvc := service.NewDevMachineService(
		devMachineRepo, devMachineAgentReg, cfg.DevMachine.Enabled, cfg.DevMachine.Domain,
		devMachineEncKey, time.Duration(cfg.DevMachine.TicketTTLSeconds)*time.Second,
		service.DevMachineImages{
			IDE: cfg.DevMachine.IDEImage, Browser: cfg.DevMachine.BrowserImage,
			Collector: cfg.DevMachine.CollectorImage,
			Egress:    cfg.DevMachine.EgressImage,
		}, cfg.FrontendURL,
	)

	// Handlers
	healthH := handler.NewHealthHandler(db)
	authH := handler.NewAuthHandler(authSvc, cfg.Environment != "development", cfg.IsSysAdmin)
	workspaceH := handler.NewWorkspaceHandler(workspaceSvc)
	teamH := handler.NewTeamHandler(teamSvc)
	issueH := handler.NewIssueHandler(issueSvc, commentSvc, userRepo, teamStatusRepo, projectRepo, cycleRepo, relationSvc)
	labelH := handler.NewLabelHandler(labelSvc)
	projectH := handler.NewProjectHandler(projectSvc)
	notifH := handler.NewNotificationHandler(notifSvc)
	wsH := handler.NewWSHandler(hub)
	relationH := handler.NewIssueRelationHandler(relationSvc)
	templateH := handler.NewIssueTemplateHandler(templateSvc)
	viewH := handler.NewViewHandler(viewSvc)
	cycleH := handler.NewCycleHandler(cycleSvc)
	teamStatusH := handler.NewTeamStatusHandler(teamStatusSvc)
	favH := handler.NewFavoriteHandler(favSvc)
	prefsH := handler.NewPreferencesHandler(prefsSvc)
	aiSettingsH := handler.NewAISettingsHandler(aiSettingsSvc)
	devMachineH := handler.NewDevMachineHandler(devMachineSvc)
	devBoxRepo := repository.NewDevBoxRepository(db)
	devBoxSvc := service.NewDevBoxService(devBoxRepo, workspaceRepo, devMachineSvc, cfg.DemoDevMachineAllowed)
	devBoxH := handler.NewDevBoxHandler(devBoxSvc)
	go func() {
		for range time.Tick(15 * time.Second) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := devBoxSvc.SweepExpired(ctx); err != nil {
				log.WithError(err).Warn("failed to expire dev box jobs")
			}
			cancel()
		}
	}()
	analyticsRepo := repository.NewAnalyticsRepository(db)
	analyticsH := handler.NewAnalyticsHandler(analyticsRepo)
	systemH := handler.NewSystemHandler(cfg.SystemUpdaterURL, cfg.SystemUpdaterToken, cfg.IsSysAdmin)
	webhookRepo := repository.NewWebhookRepository(db)
	webhookSvc := service.NewWebhookService(webhookRepo, cfg.JWTSecret)
	webhookH := handler.NewWebhookHandler(webhookSvc)
	sharedLinkRepo := repository.NewSharedLinkRepository(db)
	sharedLinkSvc := service.NewSharedLinkService(sharedLinkRepo, workspaceRepo, teamRepo, projectRepo, viewRepo, issueRepo, userRepo, teamStatusRepo, cfg.JWTSecret)
	sharedLinkH := handler.NewSharedLinkHandler(sharedLinkSvc, cfg.FrontendURL)
	shareSvc := service.NewScreenShareService(shareRepo, userRepo, hub)
	hub.AuthorizeSharePresence = shareSvc.AuthorizePresence
	// Persist presenter contact so the no-source GC grace survives restarts.
	hub.OnPresenterSeen = func(shareID uuid.UUID) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			shareSvc.TouchPresenterSeen(ctx, shareID)
		}()
	}
	shareH := handler.NewScreenShareHandler(shareSvc, userRepo)
	store, err := storage.New(cfg.Storage)
	if err != nil {
		log.Fatalf("Failed to initialize storage: %v", err)
	}
	uploadH := handler.NewUploadHandler(store, assetRepo, issueRepo, cfg.JWTSecret)
	userAvatarRepo := repository.NewUserAvatarRepository(db)
	workspaceAvatarRepo := repository.NewWorkspaceAvatarRepository(db)
	avatarH := handler.NewAvatarHandler(store, userAvatarRepo, userRepo, workspaceAvatarRepo, workspaceRepo)
	workspaceTransferRepo := repository.NewWorkspaceTransferRepository(db)
	workspaceTransferSvc := service.NewWorkspaceTransferService(workspaceTransferRepo, store)
	workspaceTransferH := handler.NewWorkspaceTransferHandler(workspaceTransferSvc)

	// Personal API keys + MCP endpoint
	apiKeyRepo := repository.NewAPIKeyRepository(db)
	apiKeySvc := service.NewAPIKeyService(apiKeyRepo, userRepo)
	apiKeyH := handler.NewAPIKeyHandler(apiKeySvc)
	analyticsRepoMCP := repository.NewAnalyticsRepository(db)

	// Forgejo integration
	var globalForgejo *service.GlobalForgejoConfig
	if cfg.Forgejo.IsConfigured() {
		globalForgejo = &service.GlobalForgejoConfig{
			URL:   cfg.Forgejo.NormalizedURL(),
			Token: cfg.Forgejo.Token,
		}
		log.WithField("url", globalForgejo.URL).Info("Global Forgejo instance configured")
	}
	forgejoRepo := repository.NewForgejoRepository(db)
	forgejoSvc := service.NewForgejoService(
		forgejoRepo, issueRepo, teamRepo, teamStatusRepo,
		crypto.DeriveKey(cfg.JWTSecret+":forgejo"), hub, cfg.FrontendURL, cfg.ForgejoWebhookURL,
		globalForgejo,
	)
	forgejoH := handler.NewForgejoHandler(forgejoSvc)
	forgejoActionsH := handler.NewForgejoActionsHandler(forgejoSvc)
	// Forgejo Actions completion webhooks trigger CI status refreshes.
	// Reconcile open PRs every five minutes in case a delivery is missed.
	go forgejoSvc.ReconcileWebhookEvents(context.Background())
	go forgejoSvc.StartStatusReconciler(context.Background(), 5*time.Minute)

	// Archive completed issues once at startup and then hourly.
	go func() {
		archive := func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			workspaces, err := issueRepo.AutoArchiveCompleted(ctx)
			if err != nil {
				log.WithError(err).Warn("failed to auto archive completed issues")
			} else {
				for _, workspace := range workspaces {
					hub.Broadcast(workspace.WorkspaceID, realtime.Event{Type: "issues.bulk_updated", Payload: map[string]int64{"count": workspace.IssueCount}})
					log.WithFields(log.Fields{"workspace_id": workspace.WorkspaceID, "count": workspace.IssueCount}).Info("completed issues archived")
				}
			}
		}
		archive()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			archive()
		}
	}()

	mcpHandler := mcpserver.NewHTTPHandler(mcpserver.Deps{
		Version:           mcpServerVersion(),
		Users:             userRepo,
		Workspaces:        workspaceRepo,
		Teams:             teamRepo,
		TeamStatuses:      teamStatusRepo,
		Projects:          projectRepo,
		Labels:            labelRepo,
		Cycles:            cycleRepo,
		Templates:         templateRepo,
		Analytics:         analyticsRepoMCP,
		IssueSvc:          issueSvc,
		CommentSvc:        commentSvc,
		RelationSvc:       relationSvc,
		WorkspaceSvc:      workspaceSvc,
		TeamSvc:           teamSvc,
		StatusSvc:         teamStatusSvc,
		ProjectSvc:        projectSvc,
		LabelSvc:          labelSvc,
		NotifSvc:          notifSvc,
		ForgejoSvc:        forgejoSvc,
		ForgejoActionsSvc: forgejoSvc,
		DevBoxSvc:         devBoxSvc,
	})

	// Background: clean up expired refresh tokens every hour
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := refreshRepo.DeleteExpired(context.Background()); err != nil {
				log.WithError(err).Warn("failed to clean up expired refresh tokens")
			} else {
				log.Info("expired refresh tokens cleaned up")
			}
		}
	}()

	// Echo
	e := echo.New()
	e.HideBanner = true

	// Global middleware
	e.Use(mw.Recovery())
	e.Use(mw.Logging())
	e.Use(mw.CORS(cfg.FrontendURL))
	e.Use(mw.SecureHeaders())

	// Health
	e.GET("/health", healthH.Health)
	e.GET("/ready", healthH.Ready)

	// OpenAPI description (public; the Scalar reference page it feeds is
	// served by the SPA at /docs, same-origin)
	e.GET("/api/openapi.json", apidocs.SpecHandler)
	e.GET("/api/docs", apidocs.DocsRedirect)

	// Auth (public). No rate limiting or login lockout: several users share
	// one egress IP (office NAT) and jointly exhausted the per-IP auth
	// bucket, locking the app out for whole networks while other networks
	// worked. Operator directive 2026-09-13: remove all access rate limits.
	auth := e.Group("/api/auth")
	auth.POST("/register", authH.Register)
	auth.POST("/login", authH.Login)
	auth.POST("/refresh", authH.Refresh)
	auth.POST("/logout", authH.Logout)

	// Public share routes (no auth, no rate limiting — see /api/auth note)
	pub := e.Group("/api/public")
	pub.GET("/share/:token", sharedLinkH.GetPublicMeta)
	pub.GET("/share/:token/issues", sharedLinkH.ListPublicIssues)
	e.GET("/api/public/assets/:token", uploadH.PublicAsset)

	// User avatars: public read so they also render on public share views
	// (uploaded avatars point users.avatar_url here; see AvatarHandler.Get).
	e.GET("/api/avatars/:userId", avatarH.Get)
	e.GET("/api/workspace-avatars/:workspaceId", avatarH.GetWorkspace)

	// Authenticated routes
	api := e.Group("/api", mw.Auth(cfg.JWTSecret))

	// User
	api.GET("/auth/me", authH.Me)
	api.PATCH("/auth/me", authH.UpdateProfile)
	api.POST("/auth/me/avatar", avatarH.Upload)
	api.DELETE("/auth/me/avatar", avatarH.Remove)
	api.GET("/preferences", prefsH.Get)
	api.PATCH("/preferences", prefsH.Update)
	api.GET("/system/update-status", systemH.UpdateStatus)
	api.POST("/system/update", systemH.StartUpdate)

	// Workspaces (no workspace context needed for list/create)
	api.GET("/workspaces", workspaceH.List)
	api.POST("/workspaces", workspaceH.Create)
	api.POST("/workspaces/import/preview", workspaceTransferH.Preview)
	api.POST("/workspaces/import", workspaceTransferH.Import)

	// Workspace-scoped routes
	ws := api.Group("/workspaces/:slug", mw.WorkspaceMembership(workspaceRepo))
	ws.GET("", workspaceH.Get)
	ws.PATCH("", workspaceH.Update, mw.RequireOwner())
	ws.POST("/avatar", avatarH.UploadWorkspace, mw.RequireOwner())
	ws.DELETE("/avatar", avatarH.RemoveWorkspace, mw.RequireOwner())
	ws.DELETE("", workspaceH.Delete, mw.RequireOwner())
	ws.GET("/export", workspaceTransferH.Export, mw.RequirePermission(domain.PermWorkspaceTransfer))
	ws.POST("/invite", workspaceH.Invite, mw.RequirePermission("member:invite"))
	ws.GET("/members", workspaceH.ListMembers)
	ws.PATCH("/members/:userId", workspaceH.UpdateMemberRole, mw.RequirePermission("member:invite"))
	ws.DELETE("/members/:userId", workspaceH.RemoveMember, mw.RequirePermission("member:invite"))

	// Teams
	ws.GET("/teams", teamH.List)
	ws.POST("/teams", teamH.Create, mw.RequirePermission("team:manage"))
	ws.GET("/teams/:teamId", teamH.Get)
	ws.PATCH("/teams/:teamId", teamH.Update, mw.RequirePermission("team:manage"))
	ws.DELETE("/teams/:teamId", teamH.Delete, mw.RequirePermission("team:manage"))
	ws.POST("/teams/:teamId/leave", teamH.Leave)

	// Team Statuses
	ws.GET("/teams/:teamId/statuses", teamStatusH.List)
	ws.POST("/teams/:teamId/statuses", teamStatusH.Create, mw.RequirePermission("team:manage"))
	ws.PATCH("/teams/:teamId/statuses/:statusId", teamStatusH.Update, mw.RequirePermission("team:manage"))
	ws.DELETE("/teams/:teamId/statuses/:statusId", teamStatusH.Delete, mw.RequirePermission("team:manage"))

	// Cycles (team-scoped)
	ws.GET("/teams/:teamId/cycles", cycleH.List)
	ws.POST("/teams/:teamId/cycles", cycleH.Create)
	ws.GET("/teams/:teamId/cycles/velocity", cycleH.Velocity)
	ws.GET("/teams/:teamId/cycles/:cycleId", cycleH.Get)
	ws.PATCH("/teams/:teamId/cycles/:cycleId", cycleH.Update)
	ws.POST("/teams/:teamId/cycles/:cycleId/complete", cycleH.Complete)
	ws.GET("/teams/:teamId/cycles/:cycleId/burndown", cycleH.Burndown)
	ws.DELETE("/teams/:teamId/cycles/:cycleId", cycleH.Delete)

	// Issues
	ws.GET("/issues", issueH.List)
	ws.POST("/issues", issueH.Create, mw.RequirePermission("issue:create"))
	ws.PATCH("/issues/bulk", issueH.BulkUpdate, mw.RequirePermission("issue:update"))
	ws.DELETE("/issues/bulk", issueH.BulkDelete, mw.RequirePermission("issue:delete_own"))
	ws.GET("/issues/:identifier", issueH.Get)
	ws.PATCH("/issues/:identifier", issueH.Update, mw.RequirePermission("issue:update"))
	ws.POST("/issues/:identifier/archive", issueH.Archive, mw.RequirePermission("issue:update"))
	ws.DELETE("/issues/:identifier/archive", issueH.Unarchive, mw.RequirePermission("issue:update"))
	ws.DELETE("/issues/:identifier", issueH.Delete, mw.RequirePermission("issue:delete_own"))
	ws.POST("/issues/:identifier/subscribe", issueH.Subscribe)
	ws.DELETE("/issues/:identifier/subscribe", issueH.Unsubscribe)
	ws.POST("/issues/:identifier/duplicate", issueH.Duplicate, mw.RequirePermission("issue:create"))
	ws.POST("/issues/:identifier/convert-to-project", issueH.ConvertToProject, mw.RequirePermission("project:manage"))
	ws.POST("/issues/:identifier/expand-description", aiSettingsH.ExpandIssueDescription, mw.RequirePermission("issue:update"))
	ws.GET("/issues/:identifier/comments", issueH.ListComments)
	ws.POST("/issues/:identifier/comments", issueH.CreateComment, mw.RequirePermission("issue:create"))
	ws.POST("/issues/:identifier/comments/:commentId/resolve", issueH.ResolveComment, mw.RequirePermission("issue:update"))
	ws.POST("/issues/:identifier/comments/:commentId/reopen", issueH.ReopenComment, mw.RequirePermission("issue:update"))
	ws.GET("/issues/:identifier/sub-issues", issueH.ListSubIssues)
	ws.POST("/issues/:identifier/sub-issues", issueH.CreateSubIssue, mw.RequirePermission("issue:create"))
	ws.POST("/issues/:identifier/sub-issues/bulk", issueH.BulkCreateSubIssues, mw.RequirePermission("issue:create"))
	ws.GET("/issues/:identifier/history", issueH.GetHistory)
	ws.POST("/issues/:identifier/triage/accept", issueH.TriageAccept, mw.RequirePermission("issue:update"))
	ws.POST("/issues/:identifier/triage/decline", issueH.TriageDecline, mw.RequirePermission("issue:update"))

	// Issue Relations
	ws.POST("/issues/:identifier/relations", relationH.Create, mw.RequirePermission("issue:update"))
	ws.GET("/issues/:identifier/relations", relationH.List)
	ws.DELETE("/issues/:identifier/relations/:relationId", relationH.Delete, mw.RequirePermission("issue:update"))

	// Issue Templates
	ws.GET("/issue-templates", templateH.List)
	ws.POST("/issue-templates", templateH.Create, mw.RequirePermission("issue:create"))
	ws.GET("/issue-templates/:id", templateH.Get)
	ws.PATCH("/issue-templates/:id", templateH.Update, mw.RequirePermission("issue:create"))
	ws.DELETE("/issue-templates/:id", templateH.Delete, mw.RequirePermission("issue:create"))

	// Labels
	ws.GET("/labels", labelH.List)
	ws.POST("/labels", labelH.Create, mw.RequirePermission("label:manage"))
	ws.PATCH("/labels/:id", labelH.Update, mw.RequirePermission("label:manage"))
	ws.DELETE("/labels/:id", labelH.Delete, mw.RequirePermission("label:manage"))

	// Projects
	ws.GET("/projects", projectH.List)
	ws.POST("/projects", projectH.Create, mw.RequirePermission("project:manage"))
	ws.GET("/projects/:id", projectH.Get)
	ws.PATCH("/projects/:id", projectH.Update, mw.RequirePermission("project:manage"))
	ws.DELETE("/projects/:id", projectH.Delete, mw.RequirePermission("project:manage"))
	ws.GET("/teams/:teamId/projects", projectH.ListByTeam)

	// Views
	ws.GET("/views", viewH.List)
	ws.POST("/views", viewH.Create)
	ws.GET("/views/:id", viewH.Get)
	ws.PATCH("/views/:id", viewH.Update)
	ws.DELETE("/views/:id", viewH.Delete)

	// Screen sharing (P2P WebRTC; joining is presence over the workspace WS)
	ws.GET("/shares", shareH.List)
	ws.POST("/shares", shareH.Create, mw.RequirePermission(domain.PermShareCreate))
	ws.GET("/shares/:id", shareH.Get)
	ws.POST("/shares/:id/end", shareH.End)

	// Analytics
	ws.GET("/analytics/overview", analyticsH.Overview)
	ws.GET("/analytics/distribution", analyticsH.IssueDistribution)
	ws.GET("/analytics/insights", analyticsH.Insights)
	ws.GET("/analytics/burnup", analyticsH.Burnup)

	// Webhooks
	ws.GET("/webhooks", webhookH.List, mw.RequirePermission("workspace:manage"))
	ws.POST("/webhooks", webhookH.Create, mw.RequirePermission("workspace:manage"))
	ws.PATCH("/webhooks/:id", webhookH.Update, mw.RequirePermission("workspace:manage"))
	ws.DELETE("/webhooks/:id", webhookH.Delete, mw.RequirePermission("workspace:manage"))

	// AI settings
	ws.GET("/ai-settings", aiSettingsH.Get, mw.RequirePermission("issue:read"))
	ws.GET("/ai-settings/issue-copy-prompt", aiSettingsH.GetIssueCopyPrompt, mw.RequirePermission("issue:read"))
	ws.PATCH("/ai-settings", aiSettingsH.Update, mw.RequireOwner())
	ws.POST("/ai-settings/test", aiSettingsH.TestConnection, mw.RequireOwner())
	ws.POST("/ai/rework", aiSettingsH.Rework, mw.RequirePermission("issue:update"))

	// Forgejo integration
	// Public webhook endpoint (no auth, signature-verified internally)
	e.POST("/api/forgejo/webhook", forgejoH.HandleWebhook)
	e.POST("/api/dev-machine-ingest/events", devMachineH.IngestEvent, mw.MachineTokenRateLimit(20, 40))
	e.POST("/api/dev-machine-ingest/logs", devMachineH.IngestLog, mw.MachineTokenRateLimit(50, 100))

	// Forgejo integration (workspace-scoped)
	ws.GET("/forgejo/status", forgejoH.Status)
	ws.PUT("/forgejo/connection", forgejoH.Connect, mw.RequirePermission("workspace:manage"))
	ws.DELETE("/forgejo/connection", forgejoH.Disconnect, mw.RequirePermission("workspace:manage"))
	ws.GET("/forgejo/repos", forgejoH.ListRepos, mw.RequirePermission("workspace:manage"))
	ws.POST("/forgejo/repos", forgejoH.LinkRepos, mw.RequirePermission("workspace:manage"))
	ws.DELETE("/forgejo/repos/:id", forgejoH.UnlinkRepo, mw.RequirePermission("workspace:manage"))
	ws.GET("/forgejo/repos/:repoId/actions/runs", forgejoActionsH.ListRuns, mw.RequirePermission("issue:read"))
	ws.GET("/forgejo/repos/:repoId/actions/runs/:runId", forgejoActionsH.GetRun, mw.RequirePermission("issue:read"))
	ws.GET("/forgejo/repos/:repoId/actions/runs/:runId/jobs", forgejoActionsH.ListJobs, mw.RequirePermission("issue:read"))
	ws.GET("/forgejo/repos/:repoId/actions/runs/:runId/jobs/:jobId", forgejoActionsH.GetJob, mw.RequirePermission("issue:read"))
	ws.GET("/forgejo/repos/:repoId/actions/runs/:runId/jobs/:jobId/logs", forgejoActionsH.GetJobLogs, mw.RequirePermission("issue:read"))
	ws.POST("/forgejo/repos/:repoId/actions/runs/:runId/cancel", forgejoActionsH.Cancel, mw.RequirePermission("workspace:manage"))
	ws.GET("/forgejo/user-mappings", forgejoH.ListUserMappings)
	ws.PUT("/forgejo/user-mappings", forgejoH.UpdateUserMappings, mw.RequirePermission("workspace:manage"))
	ws.GET("/forgejo/auto-transitions", forgejoH.ListAutoTransitions)
	ws.PATCH("/forgejo/auto-transitions", forgejoH.UpdateAutoTransitions, mw.RequirePermission("workspace:manage"))
	ws.GET("/issues/:identifier/forgejo", forgejoH.IssueForgejoActivity)
	ws.GET("/forgejo/issue-links", forgejoH.AgentIssueLinks)

	// Dev Machines — guarded by demo-mode restriction when active
	dm := ws.Group("", mw.DevMachineDemoGuard(cfg.DemoDevMachineAllowed))
	dm.GET("/dev-machines", devMachineH.List, mw.RequirePermission("dev_machine:read"))
	dm.POST("/dev-machines", devMachineH.Create, mw.RequirePermission("dev_machine:create"))
	dm.DELETE("/dev-machines/bulk", devMachineH.BulkDelete, mw.RequirePermission("dev_machine:admin"))
	dm.POST("/dev-machines/bulk/permanent-delete", devMachineH.BulkPermanentDelete, mw.RequirePermission("dev_machine:admin"))
	dm.GET("/dev-machine-names/suggestion", devMachineH.NameSuggestion, mw.RequirePermission("dev_machine:create"))
	dm.GET("/dev-machine-names/availability", devMachineH.NameAvailability, mw.RequirePermission("dev_machine:create"))
	dm.GET("/dev-machine-policy", devMachineH.GetPolicy, mw.RequirePermission("dev_machine:read"))
	dm.PATCH("/dev-machine-policy", devMachineH.UpdatePolicy, mw.RequirePermission("dev_machine:admin"))
	dm.GET("/dev-machine-scope-settings", devMachineH.ScopeSettings, mw.RequirePermission("dev_machine:read"))
	dm.GET("/dev-machine-scope-setting", devMachineH.ScopeSetting, mw.RequirePermission("dev_machine:read"))
	dm.PUT("/dev-machine-scope-setting", devMachineH.UpdateScopeSetting, mw.RequirePermission("dev_machine:manage"))
	dm.DELETE("/dev-machine-scope-setting", devMachineH.DeleteScopeSetting, mw.RequirePermission("dev_machine:manage"))
	dm.GET("/dev-machine-environments", devMachineH.Environments, mw.RequirePermission("dev_machine:read"))
	dm.POST("/dev-machine-environments", devMachineH.SnapshotEnvironment, mw.RequirePermission("dev_machine:admin"))
	dm.GET("/dev-machine-environments/:environmentId", devMachineH.GetEnvironment, mw.RequirePermission("dev_machine:read"))
	dm.DELETE("/dev-machine-environments/:environmentId", devMachineH.DeleteEnvironment, mw.RequirePermission("dev_machine:admin"))
	dm.GET("/dev-machine-providers", devMachineH.Providers, mw.RequirePermission("dev_machine:read"))
	dm.GET("/dev-machines/:machineId", devMachineH.Get, mw.RequirePermission("dev_machine:read"))
	dm.PATCH("/dev-machines/:machineId", devMachineH.Update, mw.RequirePermission("dev_machine:manage"))
	dm.DELETE("/dev-machines/:machineId", devMachineH.Delete, mw.RequirePermission("dev_machine:admin"))
	dm.POST("/dev-machines/:machineId/permanent-delete", devMachineH.PermanentDelete, mw.RequirePermission("dev_machine:admin"))
	dm.POST("/dev-machines/:machineId/start", devMachineH.Start, mw.RequirePermission("dev_machine:manage"))
	dm.POST("/dev-machines/:machineId/stop", devMachineH.Stop, mw.RequirePermission("dev_machine:manage"))
	dm.POST("/dev-machines/:machineId/pause", devMachineH.Pause, mw.RequirePermission("dev_machine:manage"))
	dm.POST("/dev-machines/:machineId/teardown", devMachineH.Teardown, mw.RequirePermission("dev_machine:manage"))
	dm.POST("/dev-machines/:machineId/activity", devMachineH.TouchActivity, mw.RequirePermission("dev_machine:read"))
	dm.GET("/dev-machines/:machineId/checkouts", devMachineH.Checkouts, mw.RequirePermission("dev_machine:read"))
	dm.POST("/dev-machines/:machineId/checkouts", devMachineH.CheckoutIssue, mw.RequirePermission("dev_machine:manage"))
	dm.GET("/dev-machines/:machineId/events", devMachineH.Events, mw.RequirePermission("dev_machine:read"))
	dm.GET("/dev-machines/:machineId/logs", devMachineH.Logs, mw.RequirePermission("dev_machine:read"))
	dm.GET("/dev-machines/:machineId/services", devMachineH.Services, mw.RequirePermission("dev_machine:read"))
	dm.GET("/dev-machines/:machineId/providers", devMachineH.MachineProviders, mw.RequirePermission("dev_machine:read"))
	dm.GET("/dev-machines/:machineId/resource-usage", devMachineH.ResourceUsage, mw.RequirePermission("dev_machine:read"))
	dm.POST("/dev-machines/:machineId/services/:service/launch", devMachineH.LaunchService, mw.RequirePermission("dev_machine:read"))
	dm.GET("/dev-machines/:machineId/terminal-sessions", devMachineH.ListTerminalSessions, mw.RequirePermission("dev_machine:read"))
	dm.POST("/dev-machines/:machineId/terminal-sessions", devMachineH.CreateTerminalSession, mw.RequirePermission("dev_machine:read"))
	dm.POST("/dev-machines/:machineId/terminal-sessions/:sessionId/close", devMachineH.CloseTerminalSession, mw.RequirePermission("dev_machine:read"))
	dm.GET("/dev-machines/:machineId/agent-runs", devMachineH.ListMachineAgentRuns, mw.RequirePermission("dev_machine:read"))
	dm.POST("/dev-machines/:machineId/agent-runs", devMachineH.CreateAgentRun, mw.RequirePermission("dev_machine:manage"))
	dm.GET("/agent-runs", devMachineH.ListAgentRuns, mw.RequirePermission("dev_machine:read"))
	dm.GET("/agent-runs/:agentRunId", devMachineH.GetAgentRun, mw.RequirePermission("dev_machine:read"))
	dm.POST("/agent-runs/:agentRunId/cancel", devMachineH.CancelAgentRun, mw.RequirePermission("dev_machine:manage"))
	dm.GET("/agent-runs/:agentRunId/trace", devMachineH.AgentRunTrace, mw.RequirePermission("dev_machine:read"))

	// Existing development hosts use an outbound worker and do not require Docker.
	dm.GET("/dev-boxes", devBoxH.List, mw.RequirePermission(domain.PermDevMachineRead))
	dm.POST("/dev-boxes", devBoxH.Create, mw.RequirePermission(domain.PermDevMachineCreate))
	dm.PATCH("/dev-boxes/:boxId", devBoxH.Update, mw.RequirePermission(domain.PermDevMachineManage))
	dm.POST("/dev-boxes/:boxId/rotate-token", devBoxH.RotateToken, mw.RequirePermission(domain.PermDevMachineManage))
	dm.DELETE("/dev-boxes/:boxId", devBoxH.Revoke, mw.RequirePermission(domain.PermDevMachineManage))
	dm.POST("/dev-boxes/:boxId/jobs", devBoxH.CreateJob, mw.RequirePermission(domain.PermDevMachineManage))
	dm.GET("/dev-box-jobs", devBoxH.ListJobs, mw.RequirePermission(domain.PermDevMachineRead))
	dm.GET("/dev-box-jobs/:jobId", devBoxH.GetJob, mw.RequirePermission(domain.PermDevMachineRead))
	dm.POST("/dev-box-jobs/:jobId/cancel", devBoxH.CancelJob, mw.RequirePermission(domain.PermDevMachineManage))
	dm.GET("/dev-box-jobs/:jobId/logs", devBoxH.JobLogs, mw.RequirePermission(domain.PermDevMachineRead))
	workers := e.Group("/api/dev-box-worker", mw.DevBoxWorkerAuth(devBoxSvc))
	workers.POST("/heartbeat", devBoxH.Heartbeat)
	workers.POST("/claim", devBoxH.Claim)
	workers.POST("/jobs/:jobId/start", devBoxH.Start)
	workers.POST("/jobs/:jobId/renew", devBoxH.Renew)
	workers.POST("/jobs/:jobId/logs", devBoxH.AppendLog)
	workers.POST("/jobs/:jobId/complete", devBoxH.Complete)

	// Favorites
	ws.GET("/favorites", favH.List)
	ws.POST("/favorites", favH.Create)
	ws.DELETE("/favorites/:id", favH.Delete)

	// Shared Links
	ws.GET("/shared-links", sharedLinkH.List)
	ws.POST("/shared-links", sharedLinkH.Create)
	ws.PATCH("/shared-links/:id", sharedLinkH.Update)
	ws.DELETE("/shared-links/:id", sharedLinkH.Delete)

	// Uploads
	ws.POST("/upload", uploadH.Upload, mw.RequirePermission("issue:create"))
	ws.GET("/assets/:assetId", uploadH.GetAsset)
	ws.POST("/issues/:identifier/prompt-assets", uploadH.SignIssuePromptAssets)

	// WebSocket
	ws.GET("/ws", wsH.Handle)

	// Notifications (user-scoped, not workspace-scoped)
	api.GET("/notifications", notifH.List)
	api.PATCH("/notifications/:id", notifH.Update)
	api.POST("/notifications/:id/read", notifH.MarkRead)
	api.POST("/notifications/:id/unread", notifH.MarkUnread)
	api.POST("/notifications/:id/snooze", notifH.Snooze)
	api.POST("/notifications/:id/unsnooze", notifH.Unsnooze)
	api.POST("/notifications/:id/archive", notifH.Archive)
	api.POST("/notifications/:id/unarchive", notifH.Unarchive)
	api.POST("/notifications/mark-all-read", notifH.MarkAllRead)

	// Personal API keys (user-scoped, usable across the owner's workspaces;
	// no rate limiting per the /api/auth note above)
	api.GET("/api-keys", apiKeyH.List)
	api.POST("/api-keys", apiKeyH.Create)
	api.DELETE("/api-keys/:id", apiKeyH.Revoke)

	// MCP endpoint: stateless streamable HTTP, authenticated with a
	// personal API key (Authorization: Bearer ku_...). Tools run with the
	// key owner's identity and workspace RBAC.
	e.Any("/mcp", echo.WrapHandler(mcpHandler), mw.APIKeyAuth(apiKeySvc))

	// Start
	addr := net.JoinHostPort(cfg.BindAddress, strconv.Itoa(cfg.Port))
	log.Infof("Starting server on %s", addr)
	if err := e.Start(addr); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func mcpServerVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "dev"
}

func runMigrate(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: server migrate [up|down|version]")
		os.Exit(1)
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}

	m, err := migrate.New("file://migrations", dbURL)
	if err != nil {
		log.Fatalf("Failed to create migrate instance: %v", err)
	}
	defer m.Close()

	switch args[0] {
	case "up":
		if err := m.Up(); err != nil && err != migrate.ErrNoChange {
			log.Fatalf("Migration up failed: %v", err)
		}
		log.Info("Migrations applied successfully")
	case "down":
		if err := m.Steps(-1); err != nil {
			log.Fatalf("Migration down failed: %v", err)
		}
		log.Info("Rolled back one migration")
	case "version":
		version, dirty, err := m.Version()
		if err != nil {
			log.Fatalf("Failed to get version: %v", err)
		}
		fmt.Printf("Version: %d, Dirty: %v\n", version, dirty)
	default:
		fmt.Printf("Unknown migrate command: %s\n", args[0])
		fmt.Println("Usage: server migrate [up|down|version]")
		os.Exit(1)
	}
}
