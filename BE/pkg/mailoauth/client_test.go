package mailoauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/SephiraCS/strifira-backend/pkg/mailconfig"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/secrets"
	"github.com/oracle/oci-go-sdk/v65/vault"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

type testCredentials struct{ err error }

func (s testCredentials) Credentials(context.Context, string) (Credentials, error) {
	return Credentials{ClientID: "test-client", ClientSecret: "test-secret"}, s.err
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOAuthAuthorizationExchangeRefreshAndRevoke(t *testing.T) {
	for _, provider := range []string{"google", "microsoft"} {
		t.Run(provider, func(t *testing.T) {
			ctx := context.Background()
			var exchanged, refreshed, revoked bool
			var reject, unsafe bool
			var secret string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, ".well-known") {
					host := "https://oauth2.googleapis.com"
					auth := "https://accounts.google.com/authorize"
					if provider == "microsoft" {
						host, auth = "https://login.microsoftonline.com", "https://login.microsoftonline.com/authorize"
					}
					if unsafe {
						host = "https://attacker.example"
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"authorization_endpoint": auth, "token_endpoint": host + "/token", "revocation_endpoint": host + "/revoke"})
					return
				}
				require.NoError(t, r.ParseForm())
				if r.URL.Path == "/revoke" {
					require.Equal(t, "refresh-rotated", r.Form.Get("token"))
					revoked = true
					return
				}
				secret = r.Form.Get("client_secret")
				require.Equal(t, "test-client", r.Form.Get("client_id"))
				require.Equal(t, "test-secret", secret)
				if reject {
					w.WriteHeader(400)
					_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"test-secret refresh-rotated"}`))
					return
				}
				if r.Form.Get("grant_type") == "authorization_code" {
					require.Equal(t, "verifier", r.Form.Get("code_verifier"))
					require.Equal(t, "test-code", r.Form.Get("code"))
					require.Equal(t, "https://app.example/mail/oauth/callback", r.Form.Get("redirect_uri"))
					exchanged = true
					_, _ = w.Write([]byte(`{"access_token":"access-initial","refresh_token":"refresh-initial","token_type":"Bearer","expires_in":3600}`))
				} else {
					require.Equal(t, "refresh_token", r.Form.Get("grant_type"))
					require.Equal(t, "refresh-initial", r.Form.Get("refresh_token"))
					refreshed = true
					_, _ = w.Write([]byte(`{"access_token":"access-rotated","refresh_token":"refresh-rotated","token_type":"Bearer","expires_in":3600}`))
				}
			}))
			defer server.Close()
			local, _ := url.Parse(server.URL)
			c := NewClient(testCredentials{}, "https://app.example/mail/oauth/callback")
			c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				copy := r.Clone(r.Context())
				u := *r.URL
				copy.URL = &u
				copy.URL.Host, copy.URL.Scheme = local.Host, local.Scheme
				return http.DefaultTransport.RoundTrip(copy)
			})
			authorization, err := c.Start(ctx, provider, "person@example.test", "state", "verifier")
			require.NoError(t, err)
			u, err := url.Parse(authorization)
			require.NoError(t, err)
			require.Equal(t, "S256", u.Query().Get("code_challenge_method"))
			require.Equal(t, oauth2.S256ChallengeFromVerifier("verifier"), u.Query().Get("code_challenge"))
			require.Equal(t, "state", u.Query().Get("state"))
			require.Equal(t, "consent", u.Query().Get("prompt"))
			require.NotContains(t, authorization, "test-secret")
			if provider == "google" {
				require.Equal(t, "offline", u.Query().Get("access_type"))
			} else {
				require.Contains(t, u.Query().Get("scope"), "offline_access")
			}
			token, err := c.Exchange(ctx, provider, "test-code", "verifier")
			require.NoError(t, err)
			require.True(t, exchanged)
			fresh, err := c.Refresh(ctx, provider, token)
			require.NoError(t, err)
			require.True(t, refreshed)
			require.Equal(t, "refresh-rotated", fresh.RefreshToken)
			require.NoError(t, c.Revoke(ctx, provider, fresh))
			require.Equal(t, provider == "google", revoked)
			reject = true
			_, err = c.Refresh(ctx, provider, token)
			require.ErrorIs(t, err, ErrAuthorization)
			require.NotContains(t, err.Error(), secret)
			require.NotContains(t, err.Error(), "refresh-rotated")
			unsafe = true
			_, err = c.Start(ctx, provider, "person@example.test", "state", "verifier")
			require.ErrorIs(t, err, ErrUnavailable)
		})
	}
}

func TestOAuthRegistrationAndAudienceGuards(t *testing.T) {
	c := NewClient(testCredentials{err: errors.New("private credential details")}, "https://app.example/mail/oauth/callback")
	_, err := c.Start(context.Background(), "google", "person@example.test", "state", "verifier")
	require.ErrorIs(t, err, ErrRegistration)
	require.NotContains(t, err.Error(), "private credential")
	_, err = c.Start(context.Background(), "unknown", "person@example.test", "state", "verifier")
	require.ErrorIs(t, err, ErrUnsupported)
	cfg := &mailconfig.Config{Incoming: mailconfig.Server{Host: "imap.gmail.com"}, Outgoing: mailconfig.Server{Host: "attacker.example"}}
	require.Empty(t, ProviderFor(cfg))
	cfg.Outgoing.Host = "smtp.gmail.com"
	require.Equal(t, "google", ProviderFor(cfg))
	require.False(t, trustedEndpoint("google", "https://oauth2.googleapis.com.attacker.example/token"))
	require.False(t, trustedEndpoint("google", "http://oauth2.googleapis.com/token"))
	require.False(t, trustedEndpoint("google", "https://oauth2.googleapis.com:444/token"))
	require.False(t, validToken(&oauth2.Token{AccessToken: "secret\x01bad", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}))
}

type testVault struct {
	pages     int
	duplicate bool
	bad       bool
}

func (v *testVault) ListSecrets(_ context.Context, r vault.ListSecretsRequest) (vault.ListSecretsResponse, error) {
	requireName := "strifira-mail-oauth-google"
	if r.Name == nil || *r.Name != requireName || r.CompartmentId == nil || *r.CompartmentId != "compartment" || r.LifecycleState != vault.SecretSummaryLifecycleStateActive {
		return vault.ListSecretsResponse{}, errors.New("bad request")
	}
	v.pages++
	if r.Page == nil {
		return vault.ListSecretsResponse{OpcNextPage: common.String("next")}, nil
	}
	items := []vault.SecretSummary{{Id: common.String("ocid.secret")}}
	if v.duplicate {
		items = append(items, items[0])
	}
	return vault.ListSecretsResponse{Items: items}, nil
}
func (v *testVault) GetSecretBundle(_ context.Context, r secrets.GetSecretBundleRequest) (secrets.GetSecretBundleResponse, error) {
	if r.SecretId == nil || *r.SecretId != "ocid.secret" || r.Stage != secrets.GetSecretBundleStageCurrent {
		return secrets.GetSecretBundleResponse{}, errors.New("bad retrieval")
	}
	value := base64.StdEncoding.EncodeToString([]byte(`{"client_id":"registered-app","client_secret":"vendor-secret"}`))
	if v.bad {
		value = "bad-base64"
	}
	return secrets.GetSecretBundleResponse{SecretBundle: secrets.SecretBundle{SecretBundleContent: secrets.Base64SecretBundleContentDetails{Content: &value}}}, nil
}

func TestOAuthVaultDiscoversCurrentSecretAndRejectsAmbiguity(t *testing.T) {
	v := &testVault{}
	credentials, err := readVaultCredentials(context.Background(), "compartment", "google", v, v)
	require.NoError(t, err)
	require.Equal(t, 2, v.pages)
	require.Equal(t, "registered-app", credentials.ClientID)
	require.Equal(t, "vendor-secret", credentials.ClientSecret)
	v.duplicate = true
	_, err = readVaultCredentials(context.Background(), "compartment", "google", v, v)
	require.ErrorIs(t, err, ErrRegistration)
	v.duplicate, v.bad = false, true
	_, err = readVaultCredentials(context.Background(), "compartment", "google", v, v)
	require.ErrorIs(t, err, ErrRegistration)
}
