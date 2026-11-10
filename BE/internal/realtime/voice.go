package realtime

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Voice rooms carry the audio channels of chat: media is peer-to-peer
// WebRTC between every pair of participants (a mesh — unlike the screen
// share's one presenter with viewers), and the hub only tracks membership,
// relays signaling, and announces rosters. No media transits the server and
// nothing is persisted: a room exists while at least one socket is in it.
//
// The events, all riding the workspace socket like the share ones:
//
//	voice.presence  client→server join/leave announcement
//	voice.presence  server→clients roster snapshot (full member list)
//	voice.sync      server→joiner targeted confirmation carrying its peer ID
//	voice.state     client→server mute/deafen update
//	voice.signal    client→server, relayed peer→peer (SDP offers/answers/ICE)
//
// Snapshots carry the FULL roster rather than deltas, so a client or a node
// that missed earlier events converges on the next one it receives.

// voiceStateTTL is how long a snapshot participant learned from another node
// stays trusted. The owning node refreshes its members' timestamps on every
// broadcast (including the client heartbeat), so an entry older than this
// belongs to a socket whose node died without a clean unregister.
const voiceStateTTL = 75 * time.Second

// voiceHeartbeat is the client's re-announce cadence (client-side mirror of
// this constant). It rebroadcasts the merged roster so missed events and
// crashed-node phantoms heal without waiting for the next join/leave.
const voiceHeartbeat = 30 * time.Second

// VoiceParticipant is one connection's live state in a voice room.
type VoiceParticipant struct {
	PeerID    uuid.UUID `json:"peer_id"`
	UserID    uuid.UUID `json:"user_id"`
	Muted     bool      `json:"muted"`
	Deafened  bool      `json:"deafened"`
	UpdatedAt time.Time `json:"updated_at"`
}

// voiceRoom is this node's authoritative membership of one channel's room.
type voiceRoom struct {
	workspaceID uuid.UUID
	members     map[*Client]*VoiceParticipant
}

// voiceRoomSnapshot is the last roster a node saw for a room — its own
// members included (every change rebroadcasts the merged view). It lets a
// node describe rooms whose members connect elsewhere and lets deliverEvent
// converge remote announcements without new wire kinds.
type voiceRoomSnapshot struct {
	workspaceID  uuid.UUID
	participants []VoiceParticipant
}

// VoiceRoomView is the read model the REST endpoint serves.
type VoiceRoomView struct {
	ChannelID    uuid.UUID
	Participants []VoiceParticipant
}

// VoicePresencePayload announces (or retracts) a connection's room membership.
type VoicePresencePayload struct {
	ChannelID string `json:"channel_id"`
	Action    string `json:"action"` // "join" | "leave"
	Muted     bool   `json:"muted"`
	Deafened  bool   `json:"deafened"`
}

// VoiceStatePayload updates the announcer's mute/deafen flags.
type VoiceStatePayload struct {
	ChannelID string `json:"channel_id"`
	Muted     bool   `json:"muted"`
	Deafened  bool   `json:"deafened"`
}

// VoiceSignalPayload relays one opaque WebRTC signaling message to another
// room member's socket. The server stamps the authenticated sender; the
// negotiation ID lets receivers drop signals that answer a torn-down peer.
type VoiceSignalPayload struct {
	ChannelID     string          `json:"channel_id"`
	ToPeer        string          `json:"to_peer"`
	NegotiationID string          `json:"negotiation_id"`
	Data          json.RawMessage `json:"data"`
}

// voiceRoomState is the wire form of a roster snapshot.
type voiceRoomState struct {
	ChannelID    string             `json:"channel_id"`
	Participants []VoiceParticipant `json:"participants"`
}

// voiceSyncPayload is the joiner-targeted join confirmation.
type voiceSyncPayload struct {
	ChannelID    string             `json:"channel_id"`
	PeerID       string             `json:"peer_id"`
	Participants []VoiceParticipant `json:"participants"`
}

func (h *Hub) handleVoicePresence(ctx context.Context, client *Client, p VoicePresencePayload) {
	channelID, err := uuid.Parse(p.ChannelID)
	if err != nil {
		return
	}
	switch p.Action {
	case "join":
		h.voiceJoin(ctx, client, channelID, p.Muted, p.Deafened)
	case "leave":
		h.voiceLeave(client, channelID)
	}
}

func (h *Hub) handleVoiceState(client *Client, p VoiceStatePayload) {
	channelID, err := uuid.Parse(p.ChannelID)
	if err != nil {
		return
	}
	h.mu.Lock()
	room := h.voiceRooms[channelID]
	member := room.members[client]
	if member != nil {
		member.Muted = p.Muted
		member.Deafened = p.Deafened
		member.UpdatedAt = time.Now()
	}
	h.mu.Unlock()
	if member != nil {
		h.broadcastVoiceRoster(client.workspaceID, channelID)
	}
}

func (h *Hub) voiceJoin(ctx context.Context, client *Client, channelID uuid.UUID, muted, deafened bool) {
	if h.AuthorizeVoicePresence == nil {
		return
	}
	authCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	allowed := h.AuthorizeVoicePresence(authCtx, client.workspaceID, client.userID, channelID)
	cancel()
	if !allowed {
		return
	}

	h.mu.Lock()
	if !h.clients[client.workspaceID][client] {
		h.mu.Unlock()
		return
	}
	room := h.voiceRooms[channelID]
	if room != nil && room.workspaceID != client.workspaceID {
		h.mu.Unlock()
		return
	}
	if room == nil {
		room = &voiceRoom{
			workspaceID: client.workspaceID,
			members:     make(map[*Client]*VoiceParticipant),
		}
		h.voiceRooms[channelID] = room
	}
	// One session per user per room: a rejoin (reload, socket reconnect)
	// retires the user's earlier connections so their replacement receives
	// signaling and a crashed tab cannot leave a half-open ghost. The old
	// sockets are not closed — media P2P collapses once peers reap the
	// departed roster entries.
	for other, member := range room.members {
		if member.UserID == client.userID && other != client {
			h.markVoiceDeparted(member.PeerID)
			delete(room.members, other)
		}
	}
	room.members[client] = &VoiceParticipant{
		PeerID:    client.peerID,
		UserID:    client.userID,
		Muted:     muted,
		Deafened:  deafened,
		UpdatedAt: time.Now(),
	}
	h.mu.Unlock()

	h.broadcastVoiceRoster(client.workspaceID, channelID)
	// The joiner learns its own peer ID and the roster atomically (snapshots
	// are broadcast to everyone and carry no per-client identity).
	h.sendToClient(client, Event{
		Type: "voice.sync",
		Payload: voiceSyncPayload{
			ChannelID:    channelID.String(),
			PeerID:       client.peerID.String(),
			Participants: h.voiceRoomParticipants(channelID),
		},
	})
}

func (h *Hub) voiceLeave(client *Client, channelID uuid.UUID) {
	h.mu.Lock()
	room := h.voiceRooms[channelID]
	if room == nil || room.workspaceID != client.workspaceID {
		h.mu.Unlock()
		return
	}
	member, ok := room.members[client]
	if !ok {
		h.mu.Unlock()
		return
	}
	h.markVoiceDeparted(member.PeerID)
	delete(room.members, client)
	h.mu.Unlock()
	h.broadcastVoiceRoster(client.workspaceID, channelID)
}

// leaveAllVoiceRooms drops a disconnecting socket from every room it joined,
// announcing each affected room (Unregister path).
func (h *Hub) leaveAllVoiceRooms(client *Client) []uuid.UUID {
	affected := make([]uuid.UUID, 0)
	h.mu.Lock()
	for channelID, room := range h.voiceRooms {
		if room.workspaceID != client.workspaceID {
			continue
		}
		member, ok := room.members[client]
		if !ok {
			continue
		}
		h.markVoiceDeparted(member.PeerID)
		delete(room.members, client)
		affected = append(affected, channelID)
	}
	h.mu.Unlock()
	return affected
}

// markVoiceDeparted remembers a locally retired peer for the snapshot TTL
// window (the departed map lives under the hub mutex; callers hold it).
func (h *Hub) markVoiceDeparted(peerID uuid.UUID) {
	now := time.Now()
	for peer, at := range h.voiceDeparted {
		if now.Sub(at) > 2*voiceStateTTL {
			delete(h.voiceDeparted, peer)
		}
	}
	h.voiceDeparted[peerID] = now
}

// voiceRoomParticipants builds the merged roster for a room: this node's
// authoritative members override the snapshot's entries, and snapshot
// entries from other nodes survive while fresh (see voiceStateTTL).
func (h *Hub) voiceRoomParticipants(channelID uuid.UUID) []VoiceParticipant {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.voiceRoomParticipantsLocked(channelID)
}

func (h *Hub) voiceRoomParticipantsLocked(channelID uuid.UUID) []VoiceParticipant {
	merged := make(map[uuid.UUID]VoiceParticipant)
	if snapshot := h.voiceSnapshots[channelID]; snapshot != nil {
		cutoff := time.Now().Add(-voiceStateTTL)
		for _, p := range snapshot.participants {
			if p.UpdatedAt.Before(cutoff) {
				continue
			}
			// A peer this node retired itself (leave, rejoin, disconnect) is
			// not resurrected by a snapshot that predates the departure.
			if _, gone := h.voiceDeparted[p.PeerID]; gone {
				continue
			}
			merged[p.PeerID] = p
		}
	}
	if room := h.voiceRooms[channelID]; room != nil {
		for _, member := range room.members {
			merged[member.PeerID] = *member
		}
	}
	if len(merged) == 0 {
		// Nobody is in the room anywhere we know of: forget it entirely so a
		// room that emptied while this node was split off cannot linger.
		delete(h.voiceSnapshots, channelID)
		return nil
	}
	participants := make([]VoiceParticipant, 0, len(merged))
	for _, p := range merged {
		participants = append(participants, p)
	}
	return participants
}

// broadcastVoiceRoster announces the room's merged roster to its audience
// (workspace-wide for public channels, members-only otherwise) and records
// it so this node can serve and re-merge the state later.
func (h *Hub) broadcastVoiceRoster(workspaceID, channelID uuid.UUID) {
	participants := h.voiceRoomParticipants(channelID)
	if participants == nil {
		participants = make([]VoiceParticipant, 0)
	}
	state := voiceRoomState{
		ChannelID:    channelID.String(),
		Participants: participants,
	}
	h.recordVoiceSnapshot(workspaceID, channelID, participants)

	recipients, workspaceWide := h.voiceAudience(workspaceID, channelID)
	event := Event{Type: "voice.presence", Payload: state}
	if workspaceWide {
		h.Broadcast(workspaceID, event)
		return
	}
	if len(recipients) == 0 {
		// The audience resolution refused (unknown or hidden channel): keep
		// the roster off the wire entirely; this node's snapshot stays the
		// internal record and later joins re-resolve.
		return
	}
	for _, recipient := range recipients {
		h.BroadcastToUser(workspaceID, recipient, event)
	}
}

// voiceAudience resolves the room's audience with a defensive fallback: an
// unwired VoiceAudience treats every room as public rather than hiding it.
func (h *Hub) voiceAudience(workspaceID, channelID uuid.UUID) ([]uuid.UUID, bool) {
	if h.VoiceAudience == nil {
		return nil, true
	}
	return h.VoiceAudience(context.Background(), workspaceID, channelID)
}

// recordVoiceSnapshot stores the roster this node just announced or received
// (both paths converge here; local members stay authoritative in merges).
func (h *Hub) recordVoiceSnapshot(workspaceID, channelID uuid.UUID, participants []VoiceParticipant) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(participants) == 0 {
		if room := h.voiceRooms[channelID]; room != nil && len(room.members) > 0 {
			return
		}
		delete(h.voiceSnapshots, channelID)
		return
	}
	stored := make([]VoiceParticipant, len(participants))
	copy(stored, participants)
	h.voiceSnapshots[channelID] = &voiceRoomSnapshot{
		workspaceID:  workspaceID,
		participants: stored,
	}
}

// recordRemoteVoiceSnapshot parses a received voice.presence event (cluster
// path) into the snapshot store. Own-node announcements are skipped: local
// membership is authoritative and the broadcast already recorded it.
func (h *Hub) recordRemoteVoiceSnapshot(workspaceID uuid.UUID, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	var state voiceRoomState
	if json.Unmarshal(data, &state) != nil {
		return
	}
	channelID, err := uuid.Parse(state.ChannelID)
	if err != nil {
		return
	}
	if room := h.voiceRooms[channelID]; room != nil && room.workspaceID == workspaceID && len(room.members) > 0 {
		// This node holds live members: keep its own view rather than letting
		// a remote broadcast racing it overwrite fresh local state.
		return
	}
	h.recordVoiceSnapshot(workspaceID, channelID, state.Participants)
}

// VoiceRoomsForWorkspace lists the rooms a REST reader can see: live local
// members merged over the freshest known remote entries. Rooms whose
// snapshot participants are all stale (their node vanished) come out empty
// and are dropped.
func (h *Hub) VoiceRoomsForWorkspace(workspaceID uuid.UUID) []VoiceRoomView {
	h.mu.RLock()
	channels := make([]uuid.UUID, 0, len(h.voiceRooms)+len(h.voiceSnapshots))
	for channelID, room := range h.voiceRooms {
		if room.workspaceID == workspaceID {
			channels = append(channels, channelID)
		}
	}
	for channelID, snapshot := range h.voiceSnapshots {
		if snapshot.workspaceID == workspaceID && h.voiceRooms[channelID] == nil {
			channels = append(channels, channelID)
		}
	}
	h.mu.RUnlock()

	views := make([]VoiceRoomView, 0, len(channels))
	for _, channelID := range channels {
		participants := h.voiceRoomParticipants(channelID)
		if len(participants) == 0 {
			continue
		}
		views = append(views, VoiceRoomView{
			ChannelID:    channelID,
			Participants: participants,
		})
	}
	return views
}

// handleVoiceSignal relays one signaling message to the addressed member.
// The sender's membership is validated on this node (its own view); delivery
// goes to whoever holds the target peer — locally, or on any node via the
// workspace subject (the envelope is tiny; media never rides it).
func (h *Hub) handleVoiceSignal(client *Client, p VoiceSignalPayload) {
	channelID, err := uuid.Parse(p.ChannelID)
	if err != nil || len(p.Data) == 0 {
		return
	}
	if _, err := uuid.Parse(p.NegotiationID); err != nil {
		return
	}
	toPeer, err := uuid.Parse(p.ToPeer)
	if err != nil {
		return
	}
	var signal struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(p.Data, &signal) != nil || (signal.Type != "offer" && signal.Type != "answer" && signal.Type != "candidate") {
		return
	}

	h.mu.RLock()
	room := h.voiceRooms[channelID]
	sender := room.members[client]
	h.mu.RUnlock()
	if room == nil || room.workspaceID != client.workspaceID || sender == nil {
		return
	}

	event := Event{
		Type: "voice.signal",
		Payload: map[string]any{
			"channel_id":     channelID.String(),
			"from":           sender.UserID.String(),
			"from_peer":      sender.PeerID.String(),
			"to_peer":        toPeer.String(),
			"negotiation_id": p.NegotiationID,
			"data":           p.Data,
		},
	}
	if h.fanout != nil {
		// Every node — this one included — receives the envelope and delivers
		// to its local member with the target peer ID (deliverEvent).
		h.fanout.publishEvent(client.workspaceID, event, "", "")
		return
	}
	h.deliverVoiceSignal(client.workspaceID, toPeer, event)
}

// voiceSignalTarget extracts the addressed peer from a voice.signal event
// payload (nil UUID when the payload is not a well-formed relay).
func voiceSignalTarget(payload any) uuid.UUID {
	data, err := json.Marshal(payload)
	if err != nil {
		return uuid.Nil
	}
	var p struct {
		ToPeer string `json:"to_peer"`
	}
	if json.Unmarshal(data, &p) != nil {
		return uuid.Nil
	}
	toPeer, err := uuid.Parse(p.ToPeer)
	if err != nil {
		return uuid.Nil
	}
	return toPeer
}

// deliverVoiceSignal hands a signaling event to the local member with the
// target peer ID. Used directly on single-node deployments and from
// deliverEvent on the cluster path.
func (h *Hub) deliverVoiceSignal(workspaceID uuid.UUID, toPeer uuid.UUID, event Event) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for client := range h.clients[workspaceID] {
		if client.peerID != toPeer {
			continue
		}
		select {
		case client.send <- data:
		default:
		}
		return
	}
}
