package mailclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"net/smtp"
	"strings"
	"time"

	messageMail "github.com/emersion/go-message/mail"
)

// OutgoingMessage is a message ready to send.
type OutgoingMessage struct {
	From        messageMail.Address
	To          []messageMail.Address
	CC          []messageMail.Address
	BCC         []messageMail.Address
	Subject     string
	Body        string
	MessageID   string
	InReplyTo   string
	References  []string
	Attachments []ParsedAttachment
}

// SendOptions are the SMTP connection parameters of an account.
type SendOptions struct {
	Host        string
	Port        int
	Security    string // none | starttls | tls
	Username    string
	Password    string
	AccessToken string
}

// SendMessage opens an SMTP connection, transmits msg and returns the raw
// RFC 5322 bytes as delivered, so the caller can archive the exact message.
// Plaintext auth is only allowed to loopback hosts: credentials never leave
// the machine unencrypted by TLS.
func SendMessage(opts SendOptions, msg *OutgoingMessage) ([]byte, error) {
	if msg.MessageID == "" {
		msg.MessageID = FallbackMessageID()
	}
	raw, err := RenderMessage(msg)
	if err != nil {
		return nil, fmt.Errorf("render message: %w", err)
	}

	client, err := dialSMTP(opts)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.Auth(smtpAuth(opts)); err != nil {
		return nil, authenticationFailure(err)
	}
	if err := client.Mail(msg.From.Address); err != nil {
		return nil, fmt.Errorf("smtp mail from: %w", err)
	}
	for _, rcpt := range allRecipients(msg) {
		if err := client.Rcpt(rcpt.Address); err != nil {
			return nil, fmt.Errorf("smtp rcpt %s: %w", rcpt.Address, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return nil, fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return nil, fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("smtp close data: %w", err)
	}
	if err := client.Quit(); err != nil {
		// Delivery already happened; a failed quit is not a failed send.
		_ = client.Close()
	}
	return raw, nil
}

// TestSMTPConnection verifies credentials without sending anything.
func TestSMTPConnection(opts SendOptions) error {
	client, err := dialSMTP(opts)
	if err != nil {
		return err
	}
	defer client.Close()
	if err := client.Auth(smtpAuth(opts)); err != nil {
		return authenticationFailure(err)
	}
	return client.Quit()
}

func allRecipients(msg *OutgoingMessage) []messageMail.Address {
	out := make([]messageMail.Address, 0, len(msg.To)+len(msg.CC)+len(msg.BCC))
	out = append(out, msg.To...)
	out = append(out, msg.CC...)
	out = append(out, msg.BCC...)
	return out
}

func addressPointers(in []messageMail.Address) []*messageMail.Address {
	out := make([]*messageMail.Address, 0, len(in))
	for i := range in {
		out = append(out, &in[i])
	}
	return out
}

func dialSMTP(opts SendOptions) (*smtp.Client, error) {
	conn, err := dialMail(context.Background(), opts.Host, opts.Port, opts.Security)
	if err != nil {
		return nil, err
	}
	c, err := smtp.NewClient(conn, opts.Host)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if opts.Security == SecurityStartTLS {
		if err := c.StartTLS(&tls.Config{ServerName: opts.Host, MinVersion: tls.VersionTLS12}); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	return c, nil
}

func isLoopbackHost(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	return h == "localhost" || strings.HasPrefix(h, "127.") || h == "::1"
}

// RenderMessage builds the RFC 5322 bytes for an outgoing message: a
// multipart/mixed message with a quoted-printable plain-text body and
// base64 attachments.
func RenderMessage(msg *OutgoingMessage) ([]byte, error) {
	date := time.Now()

	header := messageMail.Header{}
	header.SetDate(date)
	header.SetAddressList("From", []*messageMail.Address{&msg.From})
	if len(msg.To) > 0 {
		header.SetAddressList("To", addressPointers(msg.To))
	}
	if len(msg.CC) > 0 {
		header.SetAddressList("Cc", addressPointers(msg.CC))
	}
	header.SetSubject(msg.Subject)
	header.SetMessageID(msg.MessageID)
	if msg.InReplyTo != "" {
		header.Set("In-Reply-To", wrapMessageID(msg.InReplyTo))
	}
	if len(msg.References) > 0 {
		header.SetMsgIDList("References", msg.References)
	}

	var buf bytes.Buffer
	if len(msg.Attachments) == 0 {
		// Single-part text message: the writer handles the MIME header.
		// go-message applies the Content-Transfer-Encoding itself, so the
		// body must be written as raw UTF-8 (never pre-encoded).
		bodyWriter, err := messageMail.CreateSingleInlineWriter(&buf, header)
		if err != nil {
			return nil, fmt.Errorf("create mime writer: %w", err)
		}
		if _, err := bodyWriter.Write([]byte(normalizeCRLF(msg.Body))); err != nil {
			return nil, err
		}
		if err := bodyWriter.Close(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}

	mw, err := messageMail.CreateWriter(&buf, header)
	if err != nil {
		return nil, fmt.Errorf("create mime writer: %w", err)
	}

	inline := messageMail.InlineHeader{}
	inline.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
	bodyWriter, err := mw.CreateSingleInline(inline)
	if err != nil {
		return nil, fmt.Errorf("create body part: %w", err)
	}
	if _, err := bodyWriter.Write([]byte(normalizeCRLF(msg.Body))); err != nil {
		return nil, err
	}
	if err := bodyWriter.Close(); err != nil {
		return nil, err
	}

	for _, att := range msg.Attachments {
		filename := att.Filename
		if filename == "" {
			filename = "attachment"
		}
		attachmentHeader := messageMail.AttachmentHeader{}
		attachmentHeader.SetContentType(att.ContentType, nil)
		attachmentHeader.SetFilename(filename)
		attachmentWriter, err := mw.CreateAttachment(attachmentHeader)
		if err != nil {
			return nil, fmt.Errorf("create attachment part: %w", err)
		}
		// The writer base64-encodes per the header it generated; raw bytes in.
		if _, err := attachmentWriter.Write(att.Content); err != nil {
			return nil, fmt.Errorf("write attachment: %w", err)
		}
		if err := attachmentWriter.Close(); err != nil {
			return nil, err
		}
	}

	if err := mw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func wrapMessageID(id string) string {
	if strings.HasPrefix(id, "<") {
		return id
	}
	return "<" + id + ">"
}

func normalizeCRLF(body string) string {
	return strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")
}
