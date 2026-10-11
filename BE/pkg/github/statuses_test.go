package github

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCombinedStatusParsesGitHubAggregate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v3/repos/org/project/commits/abc/status", r.URL.Path)
		fmt.Fprint(w, `{"sha":"abc","state":"failure","total_count":2,"statuses":[{"state":"success","context":"build"},{"state":"failure","context":"release"}]}`)
	}))
	defer server.Close()
	status, err := NewClient(server.URL, "token").GetCombinedStatus(t.Context(), "org", "project", "abc")
	require.NoError(t, err)
	assert.Equal(t, 2, status.TotalCount)
	assert.Equal(t, "failure", status.State)
	require.Len(t, status.Statuses, 2)
	assert.Equal(t, "build", status.Statuses[0].Context)
}

func TestCheckRunsParseConclusionAndStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v3/repos/org/project/commits/abc/check-runs", r.URL.Path)
		assert.Equal(t, "application/vnd.github+json", r.Header.Get("Accept"))
		fmt.Fprint(w, `{"total_count":2,"check_runs":[{"name":"build","status":"completed","conclusion":"success"},{"name":"test","status":"in_progress","conclusion":null}]}`)
	}))
	defer server.Close()
	checks, err := NewClient(server.URL, "token").GetCheckRunsForRef(t.Context(), "org", "project", "abc")
	require.NoError(t, err)
	assert.Equal(t, 2, checks.TotalCount)
	require.Len(t, checks.CheckRuns, 2)
	assert.Equal(t, "success", checks.CheckRuns[0].Conclusion)
	assert.Empty(t, checks.CheckRuns[1].Conclusion)
}
