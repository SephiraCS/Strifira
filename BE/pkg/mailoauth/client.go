// Package mailoauth implements delegated mailbox authorization. Provider URLs
// come from trusted metadata, never from a mailbox domain or the browser.
package mailoauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SephiraCS/strifira-backend/pkg/mailconfig"
	"golang.org/x/oauth2"
)

var (
	ErrRegistration  = errors.New("A autorização deste provedor ainda não está disponível. Solicite ao administrador o cadastro do aplicativo de e-mail. Nenhuma conta foi conectada.")
	ErrUnsupported   = errors.New("Este provedor anuncia OAuth, mas ainda não possui integração de autorização suportada pelo Strifira.")
	ErrAuthorization = errors.New("A autorização foi recusada, expirou ou não permite acesso à caixa. Autorize novamente no provedor.")
	ErrUnavailable   = errors.New("Não foi possível acessar a autorização do provedor. Tente novamente em instantes.")
)

type Credentials struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

type CredentialsSource interface {
	Credentials(context.Context, string) (Credentials, error)
}

type Client struct {
	source      CredentialsSource
	redirectURI string
	http        *http.Client
}

func NewClient(source CredentialsSource, redirectURI string) *Client {
	return &Client{source: source, redirectURI: redirectURI, http: &http.Client{
		Timeout:       15 * time.Second,
		Transport:     &http.Transport{DialContext: mailconfig.PublicDialContext, TLSHandshakeTimeout: 5 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// ProviderFor binds a token audience to BOTH protocol servers. Untrusted
// autoconfig cannot redirect a Google/Microsoft bearer token to another host.
func ProviderFor(c *mailconfig.Config) string {
	switch {
	case c.Incoming.Host == "imap.gmail.com" && c.Outgoing.Host == "smtp.gmail.com":
		return "google"
	case c.Incoming.Host == "outlook.office365.com" && (c.Outgoing.Host == "smtp.office365.com" || c.Outgoing.Host == "smtp-mail.outlook.com"):
		return "microsoft"
	default:
		return ""
	}
}

func providerMetadata(provider string) (string, []string) {
	switch provider {
	case "google":
		return "https://accounts.google.com/.well-known/openid-configuration", []string{"https://mail.google.com/"}
	case "microsoft":
		return "https://login.microsoftonline.com/common/v2.0/.well-known/openid-configuration", []string{"offline_access", "https://outlook.office.com/IMAP.AccessAsUser.All", "https://outlook.office.com/SMTP.Send"}
	default:
		return "", nil
	}
}

func trustedEndpoint(provider, endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" {
		return false
	}
	if provider == "google" {
		return u.Host == "accounts.google.com" || u.Host == "oauth2.googleapis.com"
	}
	return provider == "microsoft" && u.Host == "login.microsoftonline.com"
}

func (c *Client) config(ctx context.Context, provider string) (*oauth2.Config, string, error) {
	metadata, scopes := providerMetadata(provider)
	if metadata == "" {
		return nil, "", ErrUnsupported
	}
	credentials, err := c.source.Credentials(ctx, provider)
	if err != nil || credentials.ClientID == "" || credentials.ClientSecret == "" {
		return nil, "", ErrRegistration
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, metadata, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", ErrUnavailable
	}
	defer resp.Body.Close()
	var doc struct {
		Authorization string `json:"authorization_endpoint"`
		Token         string `json:"token_endpoint"`
		Revocation    string `json:"revocation_endpoint"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&doc) != nil || !trustedEndpoint(provider, doc.Authorization) || !trustedEndpoint(provider, doc.Token) {
		return nil, "", ErrUnavailable
	}
	if doc.Revocation != "" && !trustedEndpoint(provider, doc.Revocation) {
		return nil, "", ErrUnavailable
	}
	return &oauth2.Config{ClientID: credentials.ClientID, ClientSecret: credentials.ClientSecret, RedirectURL: c.redirectURI, Scopes: scopes,
		Endpoint: oauth2.Endpoint{AuthURL: doc.Authorization, TokenURL: doc.Token, AuthStyle: oauth2.AuthStyleInParams}}, doc.Revocation, nil
}

func (c *Client) Start(ctx context.Context, provider, address, state, verifier string) (string, error) {
	config, _, err := c.config(ctx, provider)
	if err != nil {
		return "", err
	}
	options := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("login_hint", address), oauth2.SetAuthURLParam("prompt", "consent")}
	if provider == "google" {
		options = append(options, oauth2.AccessTypeOffline)
	}
	return config.AuthCodeURL(state, options...), nil
}

func (c *Client) Exchange(ctx context.Context, provider, code, verifier string) (*oauth2.Token, error) {
	config, _, err := c.config(ctx, provider)
	if err != nil {
		return nil, err
	}
	token, err := config.Exchange(context.WithValue(ctx, oauth2.HTTPClient, c.http), code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, tokenError(err)
	}
	if !validToken(token) || token.RefreshToken == "" {
		return nil, ErrAuthorization
	}
	return token, nil
}

func validToken(token *oauth2.Token) bool {
	return token != nil && token.AccessToken != "" && !strings.ContainsAny(token.AccessToken, "\r\n\x00\x01") && strings.EqualFold(token.TokenType, "Bearer") && token.Expiry.After(time.Now())
}

func tokenError(err error) error {
	var response *oauth2.RetrieveError
	if errors.As(err, &response) && (response.ErrorCode == "invalid_client" || response.ErrorCode == "unauthorized_client") {
		return ErrRegistration
	}
	if errors.As(err, &response) && (response.ErrorCode == "invalid_grant" || response.ErrorCode == "access_denied" || response.ErrorCode == "interaction_required") {
		return ErrAuthorization
	}
	// Never return vendor bodies, which may echo codes or tokens.
	return ErrUnavailable
}

func (c *Client) Refresh(ctx context.Context, provider string, token *oauth2.Token) (*oauth2.Token, error) {
	config, _, err := c.config(ctx, provider)
	if err != nil {
		return nil, err
	}
	if token.RefreshToken == "" {
		return nil, ErrAuthorization
	}
	expired := *token
	expired.Expiry = time.Now().Add(-time.Hour)
	fresh, err := config.TokenSource(context.WithValue(ctx, oauth2.HTTPClient, c.http), &expired).Token()
	if err != nil {
		return nil, tokenError(err)
	}
	if !validToken(fresh) {
		return nil, ErrAuthorization
	}
	return fresh, nil
}

// Google supports RFC 7009. Microsoft has no per-grant revocation endpoint:
// disconnect destroys local tokens, and the owner can remove consent at the
// Microsoft account portal. Never revoke all of the user's sessions via Graph.
func (c *Client) Revoke(ctx context.Context, provider string, token *oauth2.Token) error {
	if provider == "microsoft" {
		return nil
	}
	_, endpoint, err := c.config(ctx, provider)
	if err != nil {
		return err
	}
	if endpoint == "" {
		return ErrUnavailable
	}
	value := token.RefreshToken
	if value == "" {
		value = token.AccessToken
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(url.Values{"token": {value}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == 400 {
		var result struct {
			Error string `json:"error"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, 2048)).Decode(&result) == nil && result.Error == "invalid_token" {
			return nil
		}
	}
	if resp.StatusCode != 200 {
		return ErrUnavailable
	}
	return nil
}
