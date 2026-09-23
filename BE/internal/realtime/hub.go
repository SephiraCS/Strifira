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
	Type      string      `json:"type"`
	Timestamp time.Time   `json:"timestamp"`
	Payload   interface{} `json:"payload"`
}

type Client struct {
	conn         *websocket.Conn
	workspaceID  uuid.UUID
	userID       uuid.UUID
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
	ShareID string `json:"share_id"`
	Role    string `json:"role"`   // "presenter" | "viewer"
	Action  string `json:"action"` // "join" | "leave"
}

// ShareSignalPayload relays an opaque WebRTC signaling message (SDP
// description or ICE candidate) from the sender to one recipient user.
type ShareSignalPayload struct {
	ShareID string          `json:"share_id"`
	To      string          `json:"to"`
	Data    json.RawMessage `json:"data"`
}

// shareState is the live membership of one screen share.
type shareState struct {
	presenters map[*Client]bool
	viewers    map[*Client]bool
}

type Hub struct {
	mu      sync.RWMutex
	clients map[uuid.UUID]map[*Client]bool // workspaceID -> clients
	shares  map[uuid.UUID]*shareState      // shareID -> live membership
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
		presenterSeen: make(map[uuid.UUID]time.Time),
		startedAt:     startedAt,
	}
}

func (h *Hub) Register(conn *websocket.Conn, workspaceID, userID uuid.UUID) *Client {
	client := &Client{
		conn:        conn,
		workspaceID: workspaceID,
		userID:      userID,
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
			delete(state.presenters, client)
			delete(state.viewers, client)
			if len(state.presenters) == 0 && len(state.viewers) == 0 {
				delete(h.shares, shareID)
			}
			affected = append(affected, shareID)
		}
	}
	h.mu.Unlock()

	for _, shareID := range affected {
		h.broadcastSharePresence(client.workspaceID, shareID)
	}

	close(client.send)
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
					Payload: map[string]interface{}{
						"issue_id": p.IssueID,
						"user_id":  client.userID.String(),
						"x":        p.X,
						"y":        p.Y,
					},
				})
			}
		case "focus.update", "focus.leave":
			// Relay focus/cursor-position events to other clients viewing the same issue
			var raw map[string]interface{}
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
					h.SharePresence(client, shareID, p.Role, p.Action)
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
func (h *Hub) SharePresence(client *Client, shareID uuid.UUID, role, action string) {
	presenterJoined := false
	h.mu.Lock()
	if action == "leave" {
		if state, ok := h.shares[shareID]; ok {
			delete(state.presenters, client)
			delete(state.viewers, client)
			if len(state.presenters) == 0 && len(state.viewers) == 0 {
				delete(h.shares, shareID)
			}
		}
	} else {
		state := h.shares[shareID]
		if state == nil {
			state = &shareState{
				presenters: make(map[*Client]bool),
				viewers:    make(map[*Client]bool),
			}
			h.shares[shareID] = state
		}
		if role == "presenter" {
			delete(state.viewers, client)
			state.presenters[client] = true
			h.presenterSeen[shareID] = time.Now()
			presenterJoined = true
		} else {
			delete(state.presenters, client)
			state.viewers[client] = true
		}
	}
	h.mu.Unlock()

	if presenterJoined && h.OnPresenterSeen != nil {
		h.OnPresenterSeen(shareID)
	}

	h.broadcastSharePresence(client.workspaceID, shareID)
}

// handleShareSignal relays a WebRTC signaling message to every connection
// of the target user in the workspace. Only members of the share (its
// presenter or a joined viewer) may relay through it.
func (h *Hub) handleShareSignal(client *Client, p ShareSignalPayload) {
	shareID, err := uuid.Parse(p.ShareID)
	if err != nil {
		return
	}
	to, err := uuid.Parse(p.To)
	if err != nil {
		return
	}
	if len(p.Data) == 0 {
		return
	}

	h.mu.RLock()
	state := h.shares[shareID]
	member := state != nil && (state.presenters[client] || state.viewers[client])
	h.mu.RUnlock()
	if !member {
		return
	}

	h.sendToUser(client.workspaceID, to, Event{
		Type: "share.signal",
		Payload: map[string]interface{}{
			"share_id": p.ShareID,
			"from":     client.userID.String(),
			"data":     p.Data,
		},
	})
}

// broadcastSharePresence announces the share's live membership summary.
func (h *Hub) broadcastSharePresence(workspaceID, shareID uuid.UUID) {
	viewers, presenterOnline := h.shareStateLocked(shareID)
	h.Broadcast(workspaceID, Event{
		Type: "share.presence",
		Payload: map[string]interface{}{
			"share_id":         shareID.String(),
			"presenter_online": presenterOnline,
			"viewers":          viewers,
		},
	})
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
	delete(h.shares, shareID)
	delete(h.presenterSeen, shareID)
	h.mu.Unlock()
}

// sendToUser delivers the event to every connection of one user.
func (h *Hub) sendToUser(workspaceID, userID uuid.UUID, event Event) {
	event.Timestamp = time.Now()

	data, err := json.Marshal(event)
	if err != nil {
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

func (h *Hub) handlePresenceJoin(client *Client, issueID string) {
	// Leave previous issue if any
	if client.viewingIssue != "" && client.viewingIssue != issueID {
		h.handlePresenceLeave(client)
	}

	client.viewingIssue = issueID

	// Broadcast join to workspace (excluding sender)
	h.BroadcastExcluding(client.workspaceID, client, Event{
		Type: "presence.join",
		Payload: map[string]interface{}{
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
		Payload: map[string]interface{}{
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
		Payload: map[string]interface{}{
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
