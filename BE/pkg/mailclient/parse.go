// Package mailclient talks to real IMAP and SMTP servers on behalf of a
// configured mail account: fetch new INBOX messages, send composed messages
// with attachments and parse/compose RFC 5322 bodies.
package mailclient

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/mail"
	"strings"

	"github.com/emersion/go-message"
	messageMail "github.com/emersion/go-message/mail"
	"github.com/google/uuid"
)

// Connection security modes shared by IMAP and SMTP.
const (
	SecurityNone     = "none"
	SecurityStartTLS = "starttls"
	SecurityTLS      = "tls"
)

// ParsedMessage is the subset of an RFC 5322 message the app stores.
type ParsedMessage struct {
	MessageID   string
	InReplyTo   string
	References  []string
	Subject     string
	From        messageMail.Address
	ReplyTo     string
	To          []messageMail.Address
	CC          []messageMail.Address
	BodyText    string
	BodyHTML    string
	Attachments []ParsedAttachment
}

// ParsedAttachment is one MIME attachment decoded into memory.
type ParsedAttachment struct {
	ContentID   string
	Filename    string
	ContentType string
	Content     []byte
}

// ParseMessage decodes a raw RFC 5322 message. Bodies are decoded to UTF-8
// where possible; malformed parts are skipped, never fatal.
func ParseMessage(raw []byte) (*ParsedMessage, error) {
	entity, err := message.Read(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("read message: %w", err)
	}
	header := entity.Header
	mailHeader := messageMail.Header{Header: header}

	parsed := &ParsedMessage{Subject: headerSubject(mailHeader)}
	if addrs := addressList(header, "From"); len(addrs) > 0 {
		parsed.From = addrs[0]
	}
	if addrs := addressList(header, "Reply-To"); len(addrs) > 0 {
		parsed.ReplyTo = addrs[0].Address
	}
	parsed.To = addressList(header, "To")
	parsed.CC = addressList(header, "Cc")
	if id, idErr := mailHeader.MessageID(); idErr == nil {
		parsed.MessageID = normalizeMessageID(id)
	}
	if inReplyTo, _ := mailHeader.MsgIDList("In-Reply-To"); len(inReplyTo) > 0 {
		parsed.InReplyTo = normalizeMessageID(inReplyTo[0])
	}
	if refs, _ := mailHeader.MsgIDList("References"); len(refs) > 0 {
		parsed.References = refs
	} else {
		parsed.References = parseReferences(header.Get("References"))
	}

	// Walk visits the root (multipart containers included) and every leaf.
	_ = entity.Walk(func(_ []int, part *message.Entity, walkErr error) error {
		if walkErr != nil || part == nil {
			return nil // a broken part never kills the message
		}
		walkLeaf(part, parsed)
		return nil
	})
	return parsed, nil
}

// walkLeaf classifies one entity: multipart containers are skipped (their
// leaves are visited separately), text leaves feed the body and everything
// else is kept as an attachment.
func walkLeaf(entity *message.Entity, parsed *ParsedMessage) {
	contentType, params, err := entity.Header.ContentType()
	if err != nil {
		// No Content-Type: the body is plain text (RFC 5322 §5.2.2).
		body, readErr := io.ReadAll(entity.Body)
		if readErr == nil {
			parsed.BodyText += string(body)
		}
		return
	}
	if strings.HasPrefix(strings.ToLower(contentType), "multipart/") {
		return // container: leaves arrive via Walk
	}

	body, err := io.ReadAll(entity.Body)
	if err != nil {
		return
	}

	disposition, dparams, _ := entity.Header.ContentDisposition()
	isFile := disposition == "attachment" ||
		((disposition == "inline" || disposition == "") && (dparams["filename"] != "" || params["name"] != ""))

	switch {
	case isFile:
		parsed.Attachments = append(parsed.Attachments, ParsedAttachment{
			ContentID:   normalizeMessageID(entity.Header.Get("Content-ID")),
			Filename:    filenameFrom(entity, params, dparams),
			ContentType: contentType,
			Content:     body,
		})
	case strings.EqualFold(contentType, "text/html"):
		parsed.BodyHTML += decodeCharset(params["charset"], body)
	case strings.HasPrefix(strings.ToLower(contentType), "text/"):
		parsed.BodyText += decodeCharset(params["charset"], body)
	default:
		// Binary leaf without a filename: keep it rather than dropping it.
		parsed.Attachments = append(parsed.Attachments, ParsedAttachment{
			ContentID:   normalizeMessageID(entity.Header.Get("Content-ID")),
			Filename:    filenameFrom(entity, params, dparams),
			ContentType: contentType,
			Content:     body,
		})
	}
}

func filenameFrom(entity *message.Entity, params, dparams map[string]string) string {
	if name := dparams["filename"]; name != "" {
		return name
	}
	if name := params["name"]; name != "" {
		return name
	}
	contentType, _, _ := entity.Header.ContentType()
	return contentType
}

// decodeCharset converts a decoded body to UTF-8. Charsets Go cannot
// convert natively pass through unchanged rather than failing the message.
func decodeCharset(charset string, body []byte) string {
	cs := strings.ToLower(strings.TrimSpace(charset))
	if cs == "" || cs == "utf-8" || cs == "utf8" || cs == "us-ascii" || cs == "ascii" {
		return string(body)
	}
	converted, err := io.ReadAll(charsetConvert(cs, bytes.NewReader(body)))
	if err != nil {
		return string(body)
	}
	return string(converted)
}

// headerSubject decodes RFC 2047 encoded-words (=?utf-8?Q?...?=).
func headerSubject(h messageMail.Header) string {
	subject, err := h.Subject()
	if err != nil {
		return strings.TrimSpace(h.Get("Subject"))
	}
	return strings.TrimSpace(subject)
}

func addressList(h message.Header, key string) []messageMail.Address {
	raw := h.Get(key)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	mh := messageMail.Header{Header: h}
	addrs, err := mh.AddressList(key)
	if err != nil {
		// Fall back to net/mail, which tolerates looser syntax.
		parsed, perr := mail.ParseAddressList(raw)
		if perr != nil {
			return nil
		}
		addrs = make([]*messageMail.Address, 0, len(parsed))
		for _, a := range parsed {
			addrs = append(addrs, &messageMail.Address{Name: a.Name, Address: a.Address})
		}
	}
	out := make([]messageMail.Address, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, *a)
	}
	return out
}

// parseReferences splits a References header into normalized message-IDs in
// chain order (oldest first). Only used when MsgIDList fails.
func parseReferences(raw string) []string {
	fields := strings.Fields(raw)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if id := normalizeMessageID(f); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// normalizeMessageID strips angle brackets and surrounding whitespace so
// different servers' quoting still matches.
func normalizeMessageID(raw string) string {
	return strings.Trim(strings.TrimSpace(raw), "<> ")
}

// ThreadKey returns the conversation key for a message: the root of its
// References chain when present, else the In-Reply-To parent, else its own
// Message-ID. Messages without any identifier get a stable hash of their
// subject and from address so they still group deterministically.
func ThreadKey(messageID, inReplyTo string, references []string, subject, fromAddress string) string {
	if len(references) > 0 {
		return normalizeMessageID(references[0])
	}
	if inReplyTo != "" {
		return inReplyTo
	}
	if messageID != "" {
		return messageID
	}
	sum := sha256.Sum256([]byte(subject + "\x00" + fromAddress))
	return "sha256-" + hex.EncodeToString(sum[:16])
}

// FallbackMessageID mints a synthetic Message-ID so deduplication still
// works when a server never sets one.
func FallbackMessageID() string {
	return "generated-" + uuid.NewString() + "@strifira.local"
}

// charsetConvert returns a reader converting the named charset to UTF-8.
func charsetConvert(charset string, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "iso-8859-1", "latin1", "iso-8859-15", "windows-1252", "cp1252":
		return latin1Reader{r}
	default:
		// Unknown charset: pass bytes through (better than dropping mail).
		return r
	}
}

func wordCharsetReader(charset string, input io.Reader) (io.Reader, error) {
	return charsetConvert(charset, input), nil
}

// latin1Reader converts single-byte (ISO-8859-1-like) streams to UTF-8.
type latin1Reader struct{ r io.Reader }

func (l latin1Reader) Read(p []byte) (int, error) {
	buf := make([]byte, len(p))
	n, err := l.r.Read(buf)
	out := p[:0]
	for _, b := range buf[:n] {
		if b < 0x80 {
			out = append(out, b)
			continue
		}
		out = append(out, 0xC0|(b>>6), 0x80|(b&0x3F))
	}
	return len(out), err
}
