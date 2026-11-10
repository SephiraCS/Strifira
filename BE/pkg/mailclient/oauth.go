package mailclient

import (
	"net/smtp"
	"strings"

	"github.com/emersion/go-imap/client"
)

// XOAUTH2 is supported by both Gmail and Exchange IMAP/SMTP. A bearer token
// must never be passed as a LOGIN/PLAIN password or retried as one.
type xoauth2 struct{ username, token string }

func (a xoauth2) initial() ([]byte, error) {
	if a.username == "" || a.token == "" || strings.ContainsAny(a.username+a.token, "\r\n\x00\x01") {
		return nil, ErrAuthentication
	}
	return []byte("user=" + a.username + "\x01auth=Bearer " + a.token + "\x01\x01"), nil
}

type imapOAuth struct{ xoauth2 }

func (a imapOAuth) Start() (string, []byte, error) { b, err := a.initial(); return "XOAUTH2", b, err }
func (a imapOAuth) Next([]byte) ([]byte, error)    { return []byte{}, nil }

func authenticateIMAP(c *client.Client, opts FetchOptions) error {
	if opts.AccessToken != "" {
		return c.Authenticate(imapOAuth{xoauth2{opts.Username, opts.AccessToken}})
	}
	return c.Login(opts.Username, opts.Password)
}

type smtpOAuth struct{ xoauth2 }

func (a smtpOAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	b, err := a.initial()
	return "XOAUTH2", b, err
}
func (a smtpOAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return []byte{}, nil
	}
	return nil, nil
}

func smtpAuth(opts SendOptions) smtp.Auth {
	if opts.AccessToken != "" {
		return smtpOAuth{xoauth2{opts.Username, opts.AccessToken}}
	}
	return smtp.PlainAuth("", opts.Username, opts.Password, opts.Host)
}
