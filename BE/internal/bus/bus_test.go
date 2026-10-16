package bus

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitSeedURLs(t *testing.T) {
	assert.Equal(t, []string{""}, splitSeedURLs(""))
	// Trimming each entry is parseSeedURLs' job; splitSeedURLs only splits.
	assert.Equal(t, []string{"nats://a:6222", " nats://b:6222"}, splitSeedURLs("nats://a:6222, nats://b:6222"))
}

func TestParseSeedURLsIgnoresInvalid(t *testing.T) {
	urls := parseSeedURLs([]string{"nats://node-1:6222", "", "not a url", "http://only-host"})
	require.Len(t, urls, 2)
	assert.Equal(t, "node-1:6222", urls[0].Host)
	assert.Equal(t, "only-host", urls[1].Host)
}

// TestNewSingleNode starts a lone node: fully functional, cluster of one.
func TestNewSingleNode(t *testing.T) {
	b, err := New(Config{StoreDir: t.TempDir(), ClientPort: -1})
	require.NoError(t, err)
	t.Cleanup(b.Close)

	require.Equal(t, ClusterName, b.ClusterName())
	assert.Equal(t, 1, b.ClusterPeers())
	assert.NotEmpty(t, b.NodeID())
	require.Eventually(t, b.Ready, 90*time.Second, 500*time.Millisecond, "node should provision its buckets")
}

// TestClusterAutoJoin starts three nodes pointing at each other through
// seed URLs only (no full route tables) and verifies they gossip into one
// cluster: the JetStream meta layer converges to three peers and a KV write
// on one node is visible on another.
func TestClusterAutoJoin(t *testing.T) {
	log.SetLevel(log.DebugLevel)
	defer log.SetLevel(log.InfoLevel)
	var buses []*Bus
	for i := 1; i <= 3; i++ {
		// Seeds are the other two nodes; the gossip table makes them peers.
		seeds := []string{"nats://127.0.0.1:16221", "nats://127.0.0.1:16222", "nats://127.0.0.1:16223"}
		own := "nats://127.0.0.1:" + strconv.Itoa(16220+i)
		filtered := make([]string, 0, 2)
		for _, seed := range seeds {
			if seed != own {
				filtered = append(filtered, seed)
			}
		}
		b, err := New(Config{
			SeedURLs:    strings.Join(filtered, ","),
			StoreDir:    t.TempDir(),
			ClientPort:  -1, // in-process clients only: random port
			ClusterPort: 16220 + i,
		})
		require.NoError(t, err)
		buses = append(buses, b)
	}
	t.Cleanup(func() {
		for _, b := range buses {
			b.Close()
		}
	})

	// Gossip converges the route tables; the JetStream meta layer elects a
	// leader and every node sees the full peer set.
	require.Eventually(t, func() bool {
		sizes := make([]int, len(buses))
		for i, b := range buses {
			sizes[i] = b.ClusterPeers()
		}
		t.Logf("cluster sizes: %v", sizes)
		for _, b := range buses {
			if b.ClusterPeers() != 3 {
				return false
			}
		}
		return true
	}, 150*time.Second, 3*time.Second, "cluster should form three peers")

	// Provisioning runs in the background once the meta layer has a leader.
	for _, b := range buses {
		require.Eventually(t, b.Ready, 120*time.Second, 500*time.Millisecond, "every node should provision its buckets")
	}

	// Cross-node KV: write through the first node, read through the last.
	// The reconciler may still be scaling the freshly formed buckets up to
	// the full replica count, so allow the view a moment to converge.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	revision, err := buses[0].Presence().Put(ctx, "p.test.cluster", []byte("node-1"))
	require.NoError(t, err)
	require.NotZero(t, revision)

	require.Eventually(t, func() bool {
		entry, err := buses[2].Presence().Get(ctx, "p.test.cluster")
		return err == nil && string(entry.Value()) == "node-1"
	}, 30*time.Second, 500*time.Millisecond, "key written on node 1 should be readable on node 3")

	// Cross-node objects: put through one node, read through another.
	_, err = buses[0].Files().PutBytes(ctx, "cluster.txt", []byte("replicated"))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		result, err := buses[1].Files().GetBytes(ctx, "cluster.txt")
		return err == nil && string(result) == "replicated"
	}, 30*time.Second, 500*time.Millisecond, "object written on node 1 should be readable on node 2")
}
