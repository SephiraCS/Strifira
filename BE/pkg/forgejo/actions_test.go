package forgejo

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

func TestActionsClientForgejoContract(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "token test-token", r.Header.Get("Authorization"))
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/v1/repos/org/project/actions/runs":
			assert.Equal(t, "2", r.URL.Query().Get("page"))
			assert.Equal(t, "10", r.URL.Query().Get("limit"))
			assert.Equal(t, "failure", r.URL.Query().Get("status"))
			assert.Equal(t, "refs/heads/feature/ci&fix", r.URL.Query().Get("ref"))
			assert.Equal(t, "push", r.URL.Query().Get("event"))
			assert.Equal(t, "abcd", r.URL.Query().Get("head_sha"))
			assert.Equal(t, "ci.yaml", r.URL.Query().Get("workflow_id"))
			assert.Equal(t, "7", r.URL.Query().Get("run_number"))
			fmt.Fprint(w, `{"workflow_runs":[{"id":44,"index_in_repo":7,"workflow_id":"ci.yaml","prettyref":"main","commit_sha":"abcd","status":"failure","created":"2026-09-30T12:00:00Z"}],"total_count":22}`)
		case "/api/v1/repos/org/project/actions/runs/44":
			fmt.Fprint(w, `{"id":44,"status":"running"}`)
		case "/api/v1/repos/org/project/actions/runs/44/jobs":
			fmt.Fprint(w, `[{"id":52,"run_id":44,"attempt":3,"name":"Build","status":"failure"}]`)
		case "/api/v1/repos/org/project/actions/jobs/52/logs":
			assert.Equal(t, "text/plain", r.Header.Get("Accept"))
			assert.Equal(t, "2", r.URL.Query().Get("attempt"))
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "build failed\n")
		case "/api/v1/repos/org/project/actions/runs/44/cancel":
			assert.Equal(t, http.MethodPost, r.Method)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewClient(server.URL, "test-token")
	list, err := client.ListActionRuns(t.Context(), "org", "project", ActionRunOptions{Page: 2, PerPage: 10, Status: "failure", Ref: "refs/heads/feature/ci&fix", Event: "push", HeadSHA: "abcd", WorkflowID: "ci.yaml", RunNumber: 7})
	require.NoError(t, err)
	assert.EqualValues(t, 22, list.TotalCount)
	require.Len(t, list.Runs, 1)
	assert.EqualValues(t, 7, list.Runs[0].Index)
	assert.Equal(t, "main", list.Runs[0].PrettyRef)
	run, err := client.GetActionRun(t.Context(), "org", "project", 44)
	require.NoError(t, err)
	assert.Equal(t, "running", run.Status)
	jobs, err := client.ListActionJobs(t.Context(), "org", "project", 44)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.EqualValues(t, 3, jobs[0].Attempt)
	log, err := client.GetActionJobLog(t.Context(), "org", "project", 52, 2)
	require.NoError(t, err)
	assert.Equal(t, "build failed\n", log.Content)
	assert.False(t, log.Truncated)
	require.NoError(t, client.CancelActionRun(t.Context(), "org", "project", 44))
	assert.Equal(t, []string{
		"GET /api/v1/repos/org/project/actions/runs", "GET /api/v1/repos/org/project/actions/runs/44",
		"GET /api/v1/repos/org/project/actions/runs/44/jobs", "GET /api/v1/repos/org/project/actions/jobs/52/logs",
		"POST /api/v1/repos/org/project/actions/runs/44/cancel",
	}, requests)
}

func TestActionsLogsAreBounded(t *testing.T) {
	for _, size := range []int{0, MaxActionLogBytes, MaxActionLogBytes + 5000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Empty(t, r.URL.Query().Get("attempt"))
				fmt.Fprint(w, strings.Repeat("x", size))
			}))
			defer server.Close()
			log, err := NewClient(server.URL, "token").GetActionJobLog(t.Context(), "org", "project", 1, 0)
			require.NoError(t, err)
			assert.Len(t, log.Content, min(size, MaxActionLogBytes))
			assert.Equal(t, size > MaxActionLogBytes, log.Truncated)
			assert.Equal(t, MaxActionLogBytes, log.MaxBytes)
		})
	}
}

func TestForgejoClientRejectsCrossOriginRedirect(t *testing.T) {
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

func TestForgejoErrorsDoNotExposeUpstreamBodies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"token secret-token is invalid at internal.example"}`)
	}))
	defer server.Close()
	_, err := NewClient(server.URL, "secret-token").GetActionJobLog(t.Context(), "org", "project", 1, 0)
	require.Error(t, err)
	apiErr, ok := errors.AsType[*APIError](err)
	require.True(t, ok)
	assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), "internal.example")
}

func TestForgejoClientHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := NewClient("http://127.0.0.1:1", "token").GetActionRun(ctx, "org", "project", 1)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestForgejoInstanceURLRejectsNonOrigins(t *testing.T) {
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
}

func TestForgejoJSONResponsesAreBoundedAndSanitized(t *testing.T) {
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
