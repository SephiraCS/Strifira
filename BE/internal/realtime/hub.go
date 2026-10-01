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
	h.mu.Unlock()

	return client
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

// broadcastSharePresence announces the share's live membership summary.
func (h *Hub) broadcastSharePresence(workspaceID, shareID uuid.UUID) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	viewerPeers := make([]string, 0)
	viewerUsers := make(map[uuid.UUID]bool)
	presenterPeer := ""
	if state := h.shares[shareID]; state != nil && state.workspaceID == workspaceID {
		for client := range state.viewers {
			viewerPeers = append(viewerPeers, client.peerID.String())
			viewerUsers[client.userID] = true
		}
		for client := range state.presenters {
			presenterPeer = client.peerID.String()
		}
	}
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
	for client := range h.clients[workspaceID] {
		select {
		case client.send <- data:
		default:
		}
	}
}

func (h *Hub) shareStateLocked(shareID uuid.UUID) (viewers int, presenterOnline bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	state := h.shares[shareID]
	if state == nil {
		return 0, false
	}
	seen := make(map[uuid.UUID]bool)
	for c := range state.viewers {
		seen[c.userID] = true
	}
	return len(seen), len(state.presenters) > 0
}

// ShareState reports the share's live membership: distinct viewing users
// and whether any presenter connection is online.
func (h *Hub) ShareState(shareID uuid.UUID) (viewers int, presenterOnline bool) {
	return h.shareStateLocked(shareID)
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
func (h *Hub) ClearShare(shareID uuid.UUID) {
	h.mu.Lock()
	if h.shareLocks[shareID] != nil {
		h.shareEpoch[shareID]++
	}
	delete(h.shares, shareID)
	delete(h.presenterSeen, shareID)
	h.mu.Unlock()
}

func (h *Hub) handlePresenceJoin(client *Client, issueID string) {
	// Leave previous issue if any
	if client.viewingIssue != "" && client.viewingIssue != issueID {
		h.handlePresenceLeave(client)
	}

	client.viewingIssue = issueID

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
