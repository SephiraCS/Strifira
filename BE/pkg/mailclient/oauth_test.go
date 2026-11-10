package mailclient

import (
	"bufio"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend/memory"
	"github.com/emersion/go-imap/server"
	messageMail "github.com/emersion/go-message/mail"
	"github.com/emersion/go-sasl"
	"github.com/stretchr/testify/require"
)

// These protocol servers exist only in tests. Production TLS/public-network
// validation is unchanged, and LOGIN/PLAIN cannot accidentally accept tokens.
type testOAuthSASL struct {
	conn    server.Conn
	backend *memory.Backend
}

func (s testOAuthSASL) Next(response []byte) ([]byte, bool, error) {
	if string(response) != "user=username\x01auth=Bearer accepted-token\x01\x01" {
		return nil, true, errors.New("provider-secret-detail")
	}
	user, err := s.backend.Login(s.conn.Info(), "username", "password")
	s.conn.Context().User = user
	s.conn.Context().State = imap.AuthenticatedState
	return nil, true, err
}

func TestOAuthIMAPAuthenticatesFetchesAndRejectsInvalidToken(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	backend := memory.New()
	srv := server.New(backend)
	srv.AllowInsecureAuth = true
	srv.EnableAuth("XOAUTH2", func(conn server.Conn) sasl.Server { return testOAuthSASL{conn, backend} })
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })
	opts := FetchOptions{Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, Security: SecurityNone, Username: "username", Password: "deliberately-invalid-password", AccessToken: "accepted-token"}
	require.NoError(t, TestIMAPConnection(opts))
	fetched, err := FetchNewMessages(opts, 0, 0)
	require.NoError(t, err)
	require.Len(t, fetched.Messages, 1)
	opts.AccessToken = "rejected-token"
	err = TestIMAPConnection(opts)
	require.ErrorIs(t, err, ErrAuthentication)
	require.NotContains(t, err.Error(), "provider-secret-detail")
}

func TestOAuthSMTPAuthenticatesSendsAndRejectsInvalidToken(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	delivered := make(chan string, 1)
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
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				r := textproto.NewReader(bufio.NewReader(conn))
				write := func(s string) { _, _ = io.WriteString(conn, s+"\r\n") }
				write("220 local test")
				authenticated := false
				for {
					line, err := r.ReadLine()
					if err != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"):
						write("250-localhost")
						write("250 AUTH XOAUTH2")
					case strings.HasPrefix(line, "AUTH XOAUTH2 "):
						decoded, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH XOAUTH2 "))
						if string(decoded) == "user=sender@example.test\x01auth=Bearer accepted-token\x01\x01" {
							authenticated = true
							write("235 authenticated")
						} else {
							// Gmail sends a challenge, then expects an empty ack.
							write("334 " + base64.StdEncoding.EncodeToString([]byte(`{"status":"401"}`)))
							ack, e := r.ReadLine()
							if e != nil || ack != "" {
								return
							}
							write("535 provider-secret-detail")
						}
					case line == "QUIT":
						write("221 bye")
						return
					case !authenticated:
						write("530 authenticate first")
					case line == "DATA":
						write("354 data")
						data, err := r.ReadDotBytes()
						if err != nil {
							return
						}
						delivered <- string(data)
						write("250 accepted")
					default:
						write("250 OK")
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-stopped; clients.Wait() })
	opts := SendOptions{Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, Security: SecurityNone, Username: "sender@example.test", AccessToken: "accepted-token", Password: "must-not-be-used"}
	require.NoError(t, TestSMTPConnection(opts))
	_, err = SendMessage(opts, &OutgoingMessage{From: messageMail.Address{Address: opts.Username}, To: []messageMail.Address{{Address: "receiver@example.test"}}, Subject: "OAuth test", Body: "OAuth delivery"})
	require.NoError(t, err)
	select {
	case raw := <-delivered:
		require.Contains(t, raw, "OAuth delivery")
	case <-time.After(time.Second):
		t.Fatal("message not delivered")
	}
	opts.AccessToken = "refused"
	err = TestSMTPConnection(opts)
	require.ErrorIs(t, err, ErrAuthentication)
	require.NotContains(t, err.Error(), "provider-secret-detail")
}
