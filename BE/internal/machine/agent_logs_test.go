package machine

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestActiveAgentLogChunksPreserveJSONAndSecretBoundaries(t *testing.T) {
	var chunks []string
	writer := &agentLogWriter{observer: func(_, _, content string) {
		chunks = append(chunks, redact(content, map[string]string{"API_KEY": "secret-token"}))
	}}
	_, err := writer.Write([]byte(`{"type":"message","message":"secret-`))
	require.NoError(t, err)
	require.Empty(t, chunks, "incomplete JSON and secrets must stay together")
	_, err = writer.Write([]byte("token\"}\nlast fragment"))
	require.NoError(t, err)
	require.Equal(t, []string{"{\"type\":\"message\",\"message\":\"[REDACTED]\"}\n"}, chunks)
	writer.flush()
	require.Equal(t, "last fragment", chunks[1])
}

func TestActiveAgentLogsStayBounded(t *testing.T) {
	total := 0
	writer := &agentLogWriter{observer: func(_, _, content string) { total += len(content) }}
	_, err := writer.Write([]byte(strings.Repeat("line\n", maxCapturedLogBytes)))
	require.NoError(t, err)
	writer.flush()
	require.Equal(t, maxCapturedLogBytes, total)
	_, err = writer.Write([]byte("ignored\n"))
	require.NoError(t, err)
	require.Equal(t, maxCapturedLogBytes, total)
}
