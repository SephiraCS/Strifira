package handler

import (
	"bytes"
	"net/http"
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
