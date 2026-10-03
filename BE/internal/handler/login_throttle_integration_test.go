package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogin_ThrottlesRepeatedFailuresPerEmail(t *testing.T) {
	env := newIntegrationEnv(t)
	victim := env.seedOwner("victim")
	bystander := env.seedOwner("bystander")
	login := func(email, password string) int {
		return env.do(http.MethodPost, "/api/auth/login", "", map[string]string{"email": email, "password": password}).Code
	}

	for range integrationLoginMaxFailures {
		require.Equal(t, http.StatusUnauthorized, login(victim.Email, "Wrong-Pass-1"))
	}
	// Locked: even the right password (and a differently-cased email) is refused.
	rec := env.do(http.MethodPost, "/api/auth/login", "", map[string]string{"email": victim.Email, "password": victim.Password})
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
	assert.Equal(t, http.StatusTooManyRequests, login(strings.ToUpper(victim.Email), victim.Password))

	// Throttling is per email, not per client: another account from the
	// same address still signs in.
	assert.Equal(t, http.StatusOK, login(bystander.Email, bystander.Password))

	// A success clears earlier failures for that email.
	for range integrationLoginMaxFailures - 1 {
		require.Equal(t, http.StatusUnauthorized, login(bystander.Email, "Wrong-Pass-1"))
	}
	require.Equal(t, http.StatusOK, login(bystander.Email, bystander.Password))
	require.Equal(t, http.StatusUnauthorized, login(bystander.Email, "Wrong-Pass-1"))
	assert.Equal(t, http.StatusOK, login(bystander.Email, bystander.Password))
}
