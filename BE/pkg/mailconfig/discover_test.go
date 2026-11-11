package mailconfig

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const configXML = `<clientConfig version="1.1"><emailProvider id="example.org">
 <incomingServer type="imap"><hostname>imap.example.org</hostname><port>993</port><socketType>SSL</socketType><username>%EMAILADDRESS%</username><authentication>password-cleartext</authentication></incomingServer>
 <outgoingServer type="smtp"><hostname>smtp.example.org</hostname><port>587</port><socketType>STARTTLS</socketType><username>%EMAILADDRESS%</username><authentication>password-cleartext</authentication></outgoingServer>
 </emailProvider></clientConfig>`

type discoveryDNS struct {
	mx  []*net.MX
	srv map[string][]*net.SRV
	err error
}

func (d discoveryDNS) LookupMX(context.Context, string) ([]*net.MX, error) { return d.mx, d.err }
func (d discoveryDNS) LookupSRV(_ context.Context, service, _, _ string) (string, []*net.SRV, error) {
	if d.err != nil {
		return "", nil, d.err
	}
	return "", d.srv[service], nil
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDiscoveryProviderAndHostedDomains(t *testing.T) {
	for _, tc := range []struct{ address, provider, auth string }{
		{"person@gmail.com", "Google", "app_password"},
		{"person@outlook.com", "Microsoft", "oauth2"},
		{"person@icloud.com", "iCloud", "app_password"},
		{"person@fastmail.com", "Fastmail", "app_password"},
		{"person@purelymail.com", "Purelymail", "password"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			c, err := NewDiscoverer().Discover(context.Background(), tc.address)
			require.NoError(t, err)
			require.Equal(t, tc.provider, c.Provider)
			require.Equal(t, tc.auth, c.Authentication)
			require.Equal(t, "tls", c.Incoming.Security)
			require.Equal(t, tc.address, c.Outgoing.Username)
		})
	}
	d := NewDiscoverer()
	d.dns = discoveryDNS{mx: []*net.MX{{Host: "tenant.mail.protection.outlook.com."}}}
	c, err := d.Discover(context.Background(), "person@company.example")
	require.NoError(t, err)
	require.Equal(t, "Microsoft", c.Provider)
	require.Equal(t, "mx", c.Source)
	// A provider-looking substring is not a provider-owned DNS suffix.
	require.False(t, suffix("google.com.attacker.example", "google.com"))
}

// Purelymail custom domains may have no domain autoconfig or ISPDB entry.
// Recognize only its published MX, never an arbitrary MX or lookalike host.
func TestDiscoveryPurelymailMX(t *testing.T) {
	for _, host := range []string{"mailserver.purelymail.com.", "MAILSERVER.PURELYMAIL.COM."} {
		t.Run(host, func(t *testing.T) {
			d := NewDiscoverer()
			d.dns = discoveryDNS{mx: []*net.MX{{Host: host}}}
			d.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				t.Fatal("known MX must not depend on unavailable domain autoconfig")
				return nil, ErrUnavailable
			})}
			c, err := d.Discover(context.Background(), "person@custom.example")
			require.NoError(t, err)
			require.Equal(t, "Purelymail", c.Provider)
			require.Equal(t, "mx", c.Source)
			require.Equal(t, "password", c.Authentication)
			require.Equal(t, Server{Host: "imap.purelymail.com", Port: 993, Security: "tls", Username: "person@custom.example"}, c.Incoming)
			require.Equal(t, Server{Host: "smtp.purelymail.com", Port: 465, Security: "tls", Username: "person@custom.example"}, c.Outgoing)
		})
	}
	for _, host := range []string{"mailserver.purelymail.com.attacker.example.", "attackerpurelymail.com.", "purelymail.com.", "mail.custom.example."} {
		t.Run(host, func(t *testing.T) {
			d := NewDiscoverer()
			d.dns = discoveryDNS{mx: []*net.MX{{Host: host}}}
			d.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 404, Body: http.NoBody, Header: make(http.Header)}, nil
			})}
			_, err := d.Discover(context.Background(), "person@custom.example")
			require.ErrorIs(t, err, ErrNotFound)
		})
	}
}

func TestDiscoveryHTTPSDocumentAndFailures(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/valid":
			_, _ = w.Write([]byte(configXML))
		case "/missing":
			w.WriteHeader(404)
		case "/large":
			fmt.Fprint(w, strings.Repeat("x", (256<<10)+1))
		case "/redirect":
			http.Redirect(w, r, "http://127.0.0.1/private", 302)
		default:
			w.WriteHeader(503)
		}
	}))
	defer server.Close()
	d := NewDiscoverer()
	d.client = server.Client()
	d.client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	c, err := d.document(context.Background(), server.URL+"/valid", "Person@example.org")
	require.NoError(t, err)
	require.Equal(t, "Person@example.org", c.Incoming.Username)
	require.Equal(t, 587, c.Outgoing.Port)
	for _, tc := range []struct {
		path string
		err  error
	}{{"/missing", ErrNotFound}, {"/large", ErrNotFound}, {"/redirect", ErrUnavailable}, {"/down", ErrUnavailable}} {
		_, err := d.document(context.Background(), server.URL+tc.path, "person@example.org")
		require.ErrorIs(t, err, tc.err)
	}
}

func TestDiscoveryRejectsInsecureAndUnsupportedDocuments(t *testing.T) {
	for name, body := range map[string]string{
		"plaintext":                  strings.ReplaceAll(configXML, "<socketType>SSL</socketType>", "<socketType>plain</socketType>"),
		"bad host":                   strings.ReplaceAll(configXML, "imap.example.org", "127.0.0.1"),
		"invalid port":               strings.ReplaceAll(configXML, "<port>993</port>", "<port>65536</port>"),
		"unknown username token":     strings.ReplaceAll(configXML, "%EMAILADDRESS%", "%PASSWORD%"),
		"unsupported authentication": strings.ReplaceAll(configXML, "password-cleartext", "GSSAPI"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseDocument([]byte(body), "person@example.org")
			require.ErrorIs(t, err, ErrNotFound)
		})
	}
	c, err := parseDocument([]byte(strings.Replace(configXML, "%EMAILADDRESS%", "%EMAILLOCALPART%", 1)), "person@example.org")
	require.NoError(t, err)
	require.Equal(t, "person", c.Incoming.Username)
	require.Equal(t, "person@example.org", c.Outgoing.Username)
	// Advertising OAuth must never silently fall back to password auth.
	c, err = parseDocument([]byte(strings.ReplaceAll(configXML, "<authentication>password-cleartext</authentication>", "<authentication>password-cleartext</authentication><authentication>OAuth2</authentication>")), "person@example.org")
	require.NoError(t, err)
	require.Equal(t, "oauth2", c.Authentication)
}

func TestDiscoverySecureSRVAndNotFound(t *testing.T) {
	d := NewDiscoverer()
	d.dns = discoveryDNS{srv: map[string][]*net.SRV{
		"imaps":      {{Target: "imap.company.example.", Port: 993}},
		"submission": {{Target: "smtp.company.example.", Port: 587}},
	}}
	var requested []string
	d.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		requested = append(requested, r.URL.String())
		require.Equal(t, "https", r.URL.Scheme)
		return &http.Response{StatusCode: 404, Body: http.NoBody, Header: make(http.Header)}, nil
	})}
	c, err := d.Discover(context.Background(), "person@company.example")
	require.NoError(t, err)
	require.Equal(t, "dns-srv", c.Source)
	require.Equal(t, "starttls", c.Outgoing.Security)
	require.Len(t, requested, 3)
	require.NotContains(t, requested[2], "person")
	d.dns = discoveryDNS{}
	_, err = d.Discover(context.Background(), "person@unknown.example")
	require.ErrorIs(t, err, ErrNotFound)
	d.dns = discoveryDNS{err: &net.DNSError{IsTimeout: true}}
	_, err = d.Discover(context.Background(), "person@unknown.example")
	require.ErrorIs(t, err, ErrUnavailable)
}

func TestDiscoveryRejectsAddressesAndPrivateDestinations(t *testing.T) {
	for _, address := range []string{"name <person@example.com>", "person@localhost", "person@127.0.0.1", "person@example.com\r\nsecret", "missing-at"} {
		_, err := NormalizeAddress(address)
		require.ErrorIs(t, err, ErrAddress)
	}
	address, err := NormalizeAddress(" Person@EXAMPLE.COM ")
	require.NoError(t, err)
	require.Equal(t, "Person@example.com", address)
	for _, ip := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "168.63.129.16", "2001:db8::1", "64:ff9b::7f00:1", "192.0.2.1"} {
		require.False(t, publicIP(netip.MustParseAddr(ip)), ip)
	}
	for _, ip := range []string{"1.1.1.1", "2606:4700:4700::1111"} {
		require.True(t, publicIP(netip.MustParseAddr(ip)))
	}
	conn, err := PublicDialContext(context.Background(), "tcp", "127.0.0.1:80")
	require.Nil(t, conn)
	require.ErrorIs(t, err, errUnsafeDestination)
}
