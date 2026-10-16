package bus

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// NewTestKV starts an embedded single-node NATS server with JetStream in a
// temp directory and returns a KV bucket on it. Tests only.
func NewTestKV(t *testing.T, bucket string, ttl time.Duration) jetstream.KeyValue {
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
	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket, TTL: ttl})
	if err != nil {
		t.Fatalf("creating test kv bucket: %v", err)
	}
	return kv
}
