package realtime

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"nhooyr.io/websocket"
)

type Event struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Payload   any       `json:"payload"`
}

type Client struct {
	conn         *websocket.Conn
	workspaceID  uuid.UUID
	userID       uuid.UUID
	peerID       uuid.UUID
	send         chan []byte
	viewingIssue string // issue ID currently being viewed (empty if none)
}

// Incoming message types from clients
type IncomingMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type PresencePayload struct {
	IssueID string `json:"issue_id"`
}

type CursorPayload struct {
	IssueID string  `json:"issue_id"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
}

// SharePresencePayload announces a client's role in a screen share so the
// backend can count viewers and detect a gone presenter (P2P WebRTC: the
// hub carries signaling; media never transits the server).
type SharePresencePayload struct {
	ShareID        string `json:"share_id"`
	Role           string `json:"role"`   // "presenter" | "viewer"
	Action         string `json:"action"` // "join" | "leave"
	PresenterToken string `json:"presenter_token"`
}

// ShareSignalPayload relays an opaque WebRTC signaling message (SDP
// description or ICE candidate) to a member's individual socket.
type ShareSignalPayload struct {
	ShareID       string          `json:"share_id"`
	To            string          `json:"to"`
	ToPeer        string          `json:"to_peer"`
	NegotiationID string          `json:"negotiation_id"`
	Data          json.RawMessage `json:"data"`
}

// shareState is the live membership of one screen share.
type shareState struct {
	workspaceID uuid.UUID
	presenters  map[*Client]bool
	viewers     map[*Client]bool
}

type shareLifecycleLock struct {
	mu    sync.Mutex
	users int
}

type Hub struct {
	mu         sync.RWMutex
	clients    map[uuid.UUID]map[*Client]bool // workspaceID -> clients
	shares     map[uuid.UUID]*shareState      // shareID -> live membership
	shareLocks map[uuid.UUID]*shareLifecycleLock
	shareEpoch map[uuid.UUID]uint64
	// presenterSeen remembers, per share, when a presenter last announced
	// here. It deliberately survives the share's last connection leaving
	// (only ClearShare drops it): the no-source GC measures its grace from
	// this moment rather than from the share's creation, so a long-lived
	// share is not ended by a brief signaling drop.
	presenterSeen map[uuid.UUID]time.Time
	// startedAt anchors the grace window after a process restart: the
	// in-memory map above is empty then, and reconnecting presenters get a
	// fresh grace period rather than being judged against a pre-restart
	// timestamp.
	startedAt time.Time
	// OnPresenterSeen, when set, is invoked (never under the lock) after a
	// presenter announces for a share, so the persistence layer can record
	// contact across restarts. Optional and best-effort.
	OnPresenterSeen func(shareID uuid.UUID)
	// AuthorizeSharePresence must validate the active share, workspace and
	// presenter token. An unwired hub refuses membership.
	AuthorizeSharePresence func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, string) bool
	// OnBroadcast, when set, observes every workspace-wide Broadcast (never
	// under the lock), e.g. to forward domain events to outgoing webhooks.
	// It must not block; wire it before the hub starts serving.
	OnBroadcast func(workspaceID uuid.UUID, event Event)

	// closing is set by Shutdown: new upgrades are refused, and connections
	// whose upgrade was admitted earlier are closed as they register.
	closing bool
	// upgrading counts admitted upgrades that have not registered (or
	// failed) yet; Shutdown waits for them as well as for clients.
	upgrading int

	// fanout, when set, carries events to the other nodes of the cluster and
	// makes issue presence and share membership cluster-wide. A nil fanout
	// (tests, single-node) keeps every path local.
	fanout *Fanout
	// stopHeartbeat/heartbeatDone bound the goroutine refreshing this
	// node's viewers' presence keys for the whole cluster.
	stopHeartbeat chan struct{}
	heartbeatDone chan struct{}
}

// SetFanout wires the cluster fan-out before the hub starts serving. The
// hub then delivers local clients through received envelopes (its own
// publications included) and answers presence queries from the replicated
// index instead of its local client tables.
func (h *Hub) SetFanout(f *Fanout) {
	h.fanout = f
	f.SetHubHandler(h)
	h.stopHeartbeat = make(chan struct{})
	h.heartbeatDone = make(chan struct{})
	go h.presenceHeartbeat()
}

func NewHub() *Hub {
	return NewHubStartedAt(time.Now())
}

// NewHubStartedAt builds a hub whose restart-grace anchor is the given
// instant; production uses NewHub, tests simulate long-running processes.
func NewHubStartedAt(startedAt time.Time) *Hub {
	return &Hub{
		clients:       make(map[uuid.UUID]map[*Client]bool),
		shares:        make(map[uuid.UUID]*shareState),
		shareLocks:    make(map[uuid.UUID]*shareLifecycleLock),
		shareEpoch:    make(map[uuid.UUID]uint64),
		presenterSeen: make(map[uuid.UUID]time.Time),
		startedAt:     startedAt,
	}
}

func (h *Hub) Register(conn *websocket.Conn, workspaceID, userID uuid.UUID) *Client {
	client := &Client{
		conn:        conn,
		workspaceID: workspaceID,
		userID:      userID,
		peerID:      uuid.New(),
		send:        make(chan []byte, 256),
	}

	h.mu.Lock()
	if h.clients[workspaceID] == nil {
		h.clients[workspaceID] = make(map[*Client]bool)
	}
	h.clients[workspaceID][client] = true
	closing := h.closing
	h.mu.Unlock()

	if closing && conn != nil {
		go conn.Close(websocket.StatusGoingAway, "server shutting down")
	}
	return client
}

// BeginUpgrade admits a WebSocket upgrade before the connection is hijacked.
// It refuses (ok=false) once Shutdown has started, so the caller can answer
// 503 instead of upgrading. Otherwise done must be called exactly once after
// the client is registered or the upgrade failed; until then Shutdown keeps
// waiting, so a connection hijacked during shutdown cannot slip past it.
func (h *Hub) BeginUpgrade() (done func(), ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return nil, false
	}
	h.upgrading++
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			h.upgrading--
			h.mu.Unlock()
		})
	}, true
}

// Shutdown refuses new upgrades, closes every live connection with
// StatusGoingAway and waits until admitted upgrades have settled and every
// handler has unregistered its client, or until ctx ends. Hijacked
// WebSockets are invisible to http.Server.Shutdown, so the server must call
// this explicitly to let clients reconnect elsewhere and to run the
// per-connection cleanup before the process exits.
func (h *Hub) Shutdown(ctx context.Context) error {
	if h.stopHeartbeat != nil {
		close(h.stopHeartbeat)
		select {
		case <-h.heartbeatDone:
		case <-time.After(5 * time.Second):
		}
	}
	h.mu.Lock()
	h.closing = true
	conns := make([]*websocket.Conn, 0)
	for _, clients := range h.clients {
		for client := range clients {
			if client.conn != nil {
				conns = append(conns, client.conn)
			}
		}
	}
	h.mu.Unlock()

	for _, conn := range conns {
		// Close performs the close handshake (bounded by the library), so
		// connections are closed concurrently.
		go conn.Close(websocket.StatusGoingAway, "server shutting down")
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		h.mu.RLock()
		remaining := len(h.clients) + h.upgrading
		h.mu.RUnlock()
		if remaining == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (h *Hub) Unregister(client *Client) {
	h.mu.Lock()
	if !h.clients[client.workspaceID][client] {
		h.mu.Unlock()
		return
	}
	presenterDepartures := make([]uuid.UUID, 0)
	if clients, ok := h.clients[client.workspaceID]; ok {
		delete(clients, client)
		if len(clients) == 0 {
			delete(h.clients, client.workspaceID)
		}
	}
	// Leaving the socket leaves every share: broadcast the updated
	// membership so presenters' counts drop and viewers learn when the
	// presenter is gone.
	affected := make([]uuid.UUID, 0)
	for shareID, state := range h.shares {
		if state.presenters[client] || state.viewers[client] {
			if state.presenters[client] {
				h.presenterSeen[shareID] = time.Now()
				presenterDepartures = append(presenterDepartures, shareID)
			}
			delete(state.presenters, client)
			delete(state.viewers, client)
			if len(state.presenters) == 0 && len(state.viewers) == 0 {
				delete(h.shares, shareID)
			}
			affected = append(affected, shareID)
		}
	}
	close(client.send)
	h.mu.Unlock()

	for _, shareID := range presenterDepartures {
		if h.OnPresenterSeen != nil {
			h.OnPresenterSeen(shareID)
		}
	}
	for _, shareID := range affected {
		h.broadcastSharePresence(client.workspaceID, shareID)
	}
}

func (h *Hub) Broadcast(workspaceID uuid.UUID, event Event) {
	event.Timestamp = time.Now()
	if h.OnBroadcast != nil {
		h.OnBroadcast(workspaceID, event)
	}

	if h.fanout != nil {
		// Cluster path: every node — this one included — receives the
		// envelope and delivers to its local clients (deliverEvent).
		h.fanout.publishEvent(workspaceID, event, "", "")
		return
	}

	data, err := json.Marshal(event)
	if err != nil {
		log.WithError(err).Error("failed to marshal event")
		return
	}

	// Send while holding the read lock: Unregister mutates these maps and
	// closes client channels under the write lock, and an unlocked
	// iteration racing it is a fatal concurrent map read/write (or a send
	// on a closed channel) that takes the whole process — and every
	// WebSocket — down. The selects are non-blocking, so hold time is
	// bounded.
	h.mu.RLock()
	defer h.mu.RUnlock()

	for client := range h.clients[workspaceID] {
		select {
		case client.send <- data:
		default:
			// Client buffer full, skip
		}
	}
}

func (h *Hub) BroadcastToUser(workspaceID, userID uuid.UUID, event Event) {
	event.Timestamp = time.Now()

	if h.fanout != nil {
		h.fanout.publishEvent(workspaceID, event, "", userID.String())
		return
	}

	data, err := json.Marshal(event)
	if err != nil {
		log.WithError(err).Error("failed to marshal event")
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for client := range h.clients[workspaceID] {
		if client.userID == userID {
			select {
			case client.send <- data:
			default:
			}
		}
	}
}

// deliverEvent fans a received cluster envelope out to this node's local
// clients, honoring the origin's target/exclusion selectors.
func (h *Hub) deliverEvent(env *wireEnvelope) {
	if env.Event == nil {
		return
	}
	data, err := json.Marshal(*env.Event)
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for client := range h.clients[env.WorkspaceID] {
		if env.TargetUser != "" && client.userID.String() != env.TargetUser {
			continue
		}
		if env.ExcludePeer != "" && client.peerID.String() == env.ExcludePeer {
			continue
		}
		select {
		case client.send <- data:
		default:
		}
	}
}

func (h *Hub) WritePump(ctx context.Context, client *Client) {
	for {
		select {
		case msg, ok := <-client.send:
			if !ok {
				return
			}
			err := client.conn.Write(ctx, websocket.MessageText, msg)
			if err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func (h *Hub) ReadPump(ctx context.Context, client *Client) {
	defer func() {
		if client.viewingIssue != "" {
			h.handlePresenceLeave(client)
		}
	}()

	for {
		_, data, err := client.conn.Read(ctx)
		if err != nil {
			return
		}

		var msg IncomingMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "presence.join":
			var p PresencePayload
			if json.Unmarshal(msg.Payload, &p) == nil && p.IssueID != "" {
				h.handlePresenceJoin(client, p.IssueID)
			}
		case "presence.leave":
			h.handlePresenceLeave(client)
		case "cursor.move":
			var p CursorPayload
			if json.Unmarshal(msg.Payload, &p) == nil && p.IssueID != "" {
				h.BroadcastExcluding(client.workspaceID, client, Event{
					Type: "cursor.move",
					Payload: map[string]any{
						"issue_id": p.IssueID,
						"user_id":  client.userID.String(),
						"x":        p.X,
						"y":        p.Y,
					},
				})
			}
		case "focus.update", "focus.leave":
			// Relay focus/cursor-position events to other clients viewing the same issue
			var raw map[string]any
			if json.Unmarshal(msg.Payload, &raw) == nil {
				raw["user_id"] = client.userID.String()
				h.BroadcastExcluding(client.workspaceID, client, Event{
					Type:    msg.Type,
					Payload: raw,
				})
			}
		case "share.presence":
			var p SharePresencePayload
			if json.Unmarshal(msg.Payload, &p) == nil {
				if shareID, err := uuid.Parse(p.ShareID); err == nil {
					h.SharePresence(ctx, client, shareID, p.Role, p.Action, p.PresenterToken)
				}
			}
		case "share.signal":
			var p ShareSignalPayload
			if json.Unmarshal(msg.Payload, &p) == nil {
				h.handleShareSignal(client, p)
			}
		}
	}
}

// SharePresence records the client's role announcement for the share and
// broadcasts the new membership summary to the workspace (presenter online
// + distinct viewer count).
// WithShareLock serializes membership authorization and lifecycle decisions
// for one share without holding the workspace hub mutex during database I/O.
func (h *Hub) WithShareLock(shareID uuid.UUID, fn func() error) error {
	h.mu.Lock()
	lock := h.shareLocks[shareID]
	if lock == nil {
		lock = &shareLifecycleLock{}
		h.shareLocks[shareID] = lock
	}
	lock.users++
	h.mu.Unlock()
	lock.mu.Lock()
	defer func() {
		lock.mu.Unlock()
		h.mu.Lock()
		lock.users--
		if lock.users == 0 {
			delete(h.shareLocks, shareID)
			delete(h.shareEpoch, shareID)
		}
		h.mu.Unlock()
	}()
	return fn()
}

func (h *Hub) SharePresence(ctx context.Context, client *Client, shareID uuid.UUID, role, action, token string) {
	_ = h.WithShareLock(shareID, func() error {
		h.sharePresence(ctx, client, shareID, role, action, token)
		return nil
	})
}

func (h *Hub) sharePresence(ctx context.Context, client *Client, shareID uuid.UUID, role, action, token string) {
	if (role != "presenter" && role != "viewer") || (action != "join" && action != "leave") {
		return
	}
	h.mu.RLock()
	epoch := h.shareEpoch[shareID]
	h.mu.RUnlock()
	if action == "join" {
		if h.AuthorizeSharePresence == nil {
			return
		}
		authCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		allowed := h.AuthorizeSharePresence(authCtx, client.workspaceID, client.userID, shareID, role, token)
		cancel()
		if !allowed {
			return
		}
	}
	presenterSeen := false
	h.mu.Lock()
	if !h.clients[client.workspaceID][client] || epoch != h.shareEpoch[shareID] {
		h.mu.Unlock()
		return
	}
	state := h.shares[shareID]
	if state != nil && state.workspaceID != client.workspaceID {
		h.mu.Unlock()
		return
	}
	if action == "leave" {
		if state != nil {
			if state.presenters[client] {
				h.presenterSeen[shareID] = time.Now()
				presenterSeen = true
			}
			delete(state.presenters, client)
			delete(state.viewers, client)
			if len(state.presenters) == 0 && len(state.viewers) == 0 {
				delete(h.shares, shareID)
			}
		}
	} else {
		if state == nil {
			state = &shareState{
				workspaceID: client.workspaceID,
				presenters:  make(map[*Client]bool),
				viewers:     make(map[*Client]bool),
			}
			h.shares[shareID] = state
		}
		if role == "presenter" {
			// The private capture token authorizes a reconnect even while the
			// old socket is half-open. Retire its membership atomically so
			// signaling reaches this socket and its later cleanup cannot
			// remove the replacement or announce the presenter as gone.
			clear(state.presenters)
			delete(state.viewers, client)
			state.presenters[client] = true
			h.presenterSeen[shareID] = time.Now()
			presenterSeen = true
		} else {
			if state.presenters[client] {
				h.mu.Unlock()
				return
			}
			state.viewers[client] = true
		}
	}
	h.mu.Unlock()

	if presenterSeen && h.OnPresenterSeen != nil {
		h.OnPresenterSeen(shareID)
	}
	h.broadcastSharePresence(client.workspaceID, shareID)
}

// handleShareSignal routes only between opposite-role members. The first
// viewer offer addresses the presenter user; replies and subsequent ICE can
// target the authenticated socket identity returned in from_peer.
func (h *Hub) handleShareSignal(client *Client, p ShareSignalPayload) {
	shareID, err := uuid.Parse(p.ShareID)
	if err != nil || len(p.Data) == 0 {
		return
	}
	if _, err := uuid.Parse(p.NegotiationID); err != nil {
		return
	}
	var signal struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(p.Data, &signal) != nil || (signal.Type != "offer" && signal.Type != "answer" && signal.Type != "candidate") {
		return
	}
	toPeer, peerErr := uuid.Parse(p.ToPeer)
	toUser, userErr := uuid.Parse(p.To)
	if p.ToPeer != "" && peerErr != nil {
		return
	}

	if h.fanout != nil {
		// The sender's membership and role are only knowable on this node,
		// so the route is validated here; every node then delivers to the
		// opposite-role peers it holds (deliverShareSignal).
		role := h.localShareRole(shareID, client)
		if role == "" {
			return
		}
		if role == "presenter" && (signal.Type == "offer" || p.ToPeer == "") {
			return
		}
		if role == "viewer" && (signal.Type == "answer" || (p.ToPeer == "" && userErr != nil)) {
			return
		}
		h.fanout.PublishShareSignal(client.workspaceID, &signalEnvelope{
			ShareID:       shareID.String(),
			From:          client.userID.String(),
			FromPeer:      client.peerID.String(),
			FromRole:      role,
			NegotiationID: p.NegotiationID,
			To:            p.To,
			ToPeer:        p.ToPeer,
			Data:          p.Data,
		})
		return
	}

	event := Event{
		Type:      "share.signal",
		Timestamp: time.Now(),
		Payload: map[string]any{
			"share_id":       shareID.String(),
			"from":           client.userID.String(),
			"from_peer":      client.peerID.String(),
			"negotiation_id": p.NegotiationID,
			"data":           p.Data,
		},
	}
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	state := h.shares[shareID]
	if state == nil || state.workspaceID != client.workspaceID || !h.clients[client.workspaceID][client] {
		return
	}
	var recipients map[*Client]bool
	if state.presenters[client] {
		if signal.Type == "offer" || p.ToPeer == "" {
			return
		}
		recipients = state.viewers
	} else if state.viewers[client] {
		if signal.Type == "answer" || (p.ToPeer == "" && userErr != nil) {
			return
		}
		recipients = state.presenters
	} else {
		return
	}
	for recipient := range recipients {
		if !h.clients[client.workspaceID][recipient] {
			continue
		}
		if (p.ToPeer != "" && recipient.peerID != toPeer) || (p.ToPeer == "" && recipient.userID != toUser) {
			continue
		}
		select {
		case recipient.send <- data:
		default:
		}
	}
}

// localShareRole reports the sender's role in the share when its socket is
// a live member on this node, or "" when it is not a member at all.
func (h *Hub) localShareRole(shareID uuid.UUID, client *Client) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	state := h.shares[shareID]
	if state == nil || state.workspaceID != client.workspaceID || !h.clients[client.workspaceID][client] {
		return ""
	}
	if state.presenters[client] {
		return "presenter"
	}
	if state.viewers[client] {
		return "viewer"
	}
	return ""
}

// deliverShareSignal routes a cluster relayed signaling message to this
// node's opposite-role members, honoring the sender's addressing.
func (h *Hub) deliverShareSignal(env *wireEnvelope) {
	sig := env.Signal
	if sig == nil {
		return
	}
	shareID, err := uuid.Parse(sig.ShareID)
	if err != nil || len(sig.Data) == 0 {
		return
	}
	toPeer, peerErr := uuid.Parse(sig.ToPeer)
	toUser, userErr := uuid.Parse(sig.To)
	event := Event{
		Type:      "share.signal",
		Timestamp: time.Now(),
		Payload: map[string]any{
			"share_id":       shareID.String(),
			"from":           sig.From,
			"from_peer":      sig.FromPeer,
			"negotiation_id": sig.NegotiationID,
			"data":           sig.Data,
		},
	}
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	state := h.shares[shareID]
	if state == nil || state.workspaceID != env.WorkspaceID {
		return
	}
	var recipients map[*Client]bool
	switch sig.FromRole {
	case "presenter":
		if sig.ToPeer == "" || peerErr != nil {
			return
		}
		recipients = state.viewers
	case "viewer":
		if sig.ToPeer == "" && (userErr != nil || sig.To == "") {
			return
		}
		recipients = state.presenters
	default:
		return
	}
	for recipient := range recipients {
		if !h.clients[env.WorkspaceID][recipient] {
			continue
		}
		if (sig.ToPeer != "" && (peerErr != nil || recipient.peerID != toPeer)) ||
			(sig.ToPeer == "" && (userErr != nil || recipient.userID != toUser)) {
			continue
		}
		select {
		case recipient.send <- data:
		default:
		}
	}
}

// localShareSummary builds this node's local membership summary of one
// share; nil when no member is connected here.
func (h *Hub) localShareSummary(shareID uuid.UUID) *shareSummary {
	h.mu.RLock()
	defer h.mu.RUnlock()
	state := h.shares[shareID]
	if state == nil {
		return nil
	}
	summary := &shareSummary{ShareID: shareID.String()}
	for client := range state.viewers {
		summary.ViewerPeers = append(summary.ViewerPeers, client.peerID.String())
		summary.ViewerUsers = append(summary.ViewerUsers, client.userID.String())
	}
	for client := range state.presenters {
		summary.PresenterPeer = client.peerID.String()
		summary.PresenterUser = client.userID.String()
	}
	return summary
}

// broadcastSharePresence announces the share's live membership summary:
// this node's local view goes to the cluster, and every local client
// receives the cluster-merged view.
func (h *Hub) broadcastSharePresence(workspaceID, shareID uuid.UUID) {
	summary := h.localShareSummary(shareID)
	if h.fanout != nil {
		if summary != nil {
			h.fanout.PublishShareSummary(workspaceID, summary)
		}
		summary = h.fanout.MergedShare(shareID, summary)
	}
	if summary == nil {
		return
	}
	viewerPeers := summary.ViewerPeers
	if viewerPeers == nil {
		viewerPeers = make([]string, 0)
	}
	viewerUsers := make(map[uuid.UUID]bool)
	for _, user := range summary.ViewerUsers {
		if id, err := uuid.Parse(user); err == nil {
			viewerUsers[id] = true
		}
	}
	presenterPeer := summary.PresenterPeer
	data, err := json.Marshal(Event{
		Type:      "share.presence",
		Timestamp: time.Now(),
		Payload: map[string]any{
			"share_id":         shareID.String(),
			"presenter_online": presenterPeer != "",
			"presenter_peer":   presenterPeer,
			"viewer_peers":     viewerPeers,
			"viewers":          len(viewerUsers),
		},
	})
	if err != nil {
		return
	}
	// Snapshot and delivery share the lock so concurrent disconnect/join
	// summaries cannot arrive out of order and retire a newly joined peer.
	h.mu.RLock()
	defer h.mu.RUnlock()
	for client := range h.clients[workspaceID] {
		select {
		case client.send <- data:
		default:
		}
	}
}

// rebroadcastShare re-announces the merged membership when a remote node's
// summary changed (fanout callback).
func (h *Hub) rebroadcastShare(shareID uuid.UUID) {
	if h.fanout == nil {
		return
	}
	workspaceID := h.fanout.ShareWorkspace(shareID)
	if workspaceID == uuid.Nil {
		return
	}
	h.broadcastSharePresence(workspaceID, shareID)
}

// ShareState reports the share's live membership: distinct viewing users
// and whether any presenter connection is online.
// ShareState reports the share's live membership: distinct viewing users
// and whether any presenter connection is online, merged cluster-wide.
func (h *Hub) ShareState(shareID uuid.UUID) (viewers int, presenterOnline bool) {
	summary := h.localShareSummary(shareID)
	if h.fanout != nil {
		summary = h.fanout.MergedShare(shareID, summary)
	}
	if summary == nil {
		return 0, false
	}
	seen := make(map[string]bool)
	for _, user := range summary.ViewerUsers {
		seen[user] = true
	}
	return len(seen), summary.PresenterPeer != ""
}

// PresenterSeenAt reports when a presenter last announced for the share in
// this process. ok is false when no presenter was seen since startup —
// callers fall back to their persisted timestamp.
func (h *Hub) PresenterSeenAt(shareID uuid.UUID) (t time.Time, ok bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	t, ok = h.presenterSeen[shareID]
	return t, ok
}

// StartedAt reports when this hub process started. The no-source GC never
// judges a share against a contact older than this moment: after a restart
// every still-live share gets a fresh grace window to be re-announced.
func (h *Hub) StartedAt() time.Time {
	return h.startedAt
}

// ClearShare drops all membership state for the share (the share ended).
// The cluster is told with an empty summary so the other nodes stop
// counting its members.
func (h *Hub) ClearShare(shareID uuid.UUID) {
	h.mu.Lock()
	if h.shareLocks[shareID] != nil {
		h.shareEpoch[shareID]++
	}
	var workspace uuid.UUID
	if state := h.shares[shareID]; state != nil {
		workspace = state.workspaceID
	}
	delete(h.shares, shareID)
	delete(h.presenterSeen, shareID)
	h.mu.Unlock()
	if h.fanout != nil && workspace != uuid.Nil {
		h.fanout.PublishShareSummary(workspace, &shareSummary{ShareID: shareID.String()})
	}
}

func (h *Hub) handlePresenceJoin(client *Client, issueID string) {
	// Leave previous issue if any
	if client.viewingIssue != "" && client.viewingIssue != issueID {
		h.handlePresenceLeave(client)
	}

	client.viewingIssue = issueID

	// Publish the viewer to the cluster before the sync below reads it, so
	// this client's own key is already in the replicated index.
	if h.fanout != nil {
		h.fanout.PutPresence(client.workspaceID.String(), issueID, client.peerID, client.userID)
	}

	// Broadcast join to workspace (excluding sender)
	h.BroadcastExcluding(client.workspaceID, client, Event{
		Type: "presence.join",
		Payload: map[string]any{
			"issue_id": issueID,
			"user_id":  client.userID.String(),
		},
	})

	// Send sync to the joining client with current viewers
	viewers := h.GetIssueViewers(client.workspaceID, issueID, client)
	viewerIDs := make([]string, len(viewers))
	for i, uid := range viewers {
		viewerIDs[i] = uid.String()
	}
	h.sendToClient(client, Event{
		Type: "presence.sync",
		Payload: map[string]any{
			"issue_id": issueID,
			"users":    viewerIDs,
		},
	})
}

func (h *Hub) handlePresenceLeave(client *Client) {
	issueID := client.viewingIssue
	if issueID == "" {
		return
	}
	client.viewingIssue = ""

	if h.fanout != nil {
		h.fanout.DropPresence(client.workspaceID.String(), issueID, client.peerID)
	}

	h.BroadcastExcluding(client.workspaceID, client, Event{
		Type: "presence.leave",
		Payload: map[string]any{
			"issue_id": issueID,
			"user_id":  client.userID.String(),
		},
	})
}

func (h *Hub) BroadcastExcluding(workspaceID uuid.UUID, exclude *Client, event Event) {
	event.Timestamp = time.Now()

	if h.fanout != nil {
		h.fanout.publishEvent(workspaceID, event, exclude.peerID.String(), "")
		return
	}

	data, err := json.Marshal(event)
	if err != nil {
		log.WithError(err).Error("failed to marshal event")
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for client := range h.clients[workspaceID] {
		if client == exclude {
			continue
		}
		select {
		case client.send <- data:
		default:
		}
	}
}

func (h *Hub) GetIssueViewers(workspaceID uuid.UUID, issueID string, exclude *Client) []uuid.UUID {
	if h.fanout != nil {
		users := h.fanout.IssueViewers(workspaceID.String(), issueID, exclude.peerID)
		viewers := make([]uuid.UUID, 0, len(users))
		for _, user := range users {
			if id, err := uuid.Parse(user); err == nil {
				viewers = append(viewers, id)
			}
		}
		return viewers
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	var viewers []uuid.UUID
	for client := range h.clients[workspaceID] {
		if client.viewingIssue == issueID && client != exclude {
			viewers = append(viewers, client.userID)
		}
	}
	return viewers
}

// presenceHeartbeat refreshes the presence keys of this node's viewers so
// they outlive the bucket TTL while their sockets are alive.
func (h *Hub) presenceHeartbeat() {
	defer close(h.heartbeatDone)
	ticker := time.NewTicker(presenceHeartbeat)
	defer ticker.Stop()
	type activeViewer struct {
		workspace, issue string
		peer, user       uuid.UUID
	}
	for {
		select {
		case <-h.stopHeartbeat:
			return
		case <-ticker.C:
			h.mu.RLock()
			active := make([]activeViewer, 0)
			for workspaceID, clients := range h.clients {
				for client := range clients {
					if client.viewingIssue != "" {
						active = append(active, activeViewer{
							workspace: workspaceID.String(),
							issue:     client.viewingIssue,
							peer:      client.peerID,
							user:      client.userID,
						})
					}
				}
			}
			h.mu.RUnlock()
			for _, viewer := range active {
				h.fanout.PutPresence(viewer.workspace, viewer.issue, viewer.peer, viewer.user)
			}
		}
	}
}

func (h *Hub) sendToClient(client *Client, event Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if !h.clients[client.workspaceID][client] {
		return
	}
	event.Timestamp = time.Now()
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	select {
	case client.send <- data:
	default:
	}
}
