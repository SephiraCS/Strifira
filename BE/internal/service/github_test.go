package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestResolveIssueFromRefIsCaseInsensitive(t *testing.T) {
	workspaceID := uuid.New()
	issueRepo := new(mockIssueRepo)
	issueRepo.On("GetByIdentifier", mock.Anything, workspaceID, "KUA-1").
		Return(&domain.Issue{ID: uuid.New(), WorkspaceID: workspaceID, Identifier: "KUA-1"}, nil).Once()

	svc := &GitHubService{issueRepo: issueRepo}
	issue, err := svc.resolveIssueFromRef(context.Background(), workspaceID, "feature/kua-1-login-fix")

	require.NoError(t, err)
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

	svc := &GitHubService{issueRepo: issueRepo}
	issues, err := svc.resolveIssuesFromRefs(context.Background(), workspaceID, "feat(KUA-1): also touches TS-9", "see KUA-1 again")

	require.NoError(t, err)
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

	svc := &GitHubService{issueRepo: issueRepo}
	issues, err := svc.resolveKeywordIssues(context.Background(), workspaceID, "Add avatars\n\nFixes KUA-7")

	require.NoError(t, err)
	require.Len(t, issues, 1)
	assert.Equal(t, "KUA-7", issues[0].Identifier)
	issueRepo.AssertExpectations(t)
}

func TestPRStateFromEvent(t *testing.T) {
	parse := func(raw string) ghWebhookPR {
		var event ghWebhookPR
		require.NoError(t, json.Unmarshal([]byte(raw), &event))
		return event
	}
	assert.Equal(t, "merged", prStateFromEvent(parse(`{"action":"closed","pull_request":{"state":"closed","merged":true,"merged_at":"2026-09-21T10:00:00Z"}}`)))
	assert.Equal(t, "draft", prStateFromEvent(parse(`{"action":"opened","pull_request":{"state":"open","draft":true}}`)))
	assert.Equal(t, "open", prStateFromEvent(parse(`{"action":"synchronize","pull_request":{"state":"open"}}`)))
	assert.Equal(t, "closed", prStateFromEvent(parse(`{"action":"closed","pull_request":{"state":"closed"}}`)))
}

func TestGitHubWebhookPRPayloadParsing(t *testing.T) {
	payload := []byte(`{
		"action": "opened",
		"number": 5,
		"pull_request": {
			"number": 5,
			"title": "Add feature (KUA-31)",
			"state": "open",
			"merged": false,
			"html_url": "https://github.com/SephiraCS/Strifira/pull/5",
			"body": "Fixes KUA-31",
			"additions": 12,
			"deletions": 3,
			"head": {"ref": "KUA-31-feature", "sha": "9f2a1c0ffee0000000000000000000000000001"},
			"base": {"ref": "main"},
			"user": {"login": "mariogk", "avatar_url": "https://avatars.githubusercontent.com/u/1"}
		},
		"repository": {"id": 7, "full_name": "SephiraCS/Strifira", "html_url": "https://github.com/SephiraCS/Strifira"}
	}`)

	var envelope githubWebhookEnvelope
	require.NoError(t, json.Unmarshal(payload, &envelope))
	assert.Equal(t, "SephiraCS/Strifira", envelope.Repository.FullName)
	assert.Equal(t, "https://github.com/SephiraCS/Strifira", envelope.Repository.HTMLURL)
	assert.Equal(t, "https://github.com", instanceOrigin(envelope.Repository.HTMLURL))

	var event ghWebhookPR
	require.NoError(t, json.Unmarshal(payload, &event))
	assert.Equal(t, "opened", event.Action)
	assert.Equal(t, 5, event.PullRequest.Number)
	assert.Equal(t, "KUA-31-feature", event.PullRequest.Head.Ref)
	assert.Equal(t, "9f2a1c0ffee0000000000000000000000000001", event.PullRequest.Head.SHA)
	assert.Equal(t, 12, event.PullRequest.Additions)
}

func TestGitHubWebhookStatusPayloadParsing(t *testing.T) {
	variants := []string{
		`{"state":"success","sha":"abc123","repository":{"full_name":"SephiraCS/Strifira","html_url":"https://github.com/SephiraCS/Strifira"}}`,
		`{"state":"failure","commit":{"sha":"abc123"},"repository":{"full_name":"SephiraCS/Strifira","html_url":"https://github.com/SephiraCS/Strifira"}}`,
	}
	for _, raw := range variants {
		var event ghWebhookStatus
		require.NoError(t, json.Unmarshal([]byte(raw), &event))
		assert.Equal(t, "abc123", event.SHA+event.Commit.SHA)
	}
}

func TestGitHubBranchCreateSenderParsing(t *testing.T) {
	var event ghWebhookCreate
	require.NoError(t, json.Unmarshal([]byte(`{"ref_type":"branch","ref":"KUA-31-work","sender":{"login":"mariogk"}}`), &event))
	assert.Equal(t, "mariogk", event.Sender.Login)
}

func TestCheckRunEventHeadSHAParsing(t *testing.T) {
	fromSuite := []byte(`{"action":"completed","check_run":{"head_sha":"own-sha","check_suite":{"head_sha":"suite-sha"}}}`)
	completed, sha, err := checkRunEventHeadSHA(fromSuite)
	require.NoError(t, err)
	assert.True(t, completed)
	assert.Equal(t, "suite-sha", sha)

	own := []byte(`{"action":"completed","check_run":{"head_sha":"own-sha"}}`)
	completed, sha, err = checkRunEventHeadSHA(own)
	require.NoError(t, err)
	assert.True(t, completed)
	assert.Equal(t, "own-sha", sha)

	running := []byte(`{"action":"created","check_run":{"head_sha":"own-sha"}}`)
	completed, _, err = checkRunEventHeadSHA(running)
	require.NoError(t, err)
	assert.False(t, completed)
}

func TestWorkflowRunEventHeadSHAParsing(t *testing.T) {
	completed, sha, err := workflowRunEventHeadSHA([]byte(`{"action":"completed","workflow_run":{"head_sha":"head-sha"}}`))
	require.NoError(t, err)
	assert.True(t, completed)
	assert.Equal(t, "head-sha", sha)
	completed, sha, err = workflowRunEventHeadSHA([]byte(`{"action":"in_progress","workflow_run":{"head_sha":"head-sha"}}`))
	require.NoError(t, err)
	assert.False(t, completed)
	assert.Equal(t, "head-sha", sha)
}

func TestWebhookTargetURLDerivation(t *testing.T) {
	svc := &GitHubService{frontendURL: "https://strifira.example.com"}
	assert.Equal(t, "https://strifira.example.com/api/github/webhook", svc.webhookTargetURL())

	svc = &GitHubService{frontendURL: "http://localhost:5173"}
	assert.Equal(t, "http://localhost:8080/api/github/webhook", svc.webhookTargetURL())

	svc = &GitHubService{frontendURL: "https://strifira.example.com", webhookURL: "https://tunnel.example.com/hook"}
	assert.Equal(t, "https://tunnel.example.com/hook", svc.webhookTargetURL())
}
