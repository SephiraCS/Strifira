package machine

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGitSnapshotRejectsIncompletePathAndCommitRecords(t *testing.T) {
	for _, output := range []string{
		"ku/task\x00\x00false\x00forbidden",
		"ku/task\x00invalid\x00false\x00",
		"ku/task\x00\x00false\x00file\x00\x00",
		"ku/task\x00\x00unknown\x00",
	} {
		_, err := parseGitSnapshot(output)
		require.Error(t, err)
	}
	state, err := parseGitSnapshot("ku/task\x00\x00false\x00a\nfile\x00last\x00")
	require.NoError(t, err)
	require.Equal(t, []string{"a\nfile", "last"}, state.ChangedFiles)
}

func TestCaptureOverflowCannotMasqueradeAsCompleteEnumeration(t *testing.T) {
	buffer := newCappedBuffer(4)
	n, err := buffer.Write([]byte("path\x00forbidden\x00"))
	require.NoError(t, err)
	require.Equal(t, 15, n)
	require.Equal(t, "path", buffer.String())
	require.True(t, buffer.overflow)
}
