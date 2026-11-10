package realtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newVoiceTestHub(authorized bool) *Hub {
	h := NewHub()
	h.AuthorizeVoicePresence = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) bool {
		return authorized
	}
	return h
}

// voiceJoin joins a client and returns the targeted voice.sync event.
func voiceJoin(t *testing.T, h *Hub, c *Client, channelID uuid.UUID, muted, deafened bool) map[string]any {
	t.Helper()
	h.handleVoicePresence(t.Context(), c, VoicePresencePayload{
		ChannelID: channelID.String(),
		Action:    "join",
		Muted:     muted,
		Deafened:  deafened,
	})
	var sync map[string]any
	for _, event := range drainEvents(t, c) {
		if event.Type == "voice.sync" {
			payload, ok := event.Payload.(map[string]interface{})
			require.True(t, ok)
			if payload["channel_id"] == channelID.String() {
				sync = payload
			}
		}
	}
	return sync
}

// voiceRoster returns the participants array of the last voice.presence
// snapshot the client received for the channel, plus how many snapshots it
// received (an emptied room still announces with an empty roster).
func voiceRoster(t *testing.T, c *Client, channelID uuid.UUID) ([]any, int) {
	t.Helper()
	var roster []any
	count := 0
	for _, event := range drainEvents(t, c) {
		if event.Type != "voice.presence" {
			continue
		}
		payload, ok := event.Payload.(map[string]interface{})
		require.True(t, ok)
		if payload["channel_id"] == channelID.String() {
			participants, ok := payload["participants"].([]any)
			require.True(t, ok)
			roster = participants
			count++
		}
	}
	return roster, count
}

func rosterPeers(t *testing.T, roster []any) []string {
	t.Helper()
	peers := make([]string, 0, len(roster))
	for _, entry := range roster {
		participant, ok := entry.(map[string]any)
		require.True(t, ok)
		peers = append(peers, participant["peer_id"].(string))
	}
	return peers
}

func TestHub_VoiceJoinRequiresAuthorization(t *testing.T) {
	refused := newVoiceTestHub(false)
	wsID := uuid.New()
	channelID := uuid.New()
	client := refused.Register(nil, wsID, uuid.New())
	drainEvents(t, client) // the register-time presence.online announcement

	refused.handleVoicePresence(t.Context(), client, VoicePresencePayload{ChannelID: channelID.String(), Action: "join"})
	assert.Empty(t, drainEvents(t, client), "an unauthorized join produces no roster and no sync")
	assert.Empty(t, refused.VoiceRoomsForWorkspace(wsID))
}

func TestHub_VoiceJoinBroadcastsRosterAndSync(t *testing.T) {
	h := newVoiceTestHub(true)
	wsID := uuid.New()
	channelID := uuid.New()
	joiner := h.Register(nil, wsID, uuid.New())
	observer := h.Register(nil, wsID, uuid.New())

	sync := voiceJoin(t, h, joiner, channelID, false, false)
	require.NotEmpty(t, sync, "the joiner receives a voice.sync confirmation")
	assert.Equal(t, channelID.String(), sync["channel_id"])
	require.NotEmpty(t, sync["peer_id"], "the sync carries the joiner's peer id")

	roster, _ := voiceRoster(t, observer, channelID)
	require.Len(t, roster, 1, "the observer sees the merged roster")
	participant := roster[0].(map[string]any)
	assert.Equal(t, sync["peer_id"], participant["peer_id"])
	assert.Equal(t, joiner.userID.String(), participant["user_id"])
	assert.Equal(t, false, participant["muted"])

	rooms := h.VoiceRoomsForWorkspace(wsID)
	require.Len(t, rooms, 1)
	assert.Equal(t, channelID, rooms[0].ChannelID)
	require.Len(t, rooms[0].Participants, 1)
}

func TestHub_VoiceRejoinReplacesSameUserSession(t *testing.T) {
	h := newVoiceTestHub(true)
	wsID := uuid.New()
	channelID := uuid.New()
	first := h.Register(nil, wsID, uuid.New())
	observer := h.Register(nil, wsID, uuid.New())

	firstSync := voiceJoin(t, h, first, channelID, false, false)
	drainEvents(t, observer)
	// The same user joins from a second tab (new socket, new peer id).
	second := h.Register(nil, wsID, first.userID)
	drainEvents(t, observer) // presence.online of the second socket
	secondSync := voiceJoin(t, h, second, channelID, true, false)

	roster, _ := voiceRoster(t, observer, channelID)
	peers := rosterPeers(t, roster)
	require.Len(t, peers, 1, "the earlier session is retired on rejoin")
	assert.Contains(t, peers, secondSync["peer_id"])
	assert.NotContains(t, peers, firstSync["peer_id"])
	assert.Equal(t, true, roster[0].(map[string]any)["muted"])

	rooms := h.VoiceRoomsForWorkspace(wsID)
	require.Len(t, rooms, 1)
	require.Len(t, rooms[0].Participants, 1)
	assert.True(t, rooms[0].Participants[0].Muted)
}

func TestHub_VoiceSignalRelaysToTargetPeerOnly(t *testing.T) {
	h := newVoiceTestHub(true)
	wsID := uuid.New()
	channelID := uuid.New()
	alice := h.Register(nil, wsID, uuid.New())
	bob := h.Register(nil, wsID, uuid.New())
	bystander := h.Register(nil, wsID, uuid.New())

	aliceSync := voiceJoin(t, h, alice, channelID, false, false)
	bobSync := voiceJoin(t, h, bob, channelID, false, false)
	drainEvents(t, bystander)

	h.handleVoiceSignal(alice, VoiceSignalPayload{
		ChannelID:     channelID.String(),
		ToPeer:        bobSync["peer_id"].(string),
		NegotiationID: uuid.NewString(),
		Data:          json.RawMessage(`{"type":"offer","sdp":"v"}`),
	})

	events := drainEvents(t, bob)
	require.Len(t, events, 1, "the target peer receives the relay")
	assert.Equal(t, "voice.signal", events[0].Type)
	payload := events[0].Payload.(map[string]interface{})
	assert.Equal(t, aliceSync["peer_id"], payload["from_peer"])
	assert.Equal(t, alice.userID.String(), payload["from"])
	assert.NotNil(t, payload["data"])
	assert.Empty(t, drainEvents(t, bystander), "workspace bystanders receive no relays")
}

func TestHub_VoiceSignalRejectsNonMembersAndGarbage(t *testing.T) {
	h := newVoiceTestHub(true)
	wsID := uuid.New()
	channelID := uuid.New()
	member := h.Register(nil, wsID, uuid.New())
	outsider := h.Register(nil, wsID, uuid.New())

	memberSync := voiceJoin(t, h, member, channelID, false, false)
	drainEvents(t, member)

	// The outsider never joined the room.
	h.handleVoiceSignal(outsider, VoiceSignalPayload{
		ChannelID:     channelID.String(),
		ToPeer:        memberSync["peer_id"].(string),
		NegotiationID: uuid.NewString(),
		Data:          json.RawMessage(`{"type":"offer"}`),
	})
	// A member may not relay garbage signaling types.
	h.handleVoiceSignal(member, VoiceSignalPayload{
		ChannelID:     channelID.String(),
		ToPeer:        memberSync["peer_id"].(string),
		NegotiationID: uuid.NewString(),
		Data:          json.RawMessage(`{"type":"renegotiate"}`),
	})
	assert.Empty(t, drainEvents(t, member), "no relay reaches the room")
}

func TestHub_VoiceStateUpdatesRoster(t *testing.T) {
	h := newVoiceTestHub(true)
	wsID := uuid.New()
	channelID := uuid.New()
	member := h.Register(nil, wsID, uuid.New())
	observer := h.Register(nil, wsID, uuid.New())
	drainEvents(t, observer) // register-time announcements
	voiceJoin(t, h, member, channelID, false, false)
	drainEvents(t, observer)

	h.handleVoiceState(member, VoiceStatePayload{ChannelID: channelID.String(), Muted: true, Deafened: true})

	roster, updates := voiceRoster(t, observer, channelID)
	require.Len(t, roster, 1)
	assert.Equal(t, 1, updates)
	assert.Equal(t, true, roster[0].(map[string]any)["muted"])
	assert.Equal(t, true, roster[0].(map[string]any)["deafened"])

	// A non-member state update changes nothing.
	stranger := h.Register(nil, wsID, uuid.New())
	h.handleVoiceState(stranger, VoiceStatePayload{ChannelID: channelID.String(), Muted: true})
	_, updates = voiceRoster(t, observer, channelID)
	assert.Equal(t, 0, updates, "a non-member state update broadcasts no roster")
}

func TestHub_VoiceDisconnectLeavesRooms(t *testing.T) {
	h := newVoiceTestHub(true)
	wsID := uuid.New()
	channelID := uuid.New()
	member := h.Register(nil, wsID, uuid.New())
	observer := h.Register(nil, wsID, uuid.New())
	drainEvents(t, observer) // register-time announcements
	voiceJoin(t, h, member, channelID, false, false)
	drainEvents(t, observer)

	h.Unregister(member)

	roster, goodbyes := voiceRoster(t, observer, channelID)
	require.Equal(t, 1, goodbyes, "the disconnect announces the emptied roster")
	assert.Len(t, roster, 0)
	// The snapshot is cleared once no member remains anywhere.
	assert.Empty(t, h.VoiceRoomsForWorkspace(wsID))
	// The room is gone: a rejoin by the same user starts clean.
	rejoined := h.Register(nil, wsID, member.userID)
	sync := voiceJoin(t, h, rejoined, channelID, false, false)
	require.NotEmpty(t, sync)
	assert.Len(t, h.VoiceRoomsForWorkspace(wsID), 1)
}

func TestHub_VoiceRoomsMergeLocalAndSnapshot(t *testing.T) {
	h := newVoiceTestHub(true)
	wsID := uuid.New()
	channelID := uuid.New()
	local := h.Register(nil, wsID, uuid.New())
	voiceJoin(t, h, local, channelID, false, false)

	// Another node announces its own members; this node must describe the
	// merged room without confusing them with the local ones.
	remotePeer := uuid.New()
	remoteUser := uuid.New()
	h.recordVoiceSnapshot(wsID, channelID, []VoiceParticipant{{
		PeerID:    remotePeer,
		UserID:    remoteUser,
		UpdatedAt: time.Now(),
	}})

	rooms := h.VoiceRoomsForWorkspace(wsID)
	require.Len(t, rooms, 1)
	assert.Len(t, rooms[0].Participants, 2)

	// Snapshot entries older than the TTL belong to a vanished node and are
	// dropped instead of haunting the room.
	h.mu.Lock()
	snapshot := h.voiceSnapshots[channelID]
	for i := range snapshot.participants {
		if snapshot.participants[i].PeerID == remotePeer {
			snapshot.participants[i].UpdatedAt = time.Now().Add(-2 * voiceStateTTL)
		}
	}
	h.mu.Unlock()
	rooms = h.VoiceRoomsForWorkspace(wsID)
	require.Len(t, rooms, 1)
	require.Len(t, rooms[0].Participants, 1, "stale remote entries are dropped")
	assert.Equal(t, local.peerID, rooms[0].Participants[0].PeerID)
}

func TestHub_VoiceAudienceTargetsPrivateChannelMembers(t *testing.T) {
	h := newVoiceTestHub(true)
	wsID := uuid.New()
	joiner := h.Register(nil, wsID, uuid.New())
	member := h.Register(nil, wsID, uuid.New()) // a fellow channel member
	outsider := h.Register(nil, wsID, uuid.New())
	h.VoiceAudience = func(_ context.Context, _ uuid.UUID, _ uuid.UUID) ([]uuid.UUID, bool) {
		// The private channel's members: the joiner and the fellow member.
		return []uuid.UUID{joiner.userID, member.userID}, false
	}
	channelID := uuid.New()
	drainEvents(t, joiner)
	drainEvents(t, member)
	drainEvents(t, outsider)

	voiceJoin(t, h, joiner, channelID, false, false)

	assert.Empty(t, drainEvents(t, outsider), "non-members never learn a private room's roster")
	roster, _ := voiceRoster(t, member, channelID)
	assert.NotEmpty(t, roster, "channel members receive the roster")
}

func TestHub_VoiceRefusedAudienceStaysOffTheWire(t *testing.T) {
	h := newVoiceTestHub(true)
	// The audience callback refused: unknown or hidden channel.
	h.VoiceAudience = func(context.Context, uuid.UUID, uuid.UUID) ([]uuid.UUID, bool) {
		return nil, false
	}
	wsID := uuid.New()
	channelID := uuid.New()
	joiner := h.Register(nil, wsID, uuid.New())
	drainEvents(t, joiner) // the register-time presence.online announcement

	voiceJoin(t, h, joiner, channelID, false, false)

	assert.Empty(t, drainEvents(t, joiner), "a refused audience broadcasts nothing")
	// The roster stays recorded for REST reads, which filter per-user
	// visibility in the chat service; only the WS broadcast is suppressed.
	assert.Len(t, h.VoiceRoomsForWorkspace(wsID), 1)
}
