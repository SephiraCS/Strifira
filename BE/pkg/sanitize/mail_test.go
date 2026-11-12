package sanitize

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMailImagesSanitizationKeepsInertReferencesAndRejectsForgery(t *testing.T) {
	safe := PrepareMailHTML(`<style>body{display:none}</style><script>alert(1)</script><img src="cid:logo%40test" onerror="alert(2)"><img src="https://images.example.test/logo.png" srcset="https://tracker.example.test/secret"><img src="http://images.example.test/x"><img src="cid:missing"><img src="javascript:alert(3)" data-mail-image="attachment:forged"><a href="javascript:alert(4)">link</a>`, map[string]string{"logo@test": "local-id"})
	require.Equal(t, []string{"attachment:local-id", "https://images.example.test/logo.png", "blocked:", "invalid:", "blocked:"}, MailImageSources(safe))
	for _, bad := range []string{"<script", "<style", "onerror", " src=", "srcset", "forged", "javascript:"} {
		require.NotContains(t, safe, bad)
	}
	require.Contains(t, safe, "link")
	require.NotContains(t, SanitizeHTML(`<img src="cid:logo@test" data-mail-image="attachment:fake">`), "data-mail-image")
	require.True(t, NeedsMailImageRepair(`<img alt="Old logo">`))
	require.False(t, NeedsMailImageRepair(safe))
}

func TestMailImagesRemoteURLsRequireHTTPSWithoutCredentials(t *testing.T) {
	for _, source := range []string{"http://example.test/x", "//example.test/x", "data:image/png;base64,eA==", "https://u:p@example.test/x", "https://example.test:444/x", "https://example.test/x#f", "file:///etc/passwd", "javascript:alert(1)"} {
		require.False(t, IsMailRemoteImage(source), source)
	}
	require.True(t, IsMailRemoteImage("https://images.example.test/logo.png?test=1"))
	require.Equal(t, []string{"https://example.test/x"}, MailImageSources(`<img src="https://example.test/x"><img src="/api/other">`))
}
