package realtime

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	log "github.com/sirupsen/logrus"
)

// Fanout carries workspace events between the nodes of the cluster and
// keeps the cluster-wide state a node needs to answer any request:
// issue presence lives in a replicated JetStream KV bucket, and screen
// share membership is merged from per-node summaries carried on the same
// subjects as the events themselves.
//
// A nil Fanout keeps the hub entirely local (single-node deployments and
// tests take the original in-process code paths).
const (
	hubSubjectPrefix  = "strifira.hub."
	docSubjectPrefix  = "strifira.doc."
	presenceKeyPrefix = "p."
	// presenceHeartbeat refreshes local viewers' keys well inside the bus
	// bucket TTL (bus.PresenceTTL), so a dead node's viewers age out without
	// explicit cleanup.
	presenceHeartbeat = 20 * time.Second
	// docHistoryWait bounds how long a node joining a document waits for
	// the other nodes' live update logs before falling back to the
	// database log alone.
	docHistoryWait = 750 * time.Millisecond
	// docHistoryMaxBytes caps one node's history reply; a document whose
	// live log exceeds it is served from the database log instead (the
	// compaction threshold keeps live logs far below this).
	docHistoryMaxBytes = 6 << 20
)

type wireKind string

const (
	wireEvent         wireKind = "event"
	wireSharePresence wireKind = "share-presence"
	wireShareSignal   wireKind = "share-signal"
)

// wireEnvelope is the cross-node form of everything the hub spreads over
// the cluster. The zero values of the selector fields make the variants:
// TargetUser and ExcludePeer scope a plain event; Summary merges share
// membership; Signal relays WebRTC signaling between members on any node.
type wireEnvelope struct {
	Node        string          `json:"node"`
	WorkspaceID uuid.UUID       `json:"workspace_id"`
	ExcludePeer string          `json:"exclude_peer,omitempty"`
	TargetUser  string          `json:"target_user,omitempty"`
	Kind        wireKind        `json:"kind"`
	Event       *Event          `json:"event,omitempty"`
	Summary     *shareSummary   `json:"summary,omitempty"`
	Signal      *signalEnvelope `json:"signal,omitempty"`
}

// shareSummary is one node's local membership of one screen share. The
// merged membership visible to clients is the union across nodes.
type shareSummary struct {
	ShareID       string   `json:"share_id"`
	PresenterPeer string   `json:"presenter_peer,omitempty"`
	PresenterUser string   `json:"presenter_user,omitempty"`
	ViewerPeers   []string `json:"viewer_peers,omitempty"`
	ViewerUsers   []string `json:"viewer_users,omitempty"`
}

// signalEnvelope carries one WebRTC signaling message to the node(s) that
// hold the addressed peer. The sender's membership was already validated on
// its own node; receivers route to the opposite role among their locals.
type signalEnvelope struct {
	ShareID       string          `json:"share_id"`
	From          string          `json:"from"`
	FromPeer      string          `json:"from_peer"`
	FromRole      string          `json:"from_role"`
	NegotiationID string          `json:"negotiation_id"`
	To            string          `json:"to,omitempty"`
	ToPeer        string          `json:"to_peer,omitempty"`
	Data          json.RawMessage `json:"data"`
}

// DocEnvelope carries one doc-sync relay frame to the nodes with editors of
// that document. Frame is the exact client-facing JSON; Node identifies the
// origin so receivers can skip work their local state already covers, and
// ExcludePeer scopes local relay away from the origin editor.
type DocEnvelope struct {
	Node        string    `json:"node"`
	DocID       uuid.UUID `json:"doc_id"`
	ExcludePeer string    `json:"exclude_peer,omitempty"`
	Frame       []byte    `json:"frame"`
}

// remoteShare is another node's announced membership of one share.
type remoteShare struct {
	workspaceID uuid.UUID
	summary     *shareSummary
}

// Fanout publishes and receives cluster envelopes over the embedded NATS
// server and mirrors the presence bucket into a local index.
type Fanout struct {
	node string
	conn *nats.Conn
	// getPresence resolves the presence bucket on every use: the bus
	// provisions its buckets in the background after the cluster's
	// JetStream meta layer forms.
	getPresence func() jetstream.KeyValue

	mu      sync.RWMutex
	viewers map[string]string                       // presence key → user ID
	shares  map[uuid.UUID]map[string]*remoteShare   // share → origin node → membership
	docs    map[uuid.UUID]map[string]*docRemotePeer // doc → peer → remote editor

	// onShareUpdate fires (never under a lock) when a remote summary change
	// may have altered a share's merged membership, so the hub re-broadcasts
	// the merged view to its local clients.
	onShareUpdate func(shareID uuid.UUID)

	subs      []*nats.Subscription
	stopWatch chan struct{}
	watchDone chan struct{}
}

// docRemotePeer is another node's editor of a document this node knows
// about, announced and refreshed by relay frames.
type docRemotePeer struct {
	payload  map[string]any
	lastSeen time.Time
}

// NewFanout wires the cluster fan-out onto the bus client connection. The
// presence watcher fills the local index with the current bucket state and
// then follows every change, so presence queries never leave the process.
func NewFanout(conn *nats.Conn, nodeID string, getPresence func() jetstream.KeyValue) *Fanout {
	f := &Fanout{
		node:        nodeID,
		conn:        conn,
		getPresence: getPresence,
		viewers:     make(map[string]string),
		shares:      make(map[uuid.UUID]map[string]*remoteShare),
		docs:        make(map[uuid.UUID]map[string]*docRemotePeer),
		stopWatch:   make(chan struct{}),
		watchDone:   make(chan struct{}),
	}
	go f.watchPresence()
	return f
}

// SetHubHandler subscribes the workspace subjects; every envelope published
// by any node — this one included — is handed to the hub for local
// delivery. Must be called once during startup, before serving traffic.
func (f *Fanout) SetHubHandler(hub *Hub) {
	f.onShareUpdate = func(shareID uuid.UUID) { hub.rebroadcastShare(shareID) }
	sub, err := f.conn.Subscribe(hubSubjectPrefix+"*", func(msg *nats.Msg) {
		var env wireEnvelope
		if json.Unmarshal(msg.Data, &env) != nil {
			return
		}
		switch env.Kind {
		case wireEvent:
			hub.deliverEvent(&env)
		case wireSharePresence:
			f.applyShareSummary(&env)
		case wireShareSignal:
			hub.deliverShareSignal(&env)
		}
	})
	if err == nil {
		f.subs = append(f.subs, sub)
	} else {
		log.WithError(err).Error("fanout: hub subscription failed")
	}
}

// SetDocHandler subscribes the doc-sync subjects the same way.
func (f *Fanout) SetDocHandler(handler func(env *DocEnvelope)) {
	sub, err := f.conn.Subscribe(docSubjectPrefix+"*", func(msg *nats.Msg) {
		var env DocEnvelope
		if json.Unmarshal(msg.Data, &env) != nil {
			return
		}
		handler(&env)
	})
	if err == nil {
		f.subs = append(f.subs, sub)
	} else {
		log.WithError(err).Error("fanout: doc subscription failed")
	}
}

// Close stops the presence watcher and drains the subscriptions.
func (f *Fanout) Close() {
	if f == nil {
		return
	}
	select {
	case <-f.stopWatch:
		return
	default:
		close(f.stopWatch)
	}
	select {
	case <-f.watchDone:
	case <-time.After(5 * time.Second):
	}
	for _, sub := range f.subs {
		_ = sub.Drain()
	}
}

// watchPresence mirrors the presence bucket locally. The initial keyspace
// arrives first, then live updates; deletes drop the key.
func (f *Fanout) watchPresence() {
	defer close(f.watchDone)
	// Wait for the bus to provision the presence bucket (near-immediate on
	// a healthy cluster).
	var presence jetstream.KeyValue
	for {
		presence = f.getPresence()
		if presence != nil {
			break
		}
		select {
		case <-f.stopWatch:
			close(f.watchDone)
			return
		case <-time.After(time.Second):
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-f.stopWatch:
			cancel()
		case <-ctx.Done():
		}
	}()
	watcher, err := presence.Watch(ctx, presenceKeyPrefix+">")
	if err != nil {
		// Without the mirror, presence.sync answers only with local viewers
		// until the watcher can be established; log loudly and retry slowly.
		log.WithError(err).Error("fanout: presence watcher failed")
		return
	}
	defer watcher.Stop()
	for {
		select {
		case <-f.stopWatch:
			return
		case entry, ok := <-watcher.Updates():
			if !ok {
				return
			}
			if entry == nil {
				// nil marks the end of the initial replay; live updates follow.
				continue
			}
			f.mu.Lock()
			switch entry.Operation() {
			case jetstream.KeyValuePut:
				f.viewers[entry.Key()] = string(entry.Value())
			default: // delete or purge
				delete(f.viewers, entry.Key())
			}
			f.mu.Unlock()
		}
	}
}

// PutPresence announces a connected viewer (called on join and by the hub's
// heartbeat). The bucket TTL bounds the entry's life; the heartbeat refreshes it.
func (f *Fanout) PutPresence(workspaceID, issueID string, peerID, userID uuid.UUID) {
	key := presenceKey(workspaceID, issueID, peerID)
	f.mu.Lock()
	f.viewers[key] = userID.String()
	f.mu.Unlock()
	if presence := f.getPresence(); presence != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := presence.Put(ctx, key, []byte(userID.String())); err != nil {
			log.WithError(err).WithField("key", key).Warn("fanout: presence put failed")
		}
	}
}

// DropPresence removes a viewer entry (leave, disconnect, shutdown).
func (f *Fanout) DropPresence(workspaceID, issueID string, peerID uuid.UUID) {
	key := presenceKey(workspaceID, issueID, peerID)
	if presence := f.getPresence(); presence != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := presence.Delete(ctx, key); err != nil && !strings.Contains(err.Error(), "not found") {
			log.WithError(err).WithField("key", key).Warn("fanout: presence delete failed")
		}
	}
	f.mu.Lock()
	delete(f.viewers, key)
	f.mu.Unlock()
}

func presenceKey(workspaceID, issueID string, peerID uuid.UUID) string {
	return presenceKeyPrefix + workspaceID + "." + issueID + "." + peerID.String()
}

// IssueViewers reports the user IDs of everyone viewing the issue
// cluster-wide, one entry per connected peer, excluding the asking peer.
func (f *Fanout) IssueViewers(workspaceID, issueID string, excludePeer uuid.UUID) []string {
	prefix := presenceKeyPrefix + workspaceID + "." + issueID + "."
	suffix := "." + excludePeer.String()
	f.mu.RLock()
	defer f.mu.RUnlock()
	var viewers []string
	for key, user := range f.viewers {
		if strings.HasPrefix(key, prefix) && !strings.HasSuffix(key, suffix) {
			viewers = append(viewers, user)
		}
	}
	return viewers
}

// publishEvent fans a workspace event out to every node (the origin
// included — delivery to local clients happens on receive).
func (f *Fanout) publishEvent(workspaceID uuid.UUID, event Event, excludePeer, targetUser string) {
	data, err := json.Marshal(wireEnvelope{
		Node:        f.node,
		WorkspaceID: workspaceID,
		ExcludePeer: excludePeer,
		TargetUser:  targetUser,
		Kind:        wireEvent,
		Event:       &event,
	})
	if err != nil {
		log.WithError(err).Error("fanout: marshal event failed")
		return
	}
	if err := f.conn.Publish(hubSubjectPrefix+workspaceID.String(), data); err != nil {
		log.WithError(err).Error("fanout: publish event failed")
	}
}

// PublishShareSummary publishes this node's (possibly empty) membership of
// one share so every node can merge the global view.
func (f *Fanout) PublishShareSummary(workspaceID uuid.UUID, summary *shareSummary) {
	if summary == nil {
		return
	}
	data, err := json.Marshal(wireEnvelope{
		Node:        f.node,
		WorkspaceID: workspaceID,
		Kind:        wireSharePresence,
		Summary:     summary,
	})
	if err != nil {
		return
	}
	if err := f.conn.Publish(hubSubjectPrefix+workspaceID.String(), data); err != nil {
		log.WithError(err).Warn("fanout: publish share summary failed")
	}
}

// applyShareSummary records a remote node's summary and reports membership
// changes back to the hub. Own summaries are ignored: local state is
// authoritative for this node.
func (f *Fanout) applyShareSummary(env *wireEnvelope) {
	if env.Node == f.node || env.Summary == nil {
		return
	}
	shareID, err := uuid.Parse(env.Summary.ShareID)
	if err != nil {
		return
	}
	f.mu.Lock()
	if env.Summary.empty() {
		delete(f.shares[shareID], env.Node)
		if len(f.shares[shareID]) == 0 {
			delete(f.shares, shareID)
		}
	} else {
		if f.shares[shareID] == nil {
			f.shares[shareID] = make(map[string]*remoteShare)
		}
		f.shares[shareID][env.Node] = &remoteShare{workspaceID: env.WorkspaceID, summary: env.Summary}
	}
	f.mu.Unlock()

	if f.onShareUpdate != nil {
		f.onShareUpdate(shareID)
	}
}

func (s *shareSummary) empty() bool {
	return s.PresenterPeer == "" && len(s.ViewerPeers) == 0
}

// MergedShare combines the local summary (which may be nil) with the last
// known summaries of every other node. The presenter is the local one when
// present, otherwise a stable choice among the remotes; distinct viewers
// are collected by user ID.
func (f *Fanout) MergedShare(shareID uuid.UUID, local *shareSummary) *shareSummary {
	merged := &shareSummary{ShareID: shareID.String()}
	if local != nil {
		merged.PresenterPeer = local.PresenterPeer
		merged.PresenterUser = local.PresenterUser
		merged.ViewerPeers = append(merged.ViewerPeers, local.ViewerPeers...)
		merged.ViewerUsers = append(merged.ViewerUsers, local.ViewerUsers...)
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, node := range sortedNodes(f.shares[shareID]) {
		summary := f.shares[shareID][node].summary
		if summary.PresenterPeer != "" && merged.PresenterPeer == "" {
			merged.PresenterPeer = summary.PresenterPeer
			merged.PresenterUser = summary.PresenterUser
		}
		merged.ViewerPeers = append(merged.ViewerPeers, summary.ViewerPeers...)
		merged.ViewerUsers = append(merged.ViewerUsers, summary.ViewerUsers...)
	}
	return merged
}

// ShareWorkspace reports which workspace a known share belongs to, so a
// remote summary change can be re-announced to the local clients of that
// workspace. Nil UUID when nothing is known about the share.
func (f *Fanout) ShareWorkspace(shareID uuid.UUID) uuid.UUID {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, known := range f.shares[shareID] {
		return known.workspaceID
	}
	return uuid.Nil
}

// sortedNodes makes the merged presenter choice deterministic across nodes.
func sortedNodes[T any](byNode map[string]T) []string {
	nodes := make([]string, 0, len(byNode))
	for node := range byNode {
		nodes = append(nodes, node)
	}
	for i := 1; i < len(nodes); i++ {
		for j := i; j > 0 && nodes[j] < nodes[j-1]; j-- {
			nodes[j], nodes[j-1] = nodes[j-1], nodes[j]
		}
	}
	return nodes
}

// PublishShareSignal relays a validated signaling message to the nodes that
// may hold the addressed peer.
func (f *Fanout) PublishShareSignal(workspaceID uuid.UUID, sig *signalEnvelope) {
	data, err := json.Marshal(wireEnvelope{
		Node:        f.node,
		WorkspaceID: workspaceID,
		Kind:        wireShareSignal,
		Signal:      sig,
	})
	if err != nil {
		return
	}
	if err := f.conn.Publish(hubSubjectPrefix+workspaceID.String(), data); err != nil {
		log.WithError(err).Warn("fanout: publish share signal failed")
	}
}

// PublishDoc relays one doc-sync frame to every node with editors of the
// document (the origin included).
func (f *Fanout) PublishDoc(docID uuid.UUID, frame []byte, excludePeer string) {
	data, err := json.Marshal(DocEnvelope{
		Node:        f.node,
		DocID:       docID,
		ExcludePeer: excludePeer,
		Frame:       frame,
	})
	if err != nil {
		return
	}
	if err := f.conn.Publish(docSubjectPrefix+docID.String(), data); err != nil {
		log.WithError(err).Warn("fanout: publish doc frame failed")
	}
}

// Self reports this node's cluster id, so receivers can skip work their own
// publications already did synchronously.
func (f *Fanout) Self() string { return f.node }

// docHistoryReply is the responder's answer: its live update log plus the
// editor peers it holds (updates and peers travel together because a node
// creating its room needs both to catch up).
type docHistoryReply struct {
	Updates [][]byte         `json:"updates"`
	Peers   []map[string]any `json:"peers"`
}

// DocHistory asks the other nodes for their live update log and current
// editor peers of a document while this node is creating its room; the
// database log may lag the live rooms by one flush interval.
func (f *Fanout) DocHistory(ctx context.Context, docID uuid.UUID) ([][]byte, []map[string]any) {
	reply := nats.NewInbox()
	replies := make(chan []byte, 16)
	sub, err := f.conn.Subscribe(reply, func(msg *nats.Msg) {
		if len(msg.Data) == 0 || len(msg.Data) > docHistoryMaxBytes {
			return
		}
		select {
		case replies <- msg.Data:
		default:
		}
	})
	if err != nil {
		log.WithError(err).Warn("fanout: doc history subscribe failed")
		return nil, nil
	}
	defer sub.Drain()
	if err := f.conn.PublishRequest(docHistorySubject(docID), reply, nil); err != nil {
		log.WithError(err).Warn("fanout: doc history request failed")
		return nil, nil
	}
	var updates [][]byte
	var peers []map[string]any
	deadline := time.After(docHistoryWait)
	for {
		select {
		case data := <-replies:
			var replyMsg docHistoryReply
			if json.Unmarshal(data, &replyMsg) == nil {
				updates = append(updates, replyMsg.Updates...)
				peers = append(peers, replyMsg.Peers...)
			}
		case <-deadline:
			return updates, peers
		case <-ctx.Done():
			return updates, peers
		}
	}
}

func docHistorySubject(docID uuid.UUID) string {
	return docSubjectPrefix + docID.String() + ".hist"
}

// SetDocHistoryResponder answers history requests from nodes creating
// their room: the live log and editor peers this node holds, or nothing
// when the reply would exceed the message cap (the requester then relies on
// the database log).
func (f *Fanout) SetDocHistoryResponder(snapshot func(docID uuid.UUID) ([][]byte, []map[string]any)) {
	sub, err := f.conn.Subscribe(docSubjectPrefix+"*.hist", func(msg *nats.Msg) {
		parts := strings.Split(msg.Subject, ".")
		if len(parts) < 4 {
			return
		}
		docID, err := uuid.Parse(parts[2])
		if err != nil {
			return
		}
		updates, peers := snapshot(docID)
		reply := docHistoryReply{Updates: updates, Peers: peers}
		data, err := json.Marshal(reply)
		if err != nil || len(data) > docHistoryMaxBytes {
			return
		}
		_ = f.conn.Publish(msg.Reply, data)
	})
	if err == nil {
		f.subs = append(f.subs, sub)
	} else {
		log.WithError(err).Error("fanout: doc history responder failed")
	}
}
