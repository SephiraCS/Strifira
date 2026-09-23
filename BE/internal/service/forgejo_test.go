package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestResolveIssueFromRefIsCaseInsensitive(t *testing.T) {
	workspaceID := uuid.New()
	issueRepo := new(mockIssueRepo)
	issueRepo.On("GetByIdentifier", mock.Anything, workspaceID, "KUA-1").
		Return(&domain.Issue{ID: uuid.New(), WorkspaceID: workspaceID, Identifier: "KUA-1"}, nil).Once()

	svc := &ForgejoService{issueRepo: issueRepo}
	issue := svc.resolveIssueFromRef(context.Background(), workspaceID, "feature/kua-1-login-fix")

	require.NotNil(t, issue)
	assert.Equal(t, "KUA-1", issue.Identifier)
	issueRepo.AssertExpectations(t)
}

func TestResolveIssuesFromRefsLinksAllReferences(t *testing.T) {
	workspaceID := uuid.New()
	first := &domain.Issue{ID: uuid.New(), Identifier: "KUA-1"}
	second := &domain.Issue{ID: uuid.New(), Identifier: "TS-9"}
	issueRepo := new(mockIssueRepo)
	issueRepo.On("GetByIdentifier", mock.Anything, workspaceID, "KUA-1").Return(first, nil).Once()
	issueRepo.On("GetByIdentifier", mock.Anything, workspaceID, "TS-9").Return(second, nil).Once()

	svc := &ForgejoService{issueRepo: issueRepo}
	issues := svc.resolveIssuesFromRefs(context.Background(), workspaceID, "feat(KUA-1): also touches TS-9", "see KUA-1 again")

	require.Len(t, issues, 2)
	issueRepo.AssertExpectations(t)
}

func TestIssueKeywordRegex(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{"fixes", "Fixes KUA-12", []string{"KUA-12"}},
		{"lowercase keyword and identifier", "this closes kua-3", []string{"kua-3"}},
		{"resolves past tense", "resolved TS-44 as part of cleanup", []string{"TS-44"}},
		{"multiple", "fixes KUA-1 and closes TS-2", []string{"KUA-1", "TS-2"}},
		{"no keyword", "KUA-1 is related", nil},
		{"word boundary", "prefixfixes KUA-1", nil},
		{"issue-style number alone", "fixes #12", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			matches := issueKeywordRegex.FindAllStringSubmatch(tc.text, -1)
			var got []string
			for _, m := range matches {
				got = append(got, m[1])
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestResolveKeywordIssues(t *testing.T) {
	workspaceID := uuid.New()
	keywordIssue := &domain.Issue{ID: uuid.New(), Identifier: "KUA-7"}
	issueRepo := new(mockIssueRepo)
	issueRepo.On("GetByIdentifier", mock.Anything, workspaceID, "KUA-7").Return(keywordIssue, nil).Once()

	svc := &ForgejoService{issueRepo: issueRepo}
	issues := svc.resolveKeywordIssues(context.Background(), workspaceID, "Add avatars\n\nFixes KUA-7")

	require.Len(t, issues, 1)
	assert.Equal(t, "KUA-7", issues[0].Identifier)
	issueRepo.AssertExpectations(t)
}

func TestPRStateFromEvent(t *testing.T) {
	parse := func(raw string) fjWebhookPR {
		var event fjWebhookPR
		require.NoError(t, json.Unmarshal([]byte(raw), &event))
		return event
	}
	assert.Equal(t, "merged", prStateFromEvent(parse(`{"action":"closed","pull_request":{"state":"closed","merged":true,"merged_at":"2026-09-21T10:00:00Z"}}`)))
	assert.Equal(t, "draft", prStateFromEvent(parse(`{"action":"opened","pull_request":{"state":"open","draft":true}}`)))
	assert.Equal(t, "open", prStateFromEvent(parse(`{"action":"synchronized","pull_request":{"state":"open"}}`)))
	assert.Equal(t, "closed", prStateFromEvent(parse(`{"action":"closed","pull_request":{"state":"closed"}}`)))
}

func TestForgejoWebhookPRPayloadParsing(t *testing.T) {
	payload := []byte(`{
		"action": "opened",
		"number": 5,
		"pull_request": {
			"number": 5,
			"title": "Add feature (KUA-31)",
			"state": "open",
			"merged": false,
			"html_url": "https://git.terrascale.tech/TerraScale/ku/pull/5",
			"body": "Fixes KUA-31",
			"additions": 12,
			"deletions": 3,
			"head": {"ref": "KUA-31-feature", "sha": "9f2a1c0ffee0000000000000000000000000001"},
			"base": {"ref": "main"},
			"user": {"login": "mariogk", "avatar_url": "https://git.terrascale.tech/avatars/1"}
		},
		"repository": {"id": 7, "full_name": "TerraScale/ku", "html_url": "https://git.terrascale.tech/TerraScale/ku"}
	}`)

	var envelope forgejoWebhookEnvelope
	require.NoError(t, json.Unmarshal(payload, &envelope))
	assert.Equal(t, "TerraScale/ku", envelope.Repository.FullName)
	assert.Equal(t, "https://git.terrascale.tech/TerraScale/ku", envelope.Repository.HTMLURL)
	assert.Equal(t, "https://git.terrascale.tech", instanceOrigin(envelope.Repository.HTMLURL))

	var event fjWebhookPR
	require.NoError(t, json.Unmarshal(payload, &event))
	assert.Equal(t, "opened", event.Action)
	assert.Equal(t, 5, event.PullRequest.Number)
	assert.Equal(t, "KUA-31-feature", event.PullRequest.Head.Ref)
	assert.Equal(t, "9f2a1c0ffee0000000000000000000000000001", event.PullRequest.Head.SHA)
	assert.Equal(t, 12, event.PullRequest.Additions)
}

func TestForgejoWebhookStatusPayloadParsing(t *testing.T) {
	variants := []string{
		`{"state":"success","sha":"abc123","repository":{"full_name":"TerraScale/ku","html_url":"https://git.terrascale.tech/TerraScale/ku"}}`,
		`{"state":"pending","commit":{"id":"abc123"},"repository":{"full_name":"TerraScale/ku","html_url":"https://git.terrascale.tech/TerraScale/ku"}}`,
		`{"state":"failure","commit":{"sha":"abc123"},"repository":{"full_name":"TerraScale/ku","html_url":"https://git.terrascale.tech/TerraScale/ku"}}`,
	}
	for _, raw := range variants {
		var event fjWebhookStatus
		require.NoError(t, json.Unmarshal([]byte(raw), &event))
		assert.Equal(t, "abc123", event.SHA+event.Commit.ID+event.Commit.SHA)
	}
}

func TestActionRunHeadSHAUsesPRHead(t *testing.T) {
	sha, err := actionRunHeadSHA([]byte(`{"run":{"trigger_event":"pull_request","commit_sha":"merge-sha","event_payload":"{\"pull_request\":{\"head\":{\"sha\":\"head-sha\"}}}"}}`))
	require.NoError(t, err)
	assert.Equal(t, "head-sha", sha)

	sha, err = actionRunHeadSHA([]byte(`{"run":{"trigger_event":"push","commit_sha":"push-sha"}}`))
	require.NoError(t, err)
	assert.Equal(t, "push-sha", sha)

	sha, err = actionRunHeadSHA([]byte(`{"run":{"trigger_event":"pull_request","commit_sha":"merge-sha","event_payload":"{}"}}`))
	require.NoError(t, err)
	assert.Empty(t, sha)
}

func TestActionRunWebhookRepositoryEnvelope(t *testing.T) {
	var envelope forgejoWebhookEnvelope
	require.NoError(t, json.Unmarshal([]byte(`{"run":{"repository":{"full_name":"TerraScale/ku","html_url":"https://git.terrascale.tech/TerraScale/ku"}}}`), &envelope))
	assert.Empty(t, envelope.Repository.FullName)
	assert.Equal(t, "TerraScale/ku", envelope.Run.Repository.FullName)
	assert.Equal(t, "https://git.terrascale.tech", instanceOrigin(envelope.Run.Repository.HTMLURL))
}

func TestWebhookTargetURLDerivation(t *testing.T) {
	svc := &ForgejoService{frontendURL: "https://ku.example.com"}
	assert.Equal(t, "https://ku.example.com/api/forgejo/webhook", svc.webhookTargetURL())

	svc = &ForgejoService{frontendURL: "http://localhost:5173"}
	assert.Equal(t, "http://localhost:8080/api/forgejo/webhook", svc.webhookTargetURL())

	svc = &ForgejoService{frontendURL: "https://ku.example.com", webhookURL: "https://tunnel.example.com/hook"}
	assert.Equal(t, "https://tunnel.example.com/hook", svc.webhookTargetURL())
}
