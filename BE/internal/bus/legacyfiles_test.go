package bus

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A single-node bus imports the legacy uploads tree into the files bucket
// once the buckets are provisioned, and imported files leave the tree.
func TestLegacyFilesImport(t *testing.T) {
	legacyDir := t.TempDir()
	legacyFile := filepath.Join(legacyDir, "avatars", "11111111-1111-1111-1111-111111111111.png")
	require.NoError(t, os.MkdirAll(filepath.Dir(legacyFile), 0755))
	require.NoError(t, os.WriteFile(legacyFile, []byte("avatar bytes"), 0644))

	b, err := New(Config{
		StoreDir:         t.TempDir(),
		LegacyUploadsDir: legacyDir,
		ClientPort:       -1, // in-process clients only: random port
	})
	require.NoError(t, err)
	t.Cleanup(b.Close)

	require.Eventually(t, b.Ready, 30*time.Second, 200*time.Millisecond, "node should provision its buckets")

	require.Eventually(t, func() bool {
		result, err := b.Files().GetBytes(context.Background(), "avatars/11111111-1111-1111-1111-111111111111.png")
		return err == nil && string(result) == "avatar bytes"
	}, 15*time.Second, 200*time.Millisecond, "legacy avatar should be readable from the object store")

	_, statErr := os.Stat(legacyFile)
	assert.True(t, os.IsNotExist(statErr), "imported legacy file should leave the tree")
}

// Without a legacy uploads dir nothing is imported and nothing fails —
// tests never set LegacyUploadsDir, so they never sweep their store dir.
func TestLegacyFilesImportDisabledWithoutDir(t *testing.T) {
	b, err := New(Config{
		StoreDir:   t.TempDir(),
		ClientPort: -1,
	})
	require.NoError(t, err)
	t.Cleanup(b.Close)

	require.Eventually(t, b.Ready, 30*time.Second, 200*time.Millisecond, "node should provision its buckets")

	// Give a wrongly-derived import root a moment to do damage, then
	// assert the bucket is still empty.
	time.Sleep(500 * time.Millisecond)
	_, err = b.Files().List(context.Background())
	assert.Error(t, err, "the files bucket should still be empty")
}
