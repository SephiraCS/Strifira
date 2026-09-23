package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
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

var issueIdentifierRegex = regexp.MustCompile(`(?i)([A-Z][A-Z0-9]+-\d+)`)

// issueKeywordRegex matches closing keywords followed by an issue identifier,
// e.g. "Fixes KUA-12" or "resolves kua-3 and closes TS-4".
var issueKeywordRegex = regexp.MustCompile(`(?i)\b(?:fix(?:es|ed)?|clos(?:es|ed)?|resolv(?:es|ed)?)\s+([A-Z][A-Z0-9]+-\d+)`)

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
	fRepo          *repository.ForgejoRepository
	issueRepo      repository.IssueRepo
	teamRepo       repository.TeamRepo
	teamStatusRepo repository.TeamStatusRepo
	historyRepo    repository.IssueHistoryRepo
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
	historyRepo repository.IssueHistoryRepo,
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
		historyRepo:    historyRepo,
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
		return nil, fmt.Errorf("Forgejo is not connected")
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
	instanceURL = strings.TrimRight(strings.TrimSpace(instanceURL), "/")
	if instanceURL == "" || token == "" {
		return nil, fmt.Errorf("instance URL and token are required")
	}
	parsed, err := url.Parse(instanceURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return nil, fmt.Errorf("invalid instance URL")
	}

	user, err := fj.NewClient(instanceURL, token).GetCurrentUser()
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

// Disconnect removes the connection and unlinks every repo (webhooks on the
// Forgejo side are removed best-effort).
func (s *ForgejoService) Disconnect(ctx context.Context, workspaceID uuid.UUID) error {
	repos, _ := s.fRepo.ListReposByWorkspace(ctx, workspaceID)
	for i := range repos {
		_ = s.UnlinkRepo(ctx, workspaceID, repos[i].ID)
	}
	return s.fRepo.DeleteConnection(ctx, workspaceID)
}

// seedDefaultsIfMissing seeds transition rules once (never overwrites user customizations).
func (s *ForgejoService) seedDefaultsIfMissing(ctx context.Context, workspaceID uuid.UUID) {
	existing, err := s.fRepo.ListAutoTransitions(ctx, workspaceID)
	if err != nil || len(existing) > 0 {
		return
	}
	s.seedDefaults(ctx, workspaceID)
}

// seedDefaults creates the auto-transition rules (idempotent).
func (s *ForgejoService) seedDefaults(ctx context.Context, workspaceID uuid.UUID) {
	defaults := []struct {
		Event  string
		Status string
	}{
		{"branch_created", "in_progress"},
		{"pr_opened", "in_progress"},
		{"pr_checks_green", "in_review"},
		{"pr_merged", "done"},
	}
	for _, d := range defaults {
		_ = s.fRepo.UpsertAutoTransition(ctx, &domain.ForgejoAutoTransition{
			ID: uuid.New(), WorkspaceID: workspaceID,
			Event: d.Event, TargetStatus: d.Status, IsActive: true,
		})
	}

}

// webhookTargetURL derives the URL webhook deliveries should be sent to.
func (s *ForgejoService) webhookTargetURL() string {
	if s.webhookURL != "" {
		return strings.TrimRight(s.webhookURL, "/")
	}
	base := strings.Replace(s.frontendURL, ":5173", ":8080", 1)
	return strings.TrimRight(base, "/") + "/api/forgejo/webhook"
}

// GetStatus returns the integration status for a workspace.
func (s *ForgejoService) GetStatus(ctx context.Context, workspaceID uuid.UUID) (*dto.ForgejoStatusResponse, error) {
	resp := &dto.ForgejoStatusResponse{Repos: []dto.ForgejoRepoResponse{}}

	conn, err := s.resolveConnection(ctx, workspaceID)
	if err == nil && conn != nil {
		resp.Configured = true
		resp.Connected = true
		resp.GlobalConfig = conn.Global
		resp.InstanceURL = conn.InstanceURL

		if account, err := conn.client().GetCurrentUser(); err == nil {
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
	remote, err := conn.client().ListRepos()
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
	conn, err := s.resolveConnection(ctx, workspaceID)
	if err != nil {
		return err
	}
	client := conn.client()

	remote, err := client.ListRepos()
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
			continue
		}
		existing, _ := s.fRepo.GetRepoByForgejoID(ctx, workspaceID, id)
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
		hook, err := client.CreateWebhook(owner, name, target, secret, nil)
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
			owner, name, ok := splitRepoOwnerName(repo.FullName)
			if ok {
				_ = conn.client().DeleteWebhook(owner, name, *repo.WebhookID)
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
		return s.processPullRequestEvent(ctx, payload, repo)
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
	Action      string `json:"action"`
	Number      int    `json:"number"`
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
	case payload.PullRequest.Draft:
		return "draft"
	case payload.PullRequest.State == "open":
		return "open"
	default:
		return "closed"
	}
}

func (s *ForgejoService) processPullRequestEvent(ctx context.Context, payload []byte, repo *domain.ForgejoRepoModel) error {
	var event fjWebhookPR
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("unmarshaling PR event: %w", err)
	}

	state := prStateFromEvent(event)
	referenced := s.resolveIssuesFromRefs(ctx, repo.WorkspaceID,
		event.PullRequest.Head.Ref, event.PullRequest.Title, event.PullRequest.Body)
	keywordIssues := s.resolveKeywordIssues(ctx, repo.WorkspaceID, event.PullRequest.Title, event.PullRequest.Body)

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
		Number: event.PullRequest.Number, Title: event.PullRequest.Title, State: state,
		AuthorLogin: event.PullRequest.User.Login, AuthorAvatarURL: &avatarURL,
		HTMLURL: event.PullRequest.HTMLURL, HeadBranch: &headRef, HeadSHA: &headSHA,
		BaseBranch: &baseRef, Additions: event.PullRequest.Additions,
		Deletions: event.PullRequest.Deletions,
		MergedAt:  event.PullRequest.MergedAt, ClosedAt: event.PullRequest.ClosedAt,
	}
	if err := s.fRepo.UpsertPullRequest(ctx, pr); err != nil {
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
	if err := s.fRepo.ReplacePRIssueLinks(ctx, pr.ID, issueIDs); err != nil {
		return fmt.Errorf("linking PR to issues: %w", err)
	}

	mergeRule, mergeRuleErr := s.fRepo.GetAutoTransitionByEvent(ctx, repo.WorkspaceID, "pr_merged")
	for _, issue := range linkedIssues {
		switch {
		case event.Action == "opened" || event.Action == "ready_for_review":
			s.applyAutoTransition(ctx, repo.WorkspaceID, "pr_opened", issue)
		case event.PullRequest.Merged:
			if mergeRuleErr == nil && mergeRule != nil {
				s.applyAutoTransition(ctx, repo.WorkspaceID, "pr_merged", issue)
			} else if keywordSet[issue.ID] {
				// Closing keyword with no configured rule still closes.
				s.moveIssueToCompleted(ctx, repo.WorkspaceID, issue, map[uuid.UUID]bool{})
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

		issue := s.resolveIssueFromRef(ctx, repo.WorkspaceID, c.Message)
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

	issue := s.resolveIssueFromRef(ctx, repo.WorkspaceID, event.Ref)
	var issueID *uuid.UUID
	if issue != nil {
		id := issue.ID
		issueID = &id
	}

	branchURL := fmt.Sprintf("%s/src/branch/%s", repo.InstanceURL, event.Ref)
	branch := &domain.ForgejoBranch{
		ID: uuid.New(), WorkspaceID: repo.WorkspaceID, IssueID: issueID,
		ForgejoRepoID: repo.ID, Name: event.Ref, HTMLURL: &branchURL,
	}
	if err := s.fRepo.UpsertBranch(ctx, branch); err != nil {
		return fmt.Errorf("upserting branch: %w", err)
	}

	if issue != nil {
		s.applyAutoTransition(ctx, repo.WorkspaceID, "branch_created", issue)
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
	if event.Run.TriggerEvent == "pull_request" {
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

	openPRs, err := s.fRepo.ListOpenPRsByHeadSHA(ctx, repo.ID, sha)
	if err != nil || len(openPRs) == 0 {
		return nil
	}

	conn, err := s.resolveConnection(ctx, repo.WorkspaceID)
	if err != nil {
		return nil
	}
	owner, name, ok := splitRepoOwnerName(repo.FullName)
	if !ok {
		return nil
	}
	var combined *fj.CombinedStatus
	for attempt := 0; attempt < 3; attempt++ {
		combined, err = conn.client().GetCombinedStatus(owner, name, sha)
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
		return nil
	}
	allGreen := combined.AllChecksPassed()

	for i := range openPRs {
		pr := openPRs[i]
		if pr.ChecksGreen == allGreen {
			continue
		}
		if err := s.fRepo.UpdatePullRequestChecks(ctx, repo.ID, pr.Number, allGreen); err != nil {
			continue
		}
		if allGreen {
			s.applyChecksGreenTransition(ctx, repo.WorkspaceID, pr.ID)
		}
		s.broadcastRealtimeEvent(repo.WorkspaceID, realtime.Event{
			Type:    "forgejo:pr_updated",
			Payload: map[string]any{"pr_number": pr.Number, "checks_green": allGreen},
		})
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
		hook, err := client.GetWebhook(owner, name, *repo.WebhookID)
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
		newHook, err := client.CreateWebhook(owner, name, hook.URL, secret, nil)
		if err != nil {
			log.WithError(err).WithField("repo", repo.FullName).Warn("failed to create Forgejo Actions hook")
			continue
		}
		if err := s.fRepo.UpdateWebhookID(ctx, repo.ID, newHook.ID); err != nil {
			log.WithError(err).WithField("repo", repo.FullName).Warn("failed to save Forgejo Actions hook")
			_ = client.DeleteWebhook(owner, name, newHook.ID)
			continue
		}
		if err := client.DeleteWebhook(owner, name, *repo.WebhookID); err != nil {
			log.WithError(err).WithField("repo", repo.FullName).Warn("failed to remove old Forgejo hook")
		}
	}
	// One startup pass covers checks that completed before the new subscriptions.
	s.syncOpenPRChecks(ctx)
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
			_ = s.refreshChecksForSHA(ctx, &repos[i], *pr.HeadSHA)
		}
	}
}

func (s *ForgejoService) applyChecksGreenTransition(ctx context.Context, workspaceID, prID uuid.UUID) {
	issues, err := s.issuesForPR(ctx, prID)
	if err != nil {
		return
	}
	for _, issue := range issues {
		s.applyAutoTransition(ctx, workspaceID, "pr_checks_green", issue)
	}
	if len(issues) > 0 {
		s.broadcastAppRefresh(workspaceID, "issues")
	}
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
		if err == nil && issue != nil {
			issues = append(issues, issue)
		}
	}
	return issues, nil
}

// --- Issue linking ---

func (s *ForgejoService) resolveIssueFromRef(ctx context.Context, workspaceID uuid.UUID, texts ...string) *domain.Issue {
	for _, text := range texts {
		matches := issueIdentifierRegex.FindAllString(text, -1)
		for _, m := range matches {
			issue, err := s.issueRepo.GetByIdentifier(ctx, workspaceID, strings.ToUpper(m))
			if err == nil && issue != nil {
				return issue
			}
		}
	}
	return nil
}

// resolveIssuesFromRefs links every referenced issue, not just the first.
func (s *ForgejoService) resolveIssuesFromRefs(ctx context.Context, workspaceID uuid.UUID, texts ...string) []*domain.Issue {
	seen := make(map[string]bool)
	var issues []*domain.Issue
	for _, text := range texts {
		matches := issueIdentifierRegex.FindAllString(text, -1)
		for _, m := range matches {
			if seen[m] {
				continue
			}
			seen[m] = true
			issue, err := s.issueRepo.GetByIdentifier(ctx, workspaceID, strings.ToUpper(m))
			if err == nil && issue != nil {
				issues = append(issues, issue)
			}
		}
	}
	return issues
}

// resolveKeywordIssues returns issues referenced via closing keywords
// ("Fixes KUA-1") in the given texts.
func (s *ForgejoService) resolveKeywordIssues(ctx context.Context, workspaceID uuid.UUID, texts ...string) []*domain.Issue {
	seen := make(map[string]bool)
	var issues []*domain.Issue
	for _, text := range texts {
		matches := issueKeywordRegex.FindAllStringSubmatch(text, -1)
		for _, m := range matches {
			identifier := strings.ToUpper(m[1])
			if seen[identifier] {
				continue
			}
			seen[identifier] = true
			issue, err := s.issueRepo.GetByIdentifier(ctx, workspaceID, identifier)
			if err == nil && issue != nil {
				issues = append(issues, issue)
			}
		}
	}
	return issues
}

// --- Auto-Transitions ---

func (s *ForgejoService) applyAutoTransition(ctx context.Context, workspaceID uuid.UUID, event string, issue *domain.Issue) {
	rule, err := s.fRepo.GetAutoTransitionByEvent(ctx, workspaceID, event)
	if err != nil || rule == nil {
		return
	}

	oldStatus := string(issue.Status)
	newStatus := rule.TargetStatus
	if oldStatus == newStatus {
		return
	}

	issue.Status = domain.IssueStatus(newStatus)
	if rule.TargetStatusID != nil {
		issue.StatusID = rule.TargetStatusID
	} else {
		// Resolve the team's custom status ID from the slug
		ts, err := s.teamStatusRepo.GetByTeamAndSlug(ctx, issue.TeamID, newStatus)
		if err == nil && ts != nil {
			issue.StatusID = &ts.ID
		}
	}

	if err := s.issueRepo.Update(ctx, issue); err != nil {
		log.WithError(err).WithField("issue_id", issue.ID).Warn("failed to auto-transition issue")
		return
	}

	s.recordAutomationHistory(ctx, issue, oldStatus, newStatus)

	s.broadcastRealtimeEvent(workspaceID, realtime.Event{
		Type:    "issue.updated",
		Payload: issue,
	})
	s.applyStatusAutomation(ctx, workspaceID, issue, map[uuid.UUID]bool{})
}

// recordAutomationHistory writes the status-change entry attributed to the
// issue creator (issue_history.user_id is NOT NULL with an FK to users;
// uuid.Nil inserts used to fail silently).
func (s *ForgejoService) recordAutomationHistory(ctx context.Context, issue *domain.Issue, oldStatus, newStatus string) {
	if issue.CreatorID == uuid.Nil {
		return
	}
	_ = s.historyRepo.Create(ctx, issue.ID, issue.CreatorID, "status", &oldStatus, &newStatus)
}

func (s *ForgejoService) applyStatusAutomation(ctx context.Context, workspaceID uuid.UUID, issue *domain.Issue, visited map[uuid.UUID]bool) {
	if issue == nil || visited[issue.ID] || s.teamRepo == nil {
		return
	}
	visited[issue.ID] = true

	category := s.issueStatusCategory(ctx, issue)
	if category == domain.StatusCategoryCompleted {
		team, err := s.teamRepo.GetByID(ctx, issue.TeamID)
		if err == nil && team != nil && team.SubIssueAutoCloseEnabled {
			s.autoCloseSubIssues(ctx, workspaceID, issue.ID, visited)
		}
	}
	if issue.ParentID != nil {
		s.maybeAutoCloseParent(ctx, workspaceID, *issue.ParentID, visited)
	}
}

func (s *ForgejoService) maybeAutoCloseParent(ctx context.Context, workspaceID, parentID uuid.UUID, visited map[uuid.UUID]bool) {
	parent, err := s.issueRepo.GetByID(ctx, parentID)
	if err != nil || parent == nil || parent.WorkspaceID != workspaceID || visited[parent.ID] || s.teamRepo == nil {
		return
	}
	team, err := s.teamRepo.GetByID(ctx, parent.TeamID)
	if err != nil || team == nil || !team.ParentAutoCloseEnabled {
		return
	}
	total, done, err := s.issueRepo.CountSubIssues(ctx, parent.ID)
	if err != nil || total == 0 || total != done {
		return
	}
	s.moveIssueToCompleted(ctx, workspaceID, parent, visited)
}

func (s *ForgejoService) autoCloseSubIssues(ctx context.Context, workspaceID, parentID uuid.UUID, visited map[uuid.UUID]bool) {
	subIssues, err := s.issueRepo.ListSubIssues(ctx, parentID)
	if err != nil {
		return
	}
	for i := range subIssues {
		sub := subIssues[i]
		if sub.WorkspaceID != workspaceID || s.isTerminalStatus(ctx, &sub) {
			continue
		}
		s.moveIssueToCompleted(ctx, workspaceID, &sub, visited)
	}
}

func (s *ForgejoService) moveIssueToCompleted(ctx context.Context, workspaceID uuid.UUID, issue *domain.Issue, visited map[uuid.UUID]bool) {
	if issue == nil || s.isTerminalStatus(ctx, issue) {
		return
	}
	completedStatus, err := s.completedStatusForTeam(ctx, issue.TeamID)
	if err != nil || completedStatus == nil {
		return
	}

	old := string(issue.Status)
	issue.Status = domain.IssueStatus(completedStatus.Slug)
	issue.StatusID = &completedStatus.ID
	if err := s.issueRepo.Update(ctx, issue); err != nil {
		log.WithError(err).WithField("issue_id", issue.ID).Warn("failed to auto-close issue")
		return
	}
	newVal := completedStatus.Slug
	s.recordAutomationHistory(ctx, issue, old, newVal)
	s.broadcastRealtimeEvent(workspaceID, realtime.Event{Type: "issue.updated", Payload: issue})
	s.applyStatusAutomation(ctx, workspaceID, issue, visited)
}

func (s *ForgejoService) completedStatusForTeam(ctx context.Context, teamID uuid.UUID) (*domain.TeamStatus, error) {
	status, err := s.teamStatusRepo.GetByTeamAndSlug(ctx, teamID, string(domain.IssueStatusDone))
	if err == nil && status != nil {
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

func (s *ForgejoService) isTerminalStatus(ctx context.Context, issue *domain.Issue) bool {
	category := s.issueStatusCategory(ctx, issue)
	return category == domain.StatusCategoryCompleted || category == domain.StatusCategoryCancelled
}

func (s *ForgejoService) issueStatusCategory(ctx context.Context, issue *domain.Issue) domain.StatusCategory {
	if issue.StatusID != nil {
		status, err := s.teamStatusRepo.GetByID(ctx, *issue.StatusID)
		if err == nil && status != nil {
			return status.Category
		}
	}
	switch issue.Status {
	case domain.IssueStatusDone:
		return domain.StatusCategoryCompleted
	case domain.IssueStatusCancelled:
		return domain.StatusCategoryCancelled
	case domain.IssueStatusInProgress, domain.IssueStatusInReview:
		return domain.StatusCategoryStarted
	case domain.IssueStatusTodo:
		return domain.StatusCategoryUnstarted
	default:
		return domain.StatusCategoryBacklog
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
			RepoFullName: pr.RepoFullName, MergedAt: pr.MergedAt,
			CreatedAt: pr.CreatedAt, UpdatedAt: pr.UpdatedAt,
		})
	}

	for _, b := range branches {
		htmlURL := ""
		if b.HTMLURL != nil {
			htmlURL = *b.HTMLURL
		}
		resp.Branches = append(resp.Branches, dto.ForgejoBranchResponse{
			ID: b.ID.String(), Name: b.Name, HTMLURL: htmlURL, RepoFullName: b.RepoFullName,
			CreatedAt: b.CreatedAt,
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
		result = append(result, dto.ForgejoAutoTransitionResponse{
			Event: t.Event, TargetStatus: t.TargetStatus, TargetStatusID: statusID, IsActive: t.IsActive,
		})
	}
	return result, nil
}

func (s *ForgejoService) UpdateAutoTransitions(ctx context.Context, workspaceID uuid.UUID, req dto.UpdateAutoTransitionsRequest) error {
	for _, rule := range req.Transitions {
		if !forgejoLifecycleEvents[rule.Event] {
			return fmt.Errorf("unknown transition event %q", rule.Event)
		}
		if rule.IsActive && strings.TrimSpace(rule.TargetStatus) == "" && rule.TargetStatusID == nil {
			return fmt.Errorf("event %q needs a target status", rule.Event)
		}
		var statusID *uuid.UUID
		if rule.TargetStatusID != nil {
			parsed, err := uuid.Parse(*rule.TargetStatusID)
			if err != nil {
				return fmt.Errorf("invalid target_status_id: %w", err)
			}
			statusID = &parsed
		}
		t := &domain.ForgejoAutoTransition{
			ID: uuid.New(), WorkspaceID: workspaceID,
			Event: rule.Event, TargetStatus: strings.TrimSpace(rule.TargetStatus),
			TargetStatusID: statusID, IsActive: rule.IsActive,
		}
		if err := s.fRepo.UpsertAutoTransition(ctx, t); err != nil {
			return err
		}
	}
	return nil
}
