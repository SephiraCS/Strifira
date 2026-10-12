package github

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"
)

// Clients are short-lived (one per request), so conditional-request state and
// rate-limit windows live in one process-wide cache keyed by connection (API
// base + token) and request path. A 304 answer reuses the cached body and,
// like the conditional request itself, does not count against GitHub's rate
// limit.
const (
	maxCondEntries    = 256
	maxCondCacheBytes = 16 << 20
	maxCondEntryBytes = 1 << 20
	// One retry inside the caller's request only for a short server-asked
	// backoff; longer windows surface RateLimitError and the next tick,
	// webhook or user action retries after the budget resets.
	maxRateLimitRetry  = 3 * time.Second
	maxRateLimitWindow = time.Hour
	fallbackRateWindow = time.Minute
	// condFreshWindow is how long a stored GET answer is served without any
	// round trip, not even a conditional one. Webhook bursts and multiple
	// viewers polling the same resource collapse into the stored response;
	// older entries revalidate with If-None-Match (304s are free).
	condFreshWindow = 2 * time.Second
)

// ErrRateLimited reports an exhausted or briefly throttled API budget. Callers
// surface it instead of hammering GitHub until the reset time passes.
var ErrRateLimited = errors.New("GitHub API rate limit exhausted")

// RateLimitError carries when the budget is expected to return.
type RateLimitError struct {
	RetryAt time.Time
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("GitHub API rate limit exhausted until %s", e.RetryAt.Format(time.RFC3339))
}

func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

type condEntry struct {
	etag         string
	lastModified string
	body         []byte
	storedAt     time.Time
}

// fresh reports whether the entry is still inside the window where it is
// served without revalidation.
func (e condEntry) fresh() bool {
	return time.Since(e.storedAt) < condFreshWindow
}

// inflightCall is one shared round trip for identical concurrent GETs: the
// leader performs the request, waiters reuse its outcome. retryable marks a
// transport-level failure that waiters must not inherit — each retries on
// its own instead.
type inflightCall struct {
	done      chan struct{}
	status    int
	header    http.Header
	body      []byte
	err       error
	retryable bool
}

// response materializes the shared outcome as a fresh response every waiter
// can read independently.
func (call *inflightCall) response(request *http.Request) *http.Response {
	return &http.Response{
		Status:     http.StatusText(call.status),
		StatusCode: call.status,
		Header:     call.header.Clone(),
		Body:       io.NopCloser(bytes.NewReader(call.body)),
		Request:    request,
	}
}

type requestCache struct {
	mu      sync.Mutex
	entries map[string]condEntry
	order   []string
	bytes   int

	blocked map[string]time.Time

	inflight map[string]*inflightCall
}

var sharedCache = &requestCache{
	entries: map[string]condEntry{},
	blocked: map[string]time.Time{},
}

// connectionKey identifies one instance + token pair without retaining the
// token itself.
func connectionKey(apiBase, token string) string {
	digest := sha256.Sum256([]byte(apiBase + "\x00" + token))
	return hex.EncodeToString(digest[:])
}

func (rc *requestCache) lookup(conn, path string) (condEntry, bool) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	entry, ok := rc.entries[conn+"\x00"+path]
	return entry, ok
}

// store keeps only validator-bearing JSON bodies within the memory budget;
// larger bodies are still correct, they simply stop being conditionally
// requestable. When full, the oldest entries are evicted first.
func (rc *requestCache) store(conn, path string, header http.Header, body []byte) {
	etag, lastModified := header.Get("ETag"), header.Get("Last-Modified")
	if (etag == "" && lastModified == "") || len(body) > maxCondEntryBytes {
		return
	}
	key := conn + "\x00" + path
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if previous, exists := rc.entries[key]; exists {
		rc.bytes -= len(previous.body)
		delete(rc.entries, key)
		rc.order = slices.DeleteFunc(rc.order, func(stored string) bool { return stored == key })
	}
	for (len(rc.order) >= maxCondEntries || rc.bytes+len(body) > maxCondCacheBytes) && len(rc.order) > 0 {
		evict := rc.order[0]
		rc.order = rc.order[1:]
		if entry, ok := rc.entries[evict]; ok {
			rc.bytes -= len(entry.body)
			delete(rc.entries, evict)
		}
	}
	rc.entries[key] = condEntry{etag: etag, lastModified: lastModified, body: body, storedAt: time.Now()}
	rc.order = append(rc.order, key)
	rc.bytes += len(body)
}

// beginFlight registers call as the in-flight round trip for conn+path and
// reports leadership: a non-nil return is the already-running call to wait on.
func (rc *requestCache) beginFlight(conn, path string, call *inflightCall) *inflightCall {
	key := conn + "\x00" + path
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.inflight == nil {
		rc.inflight = map[string]*inflightCall{}
	}
	if existing, ok := rc.inflight[key]; ok {
		return existing
	}
	rc.inflight[key] = call
	return nil
}

// finishFlight withdraws the call (later requests lead a fresh round trip)
// and publishes the outcome to every waiter. The leader must fill the call's
// result fields before calling this.
func (rc *requestCache) finishFlight(conn, path string, call *inflightCall) {
	key := conn + "\x00" + path
	rc.mu.Lock()
	if rc.inflight != nil && rc.inflight[key] == call {
		delete(rc.inflight, key)
	}
	rc.mu.Unlock()
	close(call.done)
}

// refreshValidators updates an entry's validators when a 304 response carries
// a rotated ETag, keeping the cache usable for the next conditional request.
func (rc *requestCache) refreshValidators(conn, path string, header http.Header) {
	key := conn + "\x00" + path
	etag, lastModified := header.Get("ETag"), header.Get("Last-Modified")
	if etag == "" && lastModified == "" {
		return
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if entry, ok := rc.entries[key]; ok {
		if etag != "" {
			entry.etag = etag
		}
		if lastModified != "" {
			entry.lastModified = lastModified
		}
		rc.entries[key] = entry
	}
}

func (rc *requestCache) blockedUntil(conn string) (time.Time, bool) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	until, ok := rc.blocked[conn]
	if !ok || !time.Now().Before(until) {
		delete(rc.blocked, conn)
		return time.Time{}, false
	}
	return until, true
}

func (rc *requestCache) block(conn string, until time.Time) {
	now := time.Now()
	if until.After(now.Add(maxRateLimitWindow)) {
		until = now.Add(maxRateLimitWindow)
	}
	if !until.After(now) {
		return
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.blocked[conn] = until
}

func (rc *requestCache) unblock(conn string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	delete(rc.blocked, conn)
}

// applyConditional sets If-None-Match / If-Modified-Since for a cached GET.
func applyConditional(request *http.Request, entry condEntry) {
	if entry.etag != "" {
		request.Header.Set("If-None-Match", entry.etag)
	} else if entry.lastModified != "" {
		request.Header.Set("If-Modified-Since", entry.lastModified)
	}
}

// rateLimitState extracts GitHub's budget headers. remaining < 0 means the
// headers were absent.
func rateLimitState(header http.Header) (remaining int64, reset time.Time) {
	remaining = -1
	if value, err := strconv.ParseInt(header.Get("X-RateLimit-Remaining"), 10, 64); err == nil {
		remaining = value
	}
	if value, err := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64); err == nil && value > 0 {
		reset = time.Unix(value, 0)
	}
	return remaining, reset
}

// rateLimitExhausted reports whether a 403/429 response is throttling rather
// than a permission denial, and when the budget returns.
func rateLimitExhausted(response *http.Response) (time.Time, bool) {
	if response.StatusCode != http.StatusTooManyRequests && response.StatusCode != http.StatusForbidden {
		return time.Time{}, false
	}
	remaining, reset := rateLimitState(response.Header)
	throttled := response.StatusCode == http.StatusTooManyRequests || (remaining == 0 && (response.Header.Get("X-RateLimit-Remaining") != ""))
	if !throttled {
		return time.Time{}, false
	}
	if retryAfter, err := strconv.ParseInt(response.Header.Get("Retry-After"), 10, 64); err == nil && retryAfter >= 0 {
		return time.Now().Add(time.Duration(retryAfter) * time.Second), true
	}
	if reset.After(time.Now()) {
		return reset, true
	}
	return time.Now().Add(fallbackRateWindow), true
}

// observeRateLimit records the budget a response reports: block the
// connection while the budget is empty, clear a stale block when it refilled.
func (rc *requestCache) observeRateLimit(conn string, header http.Header) {
	remaining, reset := rateLimitState(header)
	if remaining < 0 {
		return
	}
	if remaining == 0 {
		until := reset
		if !until.After(time.Now()) {
			until = time.Now().Add(fallbackRateWindow)
		}
		rc.block(conn, until)
		return
	}
	rc.unblock(conn)
}

// sleepUntil waits for the rate-limit window, staying cancellable.
func sleepUntil(ctx context.Context, until time.Time) error {
	if delay := time.Until(until); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}
