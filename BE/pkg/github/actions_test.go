package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActionsClientGitHubContract(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "2022-11-28", r.Header.Get("X-GitHub-Api-Version"))
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/v3/repos/org/project/actions/runs":
			assert.Equal(t, "2", r.URL.Query().Get("page"))
			assert.Equal(t, "10", r.URL.Query().Get("per_page"))
			assert.Equal(t, "in_progress", r.URL.Query().Get("status"))
			assert.Equal(t, "feature/ci&fix", r.URL.Query().Get("branch"))
			assert.Equal(t, "push", r.URL.Query().Get("event"))
			assert.Equal(t, "abcd", r.URL.Query().Get("head_sha"))
			fmt.Fprint(w, `{"workflow_runs":[{"id":44,"run_number":7,"path":".github/workflows/ci.yaml","display_title":"Deploy","name":"CI","head_branch":"main","head_sha":"abcd","status":"completed","conclusion":"failure","created_at":"2026-09-30T12:00:00Z","updated_at":"2026-09-30T12:01:00Z"}],"total_count":22}`)
		case "/api/v3/repos/org/project/actions/workflows/ci.yaml/runs":
			fmt.Fprint(w, `{"workflow_runs":[],"total_count":0}`)
		case "/api/v3/repos/org/project/actions/runs/44":
			fmt.Fprint(w, `{"id":44,"status":"in_progress"}`)
		case "/api/v3/repos/org/project/actions/runs/44/jobs":
			fmt.Fprint(w, `{"total_count":1,"jobs":[{"id":52,"run_id":44,"run_attempt":3,"name":"Build","status":"completed","conclusion":"failure"}]}`)
		case "/api/v3/repos/org/project/actions/jobs/52/logs":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "build failed\n")
		case "/api/v3/repos/org/project/actions/runs/44/cancel":
			assert.Equal(t, http.MethodPost, r.Method)
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewClient(server.URL, "test-token")
	list, err := client.ListActionRuns(t.Context(), "org", "project", ActionRunOptions{Page: 2, PerPage: 10, Status: "running", Ref: "refs/heads/feature/ci&fix", Event: "push", HeadSHA: "abcd"})
	require.NoError(t, err)
	assert.EqualValues(t, 22, list.TotalCount)
	require.Len(t, list.Runs, 1)
	assert.EqualValues(t, 7, list.Runs[0].RunNumber)
	assert.Equal(t, "main", list.Runs[0].HeadBranch)
	assert.Equal(t, ".github/workflows/ci.yaml", list.Runs[0].WorkflowID)
	assert.Equal(t, "failure", list.Runs[0].Conclusion)
	// A workflow filter routes to the workflow-scoped endpoint.
	_, err = client.ListActionRuns(t.Context(), "org", "project", ActionRunOptions{Page: 1, PerPage: 10, WorkflowID: "ci.yaml"})
	require.NoError(t, err)
	run, err := client.GetActionRun(t.Context(), "org", "project", 44)
	require.NoError(t, err)
	assert.Equal(t, "in_progress", run.Status)
	jobs, err := client.ListActionJobs(t.Context(), "org", "project", 44)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.EqualValues(t, 3, jobs[0].Attempt)
	log, err := client.GetActionJobLog(t.Context(), "org", "project", 52)
	require.NoError(t, err)
	assert.Equal(t, "build failed\n", log.Content)
	assert.False(t, log.Truncated)
	require.NoError(t, client.CancelActionRun(t.Context(), "org", "project", 44))
	assert.Equal(t, []string{
		"GET /api/v3/repos/org/project/actions/runs", "GET /api/v3/repos/org/project/actions/workflows/ci.yaml/runs",
		"GET /api/v3/repos/org/project/actions/runs/44", "GET /api/v3/repos/org/project/actions/runs/44/jobs",
		"GET /api/v3/repos/org/project/actions/jobs/52/logs", "POST /api/v3/repos/org/project/actions/runs/44/cancel",
	}, requests)
}

func TestActionsStatusFilterVocabulary(t *testing.T) {
	for _, tc := range []struct{ input, query string }{
		{"", ""},
		{"queued", "queued"},
		{"waiting", "queued"},
		{"running", "in_progress"},
		{"in_progress", "in_progress"},
		{"completed", "completed"},
	} {
		t.Run("status="+tc.input, func(t *testing.T) {
			values, err := actionRunQuery(ActionRunOptions{Status: tc.input})
			require.NoError(t, err)
			assert.Equal(t, tc.query, values.Get("status"))
		})
	}
	_, err := actionRunQuery(ActionRunOptions{Status: "success"})
	assert.Error(t, err, "conclusions are not run-status filters on GitHub")
}

func TestActionsLogsAreBounded(t *testing.T) {
	for _, size := range []int{0, MaxActionLogBytes, MaxActionLogBytes + 5000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, strings.Repeat("x", size))
			}))
			defer server.Close()
			log, err := NewClient(server.URL, "token").GetActionJobLog(t.Context(), "org", "project", 1)
			require.NoError(t, err)
			assert.Len(t, log.Content, min(size, MaxActionLogBytes))
			assert.Equal(t, size > MaxActionLogBytes, log.Truncated)
			assert.Equal(t, MaxActionLogBytes, log.MaxBytes)
		})
	}
}

func TestActionsLogsFollowSignedHTTPSRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "redirected log\n")
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/signed", http.StatusFound)
	}))
	defer origin.Close()
	log, err := NewClient(origin.URL, "token").GetActionJobLog(t.Context(), "org", "project", 9)
	require.NoError(t, err)
	assert.Equal(t, "redirected log\n", log.Content)
}

func TestGitHubClientRejectsCrossOriginRedirect(t *testing.T) {
	var leaked bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = true
		fmt.Fprint(w, `{"id":1}`)
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/steal", http.StatusFound)
	}))
	defer origin.Close()
	_, err := NewClient(origin.URL, "secret-token").GetCurrentUser(t.Context())
	require.ErrorIs(t, err, ErrUnavailable)
	assert.False(t, leaked, "no request, with or without a token, should cross the origin")
}

func TestGitHubErrorsDoNotExposeUpstreamBodies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"token secret-token is invalid at internal.example"}`)
	}))
	defer server.Close()
	_, err := NewClient(server.URL, "secret-token").GetActionJobLog(t.Context(), "org", "project", 1)
	require.Error(t, err)
	apiErr, ok := errors.AsType[*APIError](err)
	require.True(t, ok)
	assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), "internal.example")
}

func TestGitHubClientHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := NewClient("http://127.0.0.1:1", "token").GetActionRun(ctx, "org", "project", 1)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestGitHubInstanceURLRejectsNonOrigins(t *testing.T) {
	for _, value := range []string{"https://token@git.example", "https://git.example/path", "https://git.example?token=secret", "https://git.example#secret", "file:///tmp", "//git.example", "https://git.example?"} {
		t.Run(value, func(t *testing.T) {
			_, err := NormalizeInstanceURL(value)
			assert.Error(t, err)
		})
	}
	got, err := NormalizeInstanceURL(" https://GIT.example:3000/ ")
	require.NoError(t, err)
	assert.Equal(t, "https://git.example:3000", got)
	for _, value := range []string{"https://git.example:443/", "https://git.example"} {
		got, err := NormalizeInstanceURL(value)
		require.NoError(t, err)
		assert.Equal(t, "https://git.example", got)
	}
	// The hosted instance accepts its bare shorthand.
	for _, value := range []string{"github.com", "GitHub.com", "https://github.com"} {
		got, err := NormalizeInstanceURL(value)
		require.NoError(t, err)
		assert.Equal(t, "https://github.com", got)
	}
}

func TestGitHubHostedInstanceUsesPublicAPIBase(t *testing.T) {
	base, err := NewClient("", "token").apiEndpoint()
	require.NoError(t, err)
	assert.Equal(t, "https://api.github.com", base)
	base, err = NewClient("github.com", "token").apiEndpoint()
	require.NoError(t, err)
	assert.Equal(t, "https://api.github.com", base)
	base, err = NewClient("https://ghe.example", "token").apiEndpoint()
	require.NoError(t, err)
	assert.Equal(t, "https://ghe.example/api/v3", base)
}

func TestGitHubJSONResponsesAreBoundedAndSanitized(t *testing.T) {
	for _, body := range []string{strings.Repeat("x", maxJSONBytes+1), `{"token":"secret-token"} unexpected-data`} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			_, err := NewClient(server.URL, "secret-token").GetCurrentUser(t.Context())
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "secret-token")
		})
	}
}
