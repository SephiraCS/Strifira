package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
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
	conn := connectionKey(server.URL+"/api/v3", "etag-token")

	first, err := NewClient(server.URL, "etag-token").GetCurrentUser(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "octo", first.Login)

	// Within the freshness window the GET is answered from the cache with no
	// round trip at all.
	second, err := NewClient(server.URL, "etag-token").GetCurrentUser(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "octo", second.Login)
	assert.EqualValues(t, 1, requests.Load(), "a fresh entry must be served without revalidation")

	// Past the window, a fresh client instance for the same connection sends a
	// conditional GET and reuses the cached body on 304.
	expireFreshness(t, conn, "/user")
	third, err := NewClient(server.URL, "etag-token").GetCurrentUser(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "octo", third.Login)
	assert.EqualValues(t, 2, requests.Load(), "the revalidated GET must be conditional and answered from cache on 304")

	// The rotated ETag from the 304 serves the next conditional request.
	expireFreshness(t, conn, "/user")
	fourth, err := NewClient(server.URL, "etag-token").GetCurrentUser(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "octo", fourth.Login)
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

// expireFreshness backdates a stored entry beyond the freshness window so the
// next GET revalidates instead of being served without a round trip.
func expireFreshness(t *testing.T, conn, path string) {
	t.Helper()
	sharedCache.mu.Lock()
	defer sharedCache.mu.Unlock()
	key := conn + "\x00" + path
	if entry, ok := sharedCache.entries[key]; ok {
		entry.storedAt = time.Now().Add(-condFreshWindow - time.Second)
		sharedCache.entries[key] = entry
	}
}

func TestFreshEntryServesRepeatedCallsWithoutRequests(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("ETag", `"v1"`)
		fmt.Fprint(w, `{"id":7,"login":"octo"}`)
	}))
	defer server.Close()
	client := NewClient(server.URL, "fresh-token")

	for i := 0; i < 5; i++ {
		user, err := client.GetCurrentUser(t.Context())
		require.NoError(t, err)
		assert.Equal(t, "octo", user.Login)
	}
	assert.EqualValues(t, 1, requests.Load(), "calls within the freshness window make no HTTP request at all")
}

func TestConcurrentIdenticalGETsShareOneRoundTrip(t *testing.T) {
	var requests atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		// Hold the leader's response open so every concurrent caller joins
		// the in-flight call instead of issuing its own request.
		<-release
		w.Header().Set("ETag", `"v1"`)
		fmt.Fprint(w, `{"id":7,"login":"octo"}`)
	}))
	defer server.Close()
	client := NewClient(server.URL, "shared-token")

	const callers = 20
	results := make(chan error, callers)
	var started sync.WaitGroup
	started.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer started.Done()
			_, err := client.GetCurrentUser(context.Background())
			results <- err
		}()
	}
	time.Sleep(300 * time.Millisecond)
	close(release)
	started.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	assert.EqualValues(t, 1, requests.Load(), "concurrent identical GETs must collapse into one upstream request")
}

func TestWaiterRetriesOwnAttemptAfterLeaderTransportFailure(t *testing.T) {
	var requests atomic.Int32
	arrived := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			close(arrived)
			// Abort the connection: the leader sees a transport error.
			panic(http.ErrAbortHandler)
		}
		w.Header().Set("ETag", `"v1"`)
		fmt.Fprint(w, `{"id":7,"login":"octo"}`)
	}))
	defer server.Close()
	client := NewClient(server.URL, "fallback-token")

	leader := make(chan error, 1)
	go func() {
		_, err := client.GetCurrentUser(context.Background())
		leader <- err
	}()
	<-arrived
	waiter := make(chan error, 1)
	go func() {
		_, err := client.GetCurrentUser(context.Background())
		waiter <- err
	}()
	// Give the waiter time to block on the leader's flight before the leader
	// aborts, so the fallback path is exercised deterministically.
	time.Sleep(200 * time.Millisecond)

	err := <-leader
	require.ErrorIs(t, err, ErrUnavailable, "the leader reports the transport failure")

	// The waiter must not inherit the failure; it leads its own attempt.
	user, err := client.GetCurrentUser(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "octo", user.Login)
	require.NoError(t, <-waiter, "a waiter falls through to its own attempt instead of sharing the error")
	assert.EqualValues(t, 2, requests.Load())
}

func TestWaiterReturnsContextErrorWhenCanceled(t *testing.T) {
	var requests atomic.Int32
	arrived := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		close(arrived)
		<-release
		w.Header().Set("ETag", `"v1"`)
		fmt.Fprint(w, `{"id":7,"login":"octo"}`)
	}))
	defer server.Close()
	client := NewClient(server.URL, "cancel-token")

	leader := make(chan error, 1)
	go func() {
		_, err := client.GetCurrentUser(context.Background())
		leader <- err
	}()
	<-arrived

	ctx, cancel := context.WithCancel(context.Background())
	waiter := make(chan error, 1)
	go func() {
		_, err := client.GetCurrentUser(ctx)
		waiter <- err
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-waiter:
		require.ErrorIs(t, err, context.Canceled, "a canceled waiter must return its context error")
	case <-time.After(5 * time.Second):
		t.Fatal("the canceled waiter did not return")
	}
	close(release)
	require.NoError(t, <-leader)
	assert.EqualValues(t, 1, requests.Load())
}
