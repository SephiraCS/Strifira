package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/realtime"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/SephiraCS/strifira-backend/pkg/crypto"
	"github.com/SephiraCS/strifira-backend/pkg/mailconfig"
	"github.com/SephiraCS/strifira-backend/pkg/mailoauth"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// The vendor and connection validator are test doubles here. The HTTP OAuth
// implementation and real XOAUTH2 protocol conversations have separate tests;
// none of these fixtures demonstrate an external vendor integration.
type fixtureMailOAuth struct {
	refreshes    atomic.Int32
	denied       atomic.Bool
	revokeFailed bool
	revoked      bool
	verifier     string
}

func (f *fixtureMailOAuth) Start(_ context.Context, _, _, state, verifier string) (string, error) {
	f.verifier = verifier
	return "https://accounts.google.com/authorize?state=" + state, nil
}
func (f *fixtureMailOAuth) Exchange(_ context.Context, _, _, verifier string) (*oauth2.Token, error) {
	if verifier != f.verifier {
		return nil, mailoauth.ErrAuthorization
	}
	return &oauth2.Token{AccessToken: "fixture-access", RefreshToken: "fixture-refresh", TokenType: "Bearer", Expiry: time.Now().Add(time.Minute)}, nil
}
func (f *fixtureMailOAuth) Refresh(_ context.Context, _ string, old *oauth2.Token) (*oauth2.Token, error) {
	f.refreshes.Add(1)
	if f.denied.Load() {
		return nil, mailoauth.ErrAuthorization
	}
	if old.RefreshToken != "fixture-refresh" {
		return nil, errors.New("refresh rotation raced")
	}
	return &oauth2.Token{AccessToken: "fixture-access-fresh", RefreshToken: "fixture-refresh-rotated", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}, nil
}
func (f *fixtureMailOAuth) Revoke(context.Context, string, *oauth2.Token) error {
	if f.revokeFailed {
		return mailoauth.ErrUnavailable
	}
	f.revoked = true
	return nil
}

func oauthTestService(t *testing.T) (*sqlx.DB, *MailService, uuid.UUID, uuid.UUID, *fixtureMailOAuth) {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not configured")
	}
	db, err := sqlx.Connect("pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ws, user := uuid.New(), uuid.New()
	_, err = db.Exec(`INSERT INTO users(id,email,name,password_hash) VALUES($1,$2,'OAuth fixture','unused')`, user, user.String()+"@example.test")
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id=$1`, user) })
	_, err = db.Exec(`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'OAuth fixture',$2,$3)`, ws, "oauth-"+ws.String(), user)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, ws) })
	s := NewMailService(repository.NewMailAccountRepository(db), repository.NewMailMessageRepository(db), repository.NewMailDraftRepository(db), realtime.NewHub(), []byte(strings.Repeat("o", 32)))
	require.NoError(t, s.ConfigureOAuth(repository.NewMailOAuthRepository(db), "https://app.example"))
	fixture := &fixtureMailOAuth{}
	s.oauth = fixture
	s.validateOAuthMailbox = func(account *domain.MailAccount, token string) error {
		require.Equal(t, "oauth2", account.AuthMethod)
		require.Equal(t, "fixture-access", token)
		require.Equal(t, "imap.gmail.com", account.IMAPHost)
		return nil
	}
	return db, s, ws, user, fixture
}

func TestMailOAuthStateIsolationPersistenceRefreshAndDisconnect(t *testing.T) {
	db, s, ws, user, fixture := oauthTestService(t)
	ctx := context.Background()
	req := dto.StartMailOAuthRequest{EmailAddress: "fixture@gmail.com", DisplayName: "Fixture"}
	started, err := s.StartOAuth(ctx, ws, user, req)
	require.NoError(t, err)
	var sessionCipher string
	require.NoError(t, db.Get(&sessionCipher, `SELECT payload_encrypted FROM mail_oauth_sessions WHERE state_hash=$1`, stateHash(started.State)))
	require.NotContains(t, sessionCipher, fixture.verifier)
	finish := dto.CompleteMailOAuthRequest{State: started.State, Code: "fixture-code"}
	_, err = s.CompleteOAuth(ctx, uuid.New(), user, finish)
	require.ErrorIs(t, err, repository.ErrMailOAuthSession)
	_, err = s.CompleteOAuth(ctx, ws, uuid.New(), finish)
	require.ErrorIs(t, err, repository.ErrMailOAuthSession)
	account, err := s.CompleteOAuth(ctx, ws, user, finish)
	require.NoError(t, err)
	require.Equal(t, "oauth2", account.AuthMethod)
	require.Empty(t, account.PasswordEncrypted)
	_, err = s.CompleteOAuth(ctx, ws, user, finish)
	require.ErrorIs(t, err, repository.ErrMailOAuthSession)
	var encrypted string
	require.NoError(t, db.Get(&encrypted, `SELECT token_encrypted FROM mail_oauth_tokens WHERE account_id=$1`, account.ID))
	require.NotContains(t, encrypted, "fixture-refresh")
	serialized, err := json.Marshal(account)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), "fixture-access")
	require.NotContains(t, string(serialized), "token_encrypted")
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := s.credential(ctx, account)
			if err == nil && token != "fixture-access-fresh" {
				err = mailoauth.ErrAuthorization
			}
			errors <- err
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, fixture.refreshes.Load())
	require.NoError(t, db.Get(&encrypted, `SELECT token_encrypted FROM mail_oauth_tokens WHERE account_id=$1`, account.ID))
	data, err := crypto.Decrypt(encrypted, s.encryptKey)
	require.NoError(t, err)
	require.Contains(t, data, "fixture-refresh-rotated")
	bad := "wrong"
	_, err = s.UpdateAccount(ctx, ws, account.ID, dto.UpdateMailAccountRequest{Password: &bad})
	require.ErrorIs(t, err, ErrMailOAuthRequired)
	fixture.revokeFailed = true
	require.ErrorIs(t, s.DeleteAccount(ctx, ws, account.ID), mailoauth.ErrUnavailable)
	_, err = s.accountRepo.GetByID(ctx, ws, account.ID)
	require.NoError(t, err)
	fixture.revokeFailed = false
	require.NoError(t, s.DeleteAccount(ctx, ws, account.ID))
	require.True(t, fixture.revoked)
	_, err = s.accountRepo.GetByID(ctx, ws, account.ID)
	require.ErrorIs(t, err, repository.ErrMailAccountNotFound)
	var count int
	require.NoError(t, db.Get(&count, `SELECT COUNT(*) FROM mail_oauth_tokens WHERE account_id=$1`, account.ID))
	require.Zero(t, count)
}

func TestMailOAuthFailuresAndRevokedGrantDoNotConnect(t *testing.T) {
	db, s, ws, user, fixture := oauthTestService(t)
	ctx := context.Background()
	req := dto.StartMailOAuthRequest{EmailAddress: "fixture@gmail.com"}
	started, err := s.StartOAuth(ctx, ws, user, req)
	require.NoError(t, err)
	_, err = s.CompleteOAuth(ctx, ws, user, dto.CompleteMailOAuthRequest{State: started.State, ProviderError: "access_denied"})
	require.ErrorIs(t, err, mailoauth.ErrAuthorization)
	started, err = s.StartOAuth(ctx, ws, user, req)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE mail_oauth_sessions SET expires_at=NOW()-INTERVAL '1 minute' WHERE state_hash=$1`, stateHash(started.State))
	require.NoError(t, err)
	_, err = s.CompleteOAuth(ctx, ws, user, dto.CompleteMailOAuthRequest{State: started.State, Code: "expired"})
	require.ErrorIs(t, err, repository.ErrMailOAuthSession)
	started, err = s.StartOAuth(ctx, ws, user, req)
	require.NoError(t, err)
	s.validateOAuthMailbox = func(*domain.MailAccount, string) error { return ErrMailAuthentication }
	_, err = s.CompleteOAuth(ctx, ws, user, dto.CompleteMailOAuthRequest{State: started.State, Code: "fixture-code"})
	require.ErrorIs(t, err, ErrMailAuthentication)
	list, err := s.ListAccounts(ctx, ws)
	require.NoError(t, err)
	require.Empty(t, list)
	s.validateOAuthMailbox = func(*domain.MailAccount, string) error { return nil }
	started, err = s.StartOAuth(ctx, ws, user, req)
	require.NoError(t, err)
	account, err := s.CompleteOAuth(ctx, ws, user, dto.CompleteMailOAuthRequest{State: started.State, Code: "fixture-code"})
	require.NoError(t, err)
	fixture.denied.Store(true)
	_, err = s.credential(ctx, account)
	require.ErrorIs(t, err, mailoauth.ErrAuthorization)
	var ciphertext string
	require.NoError(t, db.Get(&ciphertext, `SELECT token_encrypted FROM mail_oauth_tokens WHERE account_id=$1`, account.ID))
	require.Empty(t, ciphertext)
	stored, err := s.accountRepo.GetByID(ctx, ws, account.ID)
	require.NoError(t, err)
	require.NotEmpty(t, stored.LastSyncError)
	_, err = s.credential(ctx, account)
	require.ErrorIs(t, err, mailoauth.ErrAuthorization)
	require.EqualValues(t, 1, fixture.refreshes.Load())
	// Reconnect attempts by another member cannot claim the owner's grant.
	req.AccountID = account.ID.String()
	_, err = s.StartOAuth(ctx, ws, uuid.New(), req)
	require.ErrorIs(t, err, repository.ErrMailAccountNotFound)
	started, err = s.StartOAuth(ctx, ws, user, req)
	require.NoError(t, err)
	// A concurrent edit makes the old authorization stale; preserve that edit.
	name := "Concurrent change"
	_, err = s.UpdateAccount(ctx, ws, account.ID, dto.UpdateMailAccountRequest{DisplayName: &name})
	require.NoError(t, err)
	_, err = s.CompleteOAuth(ctx, ws, user, dto.CompleteMailOAuthRequest{State: started.State, Code: "fixture-code"})
	require.ErrorIs(t, err, repository.ErrMailAccountConflict)
	stored, err = s.accountRepo.GetByID(ctx, ws, account.ID)
	require.NoError(t, err)
	require.Equal(t, name, stored.DisplayName)
	require.NoError(t, s.DeleteAccount(ctx, ws, account.ID))
}

func TestMailOAuthUpgradePreservesMailboxAndRejectsStalePasswordEdits(t *testing.T) {
	_, s, ws, user, _ := oauthTestService(t)
	ctx := context.Background()
	config, err := mailconfig.NewDiscoverer().Discover(ctx, "fixture@gmail.com")
	require.NoError(t, err)
	account := accountFromConfig(ws, user, config, "Existing mailbox", "Existing signature")
	account.AuthMethod = "password"
	account.PasswordEncrypted, err = crypto.Encrypt("old-password", s.encryptKey)
	require.NoError(t, err)
	require.NoError(t, s.accountRepo.Create(ctx, account))
	stored, err := s.accountRepo.GetByID(ctx, ws, account.ID)
	require.NoError(t, err)
	draft, err := s.CreateDraft(ctx, ws, user, dto.CreateMailDraftRequest{AccountID: account.ID.String(), Subject: "Saved draft"})
	require.NoError(t, err)
	started, err := s.StartOAuth(ctx, ws, user, dto.StartMailOAuthRequest{EmailAddress: account.EmailAddress, AccountID: account.ID.String()})
	require.NoError(t, err)
	// A normal sync during consent must not invalidate a pending authorization.
	require.NoError(t, s.messageRepo.UpdateSyncState(ctx, account.ID, 1, 0, "", time.Now()))
	upgraded, err := s.CompleteOAuth(ctx, ws, user, dto.CompleteMailOAuthRequest{State: started.State, Code: "fixture-code"})
	require.NoError(t, err)
	require.Equal(t, account.ID, upgraded.ID)
	require.Equal(t, "oauth2", upgraded.AuthMethod)
	require.Equal(t, "Existing mailbox", upgraded.DisplayName)
	require.Equal(t, "Existing signature", upgraded.Signature)
	require.Empty(t, upgraded.PasswordEncrypted)
	keptDraft, _, err := s.GetDraft(ctx, ws, user, draft.ID)
	require.NoError(t, err)
	require.Equal(t, "Saved draft", keptDraft.Subject)
	stored.IMAPHost, stored.SMTPHost = "attacker.example", "attacker.example"
	require.ErrorIs(t, s.accountRepo.Update(ctx, stored), repository.ErrMailAccountConflict)
	require.ErrorIs(t, s.accountRepo.Delete(ctx, ws, account.ID), repository.ErrMailAccountNotFound)
	upgraded, err = s.accountRepo.GetByID(ctx, ws, account.ID)
	require.NoError(t, err)
	require.Equal(t, "imap.gmail.com", upgraded.IMAPHost)
	require.NoError(t, s.DeleteAccount(ctx, ws, account.ID))
}
