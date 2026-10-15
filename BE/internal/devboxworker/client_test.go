package devboxworker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/stretchr/testify/require"
)

func TestClientTokenNeverFollowsRedirectOrAppearsInPublishedContent(t *testing.T) {
	token := "strifira_" + strings.Repeat("a", 64)
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

func TestCompletionRequestAndRedirectErrorsArePermanent(t *testing.T) {
	var requests, redirects atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirects.Add(1) }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	for _, scenario := range []string{"encoding", "request construction", "unsupported protocol", "redirect"} {
		t.Run(scenario, func(t *testing.T) {
			client := NewClient(Config{ServerURL: server.URL}, "worker-registration")
			var err error
			switch scenario {
			case "encoding":
				_, err = client.call(t.Context(), "/complete", func() {}, nil, 0)
			case "request construction":
				client.baseURL = "http://[invalid"
				err = (&Worker{API: client}).completeResult(t.Context(), t.Context(), Lease{}, Result{})
			case "unsupported protocol":
				client.baseURL = "ftp://example.test"
				err = (&Worker{API: client}).completeResult(t.Context(), t.Context(), Lease{}, Result{})
			case "redirect":
				err = (&Worker{API: client}).completeResult(t.Context(), t.Context(), Lease{}, Result{})
			}
			require.Error(t, err)
			require.False(t, retryableCompletion(err))
		})
	}
	require.Equal(t, int32(1), requests.Load(), "only the single rejected redirect should reach HTTP")
	require.Zero(t, redirects.Load(), "worker credentials must never follow redirects")
}

func TestCompletionClientTimeoutLeavesBudgetForAnotherRequest(t *testing.T) {
	var attempts atomic.Int32
	firstCanceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if attempts.Add(1) == 1 {
			<-r.Context().Done()
			close(firstCanceled)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := NewClient(Config{ServerURL: server.URL}, "worker-registration")
	client.http.Timeout = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, (&Worker{API: client}).completeResult(ctx, t.Context(), Lease{}, Result{}))
	require.NoError(t, ctx.Err(), "the client timeout must leave the delivery context usable")
	select {
	case <-firstCanceled:
	case <-ctx.Done():
		t.Fatal("the first HTTP request was not canceled by the client timeout")
	}
	// A busy runner can also time out a successful response; a third attempt
	// still proves recovery within the same delivery budget and retry limit.
	require.GreaterOrEqual(t, attempts.Load(), int32(2))
	require.LessOrEqual(t, attempts.Load(), int32(3))
}

func TestCompletionInvalidPortIsPermanent(t *testing.T) {
	var attempts atomic.Int32
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		attempts.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	defer transport.CloseIdleConnections()
	client := NewClient(Config{ServerURL: "https://127.0.0.1:65536"}, "worker-registration")
	client.http.Transport = transport
	err := (&Worker{API: client}).completeResult(t.Context(), t.Context(), Lease{}, Result{})
	require.ErrorContains(t, err, "invalid port")
	require.False(t, retryableCompletion(err))
	require.Equal(t, int32(1), attempts.Load())
}

func TestCompletionNetworkFailuresDistinguishPermanentAndTransient(t *testing.T) {
	for _, test := range []struct {
		name  string
		err   error
		retry bool
	}{
		{name: "invalid address", err: net.InvalidAddrError("invalid address")},
		{name: "invalid address pointer", err: new(net.InvalidAddrError("invalid address"))},
		{name: "address error", err: &net.AddrError{Err: "missing port", Addr: "service.test"}},
		{name: "DNS NXDOMAIN", err: &net.DNSError{Err: "no such host", Name: "service.test", IsNotFound: true}},
		{name: "permanent DNS", err: &net.DNSError{Err: "DNS configuration rejected", Name: "service.test"}},
		{name: "transient DNS", err: &net.DNSError{Err: "temporary DNS failure", Name: "service.test", IsTemporary: true}, retry: true},
		{name: "DNS timeout", err: &net.DNSError{Err: "DNS timeout", Name: "service.test", IsTimeout: true}, retry: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			var attempts atomic.Int32
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.Proxy = nil
			transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				if attempts.Add(1) == 1 {
					return nil, &net.OpError{Op: "dial", Net: network, Err: test.err}
				}
				return (&net.Dialer{}).DialContext(ctx, network, address)
			}
			defer transport.CloseIdleConnections()
			client := NewClient(Config{ServerURL: server.URL}, "worker-registration")
			client.http.Transport = transport
			err := (&Worker{API: client}).completeResult(t.Context(), t.Context(), Lease{}, Result{})
			if test.retry {
				require.NoError(t, err)
				require.Equal(t, int32(2), attempts.Load())
			} else {
				require.ErrorIs(t, err, test.err)
				require.False(t, retryableCompletion(err))
				require.Equal(t, int32(1), attempts.Load())
			}
		})
	}
}

func TestCompletionUntrustedTLSCertificateIsPermanent(t *testing.T) {
	var requests, attempts atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		attempts.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	defer transport.CloseIdleConnections()
	client := NewClient(Config{ServerURL: server.URL}, "worker-registration")
	client.http.Transport = transport
	err := (&Worker{API: client}).completeResult(t.Context(), t.Context(), Lease{}, Result{})
	require.ErrorContains(t, err, "certificate")
	require.False(t, retryableCompletion(err))
	require.Equal(t, int32(1), attempts.Load())
	require.Zero(t, requests.Load(), "worker request must not reach the untrusted endpoint")
}

func TestCompletionPreservesMaximumEscapedResultAndIdenticalRetry(t *testing.T) {
	result := Result{Status: "succeeded", ExitCode: 0, Summary: strings.Repeat("<", 8192), Branch: "strifira/" + strings.Repeat("b", 252),
		Patch: strings.Repeat("<", maxPatchBytes), TestStatus: "passed", TestExitCode: new(0)}
	for range 500 {
		result.ChangedFiles = append(result.ChangedFiles, strings.Repeat("<", 4096))
	}
	for range 100 {
		result.TestsRun = append(result.TestsRun, strings.Repeat("<", 4096))
		result.RiskNotes = append(result.RiskNotes, strings.Repeat("<", 4096))
	}
	type observation struct {
		length int
		hash   [32]byte
		result domain.DevBoxJobResult
	}
	bodies := make(chan observation, 3)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, dto.DevBoxCompleteBodyLimit))
		require.NoError(t, err)
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
		// Capture the whole upload, then acknowledge it before expensive payload
		// verification can trigger an unrelated transport timeout on a busy runner.
		require.NoError(t, http.NewResponseController(w).Flush())
		var request dto.DevBoxCompleteRequest
		require.NoError(t, json.Unmarshal(body, &request))
		bodies <- observation{length: len(body), hash: sha256.Sum256(body), result: request.Result}
	}))
	defer server.Close()
	client := NewClient(Config{ServerURL: server.URL}, "strifira_"+strings.Repeat("a", 64))
	require.NoError(t, (&Worker{API: client}).completeResult(t.Context(), t.Context(), Lease{Token: "kl_" + strings.Repeat("b", 64)}, result))
	require.Equal(t, int32(2), requests.Load())
	first, replay := <-bodies, <-bodies
	require.Greater(t, first.length, 2<<20, "the old body cap rejected a valid escaped review artifact")
	require.LessOrEqual(t, first.length, dto.DevBoxCompleteBodyLimit)
	require.Equal(t, first.hash, replay.hash, "retry must preserve the full encoded result")
	for _, received := range []observation{first, replay} {
		require.True(t, reflect.DeepEqual(domain.DevBoxJobResult(result), received.result), "complete patch and metadata must arrive unchanged")
	}
}

func completionRequestLength(t *testing.T, lease string, result Result) int {
	t.Helper()
	body, err := json.Marshal(dto.DevBoxCompleteRequest{LeaseToken: lease, Result: domain.DevBoxJobResult(result)})
	require.NoError(t, err)
	return len(body)
}

func TestCompletionEncodedBodyLimitUsesFinalRedactedBytes(t *testing.T) {
	for _, scenario := range []string{"exact limit", "limit plus one", "redaction brings body to exact limit"} {
		t.Run(scenario, func(t *testing.T) {
			var requests atomic.Int32
			var received atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				n, err := io.Copy(io.Discard, r.Body)
				require.NoError(t, err)
				received.Store(n)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			token, lease := "strifira_"+strings.Repeat("a", 64), "kl_"+strings.Repeat("b", 64)
			result := Result{Status: "failed", ExitCode: -1, TestStatus: "not_run"}
			base := completionRequestLength(t, lease, result)
			switch scenario {
			case "exact limit":
				result.Patch = strings.Repeat("x", dto.DevBoxCompleteBodyLimit-base)
			case "limit plus one":
				result.Patch = strings.Repeat("x", dto.DevBoxCompleteBodyLimit-base+1)
			case "redaction brings body to exact limit":
				result.Patch = token + strings.Repeat("x", dto.DevBoxCompleteBodyLimit-base-len("[REDACTED]"))
				require.Greater(t, completionRequestLength(t, lease, result), dto.DevBoxCompleteBodyLimit)
			}
			original := result.Patch
			client := NewClient(Config{ServerURL: server.URL}, token)
			err := (&Worker{API: client}).completeResult(t.Context(), t.Context(), Lease{Token: lease}, result)
			if scenario == "limit plus one" {
				require.ErrorContains(t, err, fmt.Sprintf("%d-byte encoded body limit", dto.DevBoxCompleteBodyLimit))
				require.False(t, retryableCompletion(err))
				require.Zero(t, requests.Load(), "oversized results must fail before any HTTP request or retry")
			} else {
				require.NoError(t, err)
				require.Equal(t, int32(1), requests.Load())
				require.Equal(t, int64(dto.DevBoxCompleteBodyLimit), received.Load())
			}
			require.True(t, original == result.Patch, "size checks must never truncate or mutate the review artifact")
		})
	}
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
