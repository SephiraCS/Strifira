package bus

import (
	"context"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	log "github.com/sirupsen/logrus"

	"github.com/SephiraCS/strifira-backend/pkg/storage"
)

// legacyImportTimeout bounds one startup's import pass. Files that fail to
// upload stay in the tree and the next startup retries them.
const legacyImportTimeout = 10 * time.Minute

// importLegacyFiles moves files uploaded before the JetStream migration
// from the local uploads tree into the object store. LocalBackend wrote
// avatars, workspace logos and prompt assets under the uploads root; those
// files keep their database rows and URLs but are invisible to the
// object-store backend until copied over. Best effort: a failed pass logs
// and leaves the tree for the next boot. An empty LegacyUploadsDir (every
// test) skips the import entirely — the root must never be derived from
// StoreDir, whose parent is only the uploads root in production layouts.
func (b *Bus) importLegacyFiles(store jetstream.ObjectStore) {
	if b.legacyUploadsDir == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), legacyImportTimeout)
	defer cancel()

	imported, err := storage.ImportLegacyTree(ctx, store, b.legacyUploadsDir)
	if err != nil {
		log.WithError(err).WithField("imported", imported).Warn("legacy upload import finished with errors; remaining files retry on the next startup")
		return
	}
	if imported > 0 {
		log.WithField("imported", imported).Info("legacy uploads imported into the object store")
	}
}
