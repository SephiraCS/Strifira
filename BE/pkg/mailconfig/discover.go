// Package mailconfig discovers secure mailbox settings without credentials.
package mailconfig

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/idna"
)

var (
	ErrAddress     = errors.New("invalid mailbox address")
	ErrNotFound    = errors.New("no secure mail configuration found for this domain")
	ErrUnavailable = errors.New("mail discovery is temporarily unavailable")
)

type Server struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"`
	Username string `json:"username"`
}

type Config struct {
	OAuthProvider    string `json:"oauth_provider,omitempty"`
	EmailAddress     string `json:"email_address"`
	Provider         string `json:"provider"`
	Source           string `json:"source"`
	Authentication   string `json:"authentication"` // password, app_password, oauth2
	HelpURL          string `json:"help_url,omitempty"`
	AuthorizationURL string `json:"authorization_url,omitempty"`
	TokenURL         string `json:"token_url,omitempty"`
	Incoming         Server `json:"incoming"`
	Outgoing         Server `json:"outgoing"`
}

type resolver interface {
	LookupMX(context.Context, string) ([]*net.MX, error)
	LookupSRV(context.Context, string, string, string) (string, []*net.SRV, error)
}

type Discoverer struct {
	client *http.Client
	dns    resolver
}

func NewDiscoverer() *Discoverer {
	return &Discoverer{
		client: &http.Client{
			Timeout:   4 * time.Second,
			Transport: &http.Transport{DialContext: PublicDialContext, TLSHandshakeTimeout: 4 * time.Second},
			// A domain must publish its configuration over authenticated HTTPS.
			// Do not follow redirects to HTTP, other origins, or private networks.
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		},
		dns: net.DefaultResolver,
	}
}

func NormalizeAddress(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	a, err := mail.ParseAddress(raw)
	if err != nil || a.Address != raw || strings.ContainsAny(raw, "\r\n\x00") {
		return "", ErrAddress
	}
	local, domain, ok := strings.Cut(raw, "@")
	if !ok || local == "" {
		return "", ErrAddress
	}
	domain, err = idna.Lookup.ToASCII(strings.ToLower(domain))
	if err != nil || !validHost(domain) {
		return "", ErrAddress
	}
	return local + "@" + domain, nil
}

func validHost(host string) bool {
	if !strings.Contains(host, ".") || len(host) > 253 || net.ParseIP(host) != nil {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// Discover never receives a password and never authenticates. Callers must
// validate both protocols with the user's provider credential before saving.
func (d *Discoverer) Discover(ctx context.Context, raw string) (*Config, error) {
	address, err := NormalizeAddress(raw)
	if err != nil {
		return nil, err
	}
	_, domain, _ := strings.Cut(address, "@")
	if c := knownDomain(domain, address); c != nil {
		return c, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 18*time.Second)
	defer cancel()
	// MX matching only recognizes provider-owned suffixes. An arbitrary MX is
	// never guessed to be an IMAP/SMTP server or sent the mailbox password.
	mx, mxErr := d.dns.LookupMX(ctx, domain)
	if mxErr == nil {
		for _, record := range mx {
			host := strings.TrimSuffix(strings.ToLower(record.Host), ".")
			providerDomain := ""
			switch {
			case suffix(host, "google.com"), suffix(host, "googlemail.com"):
				providerDomain = "gmail.com"
			case suffix(host, "protection.outlook.com"):
				providerDomain = "outlook.com"
			case suffix(host, "messagingengine.com"):
				providerDomain = "fastmail.com"
			case host == "mailserver.purelymail.com":
				providerDomain = "purelymail.com"
			}
			if c := knownDomain(providerDomain, address); c != nil {
				c.Source = "mx"
				return c, nil
			}
		}
	}
	unavailable := temporary(mxErr)
	for _, endpoint := range []string{
		"https://autoconfig." + domain + "/mail/config-v1.1.xml?emailaddress=" + url.QueryEscape(address),
		"https://" + domain + "/.well-known/autoconfig/mail/config-v1.1.xml?emailaddress=" + url.QueryEscape(address),
		"https://autoconfig.thunderbird.net/v1.1/" + domain,
	} {
		c, err := d.document(ctx, endpoint, address)
		if err == nil {
			return c, nil
		}
		unavailable = unavailable || errors.Is(err, ErrUnavailable)
		if ctx.Err() != nil {
			return nil, ErrUnavailable
		}
	}
	// RFC 6186/8314: only encrypted mail access/submission services.
	incoming, inErr := d.srv(ctx, "imaps", domain, address)
	if inErr != nil {
		incoming, inErr = d.srv(ctx, "imap", domain, address)
	}
	outgoing, outErr := d.srv(ctx, "submissions", domain, address)
	if outErr != nil {
		outgoing, outErr = d.srv(ctx, "submission", domain, address)
	}
	if inErr == nil && outErr == nil {
		return &Config{EmailAddress: address, Provider: domain, Source: "dns-srv", Authentication: "password", Incoming: incoming, Outgoing: outgoing}, nil
	}
	if unavailable || temporary(inErr) || temporary(outErr) {
		return nil, ErrUnavailable
	}
	return nil, ErrNotFound
}

func temporary(err error) bool {
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrAddress) {
		return false
	}
	var dnsErr *net.DNSError
	return err != nil && (!errors.As(err, &dnsErr) || !dnsErr.IsNotFound)
}

func suffix(host, domain string) bool { return host == domain || strings.HasSuffix(host, "."+domain) }

func knownDomain(domain, address string) *Config {
	c := &Config{EmailAddress: address, Source: "provider", Authentication: "app_password"}
	var imapHost, smtpHost string
	port, security := 465, "tls"
	switch domain {
	case "gmail.com", "googlemail.com":
		c.Provider = "Google"
		imapHost = "imap.gmail.com"
		smtpHost = "smtp.gmail.com"
		c.HelpURL = "https://support.google.com/accounts/answer/185833"
		c.AuthorizationURL = "https://accounts.google.com/o/oauth2/v2/auth"
		c.TokenURL = "https://oauth2.googleapis.com/token"
	case "outlook.com", "hotmail.com", "live.com", "msn.com":
		c.Provider = "Microsoft"
		imapHost = "outlook.office365.com"
		smtpHost = "smtp.office365.com"
		port = 587
		security = "starttls"
		c.Authentication = "oauth2"
		c.AuthorizationURL = "https://login.microsoftonline.com/common/oauth2/v2.0/authorize"
		c.TokenURL = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
	case "icloud.com", "me.com", "mac.com":
		c.Provider = "iCloud"
		imapHost = "imap.mail.me.com"
		smtpHost = "smtp.mail.me.com"
		port = 587
		security = "starttls"
		c.HelpURL = "https://support.apple.com/102654"
	case "yahoo.com", "ymail.com", "rocketmail.com", "yahoo.com.br":
		c.Provider = "Yahoo"
		imapHost = "imap.mail.yahoo.com"
		smtpHost = "smtp.mail.yahoo.com"
		c.HelpURL = "https://help.yahoo.com/kb/SLN15241.html"
	case "aol.com":
		c.Provider = "AOL"
		imapHost = "imap.aol.com"
		smtpHost = "smtp.aol.com"
		c.HelpURL = "https://help.aol.com/articles/Create-and-manage-app-password"
	// https://purelymail.com/docs/setup/technical
	// Custom domains use the same TLS endpoints and full-address login.
	case "purelymail.com":
		c.Provider = "Purelymail"
		imapHost = "imap.purelymail.com"
		smtpHost = "smtp.purelymail.com"
		c.Authentication = "password"
		c.HelpURL = "https://purelymail.com/docs/setup/technical"
	case "fastmail.com", "fastmail.fm":
		c.Provider = "Fastmail"
		imapHost = "imap.fastmail.com"
		smtpHost = "smtp.fastmail.com"
		c.HelpURL = "https://www.fastmail.help/hc/en-us/articles/360058752854"
	default:
		return nil
	}
	c.Incoming = Server{Host: imapHost, Port: 993, Security: "tls", Username: address}
	c.Outgoing = Server{Host: smtpHost, Port: port, Security: security, Username: address}
	if c.Provider == "iCloud" {
		c.Incoming.Username, _, _ = strings.Cut(address, "@")
	}
	return c
}

func (d *Discoverer) srv(ctx context.Context, service, domain, address string) (Server, error) {
	_, records, err := d.dns.LookupSRV(ctx, service, "tcp", domain)
	if err != nil {
		return Server{}, err
	}
	for _, record := range records {
		host := strings.TrimSuffix(strings.ToLower(record.Target), ".")
		if !validHost(host) || record.Port == 0 {
			continue
		}
		security := "tls"
		if service == "imap" || service == "submission" {
			security = "starttls"
		}
		return Server{Host: host, Port: int(record.Port), Security: security, Username: address}, nil
	}
	return Server{}, ErrNotFound
}

type xmlServer struct {
	Type           string   `xml:"type,attr"`
	Hostname       string   `xml:"hostname"`
	Port           int      `xml:"port"`
	Socket         string   `xml:"socketType"`
	Username       string   `xml:"username"`
	Authentication []string `xml:"authentication"`
}

func (d *Discoverer) document(ctx context.Context, endpoint, address string) (*Config, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, ErrNotFound
	}
	resp, err := d.client.Do(req)
	if err != nil {
		if !temporary(err) {
			return nil, ErrNotFound
		}
		return nil, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (256<<10)+1))
	if err != nil {
		return nil, ErrUnavailable
	}
	if len(data) > 256<<10 {
		return nil, ErrNotFound
	}
	return parseDocument(data, address)
}

func parseDocument(data []byte, address string) (*Config, error) {
	var doc struct {
		XMLName   xml.Name `xml:"clientConfig"`
		Providers []struct {
			Name     string      `xml:"displayName"`
			Incoming []xmlServer `xml:"incomingServer"`
			Outgoing []xmlServer `xml:"outgoingServer"`
		} `xml:"emailProvider"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, ErrNotFound
	}
	for _, p := range doc.Providers {
		in, inAuth, ok := selectServer(p.Incoming, "imap", address)
		if !ok {
			continue
		}
		out, outAuth, ok := selectServer(p.Outgoing, "smtp", address)
		if !ok {
			continue
		}
		auth := "password"
		if inAuth == "oauth2" || outAuth == "oauth2" {
			auth = "oauth2"
		}
		_, domain, _ := strings.Cut(address, "@")
		// Use the domain as the display label, not arbitrary text from XML.
		return &Config{EmailAddress: address, Provider: domain, Source: "autoconfig", Authentication: auth, Incoming: in, Outgoing: out}, nil
	}
	return nil, ErrNotFound
}

func selectServer(servers []xmlServer, kind, address string) (Server, string, bool) {
	local, domain, _ := strings.Cut(address, "@")
	for _, s := range servers {
		if s.Type != kind {
			continue
		}
		host := strings.ToLower(strings.TrimSpace(s.Hostname))
		host = strings.ReplaceAll(host, "%emaildomain%", domain)
		if !validHost(host) || s.Port < 1 || s.Port > 65535 {
			continue
		}
		security := ""
		switch strings.ToUpper(s.Socket) {
		case "SSL":
			security = "tls"
		case "STARTTLS":
			security = "starttls"
		default:
			continue
		}
		username := strings.NewReplacer("%EMAILADDRESS%", address, "%EMAILLOCALPART%", local, "%EMAILDOMAIN%", domain).Replace(s.Username)
		if username == "" || len(username) > 255 || strings.ContainsAny(username, "%\r\n\x00") {
			continue
		}
		auth := ""
		for _, a := range s.Authentication {
			switch strings.ToLower(a) {
			case "oauth2":
				auth = "oauth2"
			case "password-cleartext":
				if auth == "" {
					auth = "password"
				}
			}
		}
		if auth == "" {
			continue
		}
		return Server{Host: host, Port: s.Port, Security: security, Username: username}, auth, true
	}
	return Server{}, "", false
}

// ServerAddress is shared with the guarded protocol dialers.
func ServerAddress(host string, port int) string { return net.JoinHostPort(host, strconv.Itoa(port)) }
