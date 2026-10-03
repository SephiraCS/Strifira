package handler

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForgejoWebhook_RejectsOversizedBody(t *testing.T) {
	env := newIntegrationEnv(t)
	oversized := bytes.Repeat([]byte("a"), MaxForgejoWebhookBodyBytes+1)
	rec := env.do(http.MethodPost, "/api/forgejo/webhook", "", oversized)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	// A body within the limit still reaches signature verification.
	rec = env.do(http.MethodPost, "/api/forgejo/webhook", "", `{"action":"opened"}`)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestGlobalBodyLimit(t *testing.T) {
	env := newIntegrationEnv(t)
	const mib = 1 << 20

	// Payloads the API legitimately accepts (24 MiB dev box completions,
	// 10 MiB attachments) pass the global limit.
	rec := env.do(http.MethodPost, "/api/test/echo-body", "", bytes.Repeat([]byte("x"), 25*mib))
	require.Equal(t, http.StatusOK, rec.Code)

	rec = env.do(http.MethodPost, "/api/test/echo-body", "", bytes.Repeat([]byte("x"), 33*mib))
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	// Workspace imports enforce their own (1 GiB) limit and are exempt.
	rec = env.do(http.MethodPost, "/api/workspaces/import", "", bytes.Repeat([]byte("x"), 40*mib))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// chunkedRequest sends body with an unknown length (Transfer-Encoding:
// chunked) through a real listener.
func chunkedRequest(t *testing.T, server http.Handler, method, path, token string, body []byte) (int, string) {
	t.Helper()
	srv := httptest.NewServer(server)
	t.Cleanup(srv.Close)
	req, err := http.NewRequest(method, srv.URL+path, io.MultiReader(bytes.NewReader(body)))
	require.NoError(t, err)
	req.ContentLength = -1 // unknown length: sent with Transfer-Encoding: chunked
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// An oversized body of unknown length is a 413 like a declared one, even
// on handlers that answer bind errors with 400.
func TestGlobalBodyLimit_ChunkedOversizedJSON(t *testing.T) {
	env := newIntegrationEnv(t)
	const mib = 1 << 20
	oversized := []byte(`{"email":"a@example.test","password":"` + strings.Repeat("x", 33*mib) + `"}`)
	status, body := chunkedRequest(t, env.server, http.MethodPost, "/api/auth/login", "", oversized)
	assert.Equal(t, http.StatusRequestEntityTooLarge, status, body)

	// Chunked bodies within the limit still reach the handler intact.
	status, body = chunkedRequest(t, env.server, http.MethodPost, "/api/test/echo-body", "", bytes.Repeat([]byte("x"), mib))
	assert.Equal(t, http.StatusOK, status, body)
	assert.Contains(t, body, `"bytes":1048576`)
	user := env.seedOwner("chunked")
	status, body = chunkedRequest(t, env.server, http.MethodPost, "/api/auth/login", "",
		[]byte(`{"email":"`+user.Email+`","password":"`+user.Password+`"}`))
	assert.Equal(t, http.StatusOK, status, body)
}
