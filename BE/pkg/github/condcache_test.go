package github

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConditionalGETReusesCachedBodyOn304(t *testing.T) {
	var requests atomic.Int32
	var expectedValidator atomic.Value
	expectedValidator.Store(`"v1"`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if match := r.Header.Get("If-None-Match"); match != "" {
			assert.Equal(t, expectedValidator.Load(), match)
			// A rotated validator must replace the cached one.
			expectedValidator.Store(`"v2"`)
			w.Header().Set("ETag", `"v2"`)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		fmt.Fprint(w, `{"id":7,"login":"octo","name":"Octo Cat"}`)
	}))
	defer server.Close()

	first, err := NewClient(server.URL, "etag-token").GetCurrentUser(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "octo", first.Login)

	// A fresh client instance for the same connection reuses the cache.
	second, err := NewClient(server.URL, "etag-token").GetCurrentUser(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "octo", second.Login)
	assert.EqualValues(t, 2, requests.Load(), "the second GET must be conditional and answered from cache on 304")

	// The rotated ETag from the 304 serves the next conditional request.
	third, err := NewClient(server.URL, "etag-token").GetCurrentUser(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "octo", third.Login)
	assert.EqualValues(t, 3, requests.Load())
}

func TestConditionalCacheIsScopedPerConnection(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		assert.Empty(t, r.Header.Get("If-None-Match"), "a different connection must not reuse another token's cache entry")
		w.Header().Set("ETag", `"v1"`)
		fmt.Fprint(w, `{"id":1,"login":"octo"}`)
	}))
	defer server.Close()

	for _, token := range []string{"token-a", "token-b"} {
		user, err := NewClient(server.URL, token).GetCurrentUser(t.Context())
		require.NoError(t, err)
		assert.Equal(t, "octo", user.Login)
	}
	assert.EqualValues(t, 2, requests.Load())
}

func TestMutationResponsesAreNotCached(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Empty(t, r.Header.Get("If-None-Match"))
		w.Header().Set("ETag", `"post"`)
		fmt.Fprint(w, `{"id":5}`)
	}))
	defer server.Close()

	for range 2 {
		hook, err := NewClient(server.URL, "post-token").CreateWebhook(t.Context(), "org", "project", "https://example.test/hook", "secret", []string{"push"})
		require.NoError(t, err)
		assert.EqualValues(t, 5, hook.ID)
	}
	assert.EqualValues(t, 2, requests.Load(), "POSTs are never served from cache")
}

func TestRateLimitedGETWaitsOutShortBackoff(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"id":7,"login":"octo"}`)
	}))
	defer server.Close()

	start := time.Now()
	user, err := NewClient(server.URL, "retry-token").GetCurrentUser(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "octo", user.Login)
	assert.EqualValues(t, 2, requests.Load(), "an idempotent GET retries once after the server-asked backoff")
	assert.GreaterOrEqual(t, time.Since(start), time.Second)
}

func TestExhaustedBudgetBlocksFurtherCalls(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") == "Bearer other-token" {
			fmt.Fprint(w, `{"id":7,"login":"octo"}`)
			return
		}
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", time.Now().Add(time.Hour).Unix()))
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	_, err := NewClient(server.URL, "blocked-token").GetCurrentUser(t.Context())
	require.ErrorIs(t, err, ErrRateLimited)
	var rateLimited *RateLimitError
	require.ErrorAs(t, err, &rateLimited)
	assert.True(t, rateLimited.RetryAt.After(time.Now()))

	// Every later call for this connection short-circuits without network.
	_, err = NewClient(server.URL, "blocked-token").ListRepos(t.Context())
	require.ErrorIs(t, err, ErrRateLimited)
	_, err = NewClient(server.URL, "blocked-token").GetActionRun(t.Context(), "org", "project", 1)
	require.ErrorIs(t, err, ErrRateLimited)
	assert.EqualValues(t, 1, requests.Load())

	// A different connection on the same instance keeps working.
	user, err := NewClient(server.URL, "other-token").GetCurrentUser(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "octo", user.Login)
	assert.EqualValues(t, 2, requests.Load())
}

func TestEmptyBudgetOnSuccessBlocksUntilReset(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", time.Now().Add(time.Hour).Unix()))
		fmt.Fprint(w, `{"id":1,"login":"octo"}`)
	}))
	defer server.Close()

	user, err := NewClient(server.URL, "drained-token").GetCurrentUser(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "octo", user.Login)

	_, err = NewClient(server.URL, "drained-token").GetCurrentUser(t.Context())
	require.ErrorIs(t, err, ErrRateLimited)
	assert.EqualValues(t, 1, requests.Load(), "a reported empty budget gates the next call")
}

func TestPermissionForbiddenIsNotRateLimiting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"Resource not accessible by integration"}`)
	}))
	defer server.Close()

	_, err := NewClient(server.URL, "denied-token").GetCurrentUser(t.Context())
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrRateLimited)
	upstream, ok := errors.AsType[*APIError](err)
	require.True(t, ok)
	assert.Equal(t, http.StatusForbidden, upstream.StatusCode)
}

func TestRefilledBudgetClearsBlock(t *testing.T) {
	conn := connectionKey("https://api.github.com", "refill-token")
	sharedCache.block(conn, time.Now().Add(time.Hour))
	until, blocked := sharedCache.blockedUntil(conn)
	require.True(t, blocked)
	assert.True(t, until.After(time.Now()))

	header := http.Header{}
	header.Set("X-RateLimit-Remaining", "5000")
	sharedCache.observeRateLimit(conn, header)
	_, blocked = sharedCache.blockedUntil(conn)
	assert.False(t, blocked, "a response reporting a refilled budget lifts the block")
}

func TestCacheEvictsWithinBudget(t *testing.T) {
	conn := connectionKey("https://git.example/api/v3", "evict-token")
	bigBody := make([]byte, maxCondEntryBytes+1)
	sharedCache.store(conn, "/too-big", http.Header{"ETag": {`"big"`}}, bigBody)
	_, cached := sharedCache.lookup(conn, "/too-big")
	assert.False(t, cached, "oversized bodies are not cached")

	header := http.Header{}
	header.Set("ETag", `"a"`)
	for i := 0; i <= maxCondEntries; i++ {
		sharedCache.store(conn, fmt.Sprintf("/page-%d", i), header, []byte(`{}`))
	}
	_, cached = sharedCache.lookup(conn, "/page-0")
	assert.False(t, cached, "the oldest entry is evicted once the entry cap is reached")
	_, cached = sharedCache.lookup(conn, fmt.Sprintf("/page-%d", maxCondEntries))
	assert.True(t, cached)
}
