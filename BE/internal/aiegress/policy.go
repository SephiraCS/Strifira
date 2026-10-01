// Package aiegress enforces the operator's network boundary for workspace AI providers.
package aiegress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrDestinationBlocked = errors.New("AI provider destination blocked by server network policy")

// Policy permits public addresses and the exact origins explicitly trusted by
// the server operator. Its zero value permits public addresses only.
type Policy struct {
	trustedOrigins map[origin]struct{}
}

type origin struct {
	scheme string
	host   string
	port   string
}

// NewPolicy parses a comma-separated list of origins, without paths or credentials.
func NewPolicy(raw string) (Policy, error) {
	var policy Policy
	if strings.TrimSpace(raw) == "" {
		return policy, nil
	}
	policy.trustedOrigins = make(map[origin]struct{})
	for i, entry := range strings.Split(raw, ",") {
		parsed, err := parseURL(strings.TrimSpace(entry))
		if err != nil || parsed.Path != "" {
			return Policy{}, fmt.Errorf("AI_PROVIDER_TRUSTED_ORIGINS entry %d must be an http or https origin without credentials, path, query, or fragment", i+1)
		}
		trusted, err := urlOrigin(parsed)
		if err != nil {
			return Policy{}, fmt.Errorf("AI_PROVIDER_TRUSTED_ORIGINS entry %d has an invalid host or port", i+1)
		}
		policy.trustedOrigins[trusted] = struct{}{}
	}
	return policy, nil
}

// Decode makes invalid operator configuration fail during envconfig startup.
func (p *Policy) Decode(raw string) error {
	policy, err := NewPolicy(raw)
	if err != nil {
		return err
	}
	*p = policy
	return nil
}

// ValidateBaseURL checks URL syntax without resolving or contacting the provider.
func ValidateBaseURL(raw string) error {
	parsed, err := parseURL(raw)
	if err != nil {
		return err
	}
	_, err = urlOrigin(parsed)
	return err
}

func parseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" || parsed.Opaque != "" {
		return nil, errors.New("base URL must be a valid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("base URL must be an http or https URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(raw, "#") {
		return nil, errors.New("base URL must not contain credentials, query parameters, or a fragment")
	}
	return parsed, nil
}

func urlOrigin(parsed *url.URL) (origin, error) {
	host, err := normalizedHost(parsed.Hostname())
	if err != nil {
		return origin{}, err
	}
	port := parsed.Port()
	if port == "" {
		if strings.HasSuffix(parsed.Host, ":") {
			return origin{}, errors.New("base URL must have a valid port")
		}
		port = "443"
		if parsed.Scheme == "http" {
			port = "80"
		}
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return origin{}, errors.New("base URL must have a valid port")
	}
	return origin{scheme: parsed.Scheme, host: host, port: strconv.Itoa(number)}, nil
}

func normalizedHost(host string) (string, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" {
			return "", errors.New("base URL must not use a scoped IP address")
		}
		return ip.Unmap().String(), nil
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" || len(host) > 253 {
		return "", errors.New("base URL must have a valid hostname")
	}
	for label := range strings.SplitSeq(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("base URL must have a valid hostname")
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return "", errors.New("base URL must have an ASCII hostname or IP address")
			}
		}
	}
	return host, nil
}

type ipResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type dialFunc func(context.Context, string, string) (net.Conn, error)

const (
	checkedDialTimeout = 30 * time.Second
	dialFallbackDelay  = 300 * time.Millisecond
	maxParallelDials   = 2
)

type requestOriginKey struct{}
type requestContextKey struct{}

type guardedTransport struct {
	transport *http.Transport
}

// NewHTTPClient disables implicit proxies and redirects. DNS is checked at each
// new connection, and only IP literals from that checked answer set are dialed.
func (p Policy) NewHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: checkedDialTimeout, KeepAlive: 30 * time.Second}
	return p.newHTTPClient(timeout, net.DefaultResolver, dialer.DialContext)
}

func (p Policy) newHTTPClient(timeout time.Duration, resolver ipResolver, dial dialFunc) *http.Client {
	transport := &http.Transport{
		// Proxy deliberately remains nil: environment proxies could otherwise
		// resolve and reach destinations outside this policy.
		DialContext:           p.checkedDial(resolver, dial),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{
		Timeout:       timeout,
		Transport:     &guardedTransport{transport: transport},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (t *guardedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	parsed, err := parseURL(request.URL.String())
	if err != nil {
		return nil, ErrDestinationBlocked
	}
	providerOrigin, err := urlOrigin(parsed)
	if err != nil {
		return nil, ErrDestinationBlocked
	}
	ctx := context.WithValue(request.Context(), requestOriginKey{}, providerOrigin)
	// http.Transport detaches connection attempts from request cancellation,
	// but retains context values. Preserve the request's original budget here.
	ctx = context.WithValue(ctx, requestContextKey{}, request.Context())
	return t.transport.RoundTrip(request.Clone(ctx))
}

func (t *guardedTransport) CloseIdleConnections() { t.transport.CloseIdleConnections() }

func (p Policy) checkedDial(resolver ipResolver, dial dialFunc) dialFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		providerOrigin, ok := ctx.Value(requestOriginKey{}).(origin)
		host, port, err := net.SplitHostPort(address)
		if err != nil || !ok {
			return nil, ErrDestinationBlocked
		}
		host, err = normalizedHost(host)
		portNumber, portErr := strconv.Atoi(port)
		if err != nil || portErr != nil || providerOrigin.host != host || providerOrigin.port != strconv.Itoa(portNumber) {
			return nil, ErrDestinationBlocked
		}
		deadline := time.Now().Add(checkedDialTimeout)
		requestCtx, _ := ctx.Value(requestContextKey{}).(context.Context)
		if requestCtx != nil {
			if requestDeadline, ok := requestCtx.Deadline(); ok && requestDeadline.Before(deadline) {
				deadline = requestDeadline
			}
		}
		ctx, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		if requestCtx != nil {
			stop := context.AfterFunc(requestCtx, cancel)
			defer stop()
		}
		contextError := func() error {
			if requestCtx != nil && requestCtx.Err() != nil {
				return requestCtx.Err()
			}
			return ctx.Err()
		}
		if err := contextError(); err != nil {
			return nil, err
		}
		addresses := []netip.Addr{}
		if literal, err := netip.ParseAddr(host); err == nil {
			addresses = append(addresses, literal)
		} else {
			addresses, err = resolver.LookupNetIP(ctx, "ip", host)
			if contextErr := contextError(); contextErr != nil {
				return nil, contextErr
			}
			if err != nil {
				return nil, err
			}
		}
		if len(addresses) == 0 {
			return nil, errors.New("AI provider hostname returned no addresses")
		}
		_, trusted := p.trustedOrigins[providerOrigin]
		for _, ip := range addresses {
			if !ip.IsValid() || ip.Zone() != "" || (!trusted && !publicAddress(ip)) {
				return nil, ErrDestinationBlocked
			}
		}
		connection, err := dialCheckedAddresses(ctx, network, port, addresses, dial)
		if contextErr := contextError(); contextErr != nil {
			if connection != nil {
				_ = connection.Close()
			}
			return nil, contextErr
		}
		return connection, err
	}
}

type dialResult struct {
	index      int
	connection net.Conn
	err        error
}

// dialCheckedAddresses restores address fallback without letting the underlying
// dialer resolve the hostname again. The full answer set must be validated before
// calling it. Like net.Dialer.DialContext, dial must honor context cancellation so
// all losing attempts can be joined before returning their winner.
func dialCheckedAddresses(ctx context.Context, network, port string, addresses []netip.Addr, dial dialFunc) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// checkedDial already started this absolute deadline before resolving DNS.
	// A child cancellation scope stops losers without granting a fresh budget.
	ctx, cancel := context.WithCancel(ctx)
	addresses = interleaveAddressFamilies(addresses)
	results := make(chan dialResult, maxParallelDials)
	var attempts sync.WaitGroup
	defer func() {
		cancel()
		attempts.Wait()
		// At most maxParallelDials results can remain queued. Joining before
		// draining also closes successful losers that raced cancellation.
		for {
			select {
			case result := <-results:
				if result.connection != nil {
					_ = result.connection.Close()
				}
			default:
				return
			}
		}
	}()

	var active, next int
	failures := make([]error, len(addresses))
	startAttempt := func() {
		if ctx.Err() != nil {
			return
		}
		index := next
		next++
		active++
		// Divide the remaining deadline across the queued waves of attempts,
		// so stalled addresses cannot occupy both slots until the request ends.
		deadline, _ := ctx.Deadline()
		waves := (len(addresses) - index + maxParallelDials - 1) / maxParallelDials
		attemptCtx, stop := context.WithTimeout(ctx, time.Until(deadline)/time.Duration(waves))
		attempts.Go(func() {
			defer stop()
			// Passing only a checked literal prevents a second DNS lookup,
			// including DNS rebinding between validation and connection.
			connection, err := dial(attemptCtx, network, net.JoinHostPort(addresses[index].Unmap().String(), port))
			if err != nil && connection != nil {
				_ = connection.Close()
				connection = nil
			}
			if err == nil && connection == nil {
				err = errors.New("AI provider dial returned no connection")
			}
			results <- dialResult{index: index, connection: connection, err: err}
		})
	}
	timer := time.NewTimer(dialFallbackDelay)
	defer timer.Stop()
	var fallback <-chan time.Time
	scheduleFallback := func() {
		if next < len(addresses) && active < maxParallelDials {
			timer.Reset(dialFallbackDelay)
			fallback = timer.C
		} else {
			timer.Stop()
			fallback = nil
		}
	}
	startAttempt()
	scheduleFallback()
	for active > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case result := <-results:
			active--
			if err := ctx.Err(); err != nil {
				if result.connection != nil {
					_ = result.connection.Close()
				}
				return nil, err
			}
			if result.err == nil {
				return result.connection, nil
			}
			failures[result.index] = result.err
			if next < len(addresses) {
				// A definite failure opens its slot immediately; only a pending
				// attempt needs the stagger before launching its fallback.
				startAttempt()
			}
			scheduleFallback()
		case <-fallback:
			startAttempt()
			scheduleFallback()
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, errors.Join(failures...)
}

// Put the other family second even when a resolver groups its IPv6 answers first.
// Every address still comes exclusively from the previously validated answer set.
func interleaveAddressFamilies(addresses []netip.Addr) []netip.Addr {
	var primary, secondary []netip.Addr
	firstIPv6 := addresses[0].Unmap().Is6()
	for _, address := range addresses {
		if address.Unmap().Is6() == firstIPv6 {
			primary = append(primary, address)
		} else {
			secondary = append(secondary, address)
		}
	}
	ordered := make([]netip.Addr, 0, len(addresses))
	for i := range max(len(primary), len(secondary)) {
		if i < len(primary) {
			ordered = append(ordered, primary[i])
		}
		if i < len(secondary) {
			ordered = append(ordered, secondary[i])
		}
	}
	return ordered
}

// Global-unicast checks alone include private and special-use addresses. Keep
// those ranges outside the default public-provider boundary, including address
// translation/tunneling ranges that can encode private IPv4 destinations.
var nonpublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	// Azure's host-local platform endpoint uses a public-looking virtual IP.
	netip.MustParsePrefix("168.63.129.16/32"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

var globalIPv6 = netip.MustParsePrefix("2000::/3")

func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || (ip.Is6() && !globalIPv6.Contains(ip)) {
		return false
	}
	for _, prefix := range nonpublicPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
