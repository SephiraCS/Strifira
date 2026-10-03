package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/realtime"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/ku/ku-backend/pkg/crypto"
	fj "github.com/ku/ku-backend/pkg/forgejo"
	log "github.com/sirupsen/logrus"
)

var issueIdentifierRegex = regexp.MustCompile(`(?i)([A-Z][A-Z0-9]*-\d+)`)
var forgejoLoginRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)
var ErrForgejoNotConnected = errors.New("Forgejo is not connected")

// issueKeywordRegex matches closing keywords followed by an issue identifier,
// e.g. "Fixes KUA-12" or "resolves kua-3 and closes TS-4".
var issueKeywordRegex = regexp.MustCompile(`(?i)\b(?:fix(?:es|ed)?|clos(?:es|ed)?|resolv(?:es|ed)?)\s+([A-Z][A-Z0-9]*-\d+)`)

// forgejoLifecycleEvents is the closed set of auto-transition events.
var forgejoLifecycleEvents = map[string]bool{
	"branch_created":  true,
	"pr_opened":       true,
	"pr_checks_green": true,
	"pr_merged":       true,
}

// GlobalForgejoConfig holds env-provided instance credentials (SaaS-style
// mode). When set, every workspace shares the connection and per-workspace
// connection rows are not used.
type GlobalForgejoConfig struct {
	URL   string
	Token string
}

type ForgejoService struct {
	fRepo          forgejoStore
	issueRepo      repository.IssueRepo
	teamRepo       repository.TeamRepo
	teamStatusRepo repository.TeamStatusRepo
	encryptionKey  []byte
	hub            *realtime.Hub
	frontendURL    string
	webhookURL     string               // optional override for private-network dev
	global         *GlobalForgejoConfig // nil = per-workspace connections
}

type forgejoConnection struct {
	InstanceURL string
	Token       string
	Account     *fj.User
	Global      bool
}

func NewForgejoService(
	fRepo *repository.ForgejoRepository,
	issueRepo repository.IssueRepo,
	teamRepo repository.TeamRepo,
	teamStatusRepo repository.TeamStatusRepo,
	encryptionKey []byte,
	hub *realtime.Hub,
	frontendURL string,
	webhookURL string,
	global *GlobalForgejoConfig,
) *ForgejoService {
	return &ForgejoService{
		fRepo:          fRepo,
		issueRepo:      issueRepo,
		teamRepo:       teamRepo,
		teamStatusRepo: teamStatusRepo,
		encryptionKey:  encryptionKey,
		hub:            hub,
		frontendURL:    frontendURL,
		webhookURL:     webhookURL,
		global:         global,
	}
}

// IsGlobalMode returns true when a shared instance connection is configured
// via environment variables.
func (s *ForgejoService) IsGlobalMode() bool {
	return s.global != nil
}

// resolveConnection returns the credentials for a workspace. Global env
// configuration wins over per-workspace rows.
func (s *ForgejoService) resolveConnection(ctx context.Context, workspaceID uuid.UUID) (*forgejoConnection, error) {
	if s.global != nil {
		return &forgejoConnection{InstanceURL: s.global.URL, Token: s.global.Token, Global: true}, nil
	}
	conn, err := s.fRepo.GetConnectionByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, ErrForgejoNotConnected
	}
	token, err := crypto.Decrypt(conn.Token, s.encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("decrypting Forgejo token: %w", err)
	}
	return &forgejoConnection{InstanceURL: conn.InstanceURL, Token: token}, nil
}

func (c *forgejoConnection) client() *fj.Client {
	return fj.NewClient(c.InstanceURL, c.Token)
}

// Connect validates the token against the instance and stores the connection.
func (s *ForgejoService) Connect(ctx context.Context, workspaceID uuid.UUID, instanceURL, token string) (*dto.ForgejoAccountResponse, error) {
	if s.global != nil {
		return nil, fmt.Errorf("Forgejo is centrally managed — per-workspace setup is not available")
	}
	if strings.TrimSpace(instanceURL) == "" || strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("instance URL and token are required")
	}
	instanceURL, err := fj.NormalizeInstanceURL(instanceURL)
	if err != nil {
		return nil, fmt.Errorf("invalid instance URL")
	}
	repos, err := s.fRepo.ListReposByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	for _, repo := range repos {
		linkedURL, err := fj.NormalizeInstanceURL(repo.InstanceURL)
		if err != nil || linkedURL != instanceURL {
			return nil, fmt.Errorf("unlink existing repositories before changing the Forgejo instance")
		}
	}

	user, err := fj.NewClient(instanceURL, token).GetCurrentUser(ctx)
	if err != nil {
		return nil, fmt.Errorf("token validation failed: %w", err)
	}

	encrypted, err := crypto.Encrypt(token, s.encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("encrypting token: %w", err)
	}
	avatarURL := user.AvatarURL
	conn := &domain.ForgejoConnection{
		ID: uuid.New(), WorkspaceID: workspaceID, InstanceURL: instanceURL,
		Token: encrypted, AccountLogin: user.Login, AccountAvatarURL: &avatarURL,
	}
	if err := s.fRepo.UpsertConnection(ctx, conn); err != nil {
		return nil, err
	}

	s.seedDefaultsIfMissing(ctx, workspaceID)
	return &dto.ForgejoAccountResponse{Login: user.Login, AvatarURL: user.AvatarURL}, nil
}

// Disconnect unlinks every repo before removing the connection. Local cleanup
// failures retain the credentials for retry; Forgejo webhooks are best-effort.
func (s *ForgejoService) Disconnect(ctx context.Context, workspaceID uuid.UUID) error {
	repos, err := s.fRepo.ListReposByWorkspace(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("listing linked Forgejo repositories: %w", err)
	}
	for _, repo := range repos {
		if err := s.UnlinkRepo(ctx, workspaceID, repo.ID); err != nil {
			return fmt.Errorf("unlinking Forgejo repository %s: %w", repo.FullName, err)
		}
	}
	return s.fRepo.DeleteConnection(ctx, workspaceID)
}

// seedDefaultsIfMissing seeds transition rules once (never overwrites user customizations).
func (s *ForgejoService) seedDefaultsIfMissing(ctx context.Context, workspaceID uuid.UUID) {
	existing, err := s.fRepo.ListAutoTransitions(ctx, workspaceID)
	if err != nil {
		return
	}
	if len(existing) == 0 {
		s.seedDefaults(ctx, workspaceID)
		return
	}
	for _, rule := range existing {
		if rule.IsActive && rule.TargetStatus == "pr" {
			s.ensureReviewStatuses(ctx, workspaceID)
			return
		}
	}
}

// seedDefaults creates the auto-transition rules and PR review state.
func (s *ForgejoService) seedDefaults(ctx context.Context, workspaceID uuid.UUID) {
	defaults := []struct {
		Event  string
		Status string
	}{
		{"branch_created", "in_progress"},
		{"pr_opened", "in_progress"},
		{"pr_checks_green", "pr"},
		{"pr_merged", "done"},
	}
	for _, d := range defaults {
		_ = s.fRepo.UpsertAutoTransition(ctx, &domain.ForgejoAutoTransition{
			ID: uuid.New(), WorkspaceID: workspaceID,
			Event: d.Event, TargetStatus: d.Status,
			AssignForgejoUser: d.Status == "in_progress", IsActive: true,
		})
	}

	s.ensureReviewStatuses(ctx, workspaceID)
}

func (s *ForgejoService) ensureReviewStatuses(ctx context.Context, workspaceID uuid.UUID) {
	if s.teamRepo == nil {
		return
	}
	teams, err := s.teamRepo.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return
	}
	for i := range teams {
		if _, err := s.ensureReviewStatus(ctx, teams[i].ID); err != nil {
			log.WithError(err).WithField("team_id", teams[i].ID).Warn("failed to create Forgejo review status")
		}
	}
}

func (s *ForgejoService) ensureReviewStatus(ctx context.Context, teamID uuid.UUID) (*domain.TeamStatus, error) {
	status, err := s.teamStatusRepo.GetByTeamAndSlug(ctx, teamID, "pr")
	if err != nil || status != nil {
		return status, err
	}
	position, err := s.teamStatusRepo.NextPosition(ctx, teamID)
	if err != nil {
		return nil, err
	}
	status = &domain.TeamStatus{ID: uuid.New(), TeamID: teamID, Name: "PR", Slug: "pr", Category: domain.StatusCategoryStarted, Color: new("#8b5cf6"), Position: position}
	if err := s.teamStatusRepo.Create(ctx, status); err != nil {
		// Another concurrent delivery may have created the same status.
		if existing, lookupErr := s.teamStatusRepo.GetByTeamAndSlug(ctx, teamID, "pr"); lookupErr == nil && existing != nil {
			return existing, nil
		}
		return nil, err
	}
	return status, nil
}

// webhookTargetURL derives the URL webhook deliveries should be sent to.
func (s *ForgejoService) webhookTargetURL() string {
	if s.webhookURL != "" {
		return strings.TrimRight(s.webhookURL, "/")
	}
	base := strings.Replace(s.frontendURL, ":5173", ":8080", 1)
	return strings.TrimRight(base, "/") + "/api/forgejo/webhook"
}

// ListLinkedRepos reads active workspace links without resolving credentials,
// contacting Forgejo, or initializing integration defaults.
func (s *ForgejoService) ListLinkedRepos(ctx context.Context, workspaceID uuid.UUID) ([]dto.ForgejoRepoResponse, error) {
	repos, err := s.fRepo.ListReposByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	result := make([]dto.ForgejoRepoResponse, 0, len(repos))
	for _, repo := range repos {
		if !repo.IsActive || repo.WorkspaceID != workspaceID {
			continue
		}
		result = append(result, dto.ForgejoRepoResponse{
			ID: repo.ID.String(), ForgejoRepoID: repo.ForgejoRepoID, FullName: repo.FullName,
			InstanceURL: repo.InstanceURL, DefaultBranch: repo.DefaultBranch, IsActive: repo.IsActive,
		})
	}
	return result, nil
}

// GetStatus returns the integration status for a workspace.
func (s *ForgejoService) GetStatus(ctx context.Context, workspaceID uuid.UUID) (*dto.ForgejoStatusResponse, error) {
	resp := &dto.ForgejoStatusResponse{Repos: []dto.ForgejoRepoResponse{}}

	conn, err := s.resolveConnection(ctx, workspaceID)
	if err != nil && !errors.Is(err, ErrForgejoNotConnected) {
		return nil, err
	}
	if err == nil && conn != nil {
		resp.Configured = true
		resp.GlobalConfig = conn.Global
		resp.InstanceURL = conn.InstanceURL

		if account, err := conn.client().GetCurrentUser(ctx); err == nil {
			resp.Connected = true
			resp.Account = &dto.ForgejoAccountResponse{Login: account.Login, AvatarURL: account.AvatarURL}
		}

		// Global mode has no Connect flow to seed defaults; self-heal once.
		s.seedDefaultsIfMissing(ctx, workspaceID)

		repos, err := s.fRepo.ListReposByWorkspace(ctx, workspaceID)
		if err != nil {
			return nil, err
		}
		for _, r := range repos {
			resp.Repos = append(resp.Repos, dto.ForgejoRepoResponse{
				ID: r.ID.String(), ForgejoRepoID: r.ForgejoRepoID, FullName: r.FullName,
				InstanceURL: r.InstanceURL, DefaultBranch: r.DefaultBranch, IsActive: r.IsActive,
			})
		}

	}
	return resp, nil
}

// ListAvailableRepos lists repositories visible to the connection token.
func (s *ForgejoService) ListAvailableRepos(ctx context.Context, workspaceID uuid.UUID) ([]dto.ForgejoAvailableRepoResponse, error) {
	conn, err := s.resolveConnection(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("Forgejo is not connected")
	}
	remote, err := conn.client().ListRepos(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing repos: %w", err)
	}

	linked, err := s.fRepo.ListReposByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	linkedMap := make(map[int64]bool)
	for _, lr := range linked {
		linkedMap[lr.ForgejoRepoID] = true
	}

	result := make([]dto.ForgejoAvailableRepoResponse, 0, len(remote))
	for _, r := range remote {
		result = append(result, dto.ForgejoAvailableRepoResponse{
			ForgejoRepoID: r.ID, FullName: r.FullName, DefaultBranch: r.DefaultBranch,
			Private: r.Private, Linked: linkedMap[r.ID],
		})
	}
	return result, nil
}

// LinkRepos links selected repos and creates a signed webhook on each.
func (s *ForgejoService) LinkRepos(ctx context.Context, workspaceID uuid.UUID, req dto.LinkForgejoReposRequest) error {
	if len(req.ForgejoRepoIDs) == 0 || len(req.ForgejoRepoIDs) > 100 {
		return fmt.Errorf("select between 1 and 100 repositories")
	}
	conn, err := s.resolveConnection(ctx, workspaceID)
	if err != nil {
		return err
	}
	client := conn.client()

	remote, err := client.ListRepos(ctx)
	if err != nil {
		return fmt.Errorf("listing repos: %w", err)
	}
	remoteMap := make(map[int64]fj.Repository)
	for _, r := range remote {
		remoteMap[r.ID] = r
	}

	target := s.webhookTargetURL()
	for _, id := range req.ForgejoRepoIDs {
		remoteRepo, ok := remoteMap[id]
		if !ok {
			return fmt.Errorf("repository is not available to the Forgejo connection")
		}
		existing, err := s.fRepo.GetRepoByForgejoID(ctx, workspaceID, id)
		if err != nil {
			return err
		}
		if existing != nil {
			continue
		}

		secretBytes := make([]byte, 32)
		if _, err := rand.Read(secretBytes); err != nil {
			return fmt.Errorf("generating webhook secret: %w", err)
		}
		secret := hex.EncodeToString(secretBytes)
		encryptedSecret, err := crypto.Encrypt(secret, s.encryptionKey)
		if err != nil {
			return fmt.Errorf("encrypting webhook secret: %w", err)
		}

		owner, name, ok := splitRepoOwnerName(remoteRepo.FullName)
		if !ok {
			continue
		}
		hook, err := client.CreateWebhook(ctx, owner, name, target, secret, nil)
		if err != nil {
			log.WithError(err).WithField("repo", remoteRepo.FullName).Warn("failed to create Forgejo webhook")
			return fmt.Errorf("creating webhook on %s failed: %w", remoteRepo.FullName, err)
		}

		repo := &domain.ForgejoRepoModel{
			ID: uuid.New(), WorkspaceID: workspaceID, ForgejoRepoID: remoteRepo.ID,
			FullName: remoteRepo.FullName, InstanceURL: conn.InstanceURL,
			DefaultBranch: remoteRepo.DefaultBranch, WebhookID: &hook.ID,
			WebhookSecret: encryptedSecret, IsActive: true,
		}
		if err := s.fRepo.CreateRepo(ctx, repo); err != nil {
			log.WithError(err).WithField("repo", remoteRepo.FullName).Warn("failed to link repo")
			// Persisting the link can fail (for example, another workspace
			// already linked the repo). Do not leave an orphaned webhook.
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			cleanupErr := client.DeleteWebhook(cleanupCtx, owner, name, hook.ID)
			cancel()
			if cleanupErr != nil {
				log.WithError(cleanupErr).WithField("repo", remoteRepo.FullName).Warn("failed to remove unpersisted Forgejo webhook")
				return fmt.Errorf("repository link could not be saved; its webhook could not be removed")
			}
			return fmt.Errorf("repository link could not be saved")
		}
	}
	return nil
}

// UnlinkRepo removes the webhook on the Forgejo side and the linked row.
func (s *ForgejoService) UnlinkRepo(ctx context.Context, workspaceID, repoID uuid.UUID) error {
	repo, err := s.fRepo.GetRepoByID(ctx, repoID)
	if err != nil || repo == nil || repo.WorkspaceID != workspaceID {
		if err == nil {
			err = fmt.Errorf("repo not linked in this workspace")
		}
		return err
	}

	if repo.WebhookID != nil {
		conn, err := s.resolveConnection(ctx, workspaceID)
		if err == nil {
			instance, instanceErr := fj.NormalizeInstanceURL(conn.InstanceURL)
			linkedInstance, linkedErr := fj.NormalizeInstanceURL(repo.InstanceURL)
			owner, name, ok := splitRepoOwnerName(repo.FullName)
			if ok && instanceErr == nil && linkedErr == nil && instance == linkedInstance {
				_ = conn.client().DeleteWebhook(ctx, owner, name, *repo.WebhookID)
			}
		}
	}
	return s.fRepo.DeleteRepo(ctx, repoID)
}

func splitRepoOwnerName(fullName string) (string, string, bool) {
	parts := strings.Split(strings.TrimSpace(fullName), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// --- Webhook processing ---

// forgejoWebhookEnvelope carries the fields every Forgejo payload shares.
type forgejoWebhookEnvelope struct {
	Repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
		HTMLURL  string `json:"html_url"`
	} `json:"repository"`
	Run struct {
		Repository struct {
			FullName string `json:"full_name"`
			HTMLURL  string `json:"html_url"`
		} `json:"repository"`
	} `json:"run"`
}

func instanceOrigin(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)
}

// VerifyWebhookSignature authenticates a delivery against the linked repo's
// webhook secret. The repository html_url in the payload pins the instance so
// the full_name lookup cannot collide across instances.
func (s *ForgejoService) VerifyWebhookSignature(ctx context.Context, payload []byte, signature string) (*domain.ForgejoRepoModel, bool) {
	var envelope forgejoWebhookEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, false
	}
	origin := instanceOrigin(envelope.Repository.HTMLURL)
	if origin == "" && envelope.Run.Repository.HTMLURL != "" {
		envelope.Repository.FullName = envelope.Run.Repository.FullName
		origin = instanceOrigin(envelope.Run.Repository.HTMLURL)
	}
	if origin == "" || envelope.Repository.FullName == "" {
		return nil, false
	}
	repo, err := s.fRepo.GetRepoByFullName(ctx, origin, envelope.Repository.FullName)
	if err != nil || repo == nil {
		return nil, false
	}
	secret, err := crypto.Decrypt(repo.WebhookSecret, s.encryptionKey)
	if err != nil {
		return nil, false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(strings.TrimSpace(signature)), []byte(expected)) {
		return nil, false
	}
	return repo, true
}

// HandleWebhookEvent processes an incoming Forgejo webhook event.
func (s *ForgejoService) HandleWebhookEvent(ctx context.Context, eventType string, payload []byte, repo *domain.ForgejoRepoModel) error {
	switch eventType {
	case "pull_request":
		err := s.processPullRequestEvent(ctx, payload, repo)
		if errors.Is(err, repository.ErrForgejoStalePullRequest) {
			return nil
		}
		return err
	case "push":
		return s.processPushEvent(ctx, payload, repo)
	case "create":
		return s.processCreateEvent(ctx, payload, repo)
	case "status":
		return s.processStatusEvent(ctx, payload, repo)
	case "action_run_success", "action_run_failure", "action_run_recover":
		return s.processActionRunEvent(ctx, payload, repo)
	default:
		return nil
	}
}

func (s *ForgejoService) broadcastAppRefresh(workspaceID uuid.UUID, resources ...string) {
	if s.hub == nil {
		return
	}
	s.hub.Broadcast(workspaceID, realtime.Event{
		Type: "app.refresh",
		Payload: map[string]any{
			"source":    "forgejo",
			"resources": resources,
		},
	})
}

func (s *ForgejoService) broadcastRealtimeEvent(workspaceID uuid.UUID, event realtime.Event) {
	if s.hub == nil {
		return
	}
	s.hub.Broadcast(workspaceID, event)
}

type fjWebhookPR struct {
	Action  string `json:"action"`
	Number  int    `json:"number"`
	Changes struct {
		Title *struct {
			From string `json:"from"`
		} `json:"title"`
	} `json:"changes"`
	PullRequest struct {
		Number    int        `json:"number"`
		Title     string     `json:"title"`
		State     string     `json:"state"`
		Draft     bool       `json:"draft"`
		Merged    bool       `json:"merged"`
		MergedAt  *time.Time `json:"merged_at"`
		ClosedAt  *time.Time `json:"closed_at"`
		HTMLURL   string     `json:"html_url"`
		Body      string     `json:"body"`
		Additions int        `json:"additions"`
		Deletions int        `json:"deletions"`
		Head      struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		User struct {
			Login     string `json:"login"`
			AvatarURL string `json:"avatar_url"`
		} `json:"user"`
	} `json:"pull_request"`
}

// prStateFromEvent maps a Forgejo PR payload onto ku's PR state vocabulary.
func prStateFromEvent(payload fjWebhookPR) string {
	switch {
	case payload.PullRequest.Merged:
		return "merged"
	case payload.PullRequest.State != "open":
		// A closed draft is closed; only open PRs can be drafts.
		return "closed"
	case payload.PullRequest.Draft:
		return "draft"
	default:
		return "open"
	}
}

func (s *ForgejoService) processPullRequestEvent(ctx context.Context, payload []byte, repo *domain.ForgejoRepoModel) error {
	var event fjWebhookPR
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("unmarshaling PR event: %w", err)
	}

	state := prStateFromEvent(event)
	referenced, err := s.resolveIssuesFromRefs(ctx, repo.WorkspaceID,
		event.PullRequest.Head.Ref, event.PullRequest.Title, event.PullRequest.Body)
	if err != nil {
		return err
	}
	keywordIssues, err := s.resolveKeywordIssues(ctx, repo.WorkspaceID, event.PullRequest.Title, event.PullRequest.Body)
	if err != nil {
		return err
	}

	linkedIssues := referenced
	for _, issue := range keywordIssues {
		found := false
		for _, existing := range linkedIssues {
			if existing.ID == issue.ID {
				found = true
				break
			}
		}
		if !found {
			linkedIssues = append(linkedIssues, issue)
		}
	}

	avatarURL := event.PullRequest.User.AvatarURL
	headRef := event.PullRequest.Head.Ref
	headSHA := event.PullRequest.Head.SHA
	baseRef := event.PullRequest.Base.Ref

	pr := &domain.ForgejoPullRequest{
		ID: uuid.New(), WorkspaceID: repo.WorkspaceID, ForgejoRepoID: repo.ID,
		Number: event.PullRequest.Number, Title: event.PullRequest.Title, Body: event.PullRequest.Body, State: state,
		AuthorLogin: event.PullRequest.User.Login, AuthorAvatarURL: &avatarURL,
		HTMLURL: event.PullRequest.HTMLURL, HeadBranch: &headRef, HeadSHA: &headSHA,
		BaseBranch: &baseRef, Additions: event.PullRequest.Additions,
		Deletions: event.PullRequest.Deletions,
		MergedAt:  event.PullRequest.MergedAt, ClosedAt: event.PullRequest.ClosedAt,
	}
	if err := s.fRepo.UpsertPullRequest(ctx, pr); err != nil {
		if errors.Is(err, repository.ErrForgejoStalePullRequest) {
			return nil
		}
		return fmt.Errorf("upserting PR: %w", err)
	}

	issueIDs := make([]uuid.UUID, 0, len(linkedIssues))
	keywordSet := make(map[uuid.UUID]bool)
	for _, issue := range keywordIssues {
		keywordSet[issue.ID] = true
	}
	for _, issue := range linkedIssues {
		issueIDs = append(issueIDs, issue.ID)
	}
	currentGreen := pr.State == "open" && pr.ChecksGreen && headSHA != ""
	active := pr.State == "open" || pr.State == "draft"
	existing, err := s.fRepo.ListIssueIDsForPR(ctx, pr.ID)
	if err != nil {
		return err
	}
	handled := make(map[uuid.UUID]bool)
	if active {
		for _, issue := range linkedIssues {
			if slices.Contains(existing, issue.ID) {
				continue
			}
			// A newly referenced issue inherits the PR's current stage: drafts
			// and edited bodies still mean work has started. Persist the link
			// after the transition succeeds so failures stay retryable.
			transition := "pr_opened"
			if currentGreen {
				transition = "pr_checks_green"
			}
			if err := s.applyAutoTransitionForPR(ctx, repo.WorkspaceID, transition, issue, pr.AuthorLogin, pr); err != nil {
				return err
			}
			handled[issue.ID] = true
		}
	}
	if err := s.fRepo.ReplacePRIssueLinks(ctx, pr, issueIDs); err != nil {
		return fmt.Errorf("linking PR to issues: %w", err)
	}

	mergeRule, mergeRuleErr := s.fRepo.GetAutoTransitionByEvent(ctx, repo.WorkspaceID, "pr_merged")
	if mergeRuleErr != nil {
		return mergeRuleErr
	}
	ready := event.Action == "edited" && event.Changes.Title != nil && isDraftTitle(event.Changes.Title.From)
	for _, issue := range linkedIssues {
		switch {
		case active && !handled[issue.ID] && !currentGreen && (event.Action == "opened" || event.Action == "reopened" || ready):
			if err := s.applyAutoTransitionForPR(ctx, repo.WorkspaceID, "pr_opened", issue, event.PullRequest.User.Login, pr); err != nil {
				return err
			}
		case event.PullRequest.Merged:
			if mergeRule != nil {
				if err := s.applyAutoTransitionForPR(ctx, repo.WorkspaceID, "pr_merged", issue, event.PullRequest.User.Login, pr); err != nil {
					return err
				}
			} else if keywordSet[issue.ID] {
				// Closing keyword with no configured rule still closes.
				if err := s.moveIssueToCompleted(ctx, repo.WorkspaceID, issue, map[uuid.UUID]bool{}, pr); err != nil {
					return err
				}
			}
		}

		s.broadcastRealtimeEvent(repo.WorkspaceID, realtime.Event{
			Type:    "forgejo:pr_updated",
			Payload: map[string]any{"issue_id": issue.ID, "pr_number": pr.Number, "state": state},
		})
	}
	if len(linkedIssues) > 0 {
		s.broadcastAppRefresh(repo.WorkspaceID, "issues")
	}
	return nil
}

func isDraftTitle(title string) bool {
	title = strings.ToUpper(strings.TrimSpace(title))
	return strings.HasPrefix(title, "WIP:") || strings.HasPrefix(title, "[WIP]") || strings.HasPrefix(title, "DRAFT:") || strings.HasPrefix(title, "[DRAFT]")
}

type fjWebhookPush struct {
	Ref     string `json:"ref"`
	Commits []struct {
		ID        string `json:"id"`
		SHA       string `json:"sha"`
		Message   string `json:"message"`
		URL       string `json:"url"`
		Timestamp any    `json:"timestamp"`
		Author    struct {
			Name     string `json:"name"`
			Username string `json:"username"`
		} `json:"author"`
	} `json:"commits"`
}

func (s *ForgejoService) processPushEvent(ctx context.Context, payload []byte, repo *domain.ForgejoRepoModel) error {
	var event fjWebhookPush
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("unmarshaling push event: %w", err)
	}

	updatedIssues := make(map[uuid.UUID]bool)
	for _, c := range event.Commits {
		sha := c.ID
		if sha == "" {
			sha = c.SHA
		}
		if sha == "" {
			continue
		}
		commitURL := c.URL
		if commitURL == "" {
			commitURL = fmt.Sprintf("%s/%s/commit/%s", repo.InstanceURL, repo.FullName, sha)
		}

		issue, err := s.resolveIssueFromRef(ctx, repo.WorkspaceID, c.Message)
		if err != nil {
			return err
		}
		var issueID *uuid.UUID
		if issue != nil {
			id := issue.ID
			issueID = &id
		}

		username := c.Author.Username
		commit := &domain.ForgejoCommit{
			ID: uuid.New(), WorkspaceID: repo.WorkspaceID, IssueID: issueID,
			ForgejoRepoID: repo.ID, SHA: sha, Message: c.Message,
			AuthorLogin: &username, HTMLURL: commitURL, CommittedAt: time.Now(),
		}
		if err := s.fRepo.UpsertCommit(ctx, commit); err != nil {
			log.WithError(err).WithField("sha", sha).Warn("failed to upsert commit")
			continue
		}

		if issue != nil {
			updatedIssues[issue.ID] = true
			s.broadcastRealtimeEvent(repo.WorkspaceID, realtime.Event{
				Type:    "forgejo:commit_pushed",
				Payload: map[string]any{"issue_id": issue.ID, "sha": commit.SHA},
			})
		}
	}
	if len(updatedIssues) > 0 {
		s.broadcastAppRefresh(repo.WorkspaceID, "issues")
	}
	return nil
}

type fjWebhookCreate struct {
	RefType string `json:"ref_type"`
	Ref     string `json:"ref"`
	Sender  struct {
		Login string `json:"login"`
	} `json:"sender"`
}

func (s *ForgejoService) processCreateEvent(ctx context.Context, payload []byte, repo *domain.ForgejoRepoModel) error {
	var event fjWebhookCreate
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("unmarshaling create event: %w", err)
	}
	if event.RefType != "branch" {
		return nil
	}
	// Forgejo sends the full ref (refs/heads/<name>) in create events.
	event.Ref = strings.TrimPrefix(event.Ref, "refs/heads/")

	issues, err := s.resolveIssuesFromRefs(ctx, repo.WorkspaceID, event.Ref)
	if err != nil {
		return err
	}
	var issueID *uuid.UUID
	if len(issues) > 0 {
		issueID = &issues[0].ID
	}

	branchURL := fmt.Sprintf("%s/%s/src/branch/%s", strings.TrimRight(repo.InstanceURL, "/"), repo.FullName, url.PathEscape(event.Ref))
	branch := &domain.ForgejoBranch{
		ID: uuid.New(), WorkspaceID: repo.WorkspaceID, IssueID: issueID,
		ForgejoRepoID: repo.ID, Name: event.Ref, HTMLURL: &branchURL,
	}
	if err := s.fRepo.UpsertBranch(ctx, branch); err != nil {
		return fmt.Errorf("upserting branch: %w", err)
	}

	for _, issue := range issues {
		if err := s.applyAutoTransition(ctx, repo.WorkspaceID, "branch_created", issue, event.Sender.Login); err != nil {
			return err
		}
		s.broadcastRealtimeEvent(repo.WorkspaceID, realtime.Event{
			Type:    "forgejo:branch_created",
			Payload: map[string]any{"issue_id": issue.ID, "branch": event.Ref},
		})
		s.broadcastAppRefresh(repo.WorkspaceID, "issues")
	}
	return nil
}

type fjWebhookStatus struct {
	State  string `json:"state"`
	SHA    string `json:"sha"`
	Commit struct {
		ID  string `json:"id"`
		SHA string `json:"sha"`
	} `json:"commit"`
}

// processStatusEvent reacts to commit-status (CI) updates. When every check
// on an open PR's head commit is green, the pr_checks_green transition fires.
func (s *ForgejoService) processStatusEvent(ctx context.Context, payload []byte, repo *domain.ForgejoRepoModel) error {
	var event fjWebhookStatus
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("unmarshaling status event: %w", err)
	}
	sha := event.SHA
	if sha == "" {
		sha = event.Commit.ID
	}
	if sha == "" {
		sha = event.Commit.SHA
	}
	if sha == "" {
		return nil
	}
	return s.refreshChecksForSHA(ctx, repo, sha)
}

// An Actions PR run may execute a merge commit, while Forgejo posts its check
// status on the PR head. Extract that head from the original event payload.
func actionRunHeadSHA(payload []byte) (string, error) {
	var event struct {
		Run struct {
			CommitSHA    string `json:"commit_sha"`
			EventPayload string `json:"event_payload"`
			TriggerEvent string `json:"trigger_event"`
		} `json:"run"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return "", fmt.Errorf("unmarshaling action run event: %w", err)
	}
	if event.Run.TriggerEvent == "pull_request" || event.Run.TriggerEvent == "pull_request_target" {
		var trigger struct {
			PullRequest struct {
				Head struct {
					SHA string `json:"sha"`
				} `json:"head"`
			} `json:"pull_request"`
		}
		if err := json.Unmarshal([]byte(event.Run.EventPayload), &trigger); err != nil {
			return "", fmt.Errorf("unmarshaling pull request trigger: %w", err)
		}
		return trigger.PullRequest.Head.SHA, nil
	}
	return event.Run.CommitSHA, nil
}

// Always read the combined status from Forgejo; one successful run alone is
// insufficient to move a linked issue to review.
func (s *ForgejoService) processActionRunEvent(ctx context.Context, payload []byte, repo *domain.ForgejoRepoModel) error {
	sha, err := actionRunHeadSHA(payload)
	if err != nil {
		return err
	}
	if sha == "" {
		return nil
	}
	return s.refreshChecksForSHA(ctx, repo, sha)
}

func (s *ForgejoService) refreshChecksForSHA(ctx context.Context, repo *domain.ForgejoRepoModel, sha string) error {
	// Keep these versions from before the external fetch. Reloading afterward
	// would let an older response borrow a newer observation's version token.
	openPRs, err := s.fRepo.ListOpenPRsByHeadSHA(ctx, repo.ID, sha)
	if err != nil {
		return err
	}
	if len(openPRs) == 0 {
		return nil
	}

	conn, err := s.resolveConnection(ctx, repo.WorkspaceID)
	if err != nil {
		return err
	}
	owner, name, ok := splitRepoOwnerName(repo.FullName)
	if !ok {
		return nil
	}
	var combined *fj.CombinedStatus
	for attempt := 0; attempt < 3; attempt++ {
		combined, err = conn.client().GetCombinedStatus(ctx, owner, name, sha)
		if err == nil && (combined.AllChecksPassed() || combined.State == "failure" || combined.State == "error") {
			break
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	if err != nil {
		log.WithError(err).WithField("repo", repo.FullName).Warn("failed to fetch combined status")
		return err
	}
	allGreen := combined.AllChecksPassed()

	for i := range openPRs {
		pr := &openPRs[i]
		if err := s.relinkPullRequestReferences(ctx, repo, pr, allGreen); err != nil {
			if errors.Is(err, repository.ErrForgejoStalePullRequest) {
				continue
			}
			return err
		}
		checksChanged := pr.ChecksGreen != allGreen
		if checksChanged && allGreen && pr.State != "draft" {
			if err := s.applyChecksGreenTransition(ctx, repo.WorkspaceID, pr); err != nil {
				if errors.Is(err, repository.ErrForgejoStalePullRequest) {
					continue
				}
				return err
			}
		}
		// Advance the version even when the flag is unchanged: a fresh failure
		// must supersede an older in-flight success without replaying transitions.
		if err := s.fRepo.UpdatePullRequestChecks(ctx, pr, allGreen); err != nil {
			if errors.Is(err, repository.ErrForgejoStalePullRequest) {
				continue
			}
			return err
		}
		if !checksChanged {
			continue
		}
		ids, err := s.fRepo.ListIssueIDsForPR(ctx, pr.ID)
		if err != nil {
			return err
		}
		for _, issueID := range ids {
			s.broadcastRealtimeEvent(repo.WorkspaceID, realtime.Event{
				Type:    "forgejo:pr_updated",
				Payload: map[string]any{"issue_id": issueID, "pr_number": pr.Number, "checks_green": allGreen},
			})
		}
	}
	return nil
}

// Catch up references that became resolvable after an audited identifier
// restore. Legacy snapshots may lack the body, so this pass only adds links;
// full webhook payloads still replace links with all current references.
// New links are saved after their transition succeeds to keep retries usable.
func (s *ForgejoService) relinkPullRequestReferences(ctx context.Context, repo *domain.ForgejoRepoModel, pr *domain.ForgejoPullRequest, green bool) error {
	texts := []string{pr.Title, pr.Body}
	if pr.HeadBranch != nil {
		texts = append(texts, *pr.HeadBranch)
	}
	references, err := s.resolveIssuesFromRefs(ctx, repo.WorkspaceID, texts...)
	if err != nil {
		return err
	}
	if len(references) == 0 {
		return nil
	}
	existing, err := s.fRepo.ListIssueIDsForPR(ctx, pr.ID)
	if err != nil {
		return err
	}
	ids := append([]uuid.UUID{}, existing...)
	for _, issue := range references {
		found := false
		for _, id := range existing {
			if id == issue.ID {
				found = true
				break
			}
		}
		if found {
			continue
		}
		event := "pr_opened"
		if green && pr.State != "draft" {
			event = "pr_checks_green"
		}
		if err := s.applyAutoTransitionForPR(ctx, repo.WorkspaceID, event, issue, pr.AuthorLogin, pr); err != nil {
			return err
		}
		ids = append(ids, issue.ID)
	}
	if len(ids) != len(existing) {
		return s.fRepo.ReplacePRIssueLinks(ctx, pr, ids)
	}
	return nil
}

// ReconcileWebhookEvents replaces old hooks because Forgejo 16 ignores Actions
// event names when editing a hook. The signed secret and URL are preserved.
func (s *ForgejoService) ReconcileWebhookEvents(ctx context.Context) {
	repos, err := s.fRepo.ListActiveReposWithHooks(ctx)
	if err != nil {
		log.WithError(err).Warn("failed to list Forgejo hooks")
		return
	}
	for i := range repos {
		repo := repos[i]
		conn, err := s.resolveConnection(ctx, repo.WorkspaceID)
		if err != nil {
			log.WithError(err).WithField("repo", repo.FullName).Warn("failed to resolve Forgejo connection")
			continue
		}
		owner, name, ok := splitRepoOwnerName(repo.FullName)
		if !ok || repo.WebhookID == nil {
			continue
		}
		client := conn.client()
		hook, err := client.GetWebhook(ctx, owner, name, *repo.WebhookID)
		if err != nil {
			log.WithError(err).WithField("repo", repo.FullName).Warn("failed to inspect Forgejo hook")
			continue
		}
		found := false
		for _, event := range hook.Events {
			if event == "action_run_success" {
				found = true
			}
		}
		if found {
			continue
		}
		secret, err := crypto.Decrypt(repo.WebhookSecret, s.encryptionKey)
		if err != nil {
			log.WithError(err).WithField("repo", repo.FullName).Warn("failed to decrypt Forgejo hook secret")
			continue
		}
		newHook, err := client.CreateWebhook(ctx, owner, name, hook.URL, secret, nil)
		if err != nil {
			log.WithError(err).WithField("repo", repo.FullName).Warn("failed to create Forgejo Actions hook")
			continue
		}
		if err := s.fRepo.UpdateWebhookID(ctx, repo.ID, newHook.ID); err != nil {
			log.WithError(err).WithField("repo", repo.FullName).Warn("failed to save Forgejo Actions hook")
			_ = client.DeleteWebhook(ctx, owner, name, newHook.ID)
			continue
		}
		if err := client.DeleteWebhook(ctx, owner, name, *repo.WebhookID); err != nil {
			log.WithError(err).WithField("repo", repo.FullName).Warn("failed to remove old Forgejo hook")
		}
	}
	// One startup pass covers checks that completed before the new subscriptions.
	s.syncOpenPRChecks(ctx)
}

// StartStatusReconciler catches missed Actions webhook deliveries. Webhooks
// remain the immediate path; each tick reads current PR heads from Forgejo.
func (s *ForgejoService) StartStatusReconciler(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.syncOpenPRChecks(ctx)
		}
	}
}

func (s *ForgejoService) syncOpenPRChecks(ctx context.Context) {
	repos, err := s.fRepo.ListReposWithOpenPRs(ctx)
	if err != nil {
		log.WithError(err).Warn("failed to list open Forgejo pull requests")
		return
	}
	for i := range repos {
		prs, err := s.fRepo.ListOpenPRsByRepo(ctx, repos[i].ID)
		if err != nil {
			continue
		}
		seen := make(map[string]bool)
		for _, pr := range prs {
			if pr.HeadSHA == nil || *pr.HeadSHA == "" || seen[*pr.HeadSHA] {
				continue
			}
			seen[*pr.HeadSHA] = true
			if err := s.refreshChecksForSHA(ctx, &repos[i], *pr.HeadSHA); err != nil {
				log.WithError(err).WithFields(log.Fields{"repo": repos[i].FullName, "pr": pr.Number}).Warn("failed to reconcile Forgejo checks")
			}
		}
	}
}

func (s *ForgejoService) applyChecksGreenTransition(ctx context.Context, workspaceID uuid.UUID, pr *domain.ForgejoPullRequest) error {
	issues, err := s.issuesForPR(ctx, pr.ID)
	if err != nil {
		return err
	}
	for _, issue := range issues {
		if err := s.applyAutoTransitionForPR(ctx, workspaceID, "pr_checks_green", issue, pr.AuthorLogin, pr); err != nil {
			return err
		}
	}
	if len(issues) > 0 {
		s.broadcastAppRefresh(workspaceID, "issues")
	}
	return nil
}

// issuesForPR lists the issues linked to a PR row.
func (s *ForgejoService) issuesForPR(ctx context.Context, prID uuid.UUID) ([]*domain.Issue, error) {
	// Linked issue ids live only in the link table; go through the activity
	// mapping used by the repository layer.
	ids, err := s.fRepo.ListIssueIDsForPR(ctx, prID)
	if err != nil {
		return nil, err
	}
	issues := make([]*domain.Issue, 0, len(ids))
	for _, id := range ids {
		issue, err := s.issueRepo.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if issue != nil {
			issues = append(issues, issue)
		}
	}
	return issues, nil
}

// --- Issue linking ---

func (s *ForgejoService) resolveIssueFromRef(ctx context.Context, workspaceID uuid.UUID, texts ...string) (*domain.Issue, error) {
	issues, err := s.resolveIssuesFromRefs(ctx, workspaceID, texts...)
	if err != nil {
		return nil, err
	}
	if len(issues) == 0 {
		return nil, nil
	}
	return issues[0], nil
}

func (s *ForgejoService) resolveIssuesFromRefs(ctx context.Context, workspaceID uuid.UUID, texts ...string) ([]*domain.Issue, error) {
	identifiers := make([]string, 0)
	for _, text := range texts {
		identifiers = append(identifiers, issueIdentifierRegex.FindAllString(text, -1)...)
	}
	return s.resolveIssueIdentifiers(ctx, workspaceID, identifiers)
}

func (s *ForgejoService) resolveKeywordIssues(ctx context.Context, workspaceID uuid.UUID, texts ...string) ([]*domain.Issue, error) {
	identifiers := make([]string, 0)
	for _, text := range texts {
		for _, match := range issueKeywordRegex.FindAllStringSubmatch(text, -1) {
			identifiers = append(identifiers, match[1])
		}
	}
	return s.resolveIssueIdentifiers(ctx, workspaceID, identifiers)
}

func (s *ForgejoService) resolveIssueIdentifiers(ctx context.Context, workspaceID uuid.UUID, identifiers []string) ([]*domain.Issue, error) {
	seen := make(map[string]bool)
	issueIDs := make(map[uuid.UUID]bool)
	var issues []*domain.Issue
	for _, identifier := range identifiers {
		identifier = strings.ToUpper(identifier)
		if seen[identifier] {
			continue
		}
		seen[identifier] = true
		issue, err := s.issueRepo.GetByIdentifier(ctx, workspaceID, identifier)
		if err != nil {
			return nil, err
		}
		if issue != nil && !issueIDs[issue.ID] {
			issueIDs[issue.ID] = true
			issues = append(issues, issue)
		}
	}
	return issues, nil
}

// --- Auto-Transitions ---

func (s *ForgejoService) applyAutoTransition(ctx context.Context, workspaceID uuid.UUID, event string, issue *domain.Issue, forgejoLogin string) error {
	return s.applyAutoTransitionForPR(ctx, workspaceID, event, issue, forgejoLogin, nil)
}

func (s *ForgejoService) applyAutoTransitionForPR(ctx context.Context, workspaceID uuid.UUID, event string, issue *domain.Issue, forgejoLogin string, expectedPR *domain.ForgejoPullRequest) error {
	for range 3 {
		err := s.applyAutoTransitionOnce(ctx, workspaceID, event, issue, forgejoLogin, expectedPR)
		if !errors.Is(err, repository.ErrForgejoAutomationConflict) {
			return err
		}
		fresh, loadErr := s.issueRepo.GetByID(ctx, issue.ID)
		if loadErr != nil {
			return loadErr
		}
		if fresh == nil {
			return nil
		}
		*issue = *fresh
	}
	return repository.ErrForgejoAutomationConflict
}

func (s *ForgejoService) applyAutoTransitionOnce(ctx context.Context, workspaceID uuid.UUID, event string, issue *domain.Issue, forgejoLogin string, expectedPR *domain.ForgejoPullRequest) error {
	if issue == nil || issue.WorkspaceID != workspaceID {
		return fmt.Errorf("issue does not belong to the automation workspace")
	}
	rule, err := s.fRepo.GetAutoTransitionByEvent(ctx, workspaceID, event)
	if err != nil {
		return err
	}
	if rule == nil || !rule.IsActive {
		return nil
	}
	category, err := s.issueStatusCategory(ctx, issue)
	if err != nil {
		return err
	}
	if category == domain.StatusCategoryCompleted {
		return s.applyStatusAutomation(ctx, workspaceID, issue, map[uuid.UUID]bool{}, expectedPR)
	}
	if category == domain.StatusCategoryCancelled {
		return nil
	}
	targetStatus, err := s.automationTargetStatus(ctx, workspaceID, issue.TeamID, rule)
	if err != nil {
		return err
	}
	if targetStatus != nil {
		past, err := s.issuePastStatus(ctx, issue, targetStatus)
		if err != nil {
			return err
		}
		if past {
			targetStatus = nil
		}
	}
	oldStatus := string(issue.Status)
	newStatus := oldStatus
	statusChanged := false
	if targetStatus != nil {
		newStatus = targetStatus.Slug
		statusChanged = oldStatus != newStatus || issue.StatusID == nil || *issue.StatusID != targetStatus.ID
	}
	targetAssigneeID := rule.TargetAssigneeID
	if rule.AssignForgejoUser && strings.TrimSpace(forgejoLogin) != "" {
		matched, err := s.fRepo.MatchWorkspaceMemberByName(ctx, workspaceID, strings.TrimSpace(forgejoLogin))
		if err != nil {
			return err
		}
		targetAssigneeID = matched
	}
	assigneeToAdd := false
	if targetAssigneeID != nil {
		assignees, err := s.issueRepo.GetAssignees(ctx, issue.ID)
		if err != nil {
			return err
		}
		alreadyAssigned := issue.AssigneeID != nil && *issue.AssigneeID == *targetAssigneeID
		for _, a := range assignees {
			if a == *targetAssigneeID {
				alreadyAssigned = true
				break
			}
		}
		assigneeToAdd = !alreadyAssigned
		if assigneeToAdd {
			member, err := s.fRepo.IsWorkspaceMember(ctx, workspaceID, *targetAssigneeID)
			if err != nil {
				return err
			}
			if !member {
				assigneeToAdd = false
			}
		}
	}
	if !statusChanged && !assigneeToAdd {
		return nil
	}
	candidate := *issue
	if statusChanged {
		candidate.Status = domain.IssueStatus(newStatus)
		candidate.StatusID = &targetStatus.ID
	}
	var addTarget *uuid.UUID
	if assigneeToAdd {
		addTarget = targetAssigneeID
	}
	if err := s.fRepo.ApplyIssueAutomation(ctx, &candidate, addTarget, expectedPR); err != nil {
		return err
	}
	*issue = candidate
	s.broadcastRealtimeEvent(workspaceID, realtime.Event{Type: "issue.updated", Payload: issue})
	if statusChanged {
		return s.applyStatusAutomation(ctx, workspaceID, issue, map[uuid.UUID]bool{}, expectedPR)
	}
	return nil
}

// Rule IDs identify a selected workflow status, while each issue must use the
// equivalent slug from its own team's workflow. An ID never crosses teams.
func (s *ForgejoService) automationTargetStatus(ctx context.Context, workspaceID, teamID uuid.UUID, rule *domain.ForgejoAutoTransition) (*domain.TeamStatus, error) {
	slug := strings.TrimSpace(rule.TargetStatus)
	if rule.TargetStatusID != nil {
		selected, err := s.teamStatusRepo.GetByID(ctx, *rule.TargetStatusID)
		if err != nil {
			return nil, err
		}
		if selected == nil {
			return nil, fmt.Errorf("automation target status no longer exists")
		}
		if selected.TeamID != teamID {
			team, err := s.teamRepo.GetByID(ctx, selected.TeamID)
			if err != nil {
				return nil, err
			}
			if team == nil || team.WorkspaceID != workspaceID {
				return nil, fmt.Errorf("automation target status is outside this workspace")
			}
		}
		slug = selected.Slug
	}
	if slug == "" {
		return nil, nil
	}
	status, err := s.teamStatusRepo.GetByTeamAndSlug(ctx, teamID, slug)
	if err != nil {
		return nil, err
	}
	if status == nil && slug == "pr" {
		return s.ensureReviewStatus(ctx, teamID)
	}
	if status == nil {
		return nil, fmt.Errorf("automation target status %q is missing from the issue's team", slug)
	}
	return status, nil
}

// Automation only advances an issue through its team's workflow: a second PR
// or branch must not pull an issue in review back to In Progress.
func (s *ForgejoService) issuePastStatus(ctx context.Context, issue *domain.Issue, target *domain.TeamStatus) (bool, error) {
	if issue.StatusID == nil || *issue.StatusID == target.ID {
		return false, nil
	}
	current, err := s.teamStatusRepo.GetByID(ctx, *issue.StatusID)
	if err != nil || current == nil {
		return false, err
	}
	if current.TeamID != target.TeamID || current.Slug != string(issue.Status) {
		return false, nil
	}
	// Categories order the workflow; board position only orders statuses within
	// one category, since the default PR status is appended after Done.
	if currentRank, targetRank := statusCategoryRank(current.Category), statusCategoryRank(target.Category); currentRank != targetRank {
		return currentRank > targetRank, nil
	}
	return current.Category == domain.StatusCategoryStarted && current.Position > target.Position, nil
}

func statusCategoryRank(category domain.StatusCategory) int {
	switch category {
	case domain.StatusCategoryBacklog:
		return 0
	case domain.StatusCategoryUnstarted:
		return 1
	case domain.StatusCategoryStarted:
		return 2
	default:
		return 3
	}
}

func (s *ForgejoService) applyStatusAutomation(ctx context.Context, workspaceID uuid.UUID, issue *domain.Issue, visited map[uuid.UUID]bool, expectedPR *domain.ForgejoPullRequest) error {
	if issue == nil || visited[issue.ID] || s.teamRepo == nil {
		return nil
	}
	visited[issue.ID] = true

	category, err := s.issueStatusCategory(ctx, issue)
	if err != nil {
		return err
	}
	if category == domain.StatusCategoryCompleted {
		team, err := s.teamRepo.GetByID(ctx, issue.TeamID)
		if err != nil {
			return err
		}
		if team != nil && team.SubIssueAutoCloseEnabled {
			if err := s.autoCloseSubIssues(ctx, workspaceID, issue.ID, visited, expectedPR); err != nil {
				return err
			}
		}
	}
	if issue.ParentID != nil {
		return s.maybeAutoCloseParent(ctx, workspaceID, *issue.ParentID, visited, expectedPR)
	}
	return nil
}

func (s *ForgejoService) maybeAutoCloseParent(ctx context.Context, workspaceID, parentID uuid.UUID, visited map[uuid.UUID]bool, expectedPR *domain.ForgejoPullRequest) error {
	parent, err := s.issueRepo.GetByID(ctx, parentID)
	if err != nil {
		return err
	}
	if parent == nil || parent.WorkspaceID != workspaceID || visited[parent.ID] || s.teamRepo == nil {
		return nil
	}
	team, err := s.teamRepo.GetByID(ctx, parent.TeamID)
	if err != nil {
		return err
	}
	if team == nil || !team.ParentAutoCloseEnabled {
		return nil
	}
	total, done, err := s.issueRepo.CountSubIssues(ctx, parent.ID)
	if err != nil {
		return err
	}
	if total == 0 || total != done {
		return nil
	}
	return s.moveIssueToCompleted(ctx, workspaceID, parent, visited, expectedPR)
}

func (s *ForgejoService) autoCloseSubIssues(ctx context.Context, workspaceID, parentID uuid.UUID, visited map[uuid.UUID]bool, expectedPR *domain.ForgejoPullRequest) error {
	subIssues, err := s.issueRepo.ListSubIssues(ctx, parentID)
	if err != nil {
		return err
	}
	for i := range subIssues {
		sub := subIssues[i]
		if sub.WorkspaceID != workspaceID {
			continue
		}
		if err := s.moveIssueToCompleted(ctx, workspaceID, &sub, visited, expectedPR); err != nil {
			return err
		}
	}
	return nil
}

func (s *ForgejoService) moveIssueToCompleted(ctx context.Context, workspaceID uuid.UUID, issue *domain.Issue, visited map[uuid.UUID]bool, expectedPR *domain.ForgejoPullRequest) error {
	if issue == nil || visited[issue.ID] {
		return nil
	}
	for range 3 {
		category, err := s.issueStatusCategory(ctx, issue)
		if err != nil {
			return err
		}
		if category == domain.StatusCategoryCancelled {
			return nil
		}
		if category == domain.StatusCategoryCompleted {
			return s.applyStatusAutomation(ctx, workspaceID, issue, visited, expectedPR)
		}
		completedStatus, err := s.completedStatusForTeam(ctx, issue.TeamID)
		if err != nil {
			return err
		}
		if completedStatus == nil {
			return fmt.Errorf("completed status is missing from the issue's team")
		}
		candidate := *issue
		candidate.Status = domain.IssueStatus(completedStatus.Slug)
		candidate.StatusID = &completedStatus.ID
		err = s.fRepo.ApplyIssueAutomation(ctx, &candidate, nil, expectedPR)
		if errors.Is(err, repository.ErrForgejoAutomationConflict) {
			fresh, err := s.issueRepo.GetByID(ctx, issue.ID)
			if err != nil {
				return err
			}
			if fresh == nil {
				return nil
			}
			*issue = *fresh
			continue
		}
		if err != nil {
			return err
		}
		*issue = candidate
		s.broadcastRealtimeEvent(workspaceID, realtime.Event{Type: "issue.updated", Payload: issue})
		return s.applyStatusAutomation(ctx, workspaceID, issue, visited, expectedPR)
	}
	return repository.ErrForgejoAutomationConflict
}

func (s *ForgejoService) completedStatusForTeam(ctx context.Context, teamID uuid.UUID) (*domain.TeamStatus, error) {
	status, err := s.teamStatusRepo.GetByTeamAndSlug(ctx, teamID, string(domain.IssueStatusDone))
	if err != nil {
		return nil, err
	}
	if status != nil {
		return status, nil
	}
	statuses, err := s.teamStatusRepo.ListByTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
	for i := range statuses {
		if statuses[i].Category == domain.StatusCategoryCompleted {
			return &statuses[i], nil
		}
	}
	return nil, nil
}

func (s *ForgejoService) issueStatusCategory(ctx context.Context, issue *domain.Issue) (domain.StatusCategory, error) {
	if issue.StatusID != nil {
		status, err := s.teamStatusRepo.GetByID(ctx, *issue.StatusID)
		if err != nil {
			return "", err
		}
		if status != nil && status.TeamID == issue.TeamID && status.Slug == string(issue.Status) {
			return status.Category, nil
		}
	}
	switch issue.Status {
	case domain.IssueStatusDone:
		return domain.StatusCategoryCompleted, nil
	case domain.IssueStatusCancelled:
		return domain.StatusCategoryCancelled, nil
	case domain.IssueStatusInProgress, domain.IssueStatusInReview:
		return domain.StatusCategoryStarted, nil
	case domain.IssueStatusTodo:
		return domain.StatusCategoryUnstarted, nil
	default:
		return domain.StatusCategoryBacklog, nil
	}
}

// --- Data Access ---

func (s *ForgejoService) GetIssueActivity(ctx context.Context, workspaceID uuid.UUID, identifier string) (*dto.ForgejoIssueActivityResponse, error) {
	issue, err := s.issueRepo.GetByIdentifier(ctx, workspaceID, identifier)
	if err != nil || issue == nil {
		return nil, fmt.Errorf("issue not found")
	}

	prs, _ := s.fRepo.ListPRsWithRepoByIssue(ctx, issue.ID)
	branches, _ := s.fRepo.ListBranchesWithRepoByIssue(ctx, issue.ID)
	commits, _ := s.fRepo.ListCommitsWithRepoByIssue(ctx, issue.ID)

	resp := &dto.ForgejoIssueActivityResponse{
		PullRequests: make([]dto.ForgejoPullRequestResponse, 0, len(prs)),
		Branches:     make([]dto.ForgejoBranchResponse, 0, len(branches)),
		Commits:      make([]dto.ForgejoCommitResponse, 0, len(commits)),
	}

	for _, pr := range prs {
		avatarURL, headBranch, headSHA, baseBranch := "", "", "", ""
		if pr.AuthorAvatarURL != nil {
			avatarURL = *pr.AuthorAvatarURL
		}
		if pr.HeadBranch != nil {
			headBranch = *pr.HeadBranch
		}
		if pr.HeadSHA != nil {
			headSHA = *pr.HeadSHA
		}
		if pr.BaseBranch != nil {
			baseBranch = *pr.BaseBranch
		}
		resp.PullRequests = append(resp.PullRequests, dto.ForgejoPullRequestResponse{
			ID: pr.ID.String(), Number: pr.Number, Title: pr.Title, State: pr.State,
			ChecksGreen: pr.ChecksGreen, AuthorLogin: pr.AuthorLogin, AuthorAvatarURL: avatarURL,
			HTMLURL: pr.HTMLURL, HeadBranch: headBranch, HeadSHA: headSHA, BaseBranch: baseBranch,
			Additions: pr.Additions, Deletions: pr.Deletions,
			RepoID: pr.ForgejoRepoID.String(), RepoFullName: pr.RepoFullName, MergedAt: pr.MergedAt,
			CreatedAt: pr.CreatedAt, UpdatedAt: pr.UpdatedAt,
		})
	}

	for _, b := range branches {
		htmlURL := ""
		if b.HTMLURL != nil {
			htmlURL = *b.HTMLURL
		}
		resp.Branches = append(resp.Branches, dto.ForgejoBranchResponse{
			ID: b.ID.String(), Name: b.Name, HTMLURL: htmlURL,
			RepoID: b.ForgejoRepoID.String(), RepoFullName: b.RepoFullName, CreatedAt: b.CreatedAt,
		})
	}

	for _, c := range commits {
		authorLogin := ""
		if c.AuthorLogin != nil {
			authorLogin = *c.AuthorLogin
		}
		shortSHA := c.SHA
		if len(shortSHA) > 7 {
			shortSHA = shortSHA[:7]
		}
		resp.Commits = append(resp.Commits, dto.ForgejoCommitResponse{
			ID: c.ID.String(), SHA: c.SHA, ShortSHA: shortSHA, Message: c.Message,
			AuthorLogin: authorLogin, HTMLURL: c.HTMLURL,
			RepoFullName: c.RepoFullName, CommittedAt: c.CommittedAt,
		})
	}

	return resp, nil
}

// --- Auto Transition Management ---

func (s *ForgejoService) ListUserMappings(ctx context.Context, workspaceID uuid.UUID) ([]dto.ForgejoUserMapping, error) {
	mappings, err := s.fRepo.ListUserMappings(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	result := make([]dto.ForgejoUserMapping, 0, len(mappings))
	for _, mapping := range mappings {
		result = append(result, dto.ForgejoUserMapping{ForgejoLogin: mapping.ForgejoLogin, UserID: mapping.UserID.String()})
	}
	return result, nil
}

func (s *ForgejoService) UpdateUserMappings(ctx context.Context, workspaceID uuid.UUID, req dto.UpdateForgejoUserMappingsRequest) error {
	if req.Mappings == nil || len(req.Mappings) > 100 {
		return fmt.Errorf("mappings must be a list of at most 100 entries")
	}
	mappings := make([]domain.ForgejoUserMapping, 0, len(req.Mappings))
	seen := make(map[string]bool, len(req.Mappings))
	for _, entry := range req.Mappings {
		login := strings.ToLower(strings.TrimSpace(entry.ForgejoLogin))
		if !forgejoLoginRegex.MatchString(login) || seen[login] {
			return fmt.Errorf("invalid or duplicate Forgejo login %q", entry.ForgejoLogin)
		}
		seen[login] = true
		userID, err := uuid.Parse(entry.UserID)
		if err != nil {
			return fmt.Errorf("invalid Ku user ID for %q", login)
		}
		member, err := s.fRepo.IsWorkspaceMember(ctx, workspaceID, userID)
		if err != nil {
			return fmt.Errorf("checking Ku member for %q: %w", login, err)
		}
		if !member {
			return fmt.Errorf("Ku user for %q is not a workspace member", login)
		}
		mappings = append(mappings, domain.ForgejoUserMapping{WorkspaceID: workspaceID, ForgejoLogin: login, UserID: userID})
	}
	return s.fRepo.ReplaceUserMappings(ctx, workspaceID, mappings)
}

func (s *ForgejoService) ListAutoTransitions(ctx context.Context, workspaceID uuid.UUID) ([]dto.ForgejoAutoTransitionResponse, error) {
	// Rules are workspace-level; make sure all four lifecycle events exist so
	// the settings page can configure each one.
	existing, err := s.fRepo.ListAutoTransitions(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	present := make(map[string]bool)
	for _, t := range existing {
		present[t.Event] = true
	}
	for _, event := range []string{"branch_created", "pr_opened", "pr_checks_green", "pr_merged"} {
		if !present[event] {
			_ = s.fRepo.UpsertAutoTransition(ctx, &domain.ForgejoAutoTransition{
				ID: uuid.New(), WorkspaceID: workspaceID, Event: event, IsActive: false,
			})
		}
	}
	transitions, err := s.fRepo.ListAutoTransitions(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	result := make([]dto.ForgejoAutoTransitionResponse, 0, len(transitions))
	for _, t := range transitions {
		var statusID *string
		if t.TargetStatusID != nil {
			id := t.TargetStatusID.String()
			statusID = &id
		}
		var assigneeID *string
		if t.TargetAssigneeID != nil {
			id := t.TargetAssigneeID.String()
			assigneeID = &id
		}
		result = append(result, dto.ForgejoAutoTransitionResponse{
			Event: t.Event, TargetStatus: t.TargetStatus, TargetStatusID: statusID,
			TargetAssigneeID: assigneeID, AssignForgejoUser: t.AssignForgejoUser, IsActive: t.IsActive,
		})
	}
	return result, nil
}

func (s *ForgejoService) UpdateAutoTransitions(ctx context.Context, workspaceID uuid.UUID, req dto.UpdateAutoTransitionsRequest) error {
	if len(req.Transitions) == 0 || len(req.Transitions) > len(forgejoLifecycleEvents) {
		return fmt.Errorf("provide between one and four transition rules")
	}
	seen := make(map[string]bool)
	validated := make([]*domain.ForgejoAutoTransition, 0, len(req.Transitions))
	for _, rule := range req.Transitions {
		if !forgejoLifecycleEvents[rule.Event] || seen[rule.Event] {
			return fmt.Errorf("unknown transition event %q", rule.Event)
		}
		seen[rule.Event] = true
		if rule.AssignForgejoUser && rule.TargetAssigneeID != nil {
			return fmt.Errorf("event %q cannot select both a fixed assignee and a Forgejo user", rule.Event)
		}
		if rule.IsActive && strings.TrimSpace(rule.TargetStatus) == "" && rule.TargetStatusID == nil && rule.TargetAssigneeID == nil && !rule.AssignForgejoUser {
			return fmt.Errorf("event %q needs a target status or assignee", rule.Event)
		}
		var statusID *uuid.UUID
		if rule.TargetStatusID != nil {
			parsed, err := uuid.Parse(*rule.TargetStatusID)
			if err != nil {
				return fmt.Errorf("invalid target_status_id: %w", err)
			}
			statusID = &parsed
			selected, err := s.teamStatusRepo.GetByID(ctx, parsed)
			if err != nil {
				return err
			}
			if selected == nil {
				return fmt.Errorf("target status does not exist")
			}
			team, err := s.teamRepo.GetByID(ctx, selected.TeamID)
			if err != nil {
				return err
			}
			if team == nil || team.WorkspaceID != workspaceID {
				return fmt.Errorf("target status is not in this workspace")
			}
			if strings.TrimSpace(rule.TargetStatus) != "" && rule.TargetStatus != selected.Slug {
				return fmt.Errorf("target status and ID identify different workflow states")
			}
			rule.TargetStatus = selected.Slug
		} else if slug := strings.TrimSpace(rule.TargetStatus); rule.IsActive && slug != "" && slug != "pr" {
			teams, err := s.teamRepo.ListByWorkspace(ctx, workspaceID)
			if err != nil {
				return err
			}
			found := false
			for _, team := range teams {
				status, err := s.teamStatusRepo.GetByTeamAndSlug(ctx, team.ID, slug)
				if err != nil {
					return err
				}
				found = found || status != nil
			}
			if !found {
				return fmt.Errorf("target status %q is not in this workspace", slug)
			}
		}
		var assigneeID *uuid.UUID
		if rule.TargetAssigneeID != nil {
			parsed, err := uuid.Parse(*rule.TargetAssigneeID)
			if err != nil {
				return fmt.Errorf("invalid target_assignee_id: %w", err)
			}
			member, err := s.fRepo.IsWorkspaceMember(ctx, workspaceID, parsed)
			if err != nil {
				return fmt.Errorf("checking target assignee: %w", err)
			}
			if !member {
				return fmt.Errorf("target assignee is not a workspace member")
			}
			assigneeID = &parsed
		}
		t := &domain.ForgejoAutoTransition{
			ID: uuid.New(), WorkspaceID: workspaceID,
			Event: rule.Event, TargetStatus: strings.TrimSpace(rule.TargetStatus),
			TargetStatusID: statusID, TargetAssigneeID: assigneeID,
			AssignForgejoUser: rule.AssignForgejoUser, IsActive: rule.IsActive,
		}
		validated = append(validated, t)
	}
	for _, rule := range validated {
		if err := s.fRepo.UpsertAutoTransition(ctx, rule); err != nil {
			return err
		}
	}
	return nil
}
