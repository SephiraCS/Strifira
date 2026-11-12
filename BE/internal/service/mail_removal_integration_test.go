package service

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/realtime"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/SephiraCS/strifira-backend/pkg/crypto"
	"github.com/SephiraCS/strifira-backend/pkg/mailclient"
	"github.com/emersion/go-imap/backend/memory"
	"github.com/emersion/go-imap/server"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

func removalFixture(t *testing.T) (*sqlx.DB, *MailService, uuid.UUID, uuid.UUID) {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL is not configured")
	}
	db, err := sqlx.Connect("pgx", os.Getenv("DATABASE_URL"))
	require.NoError(t, err)
	db.SetMaxOpenConns(10)
	t.Cleanup(func() { _ = db.Close() })
	ws, user := uuid.New(), uuid.New()
	_, err = db.Exec(`INSERT INTO users(id,email,name,password_hash) VALUES($1,$2,'Removal fixture','unused')`, user, user.String()+"@example.test")
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id=$1`, user) })
	_, err = db.Exec(`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'Removal fixture',$2,$3)`, ws, "removal-"+ws.String(), user)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, ws) })
	svc := NewMailService(repository.NewMailAccountRepository(db), repository.NewMailMessageRepository(db), repository.NewMailDraftRepository(db), realtime.NewHub(), []byte(strings.Repeat("r", 32)))
	return db, svc, ws, user
}

func removalAccount(t *testing.T, s *MailService, ws, user uuid.UUID, port int) *domain.MailAccount {
	t.Helper()
	encrypted, err := crypto.Encrypt("password", s.encryptKey)
	require.NoError(t, err)
	account := &domain.MailAccount{ID: uuid.New(), WorkspaceID: ws, CreatedBy: user, EmailAddress: uuid.NewString() + "@example.test", AuthMethod: "password", IMAPHost: "127.0.0.1", IMAPPort: port, IMAPSecurity: "none", SMTPHost: "127.0.0.1", SMTPPort: port, SMTPSecurity: "none", Username: "username", PasswordEncrypted: encrypted}
	require.NoError(t, s.accountRepo.Create(context.Background(), account))
	return account
}

func TestMailRemovalClearsOnlyLinkedDataAndPreservesProvider(t *testing.T) {
	db, s, ws, user := removalFixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	imapServer := server.New(memory.New())
	imapServer.AllowInsecureAuth = true
	go func() { _ = imapServer.Serve(listener) }()
	t.Cleanup(func() { _ = imapServer.Close() })
	port := listener.Addr().(*net.TCPAddr).Port
	account := removalAccount(t, s, ws, user, port)
	other := removalAccount(t, s, ws, user, port)
	ctx := context.Background()
	require.NoError(t, s.SyncNow(ctx, ws, account.ID))
	require.NoError(t, s.SyncNow(ctx, ws, other.ID))
	messages, err := s.ListMessages(ctx, ws, MailListQuery{AccountID: &account.ID})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	messageID := messages[0].ID
	draft, err := s.CreateDraft(ctx, ws, user, dto.CreateMailDraftRequest{AccountID: account.ID.String(), Body: "Local draft"})
	require.NoError(t, err)
	otherDraft, err := s.CreateDraft(ctx, ws, user, dto.CreateMailDraftRequest{AccountID: other.ID.String(), Body: "Keep this draft"})
	require.NoError(t, err)
	_, err = s.AddDraftAttachment(ctx, ws, user, draft.ID, "draft.txt", "text/plain", []byte("local"))
	require.NoError(t, err)
	attachmentID := uuid.New()
	_, err = db.Exec(`INSERT INTO mail_attachments(id,message_id,filename,content_type,size_bytes,content) VALUES($1,$2,'mail.txt','text/plain',3,'abc')`, attachmentID, messageID)
	require.NoError(t, err)
	// The same id in a different workspace cannot even cancel an operation.
	require.ErrorIs(t, s.DeleteAccount(ctx, uuid.New(), account.ID), repository.ErrMailAccountNotFound)
	require.NoError(t, s.DeleteAccount(ctx, ws, account.ID))
	_, err = s.accountRepo.GetByID(ctx, ws, account.ID)
	require.ErrorIs(t, err, repository.ErrMailAccountNotFound)
	_, err = s.GetMessageAttachment(ctx, ws, attachmentID)
	require.ErrorIs(t, err, repository.ErrMailMessageNotFound)
	var count int
	require.NoError(t, db.Get(&count, `SELECT count(*) FROM mail_messages WHERE account_id=$1`, account.ID))
	require.Zero(t, count)
	require.NoError(t, db.Get(&count, `SELECT count(*) FROM mail_drafts WHERE account_id=$1`, account.ID))
	require.Zero(t, count)
	require.NoError(t, db.Get(&count, `SELECT count(*) FROM mail_attachments WHERE message_id=$1 OR draft_id=$2`, messageID, draft.ID))
	require.Zero(t, count)
	_, err = s.draftRepo.GetByID(ctx, ws, user, otherDraft.ID)
	require.NoError(t, err)
	messages, err = s.ListMessages(ctx, ws, MailListQuery{AccountID: &other.ID})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	// A poller retaining the old account snapshot cannot reopen IMAP.
	require.ErrorIs(t, s.syncAccount(ctx, *account), repository.ErrMailAccountNotFound)
	_, err = s.SendDraft(ctx, ws, user, draft.ID)
	require.ErrorIs(t, err, repository.ErrMailDraftNotFound)
	// Read the real test provider again; unlinking never deletes remote mail.
	remote, err := mailclient.FetchNewMessages(s.fetchOptions(account, "password"), 0, 0)
	require.NoError(t, err)
	require.Len(t, remote.Messages, 1)
}

func TestMailRemovalCancelsActiveProviderConnectionBeforeReturning(t *testing.T) {
	for _, protocol := range []string{"IMAP", "SMTP"} {
		t.Run(protocol, func(t *testing.T) {
			_, s, ws, user := removalFixture(t)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			t.Cleanup(func() { _ = listener.Close() })
			accepted, closed := make(chan struct{}), make(chan struct{})
			// Deliberately stall before the IMAP greeting, exercising cancellation of
			// the actual socket, not just cancellation between repository calls.
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				close(accepted)
				_, _ = conn.Read(make([]byte, 1))
				close(closed)
			}()
			account := removalAccount(t, s, ws, user, listener.Addr().(*net.TCPAddr).Port)
			finished := make(chan error, 1)
			if protocol == "IMAP" {
				go func() { finished <- s.SyncNow(context.Background(), ws, account.ID) }()
			} else {
				draft, err := s.CreateDraft(context.Background(), ws, user, dto.CreateMailDraftRequest{AccountID: account.ID.String(), To: []dto.MailAddress{{Address: "recipient@example.test"}}, Body: "Never delivered"})
				require.NoError(t, err)
				go func() { _, err := s.SendDraft(context.Background(), ws, user, draft.ID); finished <- err }()
			}
			select {
			case <-accepted:
			case <-time.After(3 * time.Second):
				t.Fatal("IMAP was not opened")
			}
			// A request through the wrong workspace must leave this socket untouched.
			require.ErrorIs(t, s.DeleteAccount(context.Background(), uuid.New(), account.ID), repository.ErrMailAccountNotFound)
			select {
			case <-closed:
				t.Fatal("another workspace cancelled IMAP")
			default:
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			require.NoError(t, s.DeleteAccount(ctx, ws, account.ID))
			select {
			case <-closed:
			case <-ctx.Done():
				t.Fatal("unlink left IMAP open")
			}
			select {
			case err := <-finished:
				require.Error(t, err)
			case <-ctx.Done():
				t.Fatal("sync did not finish")
			}
			require.ErrorIs(t, s.SyncNow(ctx, ws, account.ID), repository.ErrMailAccountNotFound)
		})
	}
}

func TestMailRemovalWaitsForOtherProcessAndRejectsStaleAccess(t *testing.T) {
	db, s, ws, user := removalFixture(t)
	account := removalAccount(t, s, ws, user, 1)
	other := NewMailService(repository.NewMailAccountRepository(db), repository.NewMailMessageRepository(db), repository.NewMailDraftRepository(db), realtime.NewHub(), s.encryptKey)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- other.withAccountOperation(context.Background(), ws, account.ID, func(context.Context, *domain.MailAccount) error { close(entered); <-release; return nil })
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	removed := make(chan error, 1)
	go func() { removed <- s.DeleteAccount(ctx, ws, account.ID) }()
	// Observe the database wait rather than depending on a scheduling sleep.
	require.Eventually(t, func() bool {
		var n int
		err := db.Get(&n, `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted`)
		return err == nil && n > 0
	}, time.Second, 10*time.Millisecond)
	select {
	case err := <-removed:
		t.Fatalf("unlink finished before provider access: %v", err)
	default:
	}
	close(release)
	require.NoError(t, <-finished)
	require.NoError(t, <-removed)
	require.ErrorIs(t, other.syncAccount(ctx, *account), repository.ErrMailAccountNotFound)
}

func TestMailRemovalDatabaseFailurePreservesAccountAndAllowsRetry(t *testing.T) {
	db, s, ws, user := removalFixture(t)
	account := removalAccount(t, s, ws, user, 1)
	// A fixture-only FK veto makes the real DELETE fail without altering product code.
	table := "removal_veto_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err := db.Exec(`CREATE TABLE ` + table + ` (account_id uuid REFERENCES mail_accounts(id))`)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DROP TABLE IF EXISTS ` + table) })
	_, err = db.Exec(`INSERT INTO `+table+` VALUES($1)`, account.ID)
	require.NoError(t, err)
	require.Error(t, s.DeleteAccount(context.Background(), ws, account.ID))
	stored, err := s.accountRepo.GetByID(context.Background(), ws, account.ID)
	require.NoError(t, err)
	require.Equal(t, account.PasswordEncrypted, stored.PasswordEncrypted)
	_, err = db.Exec(`DROP TABLE ` + table)
	require.NoError(t, err)
	require.NoError(t, s.DeleteAccount(context.Background(), ws, account.ID))
}
