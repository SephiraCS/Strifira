package bus

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// persistentServerName binds raft membership to this node's durable disk,
// independent of the hostname assigned to a replacement container.
func persistentServerName(storeDir string) (string, error) {
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(storeDir, ".server-name")
	if data, err := os.ReadFile(path); err == nil {
		name := strings.TrimSpace(string(data))
		if name == "" || len(name) > 128 || strings.ContainsAny(name, " \t\r\n\x00./\\") {
			return "", errors.New("invalid persisted server name; preserve the store and restore its identity")
		}
		return name, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	// Legacy raft state cannot safely be assigned a new identity. The recovery
	// procedure saves the running server's INFO.server_name before replacement.
	if _, err := os.Stat(filepath.Join(storeDir, "jetstream", "$SYS", "_js_", "_meta_")); err == nil {
		return "", errors.New("legacy JetStream store needs its original NATS INFO.server_name in .server-name before restart")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	name := "strifira-" + hex.EncodeToString(random[:])
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	_, writeErr := fmt.Fprintln(file, name)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return "", err
	}
	dir, err := os.Open(storeDir)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return "", err
	}
	return name, nil
}
