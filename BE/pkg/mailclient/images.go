package mailclient

import (
	"context"
	"fmt"
	"github.com/emersion/go-imap"
	"io"
	"time"
)

// FetchImageSource reads one original message without changing its remote flags.
// UIDVALIDITY must match: a recreated INBOX must never supply another message.
func FetchImageSource(ctx context.Context, opts FetchOptions, uid, validity uint32) (*ParsedMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	c, err := dialIMAPContext(ctx, opts)
	if err != nil {
		return nil, err
	}
	defer c.Logout()
	if err := authenticateIMAP(c, opts); err != nil {
		return nil, err
	}
	status, err := c.Select("INBOX", true)
	if err != nil {
		return nil, err
	}
	if validity == 0 || status.UidValidity != validity {
		return nil, fmt.Errorf("mailbox identity changed")
	}
	ids := new(imap.SeqSet)
	ids.AddNum(uid)
	section := &imap.BodySectionName{Peek: true}
	messages := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() { done <- c.UidFetch(ids, []imap.FetchItem{imap.FetchUid, section.FetchItem()}, messages) }()
	var parsed *ParsedMessage
	for message := range messages {
		if message == nil || message.Uid != uid {
			continue
		}
		literal := message.GetBody(section)
		if literal == nil {
			continue
		}
		const maxSourceBytes = 25 << 20
		raw, readErr := io.ReadAll(io.LimitReader(literal, maxSourceBytes+1))
		if readErr != nil || len(raw) > maxSourceBytes {
			continue
		}
		parsed, _ = ParseMessage(raw)
	}
	if err := <-done; err != nil {
		return nil, err
	}
	if parsed == nil {
		return nil, fmt.Errorf("original message unavailable")
	}
	return parsed, nil
}
