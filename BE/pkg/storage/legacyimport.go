package storage

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gabriel-vasile/mimetype"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	log "github.com/sirupsen/logrus"
)

// ImportLegacyTree migrates the local-filesystem storage tree the
// pre-JetStream LocalBackend wrote to (the uploads root, with keys as
// slash-separated paths under it) into the object store, keeping the keys
// unchanged so existing database references keep resolving. Files already
// present in the store are leftovers of an interrupted previous import and
// are dropped from the tree; a file only leaves the tree after its upload
// succeeded, so a failed import is simply retried on the next startup.
// The object store's own directory (.jetstream) lives under the same root
// and is skipped.
//
// It runs once per node startup: a clustered node only touches its own
// volume, and keys are UUID-based, so concurrent imports from several
// nodes never contend for the same object.
func ImportLegacyTree(ctx context.Context, store jetstream.ObjectStore, root string) (int, error) {
	if _, err := os.Stat(root); err != nil {
		// A tree that does not exist is a fresh install; nothing to import.
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	existing, err := existingKeys(ctx, store)
	if err != nil {
		return 0, err
	}

	imported := 0
	var firstErr error
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)

		if existing[key] {
			// Already imported (or re-created) under this key; the copy in
			// the tree is a leftover of an interrupted previous import.
			return os.Remove(path)
		}

		contentType, err := detectFileType(path)
		if err != nil {
			return err
		}
		ok, err := putLegacyFile(ctx, store, key, path, contentType)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			log.WithFields(log.Fields{"event": "storage.legacy_import_failed", "key": key}).WithError(err).Warn("legacy upload import failed; it will be retried on the next startup")
			return nil
		}
		if !ok {
			return nil
		}
		if err := os.Remove(path); err != nil {
			log.WithFields(log.Fields{"event": "storage.legacy_cleanup_failed", "key": key}).WithError(err).Warn("imported legacy upload could not be removed from the tree")
		}
		imported++
		return nil
	})
	if err != nil {
		return imported, err
	}
	if firstErr != nil {
		return imported, firstErr
	}

	pruneEmptyDirs(root)
	return imported, nil
}

// existingKeys lists the keys already in the store. An empty bucket is
// reported as ErrNoObjects, which is just "nothing there yet".
func existingKeys(ctx context.Context, store jetstream.ObjectStore) (map[string]bool, error) {
	infos, err := store.List(ctx)
	if err != nil {
		if errors.Is(err, jetstream.ErrNoObjectsFound) || errors.Is(err, jetstream.ErrBucketNotFound) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	keys := make(map[string]bool, len(infos))
	for _, info := range infos {
		if info != nil && !info.Deleted {
			keys[info.Name] = true
		}
	}
	return keys, nil
}
func detectFileType(path string) (string, error) {
	detected, err := mimetype.DetectFile(path)
	if err != nil {
		return "", err
	}
	return detected.String(), nil
}

func putLegacyFile(ctx context.Context, store jetstream.ObjectStore, key, path, contentType string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	meta := jetstream.ObjectMeta{Name: key}
	if contentType != "" {
		meta.Headers = nats.Header{"Content-Type": []string{contentType}}
	}
	if _, err := store.Put(ctx, meta, io.Reader(f)); err != nil {
		return false, err
	}
	return true, nil
}

// pruneEmptyDirs removes directories the import emptied, deepest first so
// an emptied avatars/ tree disappears instead of leaving skeletons.
func pruneEmptyDirs(root string) {
	var dirs []string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || path == root {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		dirs = append(dirs, path)
		return nil
	})
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		// Only succeeds when the import left the directory empty.
		os.Remove(dir)
	}
}
