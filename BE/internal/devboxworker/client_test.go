package devboxworker

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClientTokenNeverFollowsRedirectOrAppearsInPublishedContent(t *testing.T) {
	token := "kub_" + strings.Repeat("a", 64)
	var leaked bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
		if strings.HasSuffix(r.URL.Path, "/heartbeat") {
			http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
			return
		}
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NotContains(t, string(body), token)
		require.Contains(t, string(body), "[REDACTED]")
		_, _ = w.Write([]byte(`{"accepted":true,"logs_truncated":false}`))
	}))
	defer server.Close()
	client := NewClient(Config{ServerURL: server.URL}, token)
	require.ErrorContains(t, client.Heartbeat(t.Context(), Heartbeat{}), "redirects")
	require.False(t, leaked)
	_, err := client.Log(t.Context(), "job", LogEntry{Content: "accidental token " + token})
	require.NoError(t, err)
}

func TestClientIdleClaimAndLeaseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/claim") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()
	client := NewClient(Config{ServerURL: server.URL}, "token")
	lease, err := client.Claim(t.Context())
	require.NoError(t, err)
	require.Nil(t, lease)
	_, err = client.Renew(t.Context(), "job", "lease")
	require.EqualError(t, err, "worker API returned HTTP 409")
}
