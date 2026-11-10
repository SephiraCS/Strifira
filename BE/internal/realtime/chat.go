package realtime

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ChatTypingPayload is the client→server typing hint.
type ChatTypingPayload struct {
	ChannelID string `json:"channel_id"`
}

// ChatTypingAudience, wired by the server bootstrap, resolves who should
// receive a typing indicator for a channel. workspaceWide routes the relay
// as a workspace broadcast (public channels); otherwise the recipient list
// is used. visible=false refuses the relay entirely (unknown or hidden
// channel).
type ChatTypingAudience func(ctx context.Context, workspaceID, userID, channelID uuid.UUID) (recipients []uuid.UUID, workspaceWide, visible bool)

// chatTypingTTL guides clients on how long a hint stays fresh.
const chatTypingTTL = 6 * time.Second

// ChatTyping relays a typing indicator to the channel audience without
// echoing it back to the sender. The membership lookup happens before any
// broadcast so the hot path stays free of I/O under the hub lock.
func (h *Hub) ChatTyping(ctx context.Context, client *Client, channelID string) {
	if h.ChatTypingAudience == nil {
		return
	}
	parsed, err := uuid.Parse(channelID)
	if err != nil {
		return
	}
	recipients, workspaceWide, visible := h.ChatTypingAudience(ctx, client.workspaceID, client.userID, parsed)
	if !visible {
		return
	}
	event := Event{
		Type: "chat.typing",
		Payload: map[string]any{
			"channel_id": channelID,
			"user_id":    client.userID.String(),
			"ttl_ms":     chatTypingTTL.Milliseconds(),
		},
	}
	if workspaceWide {
		h.BroadcastExcluding(client.workspaceID, client, event)
		return
	}
	for _, recipient := range recipients {
		if recipient == client.userID {
			continue
		}
		h.BroadcastToUser(client.workspaceID, recipient, event)
	}
}

// ConnectedUsers lists the distinct users with a live socket in the
// workspace, powering the initial chat presence snapshot. Single-node
// source of truth; the cluster fan-out path keeps per-node views (presence
// refreshes as clients reconnect).
func (h *Hub) ConnectedUsers(workspaceID uuid.UUID) []uuid.UUID {
	h.mu.RLock()
	defer h.mu.RUnlock()
	seen := make(map[uuid.UUID]bool)
	users := make([]uuid.UUID, 0)
	for client := range h.clients[workspaceID] {
		if seen[client.userID] {
			continue
		}
		seen[client.userID] = true
		users = append(users, client.userID)
	}
	return users
}

// broadcastUserPresence announces a user coming online or going offline in
// the workspace. Generic events (not chat-specific): email, voice and other
// lanes reuse them.
func (h *Hub) broadcastUserPresence(workspaceID, userID uuid.UUID, online bool) {
	eventType := "presence.offline"
	if online {
		eventType = "presence.online"
	}
	h.Broadcast(workspaceID, Event{
		Type: eventType,
		Payload: map[string]any{
			"user_id": userID.String(),
		},
	})
}
