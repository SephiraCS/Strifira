package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// JetStreamBackend stores files in the cluster's JetStream object store.
// Objects are replicated across the cluster nodes, so every node serves any
// file and no node-local filesystem state exists.
type JetStreamBackend struct {
	store   jetstream.ObjectStore
	urlBase string
}

// NewJetStream wraps an object store bucket as the file storage backend.
// Files are fetched through the API handlers, so the URL base is the same
// public path the uploads tree was served under.
func NewJetStream(store jetstream.ObjectStore) *JetStreamBackend {
	return &JetStreamBackend{store: store, urlBase: "/uploads"}
}

// errStorageNotReady marks the seconds between process start and the bus
// having provisioned the files bucket cluster-wide.
var errStorageNotReady = errors.New("file storage is not ready yet")

func (b *JetStreamBackend) Put(ctx context.Context, key string, r io.Reader, contentType string) (int64, error) {
	if b.store == nil {
		return 0, errStorageNotReady
	}
	meta := jetstream.ObjectMeta{Name: key}
	if contentType != "" {
		meta.Headers = nats.Header{"Content-Type": []string{contentType}}
	}
	info, err := b.store.Put(ctx, meta, r)
	if err != nil {
		return 0, fmt.Errorf("object store put: %w", err)
	}
	return int64(info.Size), nil
}

func (b *JetStreamBackend) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if b.store == nil {
		return nil, errStorageNotReady
	}
	result, err := b.store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, jetstream.ErrObjectNotFound) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("object store get: %w", err)
	}
	return result, nil
}

func (b *JetStreamBackend) Delete(ctx context.Context, key string) error {
	if b.store == nil {
		return errStorageNotReady
	}
	if err := b.store.Delete(ctx, key); err != nil {
		if errors.Is(err, jetstream.ErrObjectNotFound) {
			return nil
		}
		return fmt.Errorf("object store delete: %w", err)
	}
	return nil
}

func (b *JetStreamBackend) URL(_ context.Context, key string) (string, error) {
	return fmt.Sprintf("%s/%s", b.urlBase, key), nil
}
