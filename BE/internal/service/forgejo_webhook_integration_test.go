package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/realtime"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/ku/ku-backend/pkg/crypto"
	fj "github.com/ku/ku-backend/pkg/forgejo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
)

// These tests start at the signed webhook boundary rather than invoking an
// internal transition directly. Every persistence assertion uses a disposable
// database migrated with the current application schema.
type forgejoWebhookFixture struct {
	*forgejoAutomationEnv
	forgejoAutomationFixtures
	repo   *domain.ForgejoRepoModel
	secret string
}

func setupForgejoWebhook(t *testing.T, origin string) *forgejoWebhookFixture {
	t.Helper()
	env := setupForgejoAutomation(t)
	f := seedAutomationWorkspace(t, env.db, true)
	env.svc.teamRepo = repository.NewTeamRepository(env.db)
	env.svc.encryptionKey = crypto.DeriveKey("disposable-webhook-fixture")
	env.svc.hub = realtime.NewHub()
	env.svc.global = &GlobalForgejoConfig{URL: origin, Token: "disposable-forgejo-token"}
	secret := "disposable-forgejo-webhook-secret"
	encrypted, err := crypto.Encrypt(secret, env.svc.encryptionKey)
	require.NoError(t, err)
	repo := &domain.ForgejoRepoModel{
		ID: uuid.New(), WorkspaceID: f.workspace, ForgejoRepoID: 42,
		FullName: "org/project", InstanceURL: origin, DefaultBranch: "main",
		WebhookSecret: encrypted, IsActive: true,
	}
	require.NoError(t, env.svc.fRepo.CreateRepo(t.Context(), repo))
	return &forgejoWebhookFixture{env, f, repo, secret}
}

func (f *forgejoWebhookFixture) payload(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	fields["repository"] = map[string]any{
		"id": 42, "full_name": f.repo.FullName,
		"html_url": f.repo.InstanceURL + "/" + f.repo.FullName,
	}
	body, err := json.Marshal(fields)
	require.NoError(t, err)
	return body
}

func (f *forgejoWebhookFixture) verifiedRepo(t *testing.T, body []byte) *domain.ForgejoRepoModel {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(f.secret))
	_, err := mac.Write(body)
	require.NoError(t, err)
	repo, ok := f.svc.VerifyWebhookSignature(t.Context(), body, hex.EncodeToString(mac.Sum(nil)))
	require.True(t, ok, "the real-shaped delivery must identify its linked repository")
	require.Equal(t, f.repo.ID, repo.ID)
	return repo
}

func (f *forgejoWebhookFixture) deliver(t *testing.T, event string, body []byte) error {
	t.Helper()
	return f.svc.HandleWebhookEvent(t.Context(), event, body, f.verifiedRepo(t, body))
}

func (f *forgejoWebhookFixture) createBranch(t *testing.T, ref, login string) []byte {
	t.Helper()
	return f.payload(t, map[string]any{
		"ref_type": "branch", "ref": "refs/heads/" + ref,
		"sender": map[string]any{"login": login},
	})
}

func (f *forgejoWebhookFixture) pullRequest(t *testing.T, action, body, login string, merged bool) []byte {
	t.Helper()
	return f.pullRequestFor(t, 7, "FA-1", action, body, login, merged)
}

func (f *forgejoWebhookFixture) pullRequestFor(t *testing.T, number int, identifier, action, body, login string, merged bool) []byte {
	t.Helper()
	state := "open"
	if merged {
		state = "closed"
	}
	pr := map[string]any{
		"number": number, "title": "Implement " + identifier, "body": body,
		"state": state, "draft": false, "merged": merged,
		"head":     map[string]any{"ref": "feature/" + identifier, "sha": "head-commit"},
		"base":     map[string]any{"ref": "main"},
		"user":     map[string]any{"login": login},
		"html_url": fmt.Sprintf("%s/org/project/pulls/%d", f.repo.InstanceURL, number),
	}
	if merged {
		pr["merged_at"] = "2026-09-30T12:00:00Z"
		pr["closed_at"] = "2026-09-30T12:00:00Z"
	}
	return f.payload(t, map[string]any{"action": action, "number": number, "pull_request": pr})
}

func (f *forgejoWebhookFixture) addStatus(t *testing.T, teamID uuid.UUID, slug, category string, position int) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO team_statuses (id,team_id,name,slug,category,position)
		VALUES ($1,$2,$3,$3,$4,$5)`, id, teamID, slug, category, position)
	require.NoError(t, err)
	return id
}

func (f *forgejoWebhookFixture) addIssue(t *testing.T, teamID, statusID uuid.UUID, number int, identifier string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO issues
		(id,workspace_id,team_id,number,identifier_text,title,status,status_id,priority,creator_id)
		VALUES ($1,$2,$3,$4,$5,$5,'todo',$6,0,$7)`, id, f.workspace, teamID, number, identifier, statusID, f.aliceUserID)
	require.NoError(t, err)
	return id
}

func (f *forgejoWebhookFixture) statusID(t *testing.T, issueID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, f.db.GetContext(t.Context(), &id, `SELECT status_id FROM issues WHERE id=$1`, issueID))
	return id
}

func (f *forgejoWebhookFixture) historyCount(t *testing.T, issueID uuid.UUID, field string) int {
	t.Helper()
	var count int
	require.NoError(t, f.db.GetContext(t.Context(), &count, `SELECT count(*) FROM issue_history WHERE issue_id=$1 AND field=$2`, issueID, field))
	return count
}

func forgejoIssueUpdates(t *testing.T, hub *realtime.Hub, workspaceID, userID uuid.UUID) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	registered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept issue update websocket: %v", err)
			return
		}
		defer conn.CloseNow()
		client := hub.Register(conn, workspaceID, userID)
		defer hub.Unregister(client)
		close(registered)
		hub.WritePump(ctx, client)
	}))
	conn, _, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		cancel()
		_ = conn.CloseNow()
		server.Close()
	})
	select {
	case <-registered:
	case <-time.After(5 * time.Second):
		t.Fatal("issue update websocket did not register")
	}
	return conn
}

func requireForgejoIssueUpdate(t *testing.T, conn *websocket.Conn, issueID uuid.UUID, status string, statusID uuid.UUID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		_, body, err := conn.Read(ctx)
		require.NoError(t, err, "a connected issue view must receive its automation update")
		var event struct {
			Type    string       `json:"type"`
			Payload domain.Issue `json:"payload"`
		}
		var envelope struct {
			Type string `json:"type"`
		}
		require.NoError(t, json.Unmarshal(body, &envelope))
		if envelope.Type != "issue.updated" {
			continue
		}
		require.NoError(t, json.Unmarshal(body, &event))
		if event.Payload.ID != issueID {
			continue
		}
		assert.Equal(t, domain.IssueStatus(status), event.Payload.Status)
		require.NotNil(t, event.Payload.StatusID)
		assert.Equal(t, statusID, *event.Payload.StatusID)
		return
	}
}

func TestForgejoWebhookIDOnlyRuleMovesCustomStatusAndPublishesUpdate(t *testing.T) {
	f := setupForgejoWebhook(t, "https://forgejo.example.test")
	buildingID := f.addStatus(t, f.team, "building", "started", 2)
	require.NoError(t, f.svc.UpdateAutoTransitions(t.Context(), f.workspace, dto.UpdateAutoTransitionsRequest{
		Transitions: []dto.AutoTransitionRule{{Event: "branch_created", TargetStatusID: new(buildingID.String()), AssignForgejoUser: true, IsActive: true}},
	}))
	updates := forgejoIssueUpdates(t, f.svc.hub, f.workspace, f.aliceUserID)
	body := f.createBranch(t, "feature/FA-1", "pusher")
	require.NoError(t, f.deliver(t, "create", body))
	require.Equal(t, "building", f.issueStatusSlug(t, f.issueID))
	assert.Equal(t, buildingID, f.statusID(t, f.issueID))
	assert.ElementsMatch(t, []string{f.aliceUserID.String(), f.bobUserID.String(), f.pusherID.String()}, f.issueAssigneeIDs(t, f.issueID))
	requireForgejoIssueUpdate(t, updates, f.issueID, "building", buildingID)
	require.NoError(t, f.deliver(t, "create", body))
	assert.Equal(t, 1, f.historyCount(t, f.issueID, "status"))
	assert.Equal(t, 1, f.historyCount(t, f.issueID, "assignees"))
	var branches int
	require.NoError(t, f.db.GetContext(t.Context(), &branches, `SELECT count(*) FROM forgejo_branches WHERE forgejo_repo_id=$1`, f.repo.ID))
	assert.Equal(t, 1, branches)
}

func TestForgejoWebhookBranchMovesEveryReferencedIssueInItsOwnTeam(t *testing.T) {
	f := setupForgejoWebhook(t, "https://forgejo.example.test")
	buildingA := f.addStatus(t, f.team, "building", "started", 2)
	teamB := uuid.New()
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO teams (id,workspace_id,name,key) VALUES ($1,$2,'Second','K')`, teamB, f.workspace)
	require.NoError(t, err)
	todoB := f.addStatus(t, teamB, "todo", "unstarted", 0)
	buildingB := f.addStatus(t, teamB, "building", "started", 1)
	issueB := f.addIssue(t, teamB, todoB, 1, "K-1")
	require.NoError(t, f.svc.UpdateAutoTransitions(t.Context(), f.workspace, dto.UpdateAutoTransitionsRequest{
		Transitions: []dto.AutoTransitionRule{{Event: "branch_created", TargetStatus: "building", TargetStatusID: new(buildingA.String()), AssignForgejoUser: true, IsActive: true}},
	}))
	require.NoError(t, f.deliver(t, "create", f.createBranch(t, "feature/FA-1-and-k-1-and-K-1", "unmapped-contributor")))
	assert.Equal(t, "building", f.issueStatusSlug(t, f.issueID))
	assert.Equal(t, buildingA, f.statusID(t, f.issueID))
	assert.Equal(t, "building", f.issueStatusSlug(t, issueB))
	assert.Equal(t, buildingB, f.statusID(t, issueB), "a workspace rule must resolve a status in each issue's team")
	assert.ElementsMatch(t, []string{f.aliceUserID.String(), f.bobUserID.String()}, f.issueAssigneeIDs(t, f.issueID))
	assert.Empty(t, f.issueAssigneeIDs(t, issueB), "an unmapped Forgejo author must not fabricate an assignee")
	assert.Equal(t, 1, f.historyCount(t, issueB, "status"), "case variants reference the same issue exactly once")
	require.NoError(t, f.deliver(t, "create", f.createBranch(t, "follow-up/K-1", "pusher")))
	assert.Equal(t, []string{f.pusherID.String()}, f.issueAssigneeIDs(t, issueB))
	var primary uuid.UUID
	require.NoError(t, f.db.GetContext(t.Context(), &primary, `SELECT assignee_id FROM issues WHERE id=$1`, issueB))
	assert.Equal(t, f.pusherID, primary, "the first resolved contributor fills a missing primary assignee")
	assert.Equal(t, 1, f.historyCount(t, issueB, "status"))
}

func TestForgejoWebhookRepairsStatusIdentityWhenSlugAlreadyMatches(t *testing.T) {
	f := setupForgejoWebhook(t, "https://forgejo.example.test")
	buildingID := f.addStatus(t, f.team, "building", "started", 2)
	_, err := f.db.ExecContext(t.Context(), `UPDATE issues SET status='building' WHERE id=$1`, f.issueID)
	require.NoError(t, err)
	require.NoError(t, f.svc.UpdateAutoTransitions(t.Context(), f.workspace, dto.UpdateAutoTransitionsRequest{
		Transitions: []dto.AutoTransitionRule{{Event: "branch_created", TargetStatus: "building", TargetStatusID: new(buildingID.String()), IsActive: true}},
	}))
	require.NoError(t, f.deliver(t, "create", f.createBranch(t, "feature/FA-1", "pusher")))
	assert.Equal(t, buildingID, f.statusID(t, f.issueID), "a stale status ID must not leave the issue in a different board column")
}

func TestForgejoWebhookLifecycleWaitsForEveryWorkflowAndMergesOnce(t *testing.T) {
	var combined atomic.Pointer[fj.CombinedStatus]
	combined.Store(&fj.CombinedStatus{SHA: "head-commit", State: "pending", TotalCount: 2, Statuses: []fj.CommitStatus{{Context: "build", State: "success"}, {Context: "tests", State: "pending"}}})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "token disposable-forgejo-token", r.Header.Get("Authorization"))
		if r.URL.Path != "/api/v1/repos/org/project/commits/head-commit/status" {
			t.Errorf("checks must be fetched for the PR head, got %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(combined.Load()); err != nil {
			t.Errorf("encode combined status: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	f := setupForgejoWebhook(t, server.URL)
	reviewID := f.addStatus(t, f.team, "reviewing", "started", 2)
	doneID := f.addStatus(t, f.team, "shipped", "completed", 3)
	require.NoError(t, f.svc.UpdateAutoTransitions(t.Context(), f.workspace, dto.UpdateAutoTransitionsRequest{
		Transitions: []dto.AutoTransitionRule{
			{Event: "pr_opened", TargetStatus: "in_progress", IsActive: true, AssignForgejoUser: true},
			{Event: "pr_checks_green", TargetStatus: "reviewing", IsActive: true},
			{Event: "pr_merged", TargetStatus: "shipped", IsActive: true},
		},
	}))
	updates := forgejoIssueUpdates(t, f.svc.hub, f.workspace, f.aliceUserID)
	require.NoError(t, f.deliver(t, "create", f.createBranch(t, "feature/FA-1", "unmapped-contributor")))
	requireForgejoIssueUpdate(t, updates, f.issueID, "in_progress", f.progressID)
	var draft map[string]any
	require.NoError(t, json.Unmarshal(f.pullRequest(t, "opened", "Fixes FA-1", "unmapped-contributor", false), &draft))
	draft["pull_request"].(map[string]any)["draft"] = true
	draft["pull_request"].(map[string]any)["title"] = "WIP: Implement FA-1"
	opened, err := json.Marshal(draft)
	require.NoError(t, err)
	require.NoError(t, f.deliver(t, "pull_request", opened))
	assert.Equal(t, "in_progress", f.issueStatusSlug(t, f.issueID))
	assert.ElementsMatch(t, []string{f.aliceUserID.String(), f.bobUserID.String()}, f.issueAssigneeIDs(t, f.issueID))

	// Forgejo Actions runs on a synthetic merge commit; its embedded trigger
	// carries the actual PR head whose aggregate checks determine readiness.
	trigger, err := json.Marshal(map[string]any{"pull_request": map[string]any{"head": map[string]any{"sha": "head-commit"}}})
	require.NoError(t, err)
	action, err := json.Marshal(map[string]any{"run": map[string]any{
		"id": 12, "commit_sha": "merge-commit", "trigger_event": "pull_request", "event_payload": string(trigger),
		"repository": map[string]any{"id": 42, "full_name": f.repo.FullName, "html_url": f.repo.InstanceURL + "/" + f.repo.FullName},
	}})
	require.NoError(t, err)
	require.NoError(t, f.deliver(t, "action_run_success", action))
	assert.Equal(t, "in_progress", f.issueStatusSlug(t, f.issueID), "one green workflow cannot move the issue while another is pending")
	var green bool
	require.NoError(t, f.db.GetContext(t.Context(), &green, `SELECT checks_green FROM forgejo_pull_requests WHERE forgejo_repo_id=$1 AND number=7`, f.repo.ID))
	assert.False(t, green)

	combined.Store(&fj.CombinedStatus{SHA: "head-commit", State: "success", TotalCount: 2, Statuses: []fj.CommitStatus{{Context: "build", State: "success"}, {Context: "tests", State: "success"}}})
	status := f.payload(t, map[string]any{"state": "success", "sha": "head-commit", "context": "tests"})
	require.NoError(t, f.deliver(t, "status", status))
	assert.Equal(t, "in_progress", f.issueStatusSlug(t, f.issueID), "successful CI must not move a draft PR's issue into review")
	require.NoError(t, f.db.GetContext(t.Context(), &green, `SELECT checks_green FROM forgejo_pull_requests WHERE forgejo_repo_id=$1 AND number=7`, f.repo.ID))
	assert.True(t, green)
	var ready map[string]any
	require.NoError(t, json.Unmarshal(f.pullRequest(t, "edited", "Fixes FA-1", "unmapped-contributor", false), &ready))
	ready["changes"] = map[string]any{"title": map[string]any{"from": "WIP: Implement FA-1"}}
	readyBody, err := json.Marshal(ready)
	require.NoError(t, err)
	require.NoError(t, f.deliver(t, "pull_request", readyBody))
	f.svc.syncOpenPRChecks(t.Context())
	assert.Equal(t, "reviewing", f.issueStatusSlug(t, f.issueID))
	requireForgejoIssueUpdate(t, updates, f.issueID, "reviewing", reviewID)
	require.NoError(t, f.deliver(t, "action_run_success", action))
	assert.Equal(t, 2, f.historyCount(t, f.issueID, "status"))
	// Metadata-only edits must retain the already-observed successful checks.
	require.NoError(t, f.deliver(t, "pull_request", f.pullRequest(t, "edited", "Fixes FA-1; clarify the description", "unmapped-contributor", false)))
	require.NoError(t, f.db.GetContext(t.Context(), &green, `SELECT checks_green FROM forgejo_pull_requests WHERE forgejo_repo_id=$1 AND number=7`, f.repo.ID))
	assert.True(t, green)
	assert.Equal(t, 2, f.historyCount(t, f.issueID, "status"))
	staleIssue, err := f.svc.issueRepo.GetByID(t.Context(), f.issueID)
	require.NoError(t, err)
	require.NotNil(t, staleIssue)

	merged := f.pullRequest(t, "closed", "Fixes FA-1", "unmapped-contributor", true)
	require.NoError(t, f.deliver(t, "pull_request", merged))
	assert.Equal(t, "shipped", f.issueStatusSlug(t, f.issueID))
	requireForgejoIssueUpdate(t, updates, f.issueID, "shipped", doneID)
	require.NoError(t, f.deliver(t, "pull_request", merged))
	assert.Equal(t, 3, f.historyCount(t, f.issueID, "status"))
	// A webhook that read the issue before the merge must refresh its stale
	// snapshot before applying a transition, preserving completed work.
	require.NoError(t, f.svc.applyAutoTransition(t.Context(), f.workspace, "pr_opened", staleIssue, "pusher"))
	assert.Equal(t, "shipped", f.issueStatusSlug(t, f.issueID))
	assert.Equal(t, 3, f.historyCount(t, f.issueID, "status"))

	// Some teams use a nonterminal merge validation column. A delayed opened
	// delivery must preserve the merged PR and that configured issue column.
	issue2 := f.addIssue(t, f.team, f.todoID, 2, "FA-2")
	require.NoError(t, f.svc.UpdateAutoTransitions(t.Context(), f.workspace, dto.UpdateAutoTransitionsRequest{
		Transitions: []dto.AutoTransitionRule{{Event: "pr_merged", TargetStatus: "reviewing", IsActive: true}},
	}))
	opened2 := f.pullRequestFor(t, 8, "FA-2", "opened", "Fixes FA-2", "unmapped-contributor", false)
	require.NoError(t, f.deliver(t, "pull_request", opened2))
	require.NoError(t, f.deliver(t, "pull_request", f.pullRequestFor(t, 8, "FA-2", "closed", "Fixes FA-2", "unmapped-contributor", true)))
	assert.Equal(t, "reviewing", f.issueStatusSlug(t, issue2))
	require.NoError(t, f.deliver(t, "pull_request", opened2))
	assert.Equal(t, "reviewing", f.issueStatusSlug(t, issue2))
	assert.Equal(t, 2, f.historyCount(t, issue2, "status"))
	activity, err := f.svc.GetIssueActivity(t.Context(), f.workspace, "FA-2")
	require.NoError(t, err)
	require.Len(t, activity.PullRequests, 1)
	assert.Equal(t, "merged", activity.PullRequests[0].State)
	activity, err = f.svc.GetIssueActivity(t.Context(), f.workspace, "FA-1")
	require.NoError(t, err)
	require.Len(t, activity.PullRequests, 1)
	assert.Equal(t, "merged", activity.PullRequests[0].State)
	assert.True(t, activity.PullRequests[0].ChecksGreen)
	// Deliveries can arrive late or be retried after the merge. Neither branch
	// creation, a stale opened event, nor green CI may reopen completed work.
	require.NoError(t, f.deliver(t, "create", f.createBranch(t, "feature/FA-1", "pusher")))
	require.NoError(t, f.deliver(t, "pull_request", opened))
	require.NoError(t, f.deliver(t, "status", status))
	assert.Equal(t, "shipped", f.issueStatusSlug(t, f.issueID))
	assert.Equal(t, 3, f.historyCount(t, f.issueID, "status"))
}

func TestForgejoWebhookClosingKeywordsCompleteEveryIssueWithoutMergeRule(t *testing.T) {
	f := setupForgejoWebhook(t, "https://forgejo.example.test")
	doneID := f.addStatus(t, f.team, "shipped", "completed", 2)
	issue2 := f.addIssue(t, f.team, f.todoID, 2, "FA-2")
	body := f.pullRequest(t, "closed", "Fixes FA-1 and closes FA-2", "unmapped-contributor", true)
	require.NoError(t, f.svc.UpdateAutoTransitions(t.Context(), f.workspace, dto.UpdateAutoTransitionsRequest{
		Transitions: []dto.AutoTransitionRule{{Event: "pr_merged", TargetStatus: "shipped", IsActive: false}},
	}))
	require.NoError(t, f.deliver(t, "pull_request", body))
	for _, id := range []uuid.UUID{f.issueID, issue2} {
		assert.Equal(t, "todo", f.issueStatusSlug(t, id), "an explicitly disabled merge rule must also disable keyword closing")
		assert.Zero(t, f.historyCount(t, id, "status"))
	}
	_, err := f.db.ExecContext(t.Context(), `DELETE FROM forgejo_auto_transitions WHERE workspace_id=$1 AND event='pr_merged'`, f.workspace)
	require.NoError(t, err)
	require.NoError(t, f.deliver(t, "pull_request", body))
	for _, id := range []uuid.UUID{f.issueID, issue2} {
		assert.Equal(t, "shipped", f.issueStatusSlug(t, id))
		assert.Equal(t, doneID, f.statusID(t, id))
	}
	require.NoError(t, f.deliver(t, "pull_request", body))
	for _, id := range []uuid.UUID{f.issueID, issue2} {
		assert.Equal(t, 1, f.historyCount(t, id, "status"))
		var links int
		require.NoError(t, f.db.GetContext(t.Context(), &links, `SELECT count(*) FROM forgejo_pull_request_issues WHERE issue_id=$1`, id))
		assert.Equal(t, 1, links)
	}
}

func TestForgejoWebhookChecksFailureRetriesSameSHAWithoutLatchingGreen(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/repos/org/project/commits/head-commit/status", r.URL.Path)
		fmt.Fprint(w, `{"sha":"head-commit","state":"success","total_count":1,"statuses":[{"context":"test","status":"success"}]}`)
	}))
	t.Cleanup(server.Close)
	f := setupForgejoWebhook(t, server.URL)
	reviewID := f.addStatus(t, f.team, "reviewing", "started", 2)
	require.NoError(t, f.svc.UpdateAutoTransitions(t.Context(), f.workspace, dto.UpdateAutoTransitionsRequest{
		Transitions: []dto.AutoTransitionRule{{Event: "pr_checks_green", TargetStatus: "reviewing", IsActive: true}},
	}))
	require.NoError(t, f.deliver(t, "pull_request", f.pullRequest(t, "opened", "Fixes FA-1", "pusher", false)))
	_, err := f.db.ExecContext(t.Context(), `CREATE FUNCTION reject_forgejo_transition() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'disposable automation write failure'; END; $$;
		CREATE TRIGGER reject_forgejo_transition BEFORE UPDATE ON issues FOR EACH ROW EXECUTE FUNCTION reject_forgejo_transition()`)
	require.NoError(t, err)
	body := f.payload(t, map[string]any{"state": "success", "sha": "head-commit", "context": "test"})
	err = f.deliver(t, "status", body)
	require.Error(t, err, "a failed automation write must make webhook delivery retryable")
	assert.Equal(t, "todo", f.issueStatusSlug(t, f.issueID))
	assert.Zero(t, f.historyCount(t, f.issueID, "status"))
	var green bool
	require.NoError(t, f.db.GetContext(t.Context(), &green, `SELECT checks_green FROM forgejo_pull_requests WHERE forgejo_repo_id=$1 AND number=7`, f.repo.ID))
	assert.False(t, green, "the completion latch must follow successful issue writes")
	_, err = f.db.ExecContext(t.Context(), `DROP TRIGGER reject_forgejo_transition ON issues`)
	require.NoError(t, err)
	require.NoError(t, f.deliver(t, "status", body))
	assert.Equal(t, "reviewing", f.issueStatusSlug(t, f.issueID))
	assert.Equal(t, reviewID, f.statusID(t, f.issueID))
	assert.Equal(t, 1, f.historyCount(t, f.issueID, "status"))
	require.NoError(t, f.db.GetContext(t.Context(), &green, `SELECT checks_green FROM forgejo_pull_requests WHERE forgejo_repo_id=$1 AND number=7`, f.repo.ID))
	assert.True(t, green)
	require.NoError(t, f.deliver(t, "status", body))
	assert.Equal(t, 1, f.historyCount(t, f.issueID, "status"))
}

func TestForgejoWebhookOldReferencesFollowTeamKeyRename(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/repos/org/project/commits/head-commit/status", r.URL.Path)
		fmt.Fprint(w, `{"sha":"head-commit","state":"success","total_count":1,"statuses":[{"context":"test","status":"success"}]}`)
	}))
	t.Cleanup(server.Close)
	f := setupForgejoWebhook(t, server.URL)
	reviewID := f.addStatus(t, f.team, "reviewing", "started", 2)
	issue2 := f.addIssue(t, f.team, f.todoID, 2, "FA-2")
	require.NoError(t, f.svc.UpdateAutoTransitions(t.Context(), f.workspace, dto.UpdateAutoTransitionsRequest{
		Transitions: []dto.AutoTransitionRule{
			{Event: "pr_opened", TargetStatus: "in_progress", IsActive: true},
			{Event: "pr_checks_green", TargetStatus: "reviewing", IsActive: true},
		},
	}))
	teamRepo := repository.NewTeamRepository(f.db)
	team, err := teamRepo.GetByID(t.Context(), f.team)
	require.NoError(t, err)
	require.NotNil(t, team)
	count, err := teamRepo.UpdateAndRenameKey(t.Context(), team, "TS")
	require.NoError(t, err)
	assert.EqualValues(t, 2, count)
	issue, err := f.svc.issueRepo.GetByID(t.Context(), f.issueID)
	require.NoError(t, err)
	require.NotNil(t, issue)
	assert.Equal(t, "TS-1", issue.Identifier)
	// Simulate an earlier rename whose second historical identifier was only
	// restored after its PR had already arrived and completed its checks.
	_, err = f.db.ExecContext(t.Context(), `DELETE FROM issue_identifier_history WHERE workspace_id=$1 AND identifier_text='FA-2'`, f.workspace)
	require.NoError(t, err)

	// Existing branches and pull requests keep the old key after a team rename.
	// Their references must continue to resolve to the same stable issue ID.
	require.NoError(t, f.deliver(t, "pull_request", f.pullRequest(t, "opened", "Fixes fa-1; also FA-1 and FA-2", "pusher", false)))
	require.Equal(t, "in_progress", f.issueStatusSlug(t, f.issueID))
	var links []uuid.UUID
	require.NoError(t, f.db.SelectContext(t.Context(), &links, `SELECT issue_id FROM forgejo_pull_request_issues`))
	assert.Equal(t, []uuid.UUID{f.issueID}, links)
	require.NoError(t, f.deliver(t, "status", f.payload(t, map[string]any{"state": "success", "sha": "head-commit"})))
	assert.Equal(t, "reviewing", f.issueStatusSlug(t, f.issueID))
	assert.Equal(t, reviewID, f.statusID(t, f.issueID))
	assert.Equal(t, "todo", f.issueStatusSlug(t, issue2))
	_, err = f.db.ExecContext(t.Context(), `INSERT INTO issue_identifier_history (workspace_id,identifier_text,issue_id) VALUES ($1,'FA-2',$2)`, f.workspace, issue2)
	require.NoError(t, err)
	status := f.payload(t, map[string]any{"state": "success", "sha": "head-commit"})
	require.NoError(t, f.deliver(t, "status", status))
	assert.Equal(t, "reviewing", f.issueStatusSlug(t, issue2), "an already-green PR must catch up an explicitly restored issue reference")
	assert.Equal(t, reviewID, f.statusID(t, issue2))
	require.NoError(t, f.deliver(t, "status", status))
	assert.Equal(t, 2, f.historyCount(t, f.issueID, "status"), "catch-up must not repeat transitions for existing links")
	assert.Equal(t, 1, f.historyCount(t, issue2, "status"))
	issue3 := f.addIssue(t, f.team, f.todoID, 3, "TS-3")
	_, err = f.db.ExecContext(t.Context(), `UPDATE issues SET status='todo',status_id=$2,updated_at=NOW() WHERE id=$1`, f.issueID, f.todoID)
	require.NoError(t, err)
	edited := f.pullRequest(t, "edited", "Fixes FA-1; also TS-3", "pusher", false)
	require.NoError(t, f.deliver(t, "pull_request", edited))
	assert.Equal(t, "reviewing", f.issueStatusSlug(t, issue3), "a corrected reference on an already-green PR must catch up immediately")
	assert.Equal(t, "todo", f.issueStatusSlug(t, f.issueID), "editing another reference must preserve an existing issue's manual status change")
	require.NoError(t, f.deliver(t, "pull_request", edited))
	require.NoError(t, f.deliver(t, "status", status))
	assert.Equal(t, 2, f.historyCount(t, f.issueID, "status"))
	assert.Equal(t, 1, f.historyCount(t, issue2, "status"))
	assert.Equal(t, 1, f.historyCount(t, issue3, "status"))
	activity, err := f.svc.GetIssueActivity(t.Context(), f.workspace, "TS-1")
	require.NoError(t, err)
	assert.Len(t, activity.PullRequests, 1)
	activity, err = f.svc.GetIssueActivity(t.Context(), f.workspace, "TS-2")
	require.NoError(t, err)
	assert.Empty(t, activity.PullRequests, "removing a reference must remove its PR link")
	activity, err = f.svc.GetIssueActivity(t.Context(), f.workspace, "TS-3")
	require.NoError(t, err)
	require.Len(t, activity.PullRequests, 1)
	assert.True(t, activity.PullRequests[0].ChecksGreen)
}

func TestForgejoWebhookRejectsStatusFromAnotherWorkspace(t *testing.T) {
	f := setupForgejoWebhook(t, "https://forgejo.example.test")
	workspaceID, teamID := uuid.New(), uuid.New()
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO workspaces (id,name,slug,owner_id) VALUES ($1,'Other','other',$2)`, workspaceID, f.aliceUserID)
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), `INSERT INTO teams (id,workspace_id,name,key) VALUES ($1,$2,'Other','OT')`, teamID, workspaceID)
	require.NoError(t, err)
	foreignID := f.addStatus(t, teamID, "building", "started", 0)
	err = f.svc.UpdateAutoTransitions(t.Context(), f.workspace, dto.UpdateAutoTransitionsRequest{
		Transitions: []dto.AutoTransitionRule{{Event: "branch_created", TargetStatusID: new(foreignID.String()), IsActive: true}},
	})
	require.Error(t, err, "configuration must reject a status owned by another workspace")
	rule, err := f.svc.fRepo.GetAutoTransitionByEvent(t.Context(), f.workspace, "branch_created")
	require.NoError(t, err)
	require.NotNil(t, rule)
	assert.Equal(t, "in_progress", rule.TargetStatus, "invalid configuration must preserve the working rule")
	require.NotNil(t, rule.TargetStatusID)
	assert.Equal(t, f.progressID, *rule.TargetStatusID)
}

func TestForgejoWebhookOldHeadChecksCannotMoveUpdatedPullRequest(t *testing.T) {
	requested := make(chan struct{})
	released := make(chan struct{})
	notifyRequested := sync.OnceFunc(func() { close(requested) })
	release := sync.OnceFunc(func() { close(released) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/repos/org/project/commits/head-commit/status", r.URL.Path)
		notifyRequested()
		select {
		case <-released:
			fmt.Fprint(w, `{"sha":"head-commit","state":"success","total_count":1,"statuses":[{"context":"test","status":"success"}]}`)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		release()
		server.Close()
	})
	f := setupForgejoWebhook(t, server.URL)
	f.addStatus(t, f.team, "reviewing", "started", 2)
	require.NoError(t, f.svc.UpdateAutoTransitions(t.Context(), f.workspace, dto.UpdateAutoTransitionsRequest{
		Transitions: []dto.AutoTransitionRule{
			{Event: "pr_opened", TargetStatus: "in_progress", IsActive: true},
			{Event: "pr_checks_green", TargetStatus: "reviewing", IsActive: true},
		},
	}))
	require.NoError(t, f.deliver(t, "pull_request", f.pullRequest(t, "opened", "Fixes FA-1", "pusher", false)))
	status := f.payload(t, map[string]any{"state": "success", "sha": "head-commit"})
	repo := f.verifiedRepo(t, status)
	finished := make(chan error, 1)
	go func() {
		finished <- f.svc.HandleWebhookEvent(t.Context(), "status", status, repo)
	}()
	select {
	case <-requested:
	case <-time.After(5 * time.Second):
		t.Fatal("old-head status request did not reach Forgejo")
	}
	var updated map[string]any
	require.NoError(t, json.Unmarshal(f.pullRequest(t, "synchronized", "Fixes FA-1", "pusher", false), &updated))
	updated["pull_request"].(map[string]any)["head"].(map[string]any)["sha"] = "new-head-commit"
	body, err := json.Marshal(updated)
	require.NoError(t, err)
	require.NoError(t, f.deliver(t, "pull_request", body))
	release()
	select {
	case err := <-finished:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("stale status delivery did not finish")
	}
	assert.Equal(t, "in_progress", f.issueStatusSlug(t, f.issueID), "checks for a replaced PR head must not move the issue to review")
	assert.Equal(t, 1, f.historyCount(t, f.issueID, "status"))
	var pr struct {
		HeadSHA     string `db:"head_sha"`
		ChecksGreen bool   `db:"checks_green"`
	}
	require.NoError(t, f.db.GetContext(t.Context(), &pr, `SELECT head_sha,checks_green FROM forgejo_pull_requests WHERE forgejo_repo_id=$1 AND number=7`, f.repo.ID))
	assert.Equal(t, "new-head-commit", pr.HeadSHA)
	assert.False(t, pr.ChecksGreen, "the new head cannot inherit the old head's completion latch")
}

func TestForgejoWebhookGreenOpenedRedeliveryCatchesRestoredReference(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/repos/org/project/commits/head-commit/status", r.URL.Path)
		fmt.Fprint(w, `{"sha":"head-commit","state":"success","total_count":1,"statuses":[{"context":"test","status":"success"}]}`)
	}))
	t.Cleanup(server.Close)
	f := setupForgejoWebhook(t, server.URL)
	reviewID := f.addStatus(t, f.team, "reviewing", "started", 2)
	issue2 := f.addIssue(t, f.team, f.todoID, 2, "FA-2")
	require.NoError(t, f.svc.UpdateAutoTransitions(t.Context(), f.workspace, dto.UpdateAutoTransitionsRequest{
		Transitions: []dto.AutoTransitionRule{
			{Event: "pr_opened", TargetStatus: "in_progress", IsActive: true},
			{Event: "pr_checks_green", TargetStatus: "reviewing", IsActive: true},
		},
	}))
	teamRepo := repository.NewTeamRepository(f.db)
	team, err := teamRepo.GetByID(t.Context(), f.team)
	require.NoError(t, err)
	require.NotNil(t, team)
	_, err = teamRepo.UpdateAndRenameKey(t.Context(), team, "TS")
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), `DELETE FROM issue_identifier_history WHERE workspace_id=$1 AND identifier_text='FA-2'`, f.workspace)
	require.NoError(t, err)
	opened := f.pullRequest(t, "opened", "Fixes FA-1 and FA-2", "pusher", false)
	require.NoError(t, f.deliver(t, "pull_request", opened))
	status := f.payload(t, map[string]any{"state": "success", "sha": "head-commit"})
	require.NoError(t, f.deliver(t, "status", status))
	assert.Equal(t, "reviewing", f.issueStatusSlug(t, f.issueID))
	assert.Equal(t, "todo", f.issueStatusSlug(t, issue2))
	_, err = f.db.ExecContext(t.Context(), `UPDATE issues SET status='todo',status_id=$2,updated_at=NOW() WHERE id=$1`, f.issueID, f.todoID)
	require.NoError(t, err)
	_, err = f.db.ExecContext(t.Context(), `INSERT INTO issue_identifier_history (workspace_id,identifier_text,issue_id) VALUES ($1,'FA-2',$2)`, f.workspace, issue2)
	require.NoError(t, err)

	// Retrying the exact original signed delivery is how an older PR can
	// supply its missing body after a verified identifier restoration.
	require.NoError(t, f.deliver(t, "pull_request", opened))
	assert.Equal(t, "reviewing", f.issueStatusSlug(t, issue2))
	assert.Equal(t, reviewID, f.statusID(t, issue2))
	assert.Equal(t, "todo", f.issueStatusSlug(t, f.issueID), "redelivery must preserve existing issue work done after CI")
	require.NoError(t, f.deliver(t, "status", status))
	assert.Equal(t, "reviewing", f.issueStatusSlug(t, issue2))
	assert.Equal(t, "todo", f.issueStatusSlug(t, f.issueID))
	assert.Equal(t, 2, f.historyCount(t, f.issueID, "status"))
	assert.Equal(t, 1, f.historyCount(t, issue2, "status"))
	activity, err := f.svc.GetIssueActivity(t.Context(), f.workspace, "TS-2")
	require.NoError(t, err)
	require.Len(t, activity.PullRequests, 1)
	assert.True(t, activity.PullRequests[0].ChecksGreen)
}

func (f *forgejoWebhookFixture) draftPullRequest(t *testing.T, number int, action, title, body string) []byte {
	t.Helper()
	return f.payload(t, map[string]any{"action": action, "number": number, "pull_request": map[string]any{
		"number": number, "title": title, "body": body, "state": "open", "draft": true, "merged": false,
		"head":     map[string]any{"ref": "wip/no-reference", "sha": "draft-head"},
		"base":     map[string]any{"ref": "main"},
		"user":     map[string]any{"login": "pusher"},
		"html_url": fmt.Sprintf("%s/org/project/pulls/%d", f.repo.InstanceURL, number),
	}})
}

func TestForgejoWebhookDraftPullRequestStartsEveryReferencedIssue(t *testing.T) {
	f := setupForgejoWebhook(t, "https://forgejo.example.test")
	second := f.addIssue(t, f.team, f.todoID, 2, "FA-2")
	require.NoError(t, f.svc.UpdateAutoTransitions(t.Context(), f.workspace, dto.UpdateAutoTransitionsRequest{
		Transitions: []dto.AutoTransitionRule{{Event: "pr_opened", TargetStatus: "in_progress", AssignForgejoUser: true, IsActive: true}},
	}))
	require.NoError(t, f.deliver(t, "pull_request", f.draftPullRequest(t, 9, "opened", "WIP: Implement FA-1", "")))
	assert.Equal(t, "in_progress", f.issueStatusSlug(t, f.issueID), "a draft PR means work has started")
	assert.Equal(t, "todo", f.issueStatusSlug(t, second))

	// Referencing another issue by editing the body starts it too.
	require.NoError(t, f.deliver(t, "pull_request", f.draftPullRequest(t, 9, "edited", "WIP: Implement FA-1", "Also covers FA-2")))
	assert.Equal(t, "in_progress", f.issueStatusSlug(t, second))
	assert.Contains(t, f.issueAssigneeIDs(t, second), f.pusherID.String())
	assert.Equal(t, 1, f.historyCount(t, f.issueID, "status"), "redelivery for an already linked issue must not replay")
}

func TestForgejoWebhookAutomationNeverMovesAnIssueBackwards(t *testing.T) {
	f := setupForgejoWebhook(t, "https://forgejo.example.test")
	reviewID := f.addStatus(t, f.team, "pr", "started", 2)
	_, err := f.db.ExecContext(t.Context(), `UPDATE issues SET status='pr', status_id=$1 WHERE id=$2`, reviewID, f.issueID)
	require.NoError(t, err)
	require.NoError(t, f.svc.UpdateAutoTransitions(t.Context(), f.workspace, dto.UpdateAutoTransitionsRequest{
		Transitions: []dto.AutoTransitionRule{
			{Event: "branch_created", TargetStatus: "in_progress", AssignForgejoUser: true, IsActive: true},
			{Event: "pr_opened", TargetStatus: "in_progress", AssignForgejoUser: true, IsActive: true},
		},
	}))
	require.NoError(t, f.deliver(t, "create", f.createBranch(t, "follow-up/FA-1", "pusher")))
	require.NoError(t, f.deliver(t, "pull_request", f.pullRequestFor(t, 11, "FA-1", "opened", "", "pusher", false)))
	assert.Equal(t, "pr", f.issueStatusSlug(t, f.issueID), "a second branch or PR must not pull a reviewed issue back")
	assert.Equal(t, 0, f.historyCount(t, f.issueID, "status"))
	assert.Contains(t, f.issueAssigneeIDs(t, f.issueID), f.pusherID.String(), "assignment still applies")
}
