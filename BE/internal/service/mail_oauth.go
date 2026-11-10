package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/SephiraCS/strifira-backend/pkg/crypto"
	"github.com/SephiraCS/strifira-backend/pkg/mailclient"
	"github.com/SephiraCS/strifira-backend/pkg/mailconfig"
	"github.com/SephiraCS/strifira-backend/pkg/mailoauth"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

type mailOAuthClient interface {
	Start(context.Context, string, string, string, string) (string, error)
	Exchange(context.Context, string, string, string) (*oauth2.Token, error)
	Refresh(context.Context, string, *oauth2.Token) (*oauth2.Token, error)
	Revoke(context.Context, string, *oauth2.Token) error
}

func (s *MailService) ConfigureOAuth(repo *repository.MailOAuthRepository, frontendURL string) error {
	callback, err := MailOAuthCallbackURL(frontendURL)
	if err != nil {
		return err
	}
	s.oauthRepo = repo
	s.oauth = mailoauth.NewClient(mailoauth.VaultSource{}, callback)
	return nil
}

type pendingMailOAuth struct {
	Config           mailconfig.Config `json:"config"`
	Provider         string            `json:"provider"`
	Verifier         string            `json:"verifier"`
	AccountID        *uuid.UUID        `json:"account_id,omitempty"`
	ExpectedRevision *int64            `json:"expected_revision,omitempty"`
	DisplayName      string            `json:"display_name"`
	Signature        string            `json:"signature"`
}

type MailOAuthStart struct {
	AuthorizationURL string `json:"authorization_url"`
	State            string `json:"state"`
}

func stateHash(state string) string {
	h := sha256.Sum256([]byte(state))
	return hex.EncodeToString(h[:])
}

func (s *MailService) StartOAuth(ctx context.Context, workspaceID, userID uuid.UUID, req dto.StartMailOAuthRequest) (*MailOAuthStart, error) {
	config, err := s.DiscoverAccount(ctx, req.EmailAddress)
	if err != nil {
		return nil, err
	}
	provider := mailoauth.ProviderFor(config)
	if provider == "" {
		return nil, mailoauth.ErrUnsupported
	}
	if s.oauth == nil || s.oauthRepo == nil {
		return nil, mailoauth.ErrRegistration
	}
	pending := pendingMailOAuth{Config: *config, Provider: provider, Verifier: oauth2.GenerateVerifier(), DisplayName: strings.TrimSpace(req.DisplayName), Signature: req.Signature}
	if req.AccountID != "" {
		id, err := uuid.Parse(req.AccountID)
		if err != nil {
			return nil, repository.ErrMailAccountNotFound
		}
		account, err := s.accountRepo.GetByID(ctx, workspaceID, id)
		if err != nil {
			return nil, err
		}
		if account.CreatedBy != userID || account.EmailAddress != config.EmailAddress || account.AuthMethod == "oauth2" && account.OAuthProvider != provider {
			return nil, repository.ErrMailAccountNotFound
		}
		pending.AccountID = &id
		pending.ExpectedRevision = &account.CredentialsRevision
	}
	state := oauth2.GenerateVerifier()
	authorization, err := s.oauth.Start(ctx, provider, config.EmailAddress, state, pending.Verifier)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(pending)
	if err != nil {
		return nil, err
	}
	encrypted, err := crypto.Encrypt(string(data), s.encryptKey)
	if err != nil {
		return nil, err
	}
	if err := s.oauthRepo.SaveSession(ctx, stateHash(state), workspaceID, userID, encrypted); err != nil {
		return nil, err
	}
	return &MailOAuthStart{AuthorizationURL: authorization, State: state}, nil
}

func (s *MailService) CompleteOAuth(ctx context.Context, workspaceID, userID uuid.UUID, req dto.CompleteMailOAuthRequest) (*domain.MailAccount, error) {
	if s.oauth == nil || s.oauthRepo == nil {
		return nil, mailoauth.ErrRegistration
	}
	encrypted, err := s.oauthRepo.ConsumeSession(ctx, stateHash(req.State), workspaceID, userID)
	if err != nil {
		return nil, err
	}
	if req.ProviderError != "" || req.Code == "" {
		return nil, mailoauth.ErrAuthorization
	}
	data, err := crypto.Decrypt(encrypted, s.encryptKey)
	if err != nil {
		return nil, mailoauth.ErrAuthorization
	}
	var pending pendingMailOAuth
	if json.Unmarshal([]byte(data), &pending) != nil || pending.Provider == "" || pending.Provider != mailoauth.ProviderFor(&pending.Config) {
		return nil, mailoauth.ErrAuthorization
	}
	token, err := s.oauth.Exchange(ctx, pending.Provider, req.Code, pending.Verifier)
	if err != nil {
		return nil, err
	}
	account := accountFromConfig(workspaceID, userID, &pending.Config, pending.DisplayName, pending.Signature)
	account.AuthMethod, account.OAuthProvider = "oauth2", pending.Provider
	if pending.AccountID != nil {
		account.ID = *pending.AccountID
	}
	// Provider consent is necessary but not sufficient: authenticate as the
	// requested mailbox over BOTH protocols before changing any account row.
	if err := s.validateOAuthMailbox(account, token.AccessToken); err != nil {
		return nil, err
	}
	tokenData, err := json.Marshal(token)
	if err != nil {
		return nil, err
	}
	tokenEncrypted, err := crypto.Encrypt(string(tokenData), s.encryptKey)
	if err != nil {
		return nil, err
	}
	if err := s.oauthRepo.Link(ctx, account, tokenEncrypted, pending.ExpectedRevision); err != nil {
		return nil, err
	}
	return s.accountRepo.GetByID(ctx, workspaceID, account.ID)
}

func accountFromConfig(workspaceID, userID uuid.UUID, c *mailconfig.Config, name, signature string) *domain.MailAccount {
	return &domain.MailAccount{ID: uuid.New(), WorkspaceID: workspaceID, CreatedBy: userID, EmailAddress: c.EmailAddress,
		DisplayName: strings.TrimSpace(name), Signature: signature,
		IMAPHost: c.Incoming.Host, IMAPPort: c.Incoming.Port, IMAPSecurity: c.Incoming.Security,
		SMTPHost: c.Outgoing.Host, SMTPPort: c.Outgoing.Port, SMTPSecurity: c.Outgoing.Security,
		Username: c.Incoming.Username, SMTPUsername: c.Outgoing.Username}
}

func (s *MailService) testOAuthConnections(account *domain.MailAccount, token string) error {
	if err := mailclient.TestIMAPConnection(s.fetchOptions(account, token)); err != nil {
		return accountConnectionError(account, err)
	}
	if err := mailclient.TestSMTPConnection(s.sendOptions(account, token)); err != nil {
		return accountConnectionError(account, err)
	}
	return nil
}

func accountConnectionError(account *domain.MailAccount, err error) error {
	if account.AuthMethod == "oauth2" && errors.Is(err, mailclient.ErrAuthentication) {
		return mailoauth.ErrAuthorization
	}
	return mailConnectionError(err)
}

// credential obtains a fresh bearer token under a database lock shared by all
// API/sync processes. The rotating refresh token is persisted before use.
func (s *MailService) credential(ctx context.Context, account *domain.MailAccount) (string, error) {
	if account.AuthMethod != "oauth2" {
		return s.decryptPassword(account)
	}
	if account.OAuthProvider == "" || account.OAuthProvider != mailoauth.ProviderFor(&mailconfig.Config{Incoming: mailconfig.Server{Host: account.IMAPHost}, Outgoing: mailconfig.Server{Host: account.SMTPHost}}) {
		return "", mailoauth.ErrAuthorization
	}
	if s.oauth == nil || s.oauthRepo == nil {
		return "", mailoauth.ErrRegistration
	}
	var value string
	var authorizationErr error
	err := s.oauthRepo.WithToken(ctx, account.WorkspaceID, account.ID, func(row *repository.MailOAuthToken) error {
		if row.Encrypted == "" {
			return mailoauth.ErrAuthorization
		}
		data, err := crypto.Decrypt(row.Encrypted, s.encryptKey)
		if err != nil {
			return mailoauth.ErrAuthorization
		}
		var token oauth2.Token
		if json.Unmarshal([]byte(data), &token) != nil {
			return mailoauth.ErrAuthorization
		}
		if token.Expiry.Before(time.Now().Add(2 * time.Minute)) {
			fresh, err := s.oauth.Refresh(ctx, account.OAuthProvider, &token)
			if errors.Is(err, mailoauth.ErrAuthorization) {
				// A revoked grant cannot leave a seemingly working credential.
				row.Encrypted, row.SyncError = "", mailoauth.ErrAuthorization.Error()
				authorizationErr = err
				return nil
			}
			if err != nil {
				return err
			}
			token = *fresh
			data, err := json.Marshal(token)
			if err != nil {
				return err
			}
			row.Encrypted, err = crypto.Encrypt(string(data), s.encryptKey)
			if err != nil {
				return err
			}
		}
		value = token.AccessToken
		return nil
	})
	if err != nil {
		return "", err
	}
	if authorizationErr != nil {
		return "", authorizationErr
	}
	return value, nil
}

func (s *MailService) disconnectOAuth(ctx context.Context, account *domain.MailAccount) error {
	if s.oauth == nil || s.oauthRepo == nil {
		return mailoauth.ErrRegistration
	}
	return s.oauthRepo.WithToken(ctx, account.WorkspaceID, account.ID, func(row *repository.MailOAuthToken) error {
		if row.Encrypted != "" {
			data, err := crypto.Decrypt(row.Encrypted, s.encryptKey)
			if err != nil {
				return mailoauth.ErrAuthorization
			}
			var token oauth2.Token
			if json.Unmarshal([]byte(data), &token) != nil {
				return mailoauth.ErrAuthorization
			}
			if err := s.oauth.Revoke(ctx, account.OAuthProvider, &token); err != nil {
				return err
			}
		}
		row.DeleteAccount = true
		return nil
	})
}

// Parsed by tests and operational instructions without exposing credentials.
func MailOAuthCallbackURL(frontendURL string) (string, error) {
	u, err := url.Parse(frontendURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "::1" || u.Hostname() == "127.0.0.1"))) {
		return "", mailoauth.ErrRegistration
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/mail/oauth/callback"
	return u.String(), nil
}
