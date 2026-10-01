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
			fmt.Fprint(w, `{"sha":"abc","state":"success","total_count":1,"statuses":[{"context":"first","state":"success"}]}`)
		} else {
			fmt.Fprint(w, `{"sha":"abc","state":"failure","total_count":1,"statuses":[{"context":"second","state":"failure"}]}`)
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
		{"success and skipped", `{"statuses":[{"context":"build","state":"success"},{"context":"release","state":"skipped"}]}`, true},
		{"only skipped", `{"statuses":[{"context":"release","state":"skipped"}]}`, false},
		{"pending and success", `{"statuses":[{"context":"build","state":"success"},{"context":"test","state":"pending"}]}`, false},
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
