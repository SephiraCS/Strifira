package handler

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	mw "github.com/ku/ku-backend/internal/middleware"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitHubWebhook_RejectsOversizedBody(t *testing.T) {
	env := newIntegrationEnv(t)
	oversized := bytes.Repeat([]byte("a"), MaxGitHubWebhookBodyBytes+1)
	rec := env.do(http.MethodPost, "/api/github/webhook", "", oversized)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	// A body within the limit still reaches signature verification.
	rec = env.do(http.MethodPost, "/api/github/webhook", "", `{"action":"opened"}`)
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

// A client that starts a body and then withholds the rest cannot hold the
// request open: the body read deadline fails the read and the request ends.
// The archive upload routes are exempt from the size cap but not from the
// deadline; they get their own, longer one.
func TestBodyLimit_WithheldChunkedBodyTimesOut(t *testing.T) {
	const regular, upload = 300 * time.Millisecond, 700 * time.Millisecond
	e := echo.New()
	e.Use(mw.BodyLimitWith(1<<20, regular, upload))
	drain := func(c echo.Context) error {
		if _, err := io.ReadAll(c.Request().Body); err != nil {
			return c.NoContent(http.StatusBadRequest)
		}
		return c.NoContent(http.StatusOK)
	}
	e.POST("/api/test/echo-body", drain)
	e.POST("/api/workspaces/import", drain)
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)

	withhold := func(path string) time.Duration {
		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		require.NoError(t, err)
		defer conn.Close()
		_, err = io.WriteString(conn, "POST "+path+" HTTP/1.1\r\nHost: test\r\nTransfer-Encoding: chunked\r\n"+
			"Content-Type: application/json\r\n\r\n5\r\n{\"a\":\r\n")
		require.NoError(t, err)
		started := time.Now()
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		require.NoError(t, err, "%s: the server must answer instead of waiting for the withheld chunks", path)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, path)
		return time.Since(started)
	}

	elapsed := withhold("/api/test/echo-body")
	assert.Less(t, elapsed, 5*time.Second)
	elapsed = withhold("/api/workspaces/import")
	assert.GreaterOrEqual(t, elapsed, upload-100*time.Millisecond, "uploads get the longer deadline")
	assert.Less(t, elapsed, 5*time.Second, "uploads are still cut off")

	// The upload route stays exempt from the size cap.
	resp, err := http.Post(srv.URL+"/api/workspaces/import", "application/octet-stream", bytes.NewReader(make([]byte, 2<<20)))
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
