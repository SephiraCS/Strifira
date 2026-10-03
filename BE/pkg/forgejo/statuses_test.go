package forgejo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCombinedStatusReadsAllPagesBeforePassingChecks(t *testing.T) {
	var pages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.Query().Get("page"))
		w.Header().Set("X-Total-Count", "2")
		if r.URL.Query().Get("page") == "1" {
			fmt.Fprint(w, `{"sha":"abc","state":"success","total_count":1,"statuses":[{"context":"first","status":"success"}]}`)
		} else {
			fmt.Fprint(w, `{"sha":"abc","state":"failure","total_count":1,"statuses":[{"context":"second","status":"failure"}]}`)
		}
	}))
	defer server.Close()
	status, err := NewClient(server.URL, "token").GetCombinedStatus(t.Context(), "org", "project", "abc")
	require.NoError(t, err)
	assert.Equal(t, []string{"1", "2"}, pages)
	assert.Equal(t, 2, status.TotalCount)
	assert.Equal(t, "failure", status.State)
	assert.False(t, status.AllChecksPassed())
}

func TestCombinedStatusRequiresSuccessAndTreatsSkippedAsNeutral(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		pass       bool
	}{
		{"success and skipped", `{"statuses":[{"context":"build","status":"success"},{"context":"release","status":"skipped"}]}`, true},
		{"only skipped", `{"statuses":[{"context":"release","status":"skipped"}]}`, false},
		{"pending and success", `{"statuses":[{"context":"build","status":"success"},{"context":"test","status":"pending"}]}`, false},
		{"none", `{"statuses":[]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, tc.body) }))
			defer server.Close()
			status, err := NewClient(server.URL, "token").GetCombinedStatus(t.Context(), "org", "project", "abc")
			require.NoError(t, err)
			assert.Equal(t, tc.pass, status.AllChecksPassed())
		})
	}
}
