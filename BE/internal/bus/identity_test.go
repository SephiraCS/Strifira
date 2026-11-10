package bus

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPersistentServerNamePreservesLegacyIdentity(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "jetstream", "$SYS", "_js_", "_meta_"), 0700))
	_, err := persistentServerName(dir)
	require.ErrorContains(t, err, "original NATS INFO.server_name")
	name := "previous-container-79f37ac9"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".server-name"), []byte(name+"\n"), 0600))
	restored, err := persistentServerName(dir)
	require.NoError(t, err)
	assert.Equal(t, name, restored)
	// Corruption must fail closed rather than silently generating a new identity.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".server-name"), nil, 0600))
	_, err = persistentServerName(dir)
	require.ErrorContains(t, err, "invalid persisted server name")
}

func TestClusterRestartPreservesIdentityObjectsAndReplication(t *testing.T) {
	var nodes [3]*Bus
	var configs [3]Config
	t.Cleanup(func() {
		for _, node := range nodes {
			if node != nil {
				node.Close()
			}
		}
	})
	for i := range nodes {
		var seeds []string
		for j := range nodes {
			if i != j {
				seeds = append(seeds, "nats://127.0.0.1:"+strconv.Itoa(17221+j))
			}
		}
		configs[i] = Config{SeedURLs: strings.Join(seeds, ","), StoreDir: t.TempDir(), ClientPort: -1, ClusterPort: 17221 + i}
		var err error
		nodes[i], err = New(configs[i])
		require.NoError(t, err)
	}
	for _, node := range nodes {
		require.Eventually(t, node.Ready, 120*time.Second, 250*time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	require.Eventually(t, func() bool {
		for _, node := range nodes {
			if node.ClusterPeers() != 3 {
				return false
			}
		}
		return true
	}, 60*time.Second, 250*time.Millisecond)
	// Reconcile explicitly so the restart regression begins with R3, not the
	// bootstrap R2. A reconnect must retain the stronger existing redundancy.
	nodes[0].reconcileObjectStore(ctx, 3)
	nodes[0].reconcileKV(ctx, PresenceBucket, PresenceTTL, 3)
	_, err := nodes[0].Files().PutBytes(ctx, "restart.png", []byte("persisted attachment"))
	require.NoError(t, err)
	name := nodes[0].server.Name()
	nodes[0].Close()
	nodes[0] = nil
	nodes[0], err = New(configs[0])
	require.NoError(t, err)
	assert.Equal(t, name, nodes[0].server.Name())
	require.Eventually(t, nodes[0].Ready, 120*time.Second, 250*time.Millisecond)
	for _, node := range nodes {
		data, err := node.Files().GetBytes(ctx, "restart.png")
		require.NoError(t, err)
		assert.Equal(t, []byte("persisted attachment"), data)
	}
	// A temporarily incomplete membership view cannot shrink persisted buckets.
	nodes[0].reconcileObjectStore(ctx, 1)
	nodes[0].reconcileKV(ctx, PresenceBucket, PresenceTTL, 1)
	files, err := nodes[0].Files().Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, files.Replicas())
	presence, err := nodes[0].Presence().Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, presence.Config().Replicas)
}
