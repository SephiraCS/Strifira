package mailclient

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

var ErrAuthentication = errors.New("mail authentication refused")

// FetchOptions are the IMAP connection parameters of an account.
type FetchOptions struct {
	Host     string
	Port     int
	Security string // none | starttls | tls
	Username string
	Password string
}

// FetchedMessage is one new INBOX message ready for storage.
type FetchedMessage struct {
	UID          uint32
	Seen         bool
	InternalDate time.Time
	Raw          []byte
	Parsed       *ParsedMessage
}

// FetchResult carries the mailbox UIDVALIDITY and every new message.
type FetchResult struct {
	UIDValidity uint32
	Messages    []FetchedMessage
}

// Sync limits keep a single sync bounded even against huge mailboxes.
const (
	dialTimeout      = 20 * time.Second
	firstSyncMessage = 200
	perSyncLimit     = 500
)

// FetchNewMessages connects to the account's INBOX read-only and downloads
// messages newer than afterUID. When afterUID is 0 (first sync or the
// mailbox was recreated), only the newest firstSyncMessage messages are
// fetched. UIDVALIDITY is returned so callers can persist it.
func FetchNewMessages(opts FetchOptions, afterUID uint64, uidValidity uint32) (*FetchResult, error) {
	if !isLoopbackHost(opts.Host) && strings.EqualFold(opts.Security, SecurityNone) {
		return nil, fmt.Errorf("plaintext IMAP is only allowed to localhost; use STARTTLS or TLS")
	}

	conn, err := dialIMAP(opts)
	if err != nil {
		return nil, err
	}
	c := conn
	defer func() {
		_ = c.Logout()
	}()

	if err := c.Login(opts.Username, opts.Password); err != nil {
		return nil, authenticationFailure(err)
	}
	status, err := c.Select("INBOX", true)
	if err != nil {
		return nil, fmt.Errorf("imap select: %w", err)
	}
	if status.Messages == 0 {
		return &FetchResult{UIDValidity: status.UidValidity}, nil
	}

	// A recreated mailbox reuses UIDs from 1: resync the tail instead of
	// trusting stale offsets.
	if uidValidity != 0 && status.UidValidity != uidValidity {
		afterUID = 0
	}

	all, err := c.UidSearch(&imap.SearchCriteria{})
	if err != nil {
		return nil, fmt.Errorf("imap search: %w", err)
	}
	targets := selectNewUIDs(all, afterUID)
	if len(targets) == 0 {
		return &FetchResult{UIDValidity: status.UidValidity}, nil
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(targets...)

	section := &imap.BodySectionName{}
	items := []imap.FetchItem{imap.FetchUid, imap.FetchFlags, imap.FetchInternalDate, section.FetchItem()}
	messages := make(chan *imap.Message, 16)
	fetchDone := make(chan error, 1)
	go func() {
		fetchDone <- c.UidFetch(seqset, items, messages)
	}()

	result := &FetchResult{UIDValidity: status.UidValidity}
	for msg := range messages {
		if msg == nil {
			continue
		}
		raw, err := readLiteral(msg, section)
		if err != nil {
			continue // skip unreadable bodies, keep the rest
		}
		parsed, err := ParseMessage(raw)
		if err != nil {
			continue
		}
		result.Messages = append(result.Messages, FetchedMessage{
			UID:          msg.Uid,
			Seen:         hasSeenFlag(msg.Flags),
			InternalDate: msg.InternalDate,
			Raw:          raw,
			Parsed:       parsed,
		})
	}
	if err := <-fetchDone; err != nil {
		return nil, fmt.Errorf("imap fetch: %w", err)
	}
	return result, nil
}

// TestIMAPConnection verifies credentials without fetching anything.
func TestIMAPConnection(opts FetchOptions) error {
	conn, err := dialIMAP(opts)
	if err != nil {
		return err
	}
	c := conn
	defer func() { _ = c.Logout() }()
	if err := c.Login(opts.Username, opts.Password); err != nil {
		return authenticationFailure(err)
	}
	_, err = c.Select("INBOX", true)
	return err
}

// selectNewUIDs filters the mailbox UID list down to messages newer than
// afterUID, capped per sync; the first sync takes only the newest tail.
func selectNewUIDs(all []uint32, afterUID uint64) []uint32 {
	if len(all) == 0 {
		return nil
	}
	// UID SEARCH returns ascending order, but do not rely on it.
	sorted := make([]uint32, len(all))
	copy(sorted, all)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	if afterUID == 0 {
		if len(sorted) > firstSyncMessage {
			sorted = sorted[len(sorted)-firstSyncMessage:]
		}
		return sorted
	}
	out := make([]uint32, 0, len(sorted))
	for _, uid := range sorted {
		if uint64(uid) > afterUID {
			out = append(out, uid)
		}
	}
	if len(out) > perSyncLimit {
		out = out[len(out)-perSyncLimit:]
	}
	return out
}

func hasSeenFlag(flags []string) bool {
	for _, flag := range flags {
		if strings.EqualFold(flag, imap.SeenFlag) {
			return true
		}
	}
	return false
}

func readLiteral(msg *imap.Message, section *imap.BodySectionName) ([]byte, error) {
	literal := msg.GetBody(section)
	if literal == nil {
		return nil, fmt.Errorf("message %d has no body", msg.Uid)
	}
	return io.ReadAll(literal)
}

func dialIMAP(opts FetchOptions) (*client.Client, error) {
	conn, err := dialMail(context.Background(), opts.Host, opts.Port, opts.Security)
	if err != nil {
		return nil, err
	}
	c, err := client.New(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	c.Timeout = dialTimeout
	if opts.Security == SecurityStartTLS {
		if err := c.StartTLS(&tls.Config{ServerName: opts.Host, MinVersion: tls.VersionTLS12}); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	return c, nil
}
