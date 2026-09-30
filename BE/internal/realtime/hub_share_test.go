package realtime

import (
	"context"
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
	h := newShareTestHub()
	wsID := uuid.New()
	shareID := uuid.New()
	presenter := h.Register(nil, wsID, uuid.New())
	// Two connections of the same viewer user + one other viewer.
	viewer1 := h.Register(nil, wsID, uuid.New())
	viewer2 := h.Register(nil, wsID, viewer1.userID)
	viewer3 := h.Register(nil, wsID, uuid.New())

	h.SharePresence(t.Context(), presenter, shareID, "presenter", "join", "")
	h.SharePresence(t.Context(), viewer1, shareID, "viewer", "join", "")
	h.SharePresence(t.Context(), viewer2, shareID, "viewer", "join", "")
	h.SharePresence(t.Context(), viewer3, shareID, "viewer", "join", "")

	viewers, online := h.ShareState(shareID)
	assert.Equal(t, 2, viewers, "same user on two connections counts once")
	assert.True(t, online)
}

func TestHub_SharePresenceBroadcastsSummary(t *testing.T) {
	h := newShareTestHub()
	wsID := uuid.New()
	shareID := uuid.New()
	presenter := h.Register(nil, wsID, uuid.New())
	observer := h.Register(nil, wsID, uuid.New())

	h.SharePresence(t.Context(), presenter, shareID, "presenter", "join", "")

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
	h := newShareTestHub()
	wsID := uuid.New()
	shareID := uuid.New()
	presenter := h.Register(nil, wsID, uuid.New())
	viewer := h.Register(nil, wsID, uuid.New())

	h.SharePresence(t.Context(), presenter, shareID, "presenter", "join", "")
	h.SharePresence(t.Context(), viewer, shareID, "viewer", "join", "")

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
	h := newShareTestHub()
	wsID := uuid.New()
	shareID := uuid.New()
	presenter := h.Register(nil, wsID, uuid.New())
	viewer := h.Register(nil, wsID, uuid.New())
	bystander := h.Register(nil, wsID, uuid.New())

	h.SharePresence(t.Context(), presenter, shareID, "presenter", "join", "")
	h.SharePresence(t.Context(), viewer, shareID, "viewer", "join", "")
	drainEvents(t, viewer)
	drainEvents(t, bystander)

	h.handleShareSignal(presenter, ShareSignalPayload{
		ShareID:       shareID.String(),
		ToPeer:        viewer.peerID.String(),
		NegotiationID: uuid.NewString(),
		Data:          json.RawMessage(`{"type":"answer"}`),
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
	h := newShareTestHub()
	wsID := uuid.New()
	shareID := uuid.New()
	presenter := h.Register(nil, wsID, uuid.New())
	outsider := h.Register(nil, wsID, uuid.New())

	h.SharePresence(t.Context(), presenter, shareID, "presenter", "join", "")
	drainEvents(t, presenter)

	// The outsider is in the workspace but never joined the share.
	h.handleShareSignal(outsider, ShareSignalPayload{
		ShareID:       shareID.String(),
		To:            presenter.userID.String(),
		NegotiationID: uuid.NewString(),
		Data:          json.RawMessage(`{"type":"offer"}`),
	})

	assert.Empty(t, drainEvents(t, presenter), "signals from share non-members are dropped")
}

func TestHub_ClearShareDropsMembership(t *testing.T) {
	h := newShareTestHub()
	wsID := uuid.New()
	shareID := uuid.New()
	presenter := h.Register(nil, wsID, uuid.New())
	viewer := h.Register(nil, wsID, uuid.New())

	h.SharePresence(t.Context(), presenter, shareID, "presenter", "join", "")
	h.SharePresence(t.Context(), viewer, shareID, "viewer", "join", "")

	h.ClearShare(shareID)

	viewers, online := h.ShareState(shareID)
	assert.Equal(t, 0, viewers)
	assert.False(t, online)
}

func TestHub_BroadcastDoesNotBlockOnFullBuffers(t *testing.T) {
	h := newShareTestHub()
	wsID := uuid.New()
	shareID := uuid.New()
	sender := h.Register(nil, wsID, uuid.New())
	full := h.Register(nil, wsID, uuid.New())

	h.SharePresence(t.Context(), sender, shareID, "presenter", "join", "")
	h.SharePresence(t.Context(), full, shareID, "viewer", "join", "")
	// Fill the target's send buffer past capacity.
	for range 300 {
		select {
		case full.send <- []byte("{}"):
		default:
		}
	}

	done := make(chan struct{})
	go func() {
		h.handleShareSignal(sender, ShareSignalPayload{
			ShareID:       shareID.String(),
			ToPeer:        full.peerID.String(),
			NegotiationID: uuid.NewString(),
			Data:          json.RawMessage(`{"type":"candidate"}`),
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("signal relay must not block on a full client buffer")
	}
}

func TestHub_PresenterSeenSurvivesPresenterDisconnect(t *testing.T) {
	h := newShareTestHub()
	wsID := uuid.New()
	shareID := uuid.New()
	presenter := h.Register(nil, wsID, uuid.New())

	h.SharePresence(t.Context(), presenter, shareID, "presenter", "join", "")
	h.Unregister(presenter)

	if _, online := h.ShareState(shareID); online {
		t.Fatal("presenter must read as gone after disconnect")
	}
	seen, ok := h.PresenterSeenAt(shareID)
	assert.True(t, ok, "presenter sighting must survive the disconnect for the GC clock")
	assert.WithinDuration(t, time.Now(), seen, 5*time.Second)
}

func TestHub_PresenterSeenDroppedOnClearShare(t *testing.T) {
	h := newShareTestHub()
	wsID := uuid.New()
	shareID := uuid.New()
	presenter := h.Register(nil, wsID, uuid.New())

	h.SharePresence(t.Context(), presenter, shareID, "presenter", "join", "")
	h.ClearShare(shareID)

	_, ok := h.PresenterSeenAt(shareID)
	assert.False(t, ok, "an ended share must not keep sighting state")
}

func TestHub_OnPresenterSeenRecordsJoinAndDeparture(t *testing.T) {
	h := newShareTestHub()
	wsID := uuid.New()
	shareID := uuid.New()
	presenter := h.Register(nil, wsID, uuid.New())
	viewer := h.Register(nil, wsID, uuid.New())

	var seen []uuid.UUID
	h.OnPresenterSeen = func(id uuid.UUID) { seen = append(seen, id) }

	h.SharePresence(t.Context(), viewer, shareID, "viewer", "join", "")
	assert.Empty(t, seen, "viewer joins must not fire the presenter-seen callback")

	h.SharePresence(t.Context(), presenter, shareID, "presenter", "join", "")
	if assert.Len(t, seen, 1) {
		assert.Equal(t, shareID, seen[0])
	}

	h.SharePresence(t.Context(), presenter, shareID, "presenter", "leave", "")
	assert.Len(t, seen, 2, "presenter departures persist a fresh reconnect grace window")
}

// Regression: broadcasts used to iterate h.clients after releasing the read
// lock while Unregister mutated the same map (and closed client channels)
// under the write lock — a concurrent map read/write or send-on-closed-
// channel there is a fatal error that kills the process and every live
// WebSocket mid-negotiation. Run under -race this test fails on the old
// code and must stay clean.
func TestHub_BroadcastSurvivesConcurrentUnregisterStorm(t *testing.T) {
	h := newShareTestHub()
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
	for range 50 {
		c := h.Register(nil, wsID, uuid.New())
		wg.Go(func() {
			h.BroadcastToUser(wsID, c.userID, Event{Type: "share.signal", Payload: map[string]string{}})
			h.Unregister(c)
		})
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

// Most membership tests focus on hub behavior; access policy is exercised
// separately with the service-backed authorizer.
func newShareTestHub() *Hub {
	h := NewHub()
	h.AuthorizeSharePresence = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, string) bool { return true }
	return h
}

func TestHub_ShareSignalsIsolateViewerTabs(t *testing.T) {
	h := newShareTestHub()
	wsID, shareID := uuid.New(), uuid.New()
	presenter := h.Register(nil, wsID, uuid.New())
	presenterOtherTab := h.Register(nil, wsID, presenter.userID)
	viewer1 := h.Register(nil, wsID, uuid.New())
	viewer2 := h.Register(nil, wsID, viewer1.userID)
	bystander := h.Register(nil, wsID, viewer1.userID)
	h.SharePresence(t.Context(), presenter, shareID, "presenter", "join", "")
	h.SharePresence(t.Context(), viewer1, shareID, "viewer", "join", "")
	h.SharePresence(t.Context(), viewer2, shareID, "viewer", "join", "")
	for _, client := range []*Client{presenter, presenterOtherTab, viewer1, viewer2, bystander} {
		drainEvents(t, client)
	}
	negotiation := uuid.NewString()
	for _, viewer := range []*Client{viewer1, viewer2} {
		h.handleShareSignal(viewer, ShareSignalPayload{ShareID: shareID.String(), To: presenter.userID.String(), NegotiationID: negotiation, Data: json.RawMessage(`{"type":"offer"}`)})
	}
	events := drainEvents(t, presenter)
	assert.Len(t, events, 2)
	assert.Equal(t, viewer1.peerID.String(), events[0].Payload.(map[string]any)["from_peer"])
	assert.Equal(t, viewer2.peerID.String(), events[1].Payload.(map[string]any)["from_peer"])
	assert.Empty(t, drainEvents(t, presenterOtherTab), "a presenter's unrelated tab gets no offers")
	for _, signalType := range []string{"answer", "candidate"} {
		h.handleShareSignal(presenter, ShareSignalPayload{ShareID: shareID.String(), ToPeer: viewer1.peerID.String(), NegotiationID: negotiation, Data: json.RawMessage(`{"type":"` + signalType + `"}`)})
		assert.Len(t, drainEvents(t, viewer1), 1)
		assert.Empty(t, drainEvents(t, viewer2))
		assert.Empty(t, drainEvents(t, bystander))
	}
	h.Unregister(viewer1)
	for _, client := range []*Client{presenter, viewer2, bystander} {
		drainEvents(t, client)
	}
	h.handleShareSignal(presenter, ShareSignalPayload{ShareID: shareID.String(), ToPeer: viewer2.peerID.String(), NegotiationID: negotiation, Data: json.RawMessage(`{"type":"candidate"}`)})
	assert.Len(t, drainEvents(t, viewer2), 1, "the surviving tab keeps its own connection")
}

func TestHub_SharePresenceRejectsHijackAndForeignWorkspace(t *testing.T) {
	h := newShareTestHub()
	wsID, shareID := uuid.New(), uuid.New()
	presenter := h.Register(nil, wsID, uuid.New())
	otherTab := h.Register(nil, wsID, presenter.userID)
	foreign := h.Register(nil, uuid.New(), uuid.New())
	h.SharePresence(t.Context(), presenter, shareID, "presenter", "join", "")
	h.SharePresence(t.Context(), otherTab, shareID, "presenter", "join", "")
	h.SharePresence(t.Context(), foreign, shareID, "viewer", "join", "")
	h.SharePresence(t.Context(), otherTab, shareID, "presenter", "leave", "")
	assert.True(t, h.shares[shareID].presenters[presenter])
	assert.False(t, h.shares[shareID].presenters[otherTab])
	assert.False(t, h.shares[shareID].viewers[foreign])
}

func TestHub_ShareSignalRejectsNonMemberRecipientsAndSameRole(t *testing.T) {
	h := newShareTestHub()
	wsID, shareID := uuid.New(), uuid.New()
	presenter := h.Register(nil, wsID, uuid.New())
	viewer := h.Register(nil, wsID, uuid.New())
	otherViewer := h.Register(nil, wsID, uuid.New())
	outsider := h.Register(nil, wsID, uuid.New())
	h.SharePresence(t.Context(), presenter, shareID, "presenter", "join", "")
	h.SharePresence(t.Context(), viewer, shareID, "viewer", "join", "")
	h.SharePresence(t.Context(), otherViewer, shareID, "viewer", "join", "")
	for _, client := range []*Client{presenter, viewer, otherViewer, outsider} {
		drainEvents(t, client)
	}
	for _, recipient := range []*Client{otherViewer, outsider} {
		h.handleShareSignal(viewer, ShareSignalPayload{ShareID: shareID.String(), ToPeer: recipient.peerID.String(), NegotiationID: uuid.NewString(), Data: json.RawMessage(`{"type":"offer"}`)})
		assert.Empty(t, drainEvents(t, recipient))
	}
	h.handleShareSignal(presenter, ShareSignalPayload{ShareID: shareID.String(), ToPeer: outsider.peerID.String(), NegotiationID: uuid.NewString(), Data: json.RawMessage(`{"type":"answer"}`)})
	assert.Empty(t, drainEvents(t, outsider))
}

func TestHub_PresenterDisconnectStartsNewGraceWindow(t *testing.T) {
	h := newShareTestHub()
	shareID := uuid.New()
	presenter := h.Register(nil, uuid.New(), uuid.New())
	h.SharePresence(t.Context(), presenter, shareID, "presenter", "join", "")
	h.presenterSeen[shareID] = time.Now().Add(-time.Hour)
	h.Unregister(presenter)
	seen, ok := h.PresenterSeenAt(shareID)
	assert.True(t, ok)
	assert.WithinDuration(t, time.Now(), seen, time.Second)
}

func TestHub_ClearShareInvalidatesInFlightAuthorization(t *testing.T) {
	h := NewHub()
	shareID := uuid.New()
	client := h.Register(nil, uuid.New(), uuid.New())
	entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h.AuthorizeSharePresence = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, string) bool {
		close(entered)
		<-resume
		return true
	}
	go func() { defer close(done); h.SharePresence(t.Context(), client, shareID, "presenter", "join", "") }()
	<-entered
	h.ClearShare(shareID)
	close(resume)
	<-done
	_, online := h.ShareState(shareID)
	assert.False(t, online, "a stale authorization result cannot resurrect an ended share")
}
