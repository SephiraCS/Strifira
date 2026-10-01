package aiegress

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPolicyParsesExactOrigins(t *testing.T) {
	policy, err := NewPolicy(" HTTPS://PROVIDER.EXAMPLE:0443, http://localhost:11434, http://[::1]:8080 ")
	require.NoError(t, err)
	assert.Contains(t, policy.trustedOrigins, origin{scheme: "https", host: "provider.example", port: "443"})
	assert.Contains(t, policy.trustedOrigins, origin{scheme: "http", host: "localhost", port: "11434"})
	assert.Contains(t, policy.trustedOrigins, origin{scheme: "http", host: "::1", port: "8080"})
	assert.Len(t, policy.trustedOrigins, 3)
	assert.NotContains(t, policy.trustedOrigins, origin{scheme: "http", host: "provider.example", port: "443"})
	assert.NotContains(t, policy.trustedOrigins, origin{scheme: "http", host: "localhost", port: "80"})
	assert.NotContains(t, policy.trustedOrigins, origin{scheme: "http", host: "other.localhost", port: "11434"})
}

func TestPolicyRejectsAmbiguousOperatorApprovals(t *testing.T) {
	for _, value := range []string{
		"localhost:8080", "ftp://provider.example", "http://*.example", "http://provider.example/", "http://provider.example/v1",
		"http://user:secret@provider.example", "http://provider.example?key=secret", "http://provider.example?",
		"http://provider.example#", "http://provider.example:0", "http://provider.example:65536", "http://provider.example:",
		"http://[fe80::1%25eth0]", "http://provider.example,", ",http://provider.example", "http://prøvider.example",
	} {
		t.Run(value, func(t *testing.T) {
			_, err := NewPolicy(value)
			require.ErrorContains(t, err, "AI_PROVIDER_TRUSTED_ORIGINS")
			assert.NotContains(t, err.Error(), "secret")
		})
	}
	var policy Policy
	require.NoError(t, policy.Decode(""))
	assert.Empty(t, policy.trustedOrigins)
	require.NoError(t, policy.Decode("http://localhost:8080"))
	assert.Len(t, policy.trustedOrigins, 1)
}

func TestPublicAddressBoundary(t *testing.T) {
	for _, value := range []string{"1.1.1.1", "8.8.8.8", "93.184.216.34", "2606:4700:4700::1111", "2001:4860:4860::8888", "::ffff:8.8.8.8"} {
		assert.True(t, publicAddress(netip.MustParseAddr(value)), value)
	}
	for _, value := range []string{
		"0.0.0.0", "0.1.2.3", "10.0.0.1", "100.64.0.1", "100.100.100.200", "127.0.0.1", "168.63.129.16", "169.254.169.254",
		"172.16.0.1", "192.0.0.8", "192.0.2.1", "192.88.99.1", "192.168.1.1", "198.18.0.1", "198.51.100.1",
		"203.0.113.1", "224.0.0.1", "240.0.0.1", "255.255.255.255", "::", "::1", "fc00::1", "fe80::1", "ff02::1",
		"100::1", "64:ff9b::a00:1", "2001::1", "2001:2::1", "2001:db8::1", "2002:7f00:1::1", "3fff::1",
		"::ffff:127.0.0.1", "::ffff:169.254.169.254",
	} {
		assert.False(t, publicAddress(netip.MustParseAddr(value)), value)
	}
	assert.False(t, publicAddress(netip.Addr{}))
}

func TestPrivateProviderRequiresExactOperatorTrust(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		assert.Equal(t, "/v1/chat/completions", request.URL.Path)
		assert.Equal(t, "Bearer fixture-key", request.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"<p>Approved</p>"}}]}`)
	}))
	t.Cleanup(provider.Close)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, provider.URL+"/v1/chat/completions", strings.NewReader(`{"model":"fixture"}`))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer fixture-key")
	client := (Policy{}).NewHTTPClient(time.Second)
	t.Cleanup(client.CloseIdleConnections)
	_, err = client.Do(request)
	require.ErrorIs(t, err, ErrDestinationBlocked)
	assert.Zero(t, calls.Load())
	policy, err := NewPolicy(provider.URL)
	require.NoError(t, err)
	client = policy.NewHTTPClient(time.Second)
	t.Cleanup(client.CloseIdleConnections)
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, int32(1), calls.Load())
}

func TestOperatorTrustedLocalhostProviderWorks(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "local provider")
	}))
	t.Cleanup(provider.Close)
	_, port, err := net.SplitHostPort(provider.Listener.Addr().String())
	require.NoError(t, err)
	providerOrigin := "http://localhost:" + port
	policy, err := NewPolicy(providerOrigin)
	require.NoError(t, err)
	client := policy.NewHTTPClient(time.Second)
	t.Cleanup(client.CloseIdleConnections)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, providerOrigin+"/chat/completions", nil)
	require.NoError(t, err)
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusOK, response.StatusCode)
}

type resolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f resolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return f(ctx, network, host)
}

func providerContext(t *testing.T, value origin) context.Context {
	t.Helper()
	return context.WithValue(t.Context(), requestOriginKey{}, value)
}

func TestDialRejectsEntireMixedOrNonpublicAnswerSet(t *testing.T) {
	for _, addresses := range [][]netip.Addr{
		{netip.MustParseAddr("10.0.0.1")},
		{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("169.254.169.254")},
		{netip.MustParseAddr("2606:4700:4700::1111"), netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.1")},
		{netip.MustParseAddr("::ffff:127.0.0.1"), netip.MustParseAddr("8.8.8.8")},
		{netip.MustParseAddr("fe80::1%eth0")},
		{{}},
	} {
		t.Run(strings.Join(addressStrings(addresses), ","), func(t *testing.T) {
			var calls int
			resolver := resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) { return addresses, nil })
			dial := (Policy{}).checkedDial(resolver, func(context.Context, string, string) (net.Conn, error) {
				calls++
				return nil, errors.New("unexpected dial")
			})
			_, err := dial(providerContext(t, origin{scheme: "https", host: "provider.example", port: "443"}), "tcp", "provider.example:443")
			require.ErrorIs(t, err, ErrDestinationBlocked)
			assert.Zero(t, calls)
		})
	}
}

func addressStrings(addresses []netip.Addr) []string {
	values := make([]string, len(addresses))
	for i, address := range addresses {
		values[i] = address.String()
	}
	return values
}

func TestDialPinsCheckedIPsAndRevalidatesNewConnections(t *testing.T) {
	var resolutions, dials int
	resolver := resolverFunc(func(_ context.Context, network, host string) ([]netip.Addr, error) {
		resolutions++
		assert.Equal(t, "ip", network)
		assert.Equal(t, "provider.example", host)
		if resolutions > 1 {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("1.1.1.1")}, nil
	})
	dial := (Policy{}).checkedDial(resolver, func(_ context.Context, network, address string) (net.Conn, error) {
		dials++
		assert.Equal(t, "tcp", network)
		if dials == 1 {
			assert.Equal(t, "8.8.8.8:443", address)
			return nil, errors.New("first public address unavailable")
		}
		assert.Equal(t, "1.1.1.1:443", address)
		connection, peer := net.Pipe()
		t.Cleanup(func() { _ = peer.Close() })
		return connection, nil
	})
	ctx := providerContext(t, origin{scheme: "https", host: "provider.example", port: "443"})
	connection, err := dial(ctx, "tcp", "provider.example:443")
	require.NoError(t, err)
	require.NoError(t, connection.Close())
	assert.Equal(t, 1, resolutions, "a dial must not resolve again after checking its IPs")
	_, err = dial(ctx, "tcp", "provider.example:443")
	require.ErrorIs(t, err, ErrDestinationBlocked)
	assert.Equal(t, 2, resolutions)
	assert.Equal(t, 2, dials, "a rebound private address must not reach the dialer")
}

type trackedDialConnection struct {
	net.Conn
	closed atomic.Bool
}

func (c *trackedDialConnection) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

func newTrackedDialConnection(t *testing.T) *trackedDialConnection {
	t.Helper()
	connection, peer := net.Pipe()
	tracked := &trackedDialConnection{Conn: connection}
	t.Cleanup(func() {
		_ = tracked.Close()
		_ = peer.Close()
	})
	return tracked
}

func TestDialFallsBackToOtherFamilyWhileFirstAddressStalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		addresses := []netip.Addr{
			netip.MustParseAddr("2606:4700:4700::1111"),
			netip.MustParseAddr("2001:4860:4860::8888"),
			netip.MustParseAddr("8.8.8.8"),
		}
		var resolutions, canceled atomic.Int32
		resolver := resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			resolutions.Add(1)
			return addresses, nil
		})
		reachable := newTrackedDialConnection(t)
		dialed := make(chan string, len(addresses))
		dial := (Policy{}).checkedDial(resolver, func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed <- address
			assert.Equal(t, "tcp", network)
			if address == "8.8.8.8:443" {
				return reachable, nil
			}
			<-ctx.Done()
			canceled.Add(1)
			return nil, ctx.Err()
		})
		start := time.Now()
		connection, err := dial(providerContext(t, origin{scheme: "https", host: "provider.example", port: "443"}), "tcp", "provider.example:443")
		require.NoError(t, err)
		assert.Same(t, reachable, connection)
		assert.Equal(t, dialFallbackDelay, time.Since(start))
		assert.Equal(t, int32(1), resolutions.Load(), "only the validated DNS answer is used")
		assert.Equal(t, int32(1), canceled.Load(), "the stalled loser is canceled and joined before return")
		assert.False(t, reachable.closed.Load(), "the winner remains usable")
		assert.Equal(t, "[2606:4700:4700::1111]:443", <-dialed)
		assert.Equal(t, "8.8.8.8:443", <-dialed)
		assert.Empty(t, dialed, "a grouped second IPv6 answer must not delay IPv4")
	})
}

func TestDialReservesTimeForQueuedAddressesAndLimitsConcurrency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		addresses := []netip.Addr{
			netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("1.1.1.1"),
			netip.MustParseAddr("9.9.9.9"), netip.MustParseAddr("8.8.4.4"),
		}
		resolver := resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) { return addresses, nil })
		var active, maximum, calls, stopped atomic.Int32
		reachable := newTrackedDialConnection(t)
		dial := (Policy{}).checkedDial(resolver, func(ctx context.Context, _, address string) (net.Conn, error) {
			calls.Add(1)
			count := active.Add(1)
			defer active.Add(-1)
			for previous := maximum.Load(); previous < count; previous = maximum.Load() {
				if maximum.CompareAndSwap(previous, count) {
					break
				}
			}
			if address == "9.9.9.9:443" {
				return reachable, nil
			}
			<-ctx.Done()
			stopped.Add(1)
			return nil, ctx.Err()
		})
		ctx, cancel := context.WithTimeout(providerContext(t, origin{scheme: "https", host: "provider.example", port: "443"}), 6*time.Second)
		defer cancel()
		start := time.Now()
		connection, err := dial(ctx, "tcp", "provider.example:443")
		require.NoError(t, err)
		assert.Same(t, reachable, connection)
		assert.Equal(t, 3*time.Second, time.Since(start), "two stalls cannot monopolize the deadline")
		assert.Equal(t, int32(maxParallelDials), maximum.Load())
		assert.Zero(t, active.Load(), "all attempts are joined")
		assert.Equal(t, int32(3), calls.Load())
		assert.Equal(t, int32(2), stopped.Load())
	})
}

func TestDialReturnsAllAddressFailuresWithoutPolicyMisclassification(t *testing.T) {
	addresses := []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("9.9.9.9")}
	resolver := resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) { return addresses, nil })
	failures := map[string]error{
		"8.8.8.8:443": errors.New("first address unavailable"),
		"1.1.1.1:443": errors.New("second address unavailable"),
		"9.9.9.9:443": errors.New("third address unavailable"),
	}
	var calls atomic.Int32
	dial := (Policy{}).checkedDial(resolver, func(_ context.Context, _, address string) (net.Conn, error) {
		calls.Add(1)
		return nil, failures[address]
	})
	connection, err := dial(providerContext(t, origin{scheme: "https", host: "provider.example", port: "443"}), "tcp", "provider.example:443")
	assert.Nil(t, connection)
	require.Error(t, err)
	for _, failure := range failures {
		assert.ErrorIs(t, err, failure)
	}
	assert.NotErrorIs(t, err, ErrDestinationBlocked)
	assert.NotErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, int32(len(addresses)), calls.Load())
}

func TestDialCancellationJoinsBothAttemptsWithoutStartingQueuedAddresses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		addresses := []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("9.9.9.9")}
		resolver := resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) { return addresses, nil })
		ctx, cancel := context.WithCancel(providerContext(t, origin{scheme: "https", host: "provider.example", port: "443"}))
		defer cancel()
		var calls, stopped atomic.Int32
		dial := (Policy{}).checkedDial(resolver, func(ctx context.Context, _, _ string) (net.Conn, error) {
			if calls.Add(1) == maxParallelDials {
				cancel()
			}
			<-ctx.Done()
			stopped.Add(1)
			return nil, ctx.Err()
		})
		connection, err := dial(ctx, "tcp", "provider.example:443")
		assert.Nil(t, connection)
		require.ErrorIs(t, err, context.Canceled)
		assert.NotErrorIs(t, err, ErrDestinationBlocked)
		assert.Equal(t, int32(maxParallelDials), calls.Load())
		assert.Equal(t, calls.Load(), stopped.Load(), "cancellation joins every started attempt")
	})
}

func TestDialAlreadyCanceledDoesNotStartAnyAttempt(t *testing.T) {
	resolver := resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	})
	var calls atomic.Int32
	dial := (Policy{}).checkedDial(resolver, func(context.Context, string, string) (net.Conn, error) {
		calls.Add(1)
		return nil, errors.New("unexpected dial")
	})
	ctx, cancel := context.WithCancel(providerContext(t, origin{scheme: "https", host: "provider.example", port: "443"}))
	cancel()
	connection, err := dial(ctx, "tcp", "provider.example:443")
	assert.Nil(t, connection)
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, calls.Load())
}

func TestDialHonorsRequestAndOverallDeadlines(t *testing.T) {
	for _, requestTimeout := range []time.Duration{100 * time.Millisecond, time.Minute} {
		t.Run(requestTimeout.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				resolver := resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
					return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
				})
				var stopped atomic.Bool
				dial := (Policy{}).checkedDial(resolver, func(ctx context.Context, _, _ string) (net.Conn, error) {
					<-ctx.Done()
					stopped.Store(true)
					return nil, ctx.Err()
				})
				ctx, cancel := context.WithTimeout(providerContext(t, origin{scheme: "https", host: "provider.example", port: "443"}), requestTimeout)
				defer cancel()
				start := time.Now()
				connection, err := dial(ctx, "tcp", "provider.example:443")
				assert.Nil(t, connection)
				require.ErrorIs(t, err, context.DeadlineExceeded)
				assert.NotErrorIs(t, err, ErrDestinationBlocked)
				assert.Equal(t, min(requestTimeout, checkedDialTimeout), time.Since(start))
				assert.True(t, stopped.Load(), "the timed-out attempt is joined")
			})
		})
	}
}

func TestDialBoundsStalledDNSWithRequestAndOverallDeadline(t *testing.T) {
	for _, requestTimeout := range []time.Duration{100 * time.Millisecond, time.Minute} {
		t.Run(requestTimeout.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var stopped atomic.Bool
				resolver := resolverFunc(func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
					<-ctx.Done()
					stopped.Store(true)
					return nil, ctx.Err()
				})
				var calls atomic.Int32
				dial := (Policy{}).checkedDial(resolver, func(context.Context, string, string) (net.Conn, error) {
					calls.Add(1)
					return nil, errors.New("unexpected dial")
				})
				ctx, cancel := context.WithTimeout(providerContext(t, origin{scheme: "https", host: "provider.example", port: "443"}), requestTimeout)
				defer cancel()
				start := time.Now()
				connection, err := dial(ctx, "tcp", "provider.example:443")
				assert.Nil(t, connection)
				require.ErrorIs(t, err, context.DeadlineExceeded)
				assert.Equal(t, min(requestTimeout, checkedDialTimeout), time.Since(start))
				assert.True(t, stopped.Load())
				assert.Zero(t, calls.Load(), "a timed-out DNS lookup must never start a dial")
			})
		})
	}
}

func TestDialDNSAndFallbackShareOneAbsoluteDeadline(t *testing.T) {
	for _, requestTimeout := range []time.Duration{2 * time.Second, time.Minute} {
		t.Run(requestTimeout.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				budget := min(requestTimeout, checkedDialTimeout)
				dnsDelay := budget - time.Second
				var lookupDeadline time.Time
				resolver := resolverFunc(func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
					var ok bool
					lookupDeadline, ok = ctx.Deadline()
					assert.True(t, ok)
					time.Sleep(dnsDelay)
					return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("1.1.1.1")}, nil
				})
				var calls, stopped atomic.Int32
				dial := (Policy{}).checkedDial(resolver, func(ctx context.Context, _, _ string) (net.Conn, error) {
					calls.Add(1)
					deadline, ok := ctx.Deadline()
					assert.True(t, ok)
					assert.Equal(t, lookupDeadline, deadline, "DNS time must reduce the remaining dial budget")
					<-ctx.Done()
					stopped.Add(1)
					return nil, ctx.Err()
				})
				ctx, cancel := context.WithTimeout(providerContext(t, origin{scheme: "https", host: "provider.example", port: "443"}), requestTimeout)
				defer cancel()
				start := time.Now()
				connection, err := dial(ctx, "tcp", "provider.example:443")
				assert.Nil(t, connection)
				require.ErrorIs(t, err, context.DeadlineExceeded)
				assert.Equal(t, budget, time.Since(start), "fallback cannot reset the overall deadline after DNS")
				assert.Equal(t, int32(maxParallelDials), calls.Load())
				assert.Equal(t, calls.Load(), stopped.Load())
			})
		})
	}
}

func TestHTTPTransportPreservesRequestBudgetDuringStalledDNS(t *testing.T) {
	for _, test := range []struct {
		name           string
		clientTimeout  time.Duration
		requestTimeout time.Duration
		cancelAfter    time.Duration
		elapsed        time.Duration
	}{
		{name: "client deadline", clientTimeout: 100 * time.Millisecond, elapsed: 100 * time.Millisecond},
		{name: "request deadline", clientTimeout: time.Minute, requestTimeout: 100 * time.Millisecond, elapsed: 100 * time.Millisecond},
		{name: "request cancellation", clientTimeout: time.Minute, cancelAfter: 100 * time.Millisecond, elapsed: 100 * time.Millisecond},
		{name: "overall connection deadline", clientTimeout: time.Minute, elapsed: checkedDialTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var stopped atomic.Bool
				resolver := resolverFunc(func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
					<-ctx.Done()
					stopped.Store(true)
					return nil, ctx.Err()
				})
				var calls atomic.Int32
				client := (Policy{}).newHTTPClient(test.clientTimeout, resolver, func(context.Context, string, string) (net.Conn, error) {
					calls.Add(1)
					return nil, errors.New("unexpected dial")
				})
				t.Cleanup(client.CloseIdleConnections)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if test.requestTimeout > 0 {
					var stop context.CancelFunc
					ctx, stop = context.WithTimeout(ctx, test.requestTimeout)
					defer stop()
				}
				if test.cancelAfter > 0 {
					timer := time.AfterFunc(test.cancelAfter, cancel)
					defer timer.Stop()
				}
				request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://provider.example/chat/completions", nil)
				require.NoError(t, err)
				start := time.Now()
				response, err := client.Do(request)
				assert.Nil(t, response)
				if test.cancelAfter > 0 {
					require.ErrorIs(t, err, context.Canceled)
				} else {
					require.ErrorIs(t, err, context.DeadlineExceeded)
				}
				synctest.Wait()
				assert.Equal(t, test.elapsed, time.Since(start))
				assert.True(t, stopped.Load(), "the underlying transport must stop DNS when the real request ends")
				assert.Zero(t, calls.Load())
			})
		})
	}
}

func TestDialClosesSimultaneousSuccessfulLoser(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		addresses := []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("1.1.1.1")}
		resolver := resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) { return addresses, nil })
		first, second := newTrackedDialConnection(t), newTrackedDialConnection(t)
		barrier := make(chan struct{})
		var calls atomic.Int32
		dial := (Policy{}).checkedDial(resolver, func(_ context.Context, _, address string) (net.Conn, error) {
			if calls.Add(1) == maxParallelDials {
				close(barrier)
			}
			<-barrier
			if address == "8.8.8.8:443" {
				return first, nil
			}
			return second, nil
		})
		connection, err := dial(providerContext(t, origin{scheme: "https", host: "provider.example", port: "443"}), "tcp", "provider.example:443")
		require.NoError(t, err)
		assert.Equal(t, int32(2), calls.Load())
		if connection == first {
			assert.False(t, first.closed.Load())
			assert.True(t, second.closed.Load(), "a successful loser must be closed before return")
		} else {
			assert.Same(t, second, connection)
			assert.False(t, second.closed.Load())
			assert.True(t, first.closed.Load(), "a successful loser must be closed before return")
		}
	})
}

func TestDialCancellationClosesRacingSuccessfulConnections(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		addresses := []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("1.1.1.1")}
		resolver := resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) { return addresses, nil })
		first, second := newTrackedDialConnection(t), newTrackedDialConnection(t)
		ctx, cancel := context.WithCancel(providerContext(t, origin{scheme: "https", host: "provider.example", port: "443"}))
		defer cancel()
		barrier := make(chan struct{})
		var calls atomic.Int32
		dial := (Policy{}).checkedDial(resolver, func(_ context.Context, _, address string) (net.Conn, error) {
			if calls.Add(1) == maxParallelDials {
				cancel()
				close(barrier)
			}
			<-barrier
			if address == "8.8.8.8:443" {
				return first, nil
			}
			return second, nil
		})
		connection, err := dial(ctx, "tcp", "provider.example:443")
		assert.Nil(t, connection)
		require.ErrorIs(t, err, context.Canceled)
		assert.True(t, first.closed.Load(), "cancellation must close a connection that raced its context")
		assert.True(t, second.closed.Load(), "cancellation must close every successful loser")
	})
}

func TestDialClosesConnectionsAccompanyingFailures(t *testing.T) {
	resolver := resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	})
	failedConnection := newTrackedDialConnection(t)
	failure := errors.New("connection failed")
	dial := (Policy{}).checkedDial(resolver, func(context.Context, string, string) (net.Conn, error) {
		return failedConnection, failure
	})
	connection, err := dial(providerContext(t, origin{scheme: "https", host: "provider.example", port: "443"}), "tcp", "provider.example:443")
	assert.Nil(t, connection)
	require.ErrorIs(t, err, failure)
	assert.True(t, failedConnection.closed.Load())
}

func TestTrustCannotExtendToAnotherSchemeHostOrPort(t *testing.T) {
	policy, err := NewPolicy("https://provider.example:443")
	require.NoError(t, err)
	resolver := resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.0.0.2")}, nil
	})
	for _, candidate := range []origin{
		{scheme: "http", host: "provider.example", port: "443"},
		{scheme: "https", host: "provider.example", port: "8443"},
		{scheme: "https", host: "other.example", port: "443"},
	} {
		var dials int
		dial := policy.checkedDial(resolver, func(context.Context, string, string) (net.Conn, error) {
			dials++
			return nil, errors.New("unexpected dial")
		})
		_, err := dial(providerContext(t, candidate), "tcp", net.JoinHostPort(candidate.host, candidate.port))
		require.ErrorIs(t, err, ErrDestinationBlocked)
		assert.Zero(t, dials)
	}
}

func TestClientIgnoresEnvironmentProxyAndUsesCheckedPublicIP(t *testing.T) {
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { proxyCalls.Add(1) }))
	t.Cleanup(proxy.Close)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "provider.example", request.Host)
		_, _ = io.WriteString(w, "public-provider-response")
	}))
	t.Cleanup(provider.Close)
	var resolutions atomic.Int32
	resolver := resolverFunc(func(_ context.Context, _, host string) ([]netip.Addr, error) {
		resolutions.Add(1)
		assert.Equal(t, "provider.example", host)
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	})
	dialer := &net.Dialer{}
	client := (Policy{}).newHTTPClient(time.Second, resolver, func(ctx context.Context, network, address string) (net.Conn, error) {
		assert.Equal(t, "8.8.8.8:80", address)
		return dialer.DialContext(ctx, network, provider.Listener.Addr().String())
	})
	t.Cleanup(client.CloseIdleConnections)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://provider.example/chat/completions", nil)
	require.NoError(t, err)
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, "public-provider-response", string(body))
	assert.Equal(t, int32(1), resolutions.Load())
	assert.Zero(t, proxyCalls.Load())
}

func TestTrustedProvidersStillCannotRedirect(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	t.Cleanup(target.Close)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		http.Redirect(w, request, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(provider.Close)
	policy, err := NewPolicy(provider.URL + "," + target.URL)
	require.NoError(t, err)
	client := policy.NewHTTPClient(time.Second)
	t.Cleanup(client.CloseIdleConnections)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, provider.URL+"/chat/completions", nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer fixture-key")
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusTemporaryRedirect, response.StatusCode)
	assert.Zero(t, targetCalls.Load())
}

func TestDialFailsClosedWithoutMatchingRequestOrigin(t *testing.T) {
	dial := (Policy{}).checkedDial(nil, func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("unbound request reached dialer")
		return nil, nil
	})
	for _, ctx := range []context.Context{t.Context(), providerContext(t, origin{scheme: "https", host: "provider.example", port: "443"})} {
		_, err := dial(ctx, "tcp", "127.0.0.1:80")
		require.ErrorIs(t, err, ErrDestinationBlocked)
	}
}
