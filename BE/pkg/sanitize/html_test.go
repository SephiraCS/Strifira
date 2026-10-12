package sanitize

import (
	"strings"
	"testing"
)

func TestPlainTextPreservesIssueTitleSpecialCharacters(t *testing.T) {
	input := `fix/issues: use <Select> & "quoted" names`

	got := PlainText(input)

	if got != input {
		t.Fatalf("PlainText() = %q, want %q", got, input)
	}
}

func TestPlainTextDecodesEntitiesInIssueTitle(t *testing.T) {
	input := `fix/issues: use &lt;Select&gt; &amp; "quoted" names`
	want := `fix/issues: use <Select> & "quoted" names`

	got := PlainText(input)

	if got != want {
		t.Fatalf("PlainText() = %q, want %q", got, want)
	}
}

func TestSanitizeEditorContentConvertsMarkdown(t *testing.T) {
	input := strings.Join([]string{
		`# Use <Select> & friends`,
		``,
		`- [x] Preserve **bold** text`,
		`- [ ] Render ` + "`code <tag>`" + ` safely`,
		``,
		`See [docs](https://example.com/docs?q=one&ok=true).`,
	}, "\n")

	got := SanitizeEditorContent(input)

	assertContains(t, got, `<h1>Use &lt;Select&gt; &amp; friends</h1>`)
	assertContains(t, got, `data-type="taskList"`)
	assertContains(t, got, `data-checked="true"`)
	assertContains(t, got, `<strong>bold</strong>`)
	assertContains(t, got, `<code>code &lt;tag&gt;</code>`)
	assertContains(t, got, `href="https://example.com/docs?q=one&amp;ok=true"`)
}

func TestSanitizeEditorContentKeepsSafeEditorHTML(t *testing.T) {
	input := `<p>See <a href="https://example.com?a=1&b=2">docs</a> <script>alert(1)</script></p>`

	got := SanitizeEditorContent(input)

	assertContains(t, got, `<a href="https://example.com?a=1&amp;b=2"`)
	assertNotContains(t, got, `<script`)
	assertNotContains(t, got, `alert(1)`)
}

func TestSanitizeEditorContentKeepsProtectedAssetImages(t *testing.T) {
	assetID := "123e4567-e89b-12d3-a456-426614174000"
	input := `<p><img src="/api/workspaces/acme/assets/` + assetID + `" alt="Screenshot"></p>`

	got := SanitizeEditorContent(input)

	assertContains(t, got, `src="/api/workspaces/acme/assets/`+assetID+`"`)
	assertContains(t, got, `alt="Screenshot"`)
}

func TestSanitizeEditorContentRejectsUnsafeImageURL(t *testing.T) {
	input := `<p><img src="javascript:alert(1)" alt="Bad"></p>`

	got := SanitizeEditorContent(input)

	assertNotContains(t, got, `javascript:`)
}

func TestSanitizeEditorContentKeepsAttachmentMetadata(t *testing.T) {
	assetID := "123e4567-e89b-12d3-a456-426614174000"
	input := `<p><a class="editor-attachment" data-type="attachment" data-filename="requirements.pdf" data-size="2048" href="/api/workspaces/acme/assets/` + assetID + `?download=1" download="requirements.pdf" target="_blank">requirements.pdf (2 KB)</a></p>`

	got := SanitizeEditorContent(input)

	assertContains(t, got, `data-type="attachment"`)
	assertContains(t, got, `data-filename="requirements.pdf"`)
	assertContains(t, got, `data-size="2048"`)
	assertContains(t, got, `download="requirements.pdf"`)
	assertContains(t, got, `/api/workspaces/acme/assets/`+assetID+`?download=1`)
}

func TestSanitizeEditorContentConvertsMarkdownWikiLinks(t *testing.T) {
	input := strings.Join([]string{
		`# Project Home`,
		``,
		`See [[Meeting Notes]] and [[Q2 "Roadmap" & <Plans>]], plus [docs](https://example.com).`,
	}, "\n")

	got := SanitizeEditorContent(input)

	// Exact anchor the editor's wikiLink node parses (a[data-type="wiki"],
	// title from data-wiki-title) and markdown export serializes back to
	// [[Meeting Notes]].
	assertContains(t, got, `<a data-type="wiki" data-wiki-title="Meeting Notes" class="wiki-link">Meeting Notes</a>`)
	assertContains(t, got, `data-wiki-title="Q2 &#34;Roadmap&#34; &amp; &lt;Plans&gt;"`)
	assertContains(t, got, `href="https://example.com"`)
}

func TestSanitizeEditorContentKeepsMarkdownWikiLinksInCode(t *testing.T) {
	input := strings.Join([]string{
		"```",
		`[[Fenced]]`,
		"```",
		``,
		`Inline ` + "`[[Inline]]`" + ` stays literal.`,
	}, "\n")

	got := SanitizeEditorContent(input)

	assertContains(t, got, `<code>[[Fenced]]</code>`)
	assertContains(t, got, `<code>[[Inline]]</code>`)
	assertNotContains(t, got, `data-type="wiki"`)
}

func TestSanitizeEditorContentLeavesIncompleteWikiSyntaxAlone(t *testing.T) {
	input := `Broken [[link and [bracket] stay text, so does [[]].`

	got := SanitizeEditorContent(input)

	assertNotContains(t, got, `data-type="wiki"`)
	assertContains(t, got, `Broken [[link`)
}

func TestSanitizeEditorContentKeepsResolvedWikiLinkAnchor(t *testing.T) {
	input := `<p><a data-type="wiki" data-wiki-title="Meeting Notes" data-doc-id="123e4567-e89b-12d3-a456-426614174000" class="wiki-link" href="#">Meeting Notes</a></p>`

	got := SanitizeEditorContent(input)

	assertContains(t, got, `data-type="wiki"`)
	assertContains(t, got, `data-wiki-title="Meeting Notes"`)
	assertContains(t, got, `data-doc-id="123e4567-e89b-12d3-a456-426614174000"`)
}

func assertContains(t *testing.T, value, substring string) {
	t.Helper()
	if !strings.Contains(value, substring) {
		t.Fatalf("expected %q to contain %q", value, substring)
	}
}

func assertNotContains(t *testing.T, value, substring string) {
	t.Helper()
	if strings.Contains(value, substring) {
		t.Fatalf("expected %q not to contain %q", value, substring)
	}
}
