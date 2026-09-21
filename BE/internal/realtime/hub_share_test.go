package realtime

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// drainEvents reads everything currently buffered on the client's send
// channel.
func drainEvents(t *testing.T, c *Client) []Event {
	t.Helper()
	var events []Event
	for {
		select {
		case data, ok := <-c.send:
			if !ok {
				return events
			}
			var e Event
			if err := json.Unmarshal(data, &e); err != nil {
				t.Fatalf("malformed event: %v", err)
			}
			events = append(events, e)
		default:
			return events
		}
	}
}

func TestHub_SharePresenceCountsDistinctViewers(t *testing.T) {
	h := NewHub()
	wsID := uuid.New()
	shareID := uuid.New()
	presenter := h.Register(nil, wsID, uuid.New())
	// Two connections of the same viewer user + one other viewer.
	viewer1 := h.Register(nil, wsID, uuid.New())
	viewer2 := h.Register(nil, wsID, viewer1.userID)
	viewer3 := h.Register(nil, wsID, uuid.New())

	h.SharePresence(presenter, shareID, "presenter", "join")
	h.SharePresence(viewer1, shareID, "viewer", "join")
	h.SharePresence(viewer2, shareID, "viewer", "join")
	h.SharePresence(viewer3, shareID, "viewer", "join")

	viewers, online := h.ShareState(shareID)
	assert.Equal(t, 2, viewers, "same user on two connections counts once")
	assert.True(t, online)
}

func TestHub_SharePresenceBroadcastsSummary(t *testing.T) {
	h := NewHub()
	wsID := uuid.New()
	shareID := uuid.New()
	presenter := h.Register(nil, wsID, uuid.New())
	observer := h.Register(nil, wsID, uuid.New())

	h.SharePresence(presenter, shareID, "presenter", "join")

	events := drainEvents(t, observer)
	if assert.Len(t, events, 1) {
		assert.Equal(t, "share.presence", events[0].Type)
		payload, ok := events[0].Payload.(map[string]interface{})
		assert.True(t, ok)
		assert.Equal(t, shareID.String(), payload["share_id"])
		assert.Equal(t, true, payload["presenter_online"])
		assert.Equal(t, float64(0), payload["viewers"])
	}
}

func TestHub_UnregisterEndsPresenterPresence(t *testing.T) {
	h := NewHub()
	wsID := uuid.New()
	shareID := uuid.New()
	presenter := h.Register(nil, wsID, uuid.New())
	viewer := h.Register(nil, wsID, uuid.New())

	h.SharePresence(presenter, shareID, "presenter", "join")
	h.SharePresence(viewer, shareID, "viewer", "join")

	h.Unregister(presenter)

	viewers, online := h.ShareState(shareID)
	assert.Equal(t, 1, viewers, "viewer membership survives")
	assert.False(t, online, "presenter socket loss must read as presenter gone")

	// The workspace heard about it.
	var sawGone bool
	for _, e := range drainEvents(t, viewer) {
		if e.Type == "share.presence" {
			payload := e.Payload.(map[string]interface{})
			if payload["presenter_online"] == false {
				sawGone = true
			}
		}
	}
	assert.True(t, sawGone, "viewers must be told the presenter is gone")
}

func TestHub_ShareSignalRelaysToTargetUserOnly(t *testing.T) {
	h := NewHub()
	wsID := uuid.New()
	shareID := uuid.New()
	presenter := h.Register(nil, wsID, uuid.New())
	viewer := h.Register(nil, wsID, uuid.New())
	bystander := h.Register(nil, wsID, uuid.New())

	h.SharePresence(presenter, shareID, "presenter", "join")
	h.SharePresence(viewer, shareID, "viewer", "join")
	drainEvents(t, viewer)
	drainEvents(t, bystander)

	h.handleShareSignal(presenter, ShareSignalPayload{
		ShareID: shareID.String(),
		To:      viewer.userID.String(),
		Data:    json.RawMessage(`{"type":"answer"}`),
	})

	events := drainEvents(t, viewer)
	if assert.Len(t, events, 1) {
		assert.Equal(t, "share.signal", events[0].Type)
		payload := events[0].Payload.(map[string]interface{})
		assert.Equal(t, presenter.userID.String(), payload["from"])
		assert.Equal(t, shareID.String(), payload["share_id"])
		assert.NotNil(t, payload["data"])
	}
	assert.Empty(t, drainEvents(t, bystander), "non-members must not receive relays")
}

func TestHub_ShareSignalRejectsNonMembers(t *testing.T) {
	h := NewHub()
	wsID := uuid.New()
	shareID := uuid.New()
	presenter := h.Register(nil, wsID, uuid.New())
	outsider := h.Register(nil, wsID, uuid.New())

	h.SharePresence(presenter, shareID, "presenter", "join")
	drainEvents(t, presenter)

	// The outsider is in the workspace but never joined the share.
	h.handleShareSignal(outsider, ShareSignalPayload{
		ShareID: shareID.String(),
		To:      presenter.userID.String(),
		Data:    json.RawMessage(`{"type":"offer"}`),
	})

	assert.Empty(t, drainEvents(t, presenter), "signals from share non-members are dropped")
}

func TestHub_ClearShareDropsMembership(t *testing.T) {
	h := NewHub()
	wsID := uuid.New()
	shareID := uuid.New()
	presenter := h.Register(nil, wsID, uuid.New())
	viewer := h.Register(nil, wsID, uuid.New())

	h.SharePresence(presenter, shareID, "presenter", "join")
	h.SharePresence(viewer, shareID, "viewer", "join")

	h.ClearShare(shareID)

	viewers, online := h.ShareState(shareID)
	assert.Equal(t, 0, viewers)
	assert.False(t, online)
}

func TestHub_BroadcastDoesNotBlockOnFullBuffers(t *testing.T) {
	h := NewHub()
	wsID := uuid.New()
	shareID := uuid.New()
	sender := h.Register(nil, wsID, uuid.New())
	full := h.Register(nil, wsID, uuid.New())

	h.SharePresence(sender, shareID, "presenter", "join")
	// Fill the target's send buffer past capacity.
	for i := 0; i < 300; i++ {
		select {
		case full.send <- []byte("{}"):
		default:
		}
	}

	done := make(chan struct{})
	go func() {
		h.handleShareSignal(sender, ShareSignalPayload{
			ShareID: shareID.String(),
			To:      full.userID.String(),
			Data:    json.RawMessage(`{"type":"candidate"}`),
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("signal relay must not block on a full client buffer")
	}
}

// Regression: broadcasts used to iterate h.clients after releasing the read
// lock while Unregister mutated the same map (and closed client channels)
// under the write lock — a concurrent map read/write or send-on-closed-
// channel there is a fatal error that kills the process and every live
// WebSocket mid-negotiation. Run under -race this test fails on the old
// code and must stay clean.
func TestHub_BroadcastSurvivesConcurrentUnregisterStorm(t *testing.T) {
	h := NewHub()
	wsID := uuid.New()

	stop := make(chan struct{})
	broadcasterDone := make(chan struct{})
	var wg sync.WaitGroup

	// Steady broadcast load across the workspace.
	go func() {
		defer close(broadcasterDone)
		for {
			select {
			case <-stop:
				return
			default:
				h.Broadcast(wsID, Event{Type: "issue.updated", Payload: map[string]string{}})
			}
		}
	}()

	// Churn: clients register, draw a share broadcast, and unregister.
	for i := 0; i < 50; i++ {
		c := h.Register(nil, wsID, uuid.New())
		wg.Add(1)
		go func(c *Client) {
			defer wg.Done()
			h.BroadcastToUser(wsID, c.userID, Event{Type: "share.signal", Payload: map[string]string{}})
			h.Unregister(c)
		}(c)
	}

	wg.Wait()
	close(stop)
	<-broadcasterDone
	// The hub must still be usable afterwards.
	survivor := h.Register(nil, wsID, uuid.New())
	h.Broadcast(wsID, Event{Type: "share.ended", Payload: map[string]string{}})
	assert.NotEmpty(t, drainEvents(t, survivor))
	h.Unregister(survivor)
}
