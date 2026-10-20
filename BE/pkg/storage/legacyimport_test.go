package storage

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTree materializes a legacy uploads tree: a map from storage key to
// file content, laid out under dir the way LocalBackend wrote it.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for key, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(key))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	}
}

func treeFile(t *testing.T, dir, key string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(key)))
	require.NoError(t, err)
	return string(content)
}

func objectContent(t *testing.T, store jetstream.ObjectStore, key string) string {
	t.Helper()
	result, err := store.Get(context.Background(), key)
	require.NoError(t, err)
	defer result.Close()
	content, err := io.ReadAll(result)
	require.NoError(t, err)
	return string(content)
}

func TestImportLegacyTree_ImportsTreeWithUnchangedKeys(t *testing.T) {
	_, store := NewTestJetStream(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"avatars/11111111-1111-1111-1111-111111111111.png":      "avatar bytes",
		"workspace-avatars/22222222-2222-2222-2222-2222222.png": "logo bytes",
		"33333333-3333-3333-3333-333333333333.webp":             "asset bytes",
	})

	imported, err := ImportLegacyTree(context.Background(), store, root)
	require.NoError(t, err)
	assert.Equal(t, 3, imported)

	// The keys are exactly the legacy relative paths, so existing
	// database references keep resolving.
	assert.Equal(t, "avatar bytes", objectContent(t, store, "avatars/11111111-1111-1111-1111-111111111111.png"))
	assert.Equal(t, "logo bytes", objectContent(t, store, "workspace-avatars/22222222-2222-2222-2222-2222222.png"))
	assert.Equal(t, "asset bytes", objectContent(t, store, "33333333-3333-3333-3333-333333333333.webp"))
	assert.Equal(t, 3, CountObjects(t, store))

	// Imported files leave the tree (the volume keeps no second copy),
	// including the now-empty namespace directories.
	_, err = os.Stat(filepath.Join(root, "avatars", "11111111-1111-1111-1111-111111111111.png"))
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(root, "avatars"))
	assert.True(t, os.IsNotExist(err))
}

func TestImportLegacyTree_RerunIsNoopAndDropsLeftovers(t *testing.T) {
	_, store := NewTestJetStream(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"avatars/11111111-1111-1111-1111-111111111111.png": "avatar bytes",
	})

	imported, err := ImportLegacyTree(context.Background(), store, root)
	require.NoError(t, err)
	assert.Equal(t, 1, imported)

	// An interrupted previous import can leave a file whose object already
	// exists; the rerun must not upload it again and must clear the copy.
	writeTree(t, root, map[string]string{
		"avatars/11111111-1111-1111-1111-111111111111.png": "avatar bytes",
	})
	imported, err = ImportLegacyTree(context.Background(), store, root)
	require.NoError(t, err)
	assert.Equal(t, 0, imported)
	assert.Equal(t, 1, CountObjects(t, store))
	assert.Equal(t, "avatar bytes", objectContent(t, store, "avatars/11111111-1111-1111-1111-111111111111.png"))
}

func TestImportLegacyTree_SkipsObjectStoreDirAndHiddenFiles(t *testing.T) {
	_, store := NewTestJetStream(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"44444444-4444-4444-4444-444444444444.png": "asset bytes",
	})
	writeTree(t, filepath.Join(root, ".jetstream"), map[string]string{
		"streams/OBJ_bucket/msgs/1.blk": "jetstream internals",
	})
	require.NoError(t, os.WriteFile(filepath.Join(root, ".DS_Store"), []byte("junk"), 0644))

	imported, err := ImportLegacyTree(context.Background(), store, root)
	require.NoError(t, err)
	assert.Equal(t, 1, imported)
	assert.Equal(t, 1, CountObjects(t, store))

	// The object store's own directory is not part of the key space.
	_, err = store.Get(context.Background(), "streams/OBJ_bucket/msgs/1.blk")
	assert.ErrorIs(t, err, jetstream.ErrObjectNotFound)
}

func TestImportLegacyTree_MissingRootIsNoop(t *testing.T) {
	_, store := NewTestJetStream(t)
	imported, err := ImportLegacyTree(context.Background(), store, filepath.Join(t.TempDir(), "does-not-exist"))
	require.NoError(t, err)
	assert.Equal(t, 0, imported)
	assert.Equal(t, 0, CountObjects(t, store))
}
