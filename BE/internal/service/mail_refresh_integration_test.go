package service

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/realtime"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/emersion/go-imap/backend/memory"
	"github.com/emersion/go-imap/server"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMailRefreshSyncPublishesOnlyCommittedNewMessages(t *testing.T) {
	db, s, ws, user := removalFixture(t)
	provider := memory.New()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	imap := server.New(provider)
	imap.AllowInsecureAuth = true
	go func() { _ = imap.Serve(listener) }()
	t.Cleanup(func() { _ = imap.Close() })
	account := removalAccount(t, s, ws, user, listener.Addr().(*net.TCPAddr).Port)
	var events []realtime.Event
	s.hub.OnBroadcast = func(workspaceID uuid.UUID, event realtime.Event) {
		require.Equal(t, ws, workspaceID)
		if event.Type != "mail.updated" {
			return
		}
		payload := event.Payload.(map[string]any)
		if payload["deleted"] == true {
			return
		}
		require.Equal(t, account.ID.String(), payload["account_id"])
		// The listener must be able to read the newly committed cache immediately.
		messages, err := s.ListMessages(context.Background(), ws, MailListQuery{})
		require.NoError(t, err)
		require.NotEmpty(t, messages)
		events = append(events, event)
	}
	ctx := context.Background()
	s.SyncDueAccounts(ctx)
	require.Len(t, events, 1)
	require.NoError(t, s.SyncNow(ctx, ws, account.ID))
	require.Len(t, events, 1, "repeated sync must not announce or duplicate old messages")
	providerUser, err := provider.Login(nil, "username", "password")
	require.NoError(t, err)
	inbox, err := providerUser.GetMailbox("INBOX")
	require.NoError(t, err)
	require.NoError(t, inbox.CreateMessage(nil, time.Now(), strings.NewReader("From: sender@example.test\r\nTo: recipient@example.test\r\nMessage-ID: <refresh@example.test>\r\nSubject: Automatic arrival\r\n\r\nLocal test message")))
	_, err = db.Exec(`UPDATE mail_accounts SET last_synced_at = NOW() - INTERVAL '2 minutes' WHERE id=$1`, account.ID)
	require.NoError(t, err)
	s.SyncDueAccounts(ctx)
	require.Len(t, events, 2)
	require.Equal(t, 1, events[1].Payload.(map[string]any)["new_count"])
	messages, err := s.ListMessages(ctx, ws, MailListQuery{})
	require.NoError(t, err)
	require.Len(t, messages, 2)
	unread, err := s.CountUnreadInbox(ctx, ws, &account.ID)
	require.NoError(t, err)
	require.Equal(t, 1, unread)
	other, err := s.ListMessages(ctx, uuid.New(), MailListQuery{AccountID: &account.ID})
	require.NoError(t, err)
	require.Empty(t, other)
	// Preserve removal cancellation and the persisted account ownership check.
	require.NoError(t, s.DeleteAccount(ctx, ws, account.ID))
	require.ErrorIs(t, s.SyncNow(ctx, ws, account.ID), repository.ErrMailAccountNotFound)
}

func TestMailRefreshUnreadCountIgnoresSearchPageAndExcludedFolders(t *testing.T) {
	db, s, ws, user := removalFixture(t)
	account := removalAccount(t, s, ws, user, 1)
	other := removalAccount(t, s, ws, user, 1)
	_, err := db.Exec(`INSERT INTO mail_messages (workspace_id,account_id,thread_key,subject,unread,archived,spam,folder)
		SELECT $1,$2,'fixture','Unread fixture',true,false,false,'inbox' FROM generate_series(1,105)`, ws, account.ID)
	require.NoError(t, err)
	for _, flags := range []string{"false,false,false,'inbox'", "true,true,false,'inbox'", "true,false,true,'inbox'", "true,false,false,'sent'"} {
		_, err = db.Exec(`INSERT INTO mail_messages(workspace_id,account_id,thread_key,unread,archived,spam,folder) VALUES($1,$2,'fixture',`+flags+`)`, ws, account.ID)
		require.NoError(t, err)
	}
	_, err = db.Exec(`INSERT INTO mail_messages(workspace_id,account_id,thread_key,unread) VALUES($1,$2,'fixture',true)`, ws, other.ID)
	require.NoError(t, err)
	count, err := s.CountUnreadInbox(context.Background(), ws, &account.ID)
	require.NoError(t, err)
	require.Equal(t, 105, count)
	count, err = s.CountUnreadInbox(context.Background(), ws, nil)
	require.NoError(t, err)
	require.Equal(t, 106, count)
	count, err = s.CountUnreadInbox(context.Background(), uuid.New(), &account.ID)
	require.NoError(t, err)
	require.Zero(t, count)
}
