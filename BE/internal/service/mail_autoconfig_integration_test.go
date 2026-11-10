package service

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/realtime"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/SephiraCS/strifira-backend/pkg/crypto"
	"github.com/SephiraCS/strifira-backend/pkg/mailconfig"
	"github.com/emersion/go-imap/backend/memory"
	"github.com/emersion/go-imap/server"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

// A discovered configuration for loopback protocol servers belongs only to
// this test. Production discovery never returns plaintext or private hosts.
type testMailDiscovery struct {
	config *mailconfig.Config
	err    error
}

func (d testMailDiscovery) Discover(context.Context, string) (*mailconfig.Config, error) {
	return d.config, d.err
}

func TestMailAutoconfigValidatesPersistsReceivesAndSends(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not configured")
	}
	db, err := sqlx.Connect("pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	wsID, userID := uuid.New(), uuid.New()
	_, err = db.Exec(`INSERT INTO users (id,email,name,password_hash) VALUES ($1,$2,'Mail test','unused')`, userID, userID.String()+"@example.test")
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id=$1`, userID) })
	_, err = db.Exec(`INSERT INTO workspaces (id,name,slug,owner_id) VALUES ($1,'Mail test',$2,$3)`, wsID, "mail-"+wsID.String(), userID)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, wsID) })

	imapListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	imapServer := server.New(memory.New())
	imapServer.AllowInsecureAuth = true
	go func() { _ = imapServer.Serve(imapListener) }()
	t.Cleanup(func() { _ = imapServer.Close() })
	smtpPort, rejectSMTP, delivered := mailTestSMTP(t)
	_, imapPortText, _ := net.SplitHostPort(imapListener.Addr().String())
	imapPort, _ := strconv.Atoi(imapPortText)
	config := &mailconfig.Config{EmailAddress: "mail@example.test", Provider: "Test", Authentication: "password",
		Incoming: mailconfig.Server{Host: "127.0.0.1", Port: imapPort, Security: "none", Username: "username"},
		Outgoing: mailconfig.Server{Host: "127.0.0.1", Port: smtpPort, Security: "none", Username: "smtp-user"}}
	accounts := repository.NewMailAccountRepository(db)
	svc := NewMailService(accounts, repository.NewMailMessageRepository(db), repository.NewMailDraftRepository(db), realtime.NewHub(), []byte(strings.Repeat("k", 32)))
	svc.discovery = testMailDiscovery{config: config}
	ctx := context.Background()
	req := dto.CreateMailAccountRequest{EmailAddress: config.EmailAddress, Password: "wrong-password"}
	_, err = svc.CreateAccount(ctx, wsID, userID, req)
	require.ErrorIs(t, err, ErrMailAuthentication)
	require.NotContains(t, err.Error(), req.Password)
	list, err := accounts.ListByWorkspace(ctx, wsID)
	require.NoError(t, err)
	require.Empty(t, list)

	// A successful IMAP login cannot conceal refused SMTP authentication.
	req.Password = "password"
	rejectSMTP.Store(true)
	_, err = svc.CreateAccount(ctx, wsID, userID, req)
	require.ErrorIs(t, err, ErrMailAuthentication)
	list, err = accounts.ListByWorkspace(ctx, wsID)
	require.NoError(t, err)
	require.Empty(t, list)
	rejectSMTP.Store(false)
	account, err := svc.CreateAccount(ctx, wsID, userID, req)
	require.NoError(t, err)
	require.NotEqual(t, "password", account.PasswordEncrypted)
	require.Equal(t, "smtp-user", account.SMTPUsername)
	password, err := crypto.Decrypt(account.PasswordEncrypted, svc.encryptKey)
	require.NoError(t, err)
	require.Equal(t, "password", password)
	// Wait for the actual first sync to finish before reading or cleaning up.
	require.Eventually(t, func() bool {
		stored, e := accounts.GetByID(ctx, wsID, account.ID)
		return e == nil && stored.LastSyncedAt != nil
	}, 5*time.Second, 10*time.Millisecond)
	messages, err := svc.ListMessages(ctx, wsID, MailListQuery{Folder: "inbox"})
	require.NoError(t, err)
	require.Len(t, messages, 1)

	draft, err := svc.CreateDraft(ctx, wsID, userID, dto.CreateMailDraftRequest{AccountID: account.ID.String(), To: []dto.MailAddress{{Address: "recipient@example.test"}}, Subject: "Autoconfig test", Body: "Sent through the discovered server"})
	require.NoError(t, err)
	sent, err := svc.SendDraft(ctx, wsID, userID, draft.ID)
	require.NoError(t, err)
	require.Equal(t, "Autoconfig test", sent.Subject)
	select {
	case raw := <-delivered:
		require.Contains(t, raw, "Sent through the discovered server")
	case <-time.After(time.Second):
		t.Fatal("SMTP did not receive the message")
	}

	// Failed updates leave both the linked address and ciphertext untouched.
	bad := "wrong-password"
	_, err = svc.UpdateAccount(ctx, wsID, account.ID, dto.UpdateMailAccountRequest{Password: &bad})
	require.ErrorIs(t, err, ErrMailAuthentication)
	_, err = svc.UpdateAccount(ctx, wsID, account.ID, dto.UpdateMailAccountRequest{EmailAddress: "new@example.test"})
	require.ErrorIs(t, err, ErrMailCredentialRequired)
	stored, err := accounts.GetByID(ctx, wsID, account.ID)
	require.NoError(t, err)
	require.Equal(t, account.EmailAddress, stored.EmailAddress)
	require.Equal(t, account.PasswordEncrypted, stored.PasswordEncrypted)
	svc.discovery = testMailDiscovery{err: mailconfig.ErrNotFound}
	_, err = svc.CreateAccount(ctx, wsID, userID, req)
	require.ErrorIs(t, err, mailconfig.ErrNotFound)
	svc.discovery = testMailDiscovery{config: &mailconfig.Config{Authentication: "oauth2"}}
	_, err = svc.CreateAccount(ctx, wsID, userID, req)
	require.ErrorIs(t, err, ErrMailOAuthRequired)
	list, err = accounts.ListByWorkspace(ctx, wsID)
	require.NoError(t, err)
	require.Len(t, list, 1)
}

// A real loopback SMTP conversation, including authentication and DATA.
// Credentials and the test mailbox never reach an external provider.
func mailTestSMTP(t *testing.T) (int, *atomic.Bool, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var reject atomic.Bool
	delivered := make(chan string, 2)
	var clients sync.WaitGroup
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			clients.Add(1)
			go func() {
				defer clients.Done()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				reader := textproto.NewReader(bufio.NewReader(conn))
				write := func(s string) { _, _ = io.WriteString(conn, s+"\r\n") }
				write("220 localhost test SMTP")
				authenticated := false
				for {
					line, err := reader.ReadLine()
					if err != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"):
						write("250-localhost\r\n250 AUTH PLAIN")
					case strings.HasPrefix(line, "AUTH PLAIN "):
						decoded, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
						authenticated = !reject.Load() && string(decoded) == "\x00smtp-user\x00password"
						if authenticated {
							write("235 authenticated")
						} else {
							write("535 refused secret-provider-detail")
						}
					case line == "QUIT":
						write("221 goodbye")
						return
					case !authenticated:
						write("530 authentication required")
					case line == "DATA":
						write("354 send data")
						body, err := reader.ReadDotBytes()
						if err != nil {
							return
						}
						delivered <- string(body)
						write("250 accepted")
					default:
						write("250 OK")
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-stopped; clients.Wait() })
	return listener.Addr().(*net.TCPAddr).Port, &reject, delivered
}
