package service

import (
	"testing"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/pkg/mailclient"
	"github.com/stretchr/testify/assert"
)

func TestMailThreadKeyAnchorsConversationAtReferencesRoot(t *testing.T) {
	key := mailclient.ThreadKey("reply-2@srv", "reply-1@srv", []string{"root@srv", "reply-1@srv"}, "Re: hello", "a@b.c")
	assert.Equal(t, "root@srv", key)
}

func TestMailThreadKeyFallsBackToParentThenSelf(t *testing.T) {
	assert.Equal(t, "reply-1@srv",
		mailclient.ThreadKey("reply-2@srv", "reply-1@srv", nil, "Re: hello", "a@b.c"))
	assert.Equal(t, "solo@srv",
		mailclient.ThreadKey("solo@srv", "", nil, "hello", "a@b.c"))
	// No identifiers at all: a stable hash, never empty.
	assert.NotEmpty(t, mailclient.ThreadKey("", "", nil, "No ids", "a@b.c"))
}

func TestMailReplySubjectIsIdempotent(t *testing.T) {
	assert.Equal(t, "Re: meeting", replySubject("meeting"))
	assert.Equal(t, "Re: meeting", replySubject("Re: meeting"))
	// Already a reply (any casing): never stack another "Re:".
	assert.Equal(t, "RE: meeting", replySubject("RE: meeting"))
	assert.Equal(t, "", replySubject(""))
}

func TestMailForwardSubjectIsIdempotent(t *testing.T) {
	assert.Equal(t, "Fwd: report", forwardSubject("report"))
	// Already a forward (any casing): preserve the author's wording.
	assert.Equal(t, "fwd: report", forwardSubject("fwd: report"))
	assert.Equal(t, "", forwardSubject(""))
}

func TestMailComposeBodyAppendsSignature(t *testing.T) {
	assert.Equal(t, "hello\n", composeBody("hello", ""))
	assert.Equal(t, "hello\n\n--\nFilippo\n", composeBody("hello", "Filippo"))
	// A trailing newline in the body does not double up.
	assert.Equal(t, "hello\n\n--\nFilippo\n", composeBody("hello\n", "Filippo"))
}

func TestMailOriginalThreadReferencesChain(t *testing.T) {
	parent := &domain.MailMessage{MessageID: "parent@srv", InReplyTo: "root@srv"}
	assert.Equal(t, []string{"root@srv", "parent@srv"}, originalThreadReferences(parent))

	selfRooted := &domain.MailMessage{MessageID: "root@srv"}
	assert.Equal(t, []string{"root@srv"}, originalThreadReferences(selfRooted))
}

func TestMailSnippetCollapsesWhitespace(t *testing.T) {
	assert.Equal(t, "one two three", snippetText("  one\n\ntwo\tthree  "))
	long := make([]byte, 0, 400)
	for i := 0; i < 400; i++ {
		long = append(long, 'a')
	}
	assert.Len(t, snippetText(string(long)), 240)
}
