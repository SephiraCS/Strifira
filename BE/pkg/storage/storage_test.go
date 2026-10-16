package storage

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJetStreamBackend_PutGetDeleteURL(t *testing.T) {
	b, _ := NewTestJetStream(t)

	ctx := context.Background()
	key := "test-image.png"
	content := "fake image data"

	// Put
	n, err := b.Put(ctx, key, strings.NewReader(content), "image/png")
	assert.NoError(t, err)
	assert.Equal(t, int64(len(content)), n)

	// URL
	url, err := b.URL(ctx, key)
	assert.NoError(t, err)
	assert.Equal(t, "/uploads/test-image.png", url)

	// Get
	rc, err := b.Get(ctx, key)
	assert.NoError(t, err)
	data, _ := io.ReadAll(rc)
	rc.Close()
	assert.Equal(t, content, string(data))

	// Delete
	err = b.Delete(ctx, key)
	assert.NoError(t, err)
	_, err = b.Get(ctx, key)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestJetStreamBackend_NamespacedKeys(t *testing.T) {
	b, _ := NewTestJetStream(t)

	ctx := context.Background()
	key := "avatars/0d0f4d88-b3ec-4c4e-a1e6-2ac08d4ba63e.png"
	content := "avatar bytes"

	_, err := b.Put(ctx, key, strings.NewReader(content), "image/png")
	require.NoError(t, err)

	rc, err := b.Get(ctx, key)
	require.NoError(t, err)
	data, _ := io.ReadAll(rc)
	rc.Close()
	assert.Equal(t, content, string(data))

	require.NoError(t, b.Delete(ctx, key))
	_, err = b.Get(ctx, key)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestJetStreamBackend_MissingObjectIsNotExist(t *testing.T) {
	b, _ := NewTestJetStream(t)

	_, err := b.Get(context.Background(), "missing.bin")
	assert.ErrorIs(t, err, os.ErrNotExist)
	// Deleting a missing object is not an error: handlers log-and-continue.
	assert.NoError(t, b.Delete(context.Background(), "missing.bin"))
}

func TestJetStreamBackend_PutOverwrites(t *testing.T) {
	b, store := NewTestJetStream(t)

	ctx := context.Background()
	_, err := b.Put(ctx, "doc.txt", strings.NewReader("first"), "text/plain")
	require.NoError(t, err)
	_, err = b.Put(ctx, "doc.txt", strings.NewReader("second version"), "text/plain")
	require.NoError(t, err)

	rc, err := b.Get(ctx, "doc.txt")
	require.NoError(t, err)
	data, _ := io.ReadAll(rc)
	rc.Close()
	assert.Equal(t, "second version", string(data))

	assert.Equal(t, 1, CountObjects(t, store))
}

// The chunked object encoding keeps large payloads intact end to end.
func TestJetStreamBackend_LargePayload(t *testing.T) {
	b, _ := NewTestJetStream(t)

	ctx := context.Background()
	content := strings.Repeat("x", 512*1024) + strings.Repeat("y", 512*1024)

	_, err := b.Put(ctx, "large.bin", strings.NewReader(content), "application/octet-stream")
	require.NoError(t, err)

	rc, err := b.Get(ctx, "large.bin")
	require.NoError(t, err)
	data, err := io.ReadAll(rc)
	rc.Close()
	require.NoError(t, err)
	assert.Equal(t, content, string(data))
}
