package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestBoundedSummaryPreservesUTF8(t *testing.T) {
	for _, test := range []struct {
		name string
		text string
		want string
	}{
		{name: "full summary", text: " \nRésumé: 修正完了 🙂\t", want: "Résumé: 修正完了 🙂"},
		{name: "exact ASCII limit", text: strings.Repeat("a", 4000), want: strings.Repeat("a", 4000)},
		{name: "ASCII over limit", text: strings.Repeat("a", 4001), want: strings.Repeat("a", 4000)},
		{name: "two-byte boundary", text: strings.Repeat("a", 3999) + "é", want: strings.Repeat("a", 3999)},
		{name: "three-byte boundary", text: strings.Repeat("a", 3998) + "界", want: strings.Repeat("a", 3998)},
		{name: "four-byte boundary", text: strings.Repeat("a", 3997) + "🙂", want: strings.Repeat("a", 3997)},
		{name: "complete rune at limit", text: strings.Repeat("a", 3996) + "🙂tail", want: strings.Repeat("a", 3996) + "🙂"},
		{name: "invalid bytes", text: " \nresult: \xff\xfe fixed\t", want: "result: \uFFFD fixed"},
		{name: "invalid truncated rune", text: "result: \xe7\x95", want: "result: \uFFFD"},
		{name: "replacement crossing limit", text: strings.Repeat("a", 3998) + "\xfftail", want: strings.Repeat("a", 3998)},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := boundedSummary(test.text)
			require.True(t, utf8.ValidString(got), "summary must remain valid PostgreSQL text")
			require.LessOrEqual(t, len(got), 4000)
			require.Equal(t, test.want, got)
		})
	}
}

func TestDefaultResultRepairsInvalidUTF8(t *testing.T) {
	for _, test := range []struct {
		name   string
		stdout string
		stderr string
	}{
		{name: "stdout", stdout: "done: \xff\xfe"},
		{name: "stderr fallback", stdout: " \n\t", stderr: "done: \xff\xfe"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := defaultResult(test.stdout, test.stderr, 1)
			require.Equal(t, "failed", result.Status)
			require.Equal(t, "done: \uFFFD", result.Summary)
			require.True(t, utf8.ValidString(result.Summary))
		})
	}
}

func TestProviderResultsBoundSummariesAtUTF8Boundary(t *testing.T) {
	for _, test := range []struct {
		name   string
		parse  func(string, string, int) RunResult
		encode func(string) any
	}{
		{name: "default stdout", parse: defaultResult},
		{name: "default stderr", parse: func(text, _ string, exitCode int) RunResult {
			return defaultResult(" \n", text, exitCode)
		}},
		{name: "Claude", parse: parseClaudeResult, encode: func(text string) any {
			return map[string]any{"type": "result", "subtype": "success", "result": text}
		}},
		{name: "Codex", parse: parseCodexResult, encode: func(text string) any {
			return map[string]any{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": text}}
		}},
		{name: "OpenCode", parse: NewOpenCodeProvider("").ParseResult, encode: func(text string) any {
			return map[string]any{"type": "text", "part": map[string]any{"type": "text", "text": text}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, text := range []string{"Résumé: 修正完了 🙂", strings.Repeat("a", 3999) + "界tail"} {
				stdout := text
				if test.encode != nil {
					encoded, err := json.Marshal(test.encode(text))
					require.NoError(t, err)
					stdout = string(encoded)
					if test.name == "Codex" {
						stdout += "\n" + `{"type":"turn.completed"}`
					}
				}
				result := test.parse(stdout, "", 0)
				require.Equal(t, "succeeded", result.Status)
				require.True(t, utf8.ValidString(result.Summary))
				if len(text) <= 4000 {
					require.Equal(t, text, result.Summary)
				} else {
					require.Equal(t, strings.Repeat("a", 3999), result.Summary)
				}
			}
		})
	}
}
