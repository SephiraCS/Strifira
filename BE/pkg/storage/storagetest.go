package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// TestBucket is the object store bucket name the tests share.
const TestBucket = "strifira-files-test"

// NewTestJetStream starts an embedded single-node NATS server with
// JetStream in a temp directory and returns the files backend bound to it
// plus the raw store (for object-level assertions). Tests only: production
// always runs inside the bus.
func NewTestJetStream(t *testing.T) (Backend, jetstream.ObjectStore) {
	t.Helper()

	opts := &server.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		JetStream: true,
		StoreDir:  t.TempDir(),
	}
	srv := server.New(opts)
	srv.Start()
	t.Cleanup(srv.Shutdown)
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded nats server did not become ready")
	}

	nc, err := nats.Connect(srv.ClientURL(), nats.InProcessServer(srv))
	if err != nil {
		t.Fatalf("connecting to embedded nats server: %v", err)
	}
	t.Cleanup(nc.Close)

	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("enabling jetstream: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := js.CreateObjectStore(ctx, jetstream.ObjectStoreConfig{
		Bucket:  TestBucket,
		Storage: jetstream.FileStorage,
	})
	if err != nil {
		t.Fatalf("creating test object store: %v", err)
	}
	storeRef := store
	return New(func() jetstream.ObjectStore { return storeRef }), store
}

// CountObjects reports how many objects currently exist in the store — the
// object-store analogue of counting files in a directory tree. An empty
// bucket is zero objects, not an error.
func CountObjects(t *testing.T, store jetstream.ObjectStore) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	infos, err := store.List(ctx)
	if err != nil {
		if errors.Is(err, jetstream.ErrNoObjectsFound) {
			return 0
		}
		t.Fatalf("listing objects: %v", err)
	}
	count := 0
	for _, info := range infos {
		if info != nil && !info.Deleted {
			count++
		}
	}
	return count
}
