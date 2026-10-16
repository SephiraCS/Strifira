package storage

import "github.com/nats-io/nats.go/jetstream"

// New wraps the cluster's files object store as the storage backend. There
// is one storage system: JetStream, replicated across the nodes, so any
// node serves any file. The getter resolves the bucket per use because the
// bus provisions it shortly after startup.
func New(getStore func() jetstream.ObjectStore) Backend {
	return NewJetStream(getStore)
}
