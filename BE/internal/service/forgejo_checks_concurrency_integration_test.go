package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/dto"
	fj "github.com/ku/ku-backend/pkg/forgejo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForgejoWebhookOpeningAutomationRequiresOpenNonDraftPR(t *testing.T) {
	f := setupForgejoWebhook(t, "https://forgejo.example.test")
	require.NoError(t, f.svc.UpdateAutoTransitions(t.Context(), f.workspace, dto.UpdateAutoTransitionsRequest{
		Transitions: []dto.AutoTransitionRule{{Event: "pr_opened", TargetStatus: "in_progress", AssignForgejoUser: true, IsActive: true}},
	}))
	for i, tc := range []struct {
		name     string
		action   string
		state    string
		draft    bool
		automate bool
	}{
		{name: "draft opened", action: "opened", state: "open", draft: true},
		{name: "draft reopened", action: "reopened", state: "open", draft: true},
		{name: "closed opened", action: "opened", state: "closed"},
		{name: "closed reopened", action: "reopened", state: "closed"},
		{name: "closed ready", action: "edited", state: "closed"},
		{name: "open reopened", action: "reopened", state: "open", automate: true},
		{name: "open ready", action: "edited", state: "open", automate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			number := i + 2
			identifier := fmt.Sprintf("FA-%d", number)
			issueID := f.addIssue(t, f.team, f.todoID, number, identifier)
			var event map[string]any
			require.NoError(t, json.Unmarshal(f.pullRequestFor(t, number, identifier, tc.action, "Fixes "+identifier, "pusher", false), &event))
			pr := event["pull_request"].(map[string]any)
			pr["state"], pr["draft"] = tc.state, tc.draft
			if tc.action == "edited" {
				event["changes"] = map[string]any{"title": map[string]any{"from": "WIP: Implement " + identifier}}
			}
			if tc.action == "reopened" && tc.draft {
				// Exercise the actual draft close/reopen lifecycle. Its references
				// remain visible, but none of these deliveries may move or assign.
				event["action"] = "opened"
				opened, err := json.Marshal(event)
				require.NoError(t, err)
				require.NoError(t, f.deliver(t, "pull_request", opened))
				event["action"], pr["state"] = "closed", "closed"
				closed, err := json.Marshal(event)
				require.NoError(t, err)
				require.NoError(t, f.deliver(t, "pull_request", closed))
				event["action"], pr["state"] = tc.action, tc.state
			}
			body, err := json.Marshal(event)
			require.NoError(t, err)
			require.NoError(t, f.deliver(t, "pull_request", body))
			require.NoError(t, f.deliver(t, "pull_request", body))
			if tc.automate {
				assert.Equal(t, "in_progress", f.issueStatusSlug(t, issueID))
				assert.Equal(t, []string{f.pusherID.String()}, f.issueAssigneeIDs(t, issueID))
				assert.Equal(t, 1, f.historyCount(t, issueID, "status"))
				assert.Equal(t, 1, f.historyCount(t, issueID, "assignees"))
			} else {
				assert.Equal(t, "todo", f.issueStatusSlug(t, issueID))
				assert.Empty(t, f.issueAssigneeIDs(t, issueID))
				assert.Zero(t, f.historyCount(t, issueID, "status"))
				assert.Zero(t, f.historyCount(t, issueID, "assignees"))
			}
			activity, err := f.svc.GetIssueActivity(t.Context(), f.workspace, identifier)
			require.NoError(t, err)
			require.Len(t, activity.PullRequests, 1, "suppressed automation must still keep the issue's PR link")
		})
	}
}

func TestForgejoWebhookOlderGreenObservationCannotBorrowNewerFailureVersion(t *testing.T) {
	for _, initialGreen := range []bool{false, true} {
		t.Run(fmt.Sprintf("initial_green_%t", initialGreen), func(t *testing.T) {
			requested, released := make(chan struct{}), make(chan struct{})
			release := sync.OnceFunc(func() { close(released) })
			defer release()
			var requests atomic.Int64
			var current atomic.Pointer[fj.CombinedStatus]
			green := &fj.CombinedStatus{SHA: "head-commit", State: "success", TotalCount: 1, Statuses: []fj.CommitStatus{{Context: "tests", State: "success"}}}
			failure := &fj.CombinedStatus{SHA: "head-commit", State: "failure", TotalCount: 1, Statuses: []fj.CommitStatus{{Context: "tests", State: "failure"}}}
			current.Store(failure)
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/v1/repos/org/project/commits/head-commit/status", func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "token disposable-forgejo-token", r.Header.Get("Authorization"))
				combined := current.Load()
				if requests.Add(1) == 1 {
					// Refresh A observes success first. Hold its response while
					// refresh B fetches and persists the newer failure for the same SHA.
					combined = green
					close(requested)
					select {
					case <-released:
					case <-r.Context().Done():
						return
					}
				}
				if err := json.NewEncoder(w).Encode(combined); err != nil {
					t.Errorf("encode combined status: %v", err)
				}
			})
			server := httptest.NewServer(mux)
			t.Cleanup(func() {
				release()
				server.Close()
			})
			f := setupForgejoWebhook(t, server.URL)
			reviewID := f.addStatus(t, f.team, "reviewing", "started", 2)
			require.NoError(t, f.svc.UpdateAutoTransitions(t.Context(), f.workspace, dto.UpdateAutoTransitionsRequest{
				Transitions: []dto.AutoTransitionRule{
					{Event: "pr_opened", TargetStatus: "in_progress", IsActive: true},
					{Event: "pr_checks_green", TargetStatus: "reviewing", TargetAssigneeID: new(f.pusherID.String()), IsActive: true},
				},
			}))
			require.NoError(t, f.deliver(t, "pull_request", f.pullRequest(t, "opened", "Fixes FA-1", "pusher", false)))
			_, err := f.db.ExecContext(t.Context(), `UPDATE forgejo_pull_requests SET checks_green=$1,updated_at=NOW() WHERE forgejo_repo_id=$2 AND number=7`, initialGreen, f.repo.ID)
			require.NoError(t, err)
			before, err := f.svc.fRepo.GetPRByRepoAndNumber(t.Context(), f.repo.ID, 7)
			require.NoError(t, err)
			require.NotNil(t, before)
			statusHistory := f.historyCount(t, f.issueID, "status")
			assigneeHistory := f.historyCount(t, f.issueID, "assignees")
			assignees := f.issueAssigneeIDs(t, f.issueID)
			status := f.payload(t, map[string]any{"state": "success", "sha": "head-commit"})
			repo := f.verifiedRepo(t, status)
			finished := make(chan error, 1)
			ctx := t.Context()
			go func() { finished <- f.svc.HandleWebhookEvent(ctx, "status", status, repo) }()
			select {
			case <-requested:
			case <-time.After(5 * time.Second):
				t.Fatal("older green refresh did not reach Forgejo")
			}

			// The reconciler and webhook share the production refresh path.
			f.svc.syncOpenPRChecks(t.Context())
			newer, err := f.svc.fRepo.GetPRByRepoAndNumber(t.Context(), f.repo.ID, 7)
			require.NoError(t, err)
			require.NotNil(t, newer)
			require.False(t, newer.ChecksGreen)
			require.False(t, newer.UpdatedAt.Equal(before.UpdatedAt), "even a false-to-false observation must invalidate the earlier refresh")
			require.Equal(t, int64(2), requests.Load())
			release()
			select {
			case err := <-finished:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("older green refresh did not finish")
			}
			final, err := f.svc.fRepo.GetPRByRepoAndNumber(t.Context(), f.repo.ID, 7)
			require.NoError(t, err)
			require.NotNil(t, final)
			assert.False(t, final.ChecksGreen, "older success must not overwrite the persisted newer failure")
			assert.Equal(t, newer.UpdatedAt, final.UpdatedAt, "stale success cannot acquire or advance the newer version")
			assert.Equal(t, "in_progress", f.issueStatusSlug(t, f.issueID))
			assert.Equal(t, statusHistory, f.historyCount(t, f.issueID, "status"))
			assert.Equal(t, assigneeHistory, f.historyCount(t, f.issueID, "assignees"))
			assert.Equal(t, assignees, f.issueAssigneeIDs(t, f.issueID))

			// A newly fetched success can still move and assign once. Replaying
			// it preserves the user's subsequent manual board change.
			current.Store(green)
			require.NoError(t, f.deliver(t, "status", status))
			assert.Equal(t, reviewID, f.statusID(t, f.issueID))
			assert.Contains(t, f.issueAssigneeIDs(t, f.issueID), f.pusherID.String())
			assert.Equal(t, statusHistory+1, f.historyCount(t, f.issueID, "status"))
			assert.Equal(t, assigneeHistory+1, f.historyCount(t, f.issueID, "assignees"))
			_, err = f.db.ExecContext(t.Context(), `UPDATE issues SET status='in_progress',status_id=$2,updated_at=NOW() WHERE id=$1`, f.issueID, f.progressID)
			require.NoError(t, err)
			require.NoError(t, f.deliver(t, "status", status))
			assert.Equal(t, f.progressID, f.statusID(t, f.issueID))
			assert.Equal(t, statusHistory+1, f.historyCount(t, f.issueID, "status"))
			assert.Equal(t, assigneeHistory+1, f.historyCount(t, f.issueID, "assignees"))
			var linkIDs []uuid.UUID
			require.NoError(t, f.db.SelectContext(t.Context(), &linkIDs, `SELECT issue_id FROM forgejo_pull_request_issues WHERE pr_id=$1`, final.ID))
			assert.Equal(t, []uuid.UUID{f.issueID}, linkIDs)
		})
	}
}
