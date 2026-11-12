package service

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/SephiraCS/strifira-backend/pkg/mailclient"
	"github.com/SephiraCS/strifira-backend/pkg/sanitize"
	"github.com/emersion/go-imap/backend/memory"
	"github.com/emersion/go-imap/server"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

var testMailImage, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lJkAAAAASUVORK5CYII=")

func TestMailImagesSyncRepairPersistenceAndOwnership(t *testing.T) {
	db, s, ws, user := removalFixture(t)
	provider := memory.New()
	u, err := provider.Login(nil, "username", "password")
	require.NoError(t, err)
	box, err := u.GetMailbox("INBOX")
	require.NoError(t, err)
	raw := "From: sender@example.test\r\nMessage-ID: <image-sync@test>\r\nSubject: MIME image\r\nMIME-Version: 1.0\r\nContent-Type: multipart/related; boundary=test\r\n\r\n--test\r\nContent-Type: text/html\r\n\r\n<p>Readable text</p><img src=\"cid:logo%40test\" alt=\"Logo\"><img src=\"cid:missing\"><script>alert(1)</script>\r\n--test\r\nContent-Type: image/png\r\nContent-ID: <logo@test>\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString(testMailImage) + "\r\n--test--\r\n"
	require.NoError(t, box.CreateMessage(nil, time.Now(), strings.NewReader(raw)))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	imap := server.New(provider)
	imap.AllowInsecureAuth = true
	go func() { _ = imap.Serve(listener) }()
	t.Cleanup(func() { _ = imap.Close() })
	a := removalAccount(t, s, ws, user, listener.Addr().(*net.TCPAddr).Port)
	ctx := context.Background()
	require.NoError(t, s.SyncNow(ctx, ws, a.ID))
	messages, err := s.ListMessages(ctx, ws, MailListQuery{})
	require.NoError(t, err)
	var id uuid.UUID
	for _, m := range messages {
		if m.Subject == "MIME image" {
			id = m.ID
		}
	}
	require.NotEqual(t, uuid.Nil, id)
	detail, attachments, err := s.GetMessage(ctx, ws, id)
	require.NoError(t, err)
	require.Len(t, attachments, 1)
	source := "attachment:" + attachments[0].ID.String()
	require.Contains(t, detail.BodyHTML, source)
	require.NotContains(t, detail.BodyHTML, "<script")
	data, err := s.GetMessageImage(ctx, ws, id, source)
	require.NoError(t, err)
	require.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(testMailImage), data)
	_, err = s.GetMessageImage(ctx, uuid.New(), id, source)
	require.ErrorIs(t, err, repository.ErrMailMessageNotFound)
	_, err = s.GetMessageImage(ctx, ws, id, "https://unlisted.example.test/x")
	require.ErrorIs(t, err, ErrMailImageInvalid)
	// A different message in the same account cannot claim the cached image ID.
	other := uuid.New()
	_, err = db.Exec(`INSERT INTO mail_messages(id,workspace_id,account_id,thread_key,body_html) VALUES($1,$2,$3,'other',$4)`, other, ws, a.ID, `<img data-mail-image="`+source+`">`)
	require.NoError(t, err)
	_, err = s.GetMessageImage(ctx, ws, other, source)
	require.ErrorIs(t, err, repository.ErrMailMessageNotFound)
	// Recreate the old stored representation. Recovery must preserve message and
	// attachment IDs, flags, sync cursor and remote unread flags.
	_, err = db.Exec(`UPDATE mail_messages SET body_html='<p>Readable text</p><img alt="Logo">',starred=true,unread=false WHERE id=$1`, id)
	require.NoError(t, err)
	validity, lastUID, err := s.messageRepo.SyncState(ctx, a.ID)
	require.NoError(t, err)
	repaired, after, err := s.GetMessage(ctx, ws, id)
	require.NoError(t, err)
	require.Contains(t, repaired.BodyHTML, source)
	require.True(t, repaired.Starred)
	require.False(t, repaired.Unread)
	require.Equal(t, attachments[0].ID, after[0].ID)
	reopened, _, err := s.GetMessage(ctx, ws, id)
	require.NoError(t, err)
	require.Equal(t, repaired.BodyHTML, reopened.BodyHTML)
	nextValidity, nextUID, err := s.messageRepo.SyncState(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, validity, nextValidity)
	require.Equal(t, lastUID, nextUID)
	original, err := mailclient.FetchImageSource(ctx, s.fetchOptions(a, "password"), uint32(repaired.UID), validity)
	require.NoError(t, err)
	require.Contains(t, original.BodyHTML, "cid:logo%40test")
	_, err = mailclient.FetchImageSource(ctx, s.fetchOptions(a, "password"), uint32(repaired.UID), validity+1)
	require.Error(t, err)
	require.NoError(t, s.DeleteAccount(ctx, ws, a.ID))
	_, err = s.GetMessageImage(ctx, ws, id, source)
	require.ErrorIs(t, err, repository.ErrMailMessageNotFound)
}

func TestMailImagesRemoteSecurityAndFailures(t *testing.T) {
	db, s, ws, user := removalFixture(t)
	a := removalAccount(t, s, ws, user, 1)
	ctx := context.Background()
	id := uuid.New()
	requests := 0
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		require.Empty(t, r.Header.Get("Cookie"))
		require.Empty(t, r.Header.Get("Authorization"))
		require.Empty(t, r.Header.Get("Referer"))
		switch r.URL.Path {
		case "/ok":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write(testMailImage)
		case "/svg":
			_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`))
		case "/html":
			_, _ = w.Write([]byte("<script>alert(1)</script>"))
		case "/redirect":
			http.Redirect(w, r, "http://127.0.0.1/private", 302)
		case "/big":
			_, _ = w.Write(make([]byte, mailImageMaxBytes+1))
		default:
			w.WriteHeader(404)
		}
	}))
	defer provider.Close()
	// Test-only DNS/port mapping reaches an isolated TLS server. Production uses
	// the public-only dialer, and TLS uses the test server's trusted CA.
	client := provider.Client()
	transport := client.Transport.(*http.Transport)
	transport.TLSClientConfig.ServerName = "example.com"
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, provider.Listener.Addr().String())
	}
	client.CheckRedirect = mailImageHTTP.CheckRedirect
	s.imageHTTP = client
	for _, scenario := range []struct {
		path string
		want error
	}{{"/ok", nil}, {"/svg", ErrMailImageBlocked}, {"/html", ErrMailImageBlocked}, {"/redirect", ErrMailImageBlocked}, {"/big", ErrMailImageUnavailable}, {"/missing", ErrMailImageUnavailable}} {
		source := "https://images.example.test" + scenario.path
		body := sanitize.PrepareMailHTML(`<img src="`+source+`">`, nil)
		_, err := db.Exec(`INSERT INTO mail_messages(id,workspace_id,account_id,thread_key,body_html) VALUES($1,$2,$3,'remote',$4) ON CONFLICT(id) DO UPDATE SET body_html=$4`, id, ws, a.ID, body)
		require.NoError(t, err)
		data, err := s.GetMessageImage(ctx, ws, id, source)
		if scenario.want == nil {
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(data, "data:image/png;base64,"))
		} else {
			require.ErrorIs(t, err, scenario.want)
		}
	}
	require.Equal(t, 6, requests, "the insecure redirect must not be followed")
	s.imageHTTP = mailImageHTTP
	for _, source := range []string{"https://127.0.0.1/x", "https://169.254.169.254/latest/meta-data", "https://[::1]/x"} {
		_, err := db.Exec(`UPDATE mail_messages SET body_html=$1 WHERE id=$2`, sanitize.PrepareMailHTML(`<img src="`+source+`">`, nil), id)
		require.NoError(t, err)
		_, err = s.GetMessageImage(ctx, ws, id, source)
		require.ErrorIs(t, err, ErrMailImageBlocked)
	}
	// TLS verification remains active with a public destination mapped to a
	// local provider but without trusting its certificate.
	untrusted := &http.Client{Transport: &http.Transport{DialContext: transport.DialContext}, CheckRedirect: mailImageHTTP.CheckRedirect}
	s.imageHTTP = untrusted
	source := "https://images.example.test/ok"
	_, err := db.Exec(`UPDATE mail_messages SET body_html=$1 WHERE id=$2`, sanitize.PrepareMailHTML(`<img src="`+source+`">`, nil), id)
	require.NoError(t, err)
	_, err = s.GetMessageImage(ctx, ws, id, source)
	require.ErrorIs(t, err, ErrMailImageUnavailable)
}
