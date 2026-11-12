package mailclient

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"time"

	"github.com/SephiraCS/strifira-backend/pkg/mailconfig"
)

func dialMail(ctx context.Context, host string, port int, security string) (net.Conn, error) {
	address := mailconfig.ServerAddress(host, port)
	var conn net.Conn
	var err error
	if security == SecurityNone && isLoopbackHost(host) {
		// Existing loopback-only transport for integration test mail servers.
		d := net.Dialer{Timeout: dialTimeout}
		conn, err = d.DialContext(ctx, "tcp", address)
	} else if security == SecurityTLS || security == SecurityStartTLS {
		conn, err = mailconfig.PublicDialContext(ctx, "tcp", address)
	} else {
		return nil, errors.New("mail requires TLS or STARTTLS")
	}
	if err != nil {
		return nil, err
	}
	// Cancellation must also interrupt protocol reads after dialing, including
	// a server that accepted the connection but never sent its greeting.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	if ctx.Err() != nil {
		stop()
		_ = conn.Close()
		return nil, ctx.Err()
	}
	// Includes greeting, TLS handshake, authentication and protocol commands.
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))
	if security == SecurityTLS {
		secure := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err := secure.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return secure, nil
	}
	return conn, nil
}

func authenticationFailure(err error) error {
	var networkError net.Error
	if errors.As(err, &networkError) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return err
	}
	return ErrAuthentication
}
