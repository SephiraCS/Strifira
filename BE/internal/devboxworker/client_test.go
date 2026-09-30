package devboxworker

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
				_, err = client.call(t.Context(), "/complete", func() {}, nil)
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if attempts.Add(1) == 1 {
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := NewClient(Config{ServerURL: server.URL}, "worker-registration")
	client.http.Timeout = 30 * time.Millisecond
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	require.NoError(t, (&Worker{API: client}).completeResult(ctx, t.Context(), Lease{}, Result{}))
	require.Equal(t, int32(2), attempts.Load())
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
