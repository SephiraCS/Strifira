package forgejo

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestClient(handler http.HandlerFunc) (*Client, *httptest.Server) {
	server := httptest.NewServer(handler)
	return NewClient(server.URL, "test-token-0123456789abcdef"), server
}

func TestGetCurrentUserSendsTokenHeader(t *testing.T) {
	client, server := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "token test-token-0123456789abcdef", r.Header.Get("Authorization"))
		assert.Equal(t, "/api/v1/user", r.URL.Path)
		_, _ = w.Write([]byte(`{"id":3,"login":"terrascale-bot","avatar_url":"https://x/a.png"}`))
	})
	defer server.Close()

	user, err := client.GetCurrentUser()
	require.NoError(t, err)
	assert.Equal(t, "terrascale-bot", user.Login)
}

func TestListReposPaginates(t *testing.T) {
	client, server := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/user/repos", r.URL.Path)
		page := r.URL.Query().Get("page")
		var batch []Repository
		if page == "1" {
			batch = make([]Repository, 50)
		} else if page == "2" {
			batch = []Repository{{ID: 51, FullName: "TerraScale/ku"}}
		} else {
			t.Fatalf("unexpected page %s", page)
		}
		_ = json.NewEncoder(w).Encode(batch)
	})
	defer server.Close()

	repos, err := client.ListRepos()
	require.NoError(t, err)
	require.Len(t, repos, 51)
	assert.Equal(t, "TerraScale/ku", repos[50].FullName)
}

func TestCreateWebhookPayloadShape(t *testing.T) {
	client, server := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/repos/TerraScale/ku/hooks", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)

		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "forgejo", body["type"])
		assert.Equal(t, true, body["active"])
		assert.ElementsMatch(t, []any{"push", "pull_request", "create", "status"}, body["events"])
		config := body["config"].(map[string]any)
		assert.Equal(t, "https://ku.example.com/api/forgejo/webhook", config["url"])
		assert.Equal(t, "json", config["content_type"])
		assert.Equal(t, "hunter2", config["secret"])

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":99,"type":"forgejo"}`))
	})
	defer server.Close()

	hook, err := client.CreateWebhook("TerraScale", "ku", "https://ku.example.com/api/forgejo/webhook", "hunter2", nil)
	require.NoError(t, err)
	assert.Equal(t, int64(99), hook.ID)
}

func TestClientSurfacesAPIErrors(t *testing.T) {
	client, server := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"token invalid"}`)
	})
	defer server.Close()

	_, err := client.GetCurrentUser()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

func TestCombinedStatusAllChecksPassed(t *testing.T) {
	var c CombinedStatus
	require.NoError(t, json.Unmarshal([]byte(`{"state":"success","sha":"abc","total_count":2,"statuses":[{"state":"success"},{"state":"success"}]}`), &c))
	assert.True(t, c.AllChecksPassed())

	c = CombinedStatus{}
	require.NoError(t, json.Unmarshal([]byte(`{"state":"pending","sha":"abc","total_count":1,"statuses":[{"state":"pending"}]}`), &c))
	assert.False(t, c.AllChecksPassed())

	c = CombinedStatus{}
	require.NoError(t, json.Unmarshal([]byte(`{"state":"success","sha":"abc","total_count":0,"statuses":[]}`), &c))
	assert.False(t, c.AllChecksPassed())
}
