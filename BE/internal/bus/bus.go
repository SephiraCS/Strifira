// Package bus runs the embedded NATS server that carries every form of
// cross-node communication: workspace realtime fan-out, doc-sync relay,
// screen-share signaling and the JetStream buckets backing files (object
// store), issue presence and the login throttle (KV).
//
// Every node runs an identical configuration, so a node discovers the rest
// of the cluster through the seed URLs alone: NATS gossip propagates the
// full route table after the first successful connection, which makes
// scaling the deployment a matter of pointing the new node at any peer.
// With no seed URLs the node forms a fully functional single-node cluster,
// which is how development and single-instance deployments run.
package bus

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	log "github.com/sirupsen/logrus"
)

const (
	// ClusterName groups the routes: every node must use the same name or
	// the servers refuse to connect to each other.
	ClusterName = "strifira"

	// ClientPort serves NATS client connections. Only this process connects
	// to it (through the in-process listener below), but the port is bound
	// on the container address so operators can inspect the bus with the
	// standard NATS tooling.
	ClientPort = 4222
	// ClusterPort accepts route connections from the other nodes.
	ClusterPort = 6222

	// FilesBucket holds uploaded files and avatars as a JetStream object
	// store. Replicated across the cluster, every node serves any file.
	FilesBucket = "strifira-files"
	// PresenceBucket tracks issue viewers (one key per connected peer) with
	// a short TTL refreshed by heartbeats, so a dead node's viewers
	// disappear without explicit cleanup.
	PresenceBucket = "strifira-presence"
	// ThrottleBucket tracks failed login attempts per email cluster-wide.
	ThrottleBucket = "strifira-login-throttle"
	// ClusterBucket holds one heartbeating key per node, so every node sees
	// the live cluster size (the NATS-internal peer list is only complete
	// on the meta-layer leader).
	ClusterBucket = "strifira-cluster"
	// Membership heartbeat cadence and TTL: a dead node drops out of the
	// count within three missed beats.
	membershipInterval = 15 * time.Second
	membershipTTL      = 45 * time.Second
	membershipPrefix   = "node."

	// maxReplicas caps JetStream bucket replication regardless of cluster
	// size; beyond it the write amplification outweighs the redundancy.
	maxReplicas = 5
	// PresenceTTL must comfortably exceed the hub's presence heartbeat so a
	// lost viewer key only ever lags a refresh, not an active viewer.
	PresenceTTL = time.Minute
	// reconcileInterval re-aligns bucket replication with the current
	// cluster size: buckets are created at startup, when the routes may not
	// have joined yet, so replication grows as nodes join.
	reconcileInterval = 30 * time.Second
)

// DefaultStoreDir keeps JetStream's file storage on the persistent uploads
// volume, where every deployment already mounts durable disk.
const DefaultStoreDir = "./uploads/.jetstream"

// DefaultLegacyUploadsDir is the uploads root the pre-JetStream
// LocalBackend wrote to; on startup its tree is imported into the object
// store. It is the parent of DefaultStoreDir, but is configured
// separately: the import must only ever sweep the legacy tree, never
// whatever directory happens to hold the object store.
const DefaultLegacyUploadsDir = "./uploads"

// Config wires the bus to its environment. SeedURLs is the raw
// comma-separated list of the other nodes' cluster addresses
// (NATS_SEED_URLS); StoreDir is where JetStream persists file storage and
// LegacyUploadsDir the local-filesystem tree imported into it on startup
// (empty disables the import). ClientPort/ClusterPort are internal-API
// overrides for tests hosting several nodes in one process; zero selects
// the production defaults.
type Config struct {
	SeedURLs         string
	StoreDir         string
	LegacyUploadsDir string
	ClientPort       int
	ClusterPort      int
}

// Bus owns the embedded NATS server, its client connection and the shared
// JetStream buckets.
type Bus struct {
	server           *server.Server
	conn             *nats.Conn
	js               jetstream.JetStream
	storeDir         string
	legacyUploadsDir string
	stopping         chan struct{}
	done             chan struct{}

	// The buckets are provisioned in the background: in a cluster, any
	// JetStream API request waits for the meta-layer leader, which only
	// exists once the routes have formed — blocking New() would deadlock
	// nodes starting around each other. Accessors return nil until then
	// and every consumer treats that as "not ready yet".
	files     atomic.Value // jetstream.ObjectStore
	presence  atomic.Value // jetstream.KeyValue
	throttle  atomic.Value // jetstream.KeyValue
	clusterKV atomic.Value // jetstream.KeyValue
	clusterN  atomic.Int64 // live nodes counted by the membership heartbeat
}

// logrusNATS routes the embedded server's log lines through logrus so
// cluster formation is visible in the same stream as the application logs.
type logrusNATS struct{}

func (logrusNATS) Noticef(format string, v ...any) { log.Infof("[nats] "+format, v...) }
func (logrusNATS) Warnf(format string, v ...any)   { log.Warnf("[nats] "+format, v...) }
func (logrusNATS) Fatalf(format string, v ...any)  { log.Errorf("[nats] "+format, v...) }
func (logrusNATS) Errorf(format string, v ...any)  { log.Errorf("[nats] "+format, v...) }
func (logrusNATS) Debugf(format string, v ...any)  {}
func (logrusNATS) Tracef(format string, v ...any)  {}

// New starts the embedded server, connects to it and provisions the shared
// buckets. It fails fast: a node that cannot serve the bus cannot serve
// files, presence or realtime fan-out.
func New(cfg Config) (*Bus, error) {
	clientPort := cfg.ClientPort
	if clientPort == 0 {
		clientPort = ClientPort
	}
	clusterPort := cfg.ClusterPort
	if clusterPort == 0 {
		clusterPort = ClusterPort
	}
	opts := &server.Options{
		// The JetStream cluster layer requires an explicit, unique server
		// name; the hostname plus a fresh suffix keeps instances on one
		// host distinct.
		ServerName: fmt.Sprintf("%s-%s", shortHostName(), newSuffix()),
		Host:       "0.0.0.0",
		Port:       clientPort,
		// Files and KV are durable state: file storage, in the persistent
		// uploads volume next to where local storage used to live.
		JetStream: true,
		StoreDir:  cfg.StoreDir,
	}
	// The cluster listener only exists when there are peers to reach: a
	// node without seeds runs as a single-node cluster (JetStream
	// standalone), and one with seeds accepts its peers' route connections
	// and gossips the rest of the membership.
	routes := parseSeedURLs(splitSeedURLs(cfg.SeedURLs))
	if len(routes) > 0 {
		opts.Cluster = server.ClusterOpts{
			Name: ClusterName,
			Host: "0.0.0.0",
			Port: clusterPort,
			// Every node is configured with the full seed list, so gossip
			// advertising only produces duplicate route churn (the
			// advertised wildcard bind address is useless to peers anyway).
			NoAdvertise: true,
		}
		opts.Routes = routes
	}
	srv := server.New(opts)
	srv.SetLogger(logrusNATS{}, false, false)
	srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		srv.Shutdown()
		return nil, errors.New("embedded nats server did not become ready")
	}

	nc, err := nats.Connect(srv.ClientURL(), nats.InProcessServer(srv), nats.Name("strifira-node"))
	if err != nil {
		srv.Shutdown()
		return nil, fmt.Errorf("connecting to embedded nats server: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		srv.Shutdown()
		return nil, fmt.Errorf("enabling jetstream: %w", err)
	}

	b := &Bus{
		server:           srv,
		conn:             nc,
		js:               js,
		storeDir:         cfg.StoreDir,
		legacyUploadsDir: cfg.LegacyUploadsDir,
		stopping:         make(chan struct{}),
		done:             make(chan struct{}),
	}

	log.WithFields(log.Fields{
		"node_id":   srv.ID(),
		"cluster":   ClusterName,
		"seeds":     len(routes),
		"store_dir": cfg.StoreDir,
	}).Info("embedded nats server started")
	go b.provision()
	go b.reconcile()
	return b, nil
}

// loginLockoutTTL bounds the throttle bucket's key lifetime. It mirrors the
// server's login lockout (main.go), plus margin, because KV writes refresh
// the TTL and an email must not be re-admitted before its lockout expires.
const loginLockoutTTL = 30 * time.Minute

// shortHostName is the machine hostname, sanitized to a NATS-safe token.
func shortHostName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "node"
	}
	var b strings.Builder
	for _, r := range host {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "node"
	}
	return b.String()
}

// newSuffix makes concurrent instances on one host distinct.
func newSuffix() string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf)
}

// splitSeedURLs splits the comma-separated NATS_SEED_URLS value.
func splitSeedURLs(raw string) []string {
	return strings.Split(raw, ",")
}

func parseSeedURLs(raw []string) []*url.URL {
	urls := make([]*url.URL, 0, len(raw))
	for _, seed := range raw {
		seed = strings.TrimSpace(seed)
		if seed == "" {
			continue
		}
		u, err := url.Parse(seed)
		if err != nil || u.Host == "" {
			log.WithField("seed", seed).Warn("ignoring invalid nats seed url")
			continue
		}
		urls = append(urls, u)
	}
	return urls
}

// replicaCount sizes new buckets before the membership view exists: one
// replica, which the reconciler scales up as the cluster forms.
func replicaCount(srv *server.Server) int {
	if srv.JetStreamIsClustered() {
		return 2
	}
	return 1
}

func ensureObjectStore(ctx context.Context, js jetstream.JetStream, replicas int) (jetstream.ObjectStore, error) {
	os, err := js.CreateOrUpdateObjectStore(ctx, jetstream.ObjectStoreConfig{
		Bucket:   FilesBucket,
		Storage:  jetstream.FileStorage,
		Replicas: replicas,
	})
	if err != nil {
		return nil, fmt.Errorf("provisioning %s object store: %w", FilesBucket, err)
	}
	return os, nil
}

func ensureKV(ctx context.Context, js jetstream.JetStream, bucket string, ttl time.Duration, replicas int) (jetstream.KeyValue, error) {
	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:   bucket,
		TTL:      ttl,
		Storage:  jetstream.FileStorage,
		Replicas: replicas,
	})
	if err != nil {
		return nil, fmt.Errorf("provisioning %s kv bucket: %w", bucket, err)
	}
	return kv, nil
}

// provision creates the shared buckets, retrying until the cluster's
// JetStream meta layer can serve the requests. Once done, the reconcile
// loop takes over replication upkeep.
func (b *Bus) provision() {
	defer close(b.done)
	if b.server.JetStreamIsClustered() && !b.awaitRouteStability() {
		return
	}
	for {
		select {
		case <-b.stopping:
			return
		default:
		}
		replicas := replicaCount(b.server)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		files, filesErr := ensureObjectStore(ctx, b.js, replicas)
		presence, presenceErr := ensureKV(ctx, b.js, PresenceBucket, PresenceTTL, replicas)
		// The throttle outlives the lockout window, so its TTL is set
		// generously and expiry itself is computed from the recorded
		// timestamp.
		throttle, throttleErr := ensureKV(ctx, b.js, ThrottleBucket, loginLockoutTTL, replicas)
		cluster, clusterErr := ensureKV(ctx, b.js, ClusterBucket, membershipTTL, replicas)
		cancel()
		if filesErr == nil && presenceErr == nil && throttleErr == nil && clusterErr == nil {
			b.files.Store(files)
			b.presence.Store(presence)
			b.throttle.Store(throttle)
			b.clusterKV.Store(cluster)
			b.clusterN.Store(1)
			log.WithField("replicas", replicas).Info("jetstream buckets provisioned")
			go b.importLegacyFiles(files)
			go b.membership()
			return
		}
		for _, err := range []error{filesErr, presenceErr, throttleErr, clusterErr} {
			if err != nil {
				log.WithError(err).Warn("jetstream provisioning pending; retrying")
				break
			}
		}
		select {
		case <-b.stopping:
			return
		case <-time.After(3 * time.Second):
		}
	}
}

// membership heartbeats this node's key and recounts the live nodes, so
// the whole cluster converges on the same size view.
func (b *Bus) membership() {
	ticker := time.NewTicker(membershipInterval)
	defer ticker.Stop()
	b.beat()
	for {
		select {
		case <-b.stopping:
			return
		case <-ticker.C:
			b.beat()
		}
	}
}

func (b *Bus) beat() {
	cluster := b.ClusterKV()
	if cluster == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key := membershipPrefix + b.NodeID()
	if _, err := cluster.Put(ctx, key, []byte(strconv.FormatInt(time.Now().UnixNano(), 36))); err != nil {
		log.WithError(err).Warn("bus: cluster membership heartbeat failed")
		return
	}
	keys, err := cluster.Keys(ctx)
	if err != nil {
		log.WithError(err).Debug("bus: cluster membership count failed")
		return
	}
	live := int64(0)
	for _, k := range keys {
		if strings.HasPrefix(k, membershipPrefix) {
			live++
		}
	}
	if live > 0 {
		b.clusterN.Store(live)
	}
	if live != int64(b.ClusterPeers()) {
		log.WithField("nodes", live).Info("bus: cluster size changed")
	}
}

// awaitRouteStability blocks until the route table has been non-empty and
// unchanged for several samples, so the JetStream meta layer has settled
// into one leader. Bucket creation must never happen under a transient
// split-brain leader: each island would happily create its own copy of a
// bucket and the merged cluster would inherit divergent state. A clustered
// node that never reaches a peer never provisions — its /ready stays down
// and the load balancer keeps it out of rotation, which is the honest
// behavior for a node that cannot reach its cluster.
func (b *Bus) awaitRouteStability() bool {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	stable := 0
	last := -1
	for {
		select {
		case <-b.stopping:
			return false
		case <-tick.C:
		}
		routes := b.server.NumRoutes()
		if routes > 0 && routes == last {
			stable++
			if stable >= 5 {
				return true
			}
		} else {
			stable = 0
		}
		last = routes
	}
}

// reconcile keeps bucket replication aligned with the cluster: a node that
// starts alone (or before its peers join) provisions replicas=1 and scales
// the buckets up as more nodes join. Replication only grows: shrinking a
// live stream because a node dropped out would cut redundancy exactly when
// it matters.
func (b *Bus) reconcile() {
	select {
	case <-b.stopping:
		return
	case <-b.done:
	}
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-b.stopping:
			return
		case <-ticker.C:
		}
		desired := b.ClusterPeers()
		if desired > maxReplicas {
			desired = maxReplicas
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		b.reconcileObjectStore(ctx, desired)
		b.reconcileKV(ctx, PresenceBucket, PresenceTTL, desired)
		b.reconcileKV(ctx, ThrottleBucket, loginLockoutTTL, desired)
		b.reconcileKV(ctx, ClusterBucket, membershipTTL, desired)
		cancel()
	}
}

func (b *Bus) reconcileObjectStore(ctx context.Context, desired int) {
	files := b.Files()
	if files == nil {
		return
	}
	info, err := files.Status(ctx)
	if err != nil {
		log.WithError(err).Debug("bus reconcile: object store status failed")
		return
	}
	if info.Replicas() == desired {
		return
	}
	if _, err := b.js.UpdateObjectStore(ctx, jetstream.ObjectStoreConfig{
		Bucket:   FilesBucket,
		Storage:  jetstream.FileStorage,
		Replicas: desired,
	}); err != nil {
		log.WithError(err).WithField("replicas", desired).Warn("bus reconcile: object store rescale failed")
	}
}

func (b *Bus) reconcileKV(ctx context.Context, bucket string, ttl time.Duration, desired int) {
	kv, err := b.js.KeyValue(ctx, bucket)
	if err != nil {
		log.WithError(err).WithField("bucket", bucket).Debug("bus reconcile: kv status failed")
		return
	}
	status, err := kv.Status(ctx)
	if err != nil {
		log.WithError(err).WithField("bucket", bucket).Debug("bus reconcile: kv status failed")
		return
	}
	if status.Config().Replicas == desired {
		return
	}
	if _, err := b.js.UpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:   bucket,
		TTL:      ttl,
		Storage:  jetstream.FileStorage,
		Replicas: desired,
	}); err != nil {
		log.WithError(err).WithField("bucket", bucket).WithField("replicas", desired).Warn("bus reconcile: kv rescale failed")
	}
}

// NodeID identifies this node in cluster envelopes and logs.
func (b *Bus) NodeID() string { return b.server.ID() }

// ClusterName reports the NATS cluster this node belongs to. The name is a
// package constant: every node must use the same one to gossip together.
func (b *Bus) ClusterName() string { return ClusterName }

// ClusterPeers reports how many nodes the cluster currently spans
// (including this node), counted from the membership heartbeats. A
// standalone node counts as a cluster of one.
func (b *Bus) ClusterPeers() int {
	if n := b.clusterN.Load(); n > 0 {
		return int(n)
	}
	return 1
}

// ClusterKV returns the cluster-membership KV bucket, or nil until
// provisioned.
func (b *Bus) ClusterKV() jetstream.KeyValue {
	if v := b.clusterKV.Load(); v != nil {
		return v.(jetstream.KeyValue)
	}
	return nil
}

// Conn exposes the client connection for cross-node messaging subjects.
func (b *Bus) Conn() *nats.Conn { return b.conn }

// Files returns the uploaded-files object store, or nil until the buckets
// are provisioned (seconds after startup at most).
func (b *Bus) Files() jetstream.ObjectStore {
	if v := b.files.Load(); v != nil {
		return v.(jetstream.ObjectStore)
	}
	return nil
}

// Presence returns the issue-presence KV bucket, or nil until provisioned.
func (b *Bus) Presence() jetstream.KeyValue {
	if v := b.presence.Load(); v != nil {
		return v.(jetstream.KeyValue)
	}
	return nil
}

// Throttle returns the login-throttle KV bucket, or nil until provisioned.
func (b *Bus) Throttle() jetstream.KeyValue {
	if v := b.throttle.Load(); v != nil {
		return v.(jetstream.KeyValue)
	}
	return nil
}

// Ready reports whether the node can serve files, presence and throttling:
// the client connection is up and every shared bucket is provisioned.
// /ready gates on it so traffic only lands on complete nodes.
func (b *Bus) Ready() bool {
	return b.conn.IsConnected() && b.Files() != nil && b.Presence() != nil && b.Throttle() != nil
}

// Close drains the client connection and shuts the embedded server down.
func (b *Bus) Close() {
	if b == nil {
		return
	}
	select {
	case <-b.stopping:
	default:
		close(b.stopping)
	}
	select {
	case <-b.done:
	case <-time.After(reconcileInterval + 5*time.Second):
	}
	if b.conn != nil {
		b.conn.Drain()
	}
	if b.server != nil {
		b.server.Shutdown()
	}
}
