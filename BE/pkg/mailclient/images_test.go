package mailclient

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMailImagesMIMEKeepsCIDWithAndWithoutFilename(t *testing.T) {
	raw := "From: sender@example.test\r\nMIME-Version: 1.0\r\nContent-Type: multipart/related; boundary=test\r\n\r\n"
	raw += "--test\r\nContent-Type: text/html\r\n\r\n<img src=\"cid:logo%40test\"><img src=\"cid:icon@test\">\r\n"
	for _, disposition := range []string{"inline; filename=logo.png", "inline"} {
		cid := "logo@test"
		if disposition == "inline" {
			cid = "icon@test"
		}
		raw += "--test\r\nContent-Type: image/png\r\nContent-ID: <" + cid + ">\r\nContent-Disposition: " + disposition + "\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString([]byte("image bytes")) + "\r\n"
	}
	raw += "--test--\r\n"
	parsed, err := ParseMessage([]byte(raw))
	require.NoError(t, err)
	require.Len(t, parsed.Attachments, 2)
	require.Equal(t, "logo@test", parsed.Attachments[0].ContentID)
	require.Equal(t, "icon@test", parsed.Attachments[1].ContentID)
	require.Equal(t, []byte("image bytes"), parsed.Attachments[1].Content)
	require.Contains(t, parsed.BodyHTML, "cid:logo%40test")
}
