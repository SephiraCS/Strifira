package storage

import "github.com/nats-io/nats.go/jetstream"

// New wraps the cluster's files object store as the storage backend. There
// is one storage system: JetStream, replicated across the nodes, so any
// node serves any file.
func New(store jetstream.ObjectStore) Backend {
	return NewJetStream(store)
}
