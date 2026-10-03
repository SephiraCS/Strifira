package handler

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
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
	// Locked: even the right password is refused.
	rec := env.do(http.MethodPost, "/api/auth/login", "", map[string]string{"email": victim.Email, "password": victim.Password})
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))

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

// Authentication matches emails exactly, so a case variant is a different
// account: it has its own counter and its successful logins must not reset
// the lockout of the original account.
func TestLogin_ThrottleKeyMatchesAccountIdentity(t *testing.T) {
	env := newIntegrationEnv(t)
	victim := env.seedOwner("victim")
	variantEmail := strings.ToUpper(victim.Email)
	variantPassword := "Variant-Pass-123"
	hash, err := bcrypt.GenerateFromPassword([]byte(variantPassword), bcrypt.MinCost)
	require.NoError(t, err)
	_, err = env.db.Exec(`INSERT INTO users (id,email,name,password_hash) VALUES ($1,$2,'variant',$3)`, uuid.New(), variantEmail, string(hash))
	require.NoError(t, err)
	login := func(email, password string) int {
		return env.do(http.MethodPost, "/api/auth/login", "", map[string]string{"email": email, "password": password}).Code
	}

	for range integrationLoginMaxFailures - 1 {
		require.Equal(t, http.StatusUnauthorized, login(victim.Email, "Wrong-Pass-1"))
		// Logging into the case variant must not clear the victim's failures.
		require.Equal(t, http.StatusOK, login(variantEmail, variantPassword))
	}
	require.Equal(t, http.StatusUnauthorized, login(victim.Email, "Wrong-Pass-1"))
	assert.Equal(t, http.StatusTooManyRequests, login(victim.Email, victim.Password), "the victim must be locked")
	assert.Equal(t, http.StatusOK, login(variantEmail, variantPassword), "the case variant is a separate account and is not locked")
}

// Concurrent guesses must not slip past the lockout: at most maxFailures
// passwords are ever checked, however many requests race.
func TestLogin_ConcurrentAttemptsCannotExceedLimit(t *testing.T) {
	env := newIntegrationEnv(t)
	victim := env.seedOwner("victim")

	const parallel = 40
	codes := make([]int, parallel)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range parallel {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes[i] = env.do(http.MethodPost, "/api/auth/login", "", map[string]string{"email": victim.Email, "password": "Wrong-Pass-1"}).Code
		}()
	}
	close(start)
	wg.Wait()

	checked, rejected := 0, 0
	for _, code := range codes {
		switch code {
		case http.StatusUnauthorized:
			checked++
		case http.StatusTooManyRequests:
			rejected++
		default:
			t.Fatalf("unexpected status %d", code)
		}
	}
	assert.Equal(t, integrationLoginMaxFailures, checked, "exactly the limit of passwords may be checked")
	assert.Equal(t, parallel-integrationLoginMaxFailures, rejected)
	// The admitted guesses all failed, so the account is now locked.
	assert.Equal(t, http.StatusTooManyRequests, env.do(http.MethodPost, "/api/auth/login", "",
		map[string]string{"email": victim.Email, "password": victim.Password}).Code)
}
