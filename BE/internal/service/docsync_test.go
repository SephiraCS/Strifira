package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeUpdateRepo records persisted Yjs updates without a database.
type fakeUpdateRepo struct {
	mu      sync.Mutex
	updates [][]byte
	compact [][]byte
	tail    [][]byte
	// onCompact fires on entry to Compact (test hook to widen the
	// compaction window); it must not call back into the repo.
	onCompact func()
}

var _ repository.DocumentUpdateRepo = (*fakeUpdateRepo)(nil)

func (f *fakeUpdateRepo) AppendUpdates(ctx context.Context, documentID uuid.UUID, updates [][]byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, updates...)
	return nil
}

func (f *fakeUpdateRepo) ListUpdates(ctx context.Context, documentID uuid.UUID) ([][]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.updates))
	copy(out, f.updates)
	return out, nil
}

func (f *fakeUpdateRepo) CountUpdates(ctx context.Context, documentID uuid.UUID) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.updates), nil
}

func (f *fakeUpdateRepo) Compact(ctx context.Context, documentID uuid.UUID, snapshot []byte, tail [][]byte) error {
	if f.onCompact != nil {
		f.onCompact()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.compact = [][]byte{snapshot}
	f.tail = tail
	f.updates = append([][]byte{snapshot}, tail...)
	return nil
}

func (f *fakeUpdateRepo) DeleteByDocument(ctx context.Context, documentID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = nil
	return nil
}

func (f *fakeUpdateRepo) len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.updates)
}

func newDocSyncTestService(t *testing.T) (*fakeUpdateRepo, *DocSyncService) {
	t.Helper()
	docRepo := newFakeDocumentRepo()
	updateRepo := &fakeUpdateRepo{}
	return updateRepo, NewDocSyncService(docRepo, updateRepo)
}

func docSyncJoinClient(t *testing.T, svc *DocSyncService, docID uuid.UUID, canWrite bool) *DocSyncClient {
	t.Helper()
	client, _, _, err := svc.Join(context.Background(), docID, uuid.New(), "Tester", "#112233", canWrite, func() {})
	require.NoError(t, err)
	return client
}

func drainDocSync(client *DocSyncClient) []docSyncEnvelope {
	var out []docSyncEnvelope
	for {
		select {
		case data := <-client.Outbound():
			var msg docSyncEnvelope
			if json.Unmarshal(data, &msg) == nil {
				out = append(out, msg)
			}
		default:
			return out
		}
	}
}

func findDocSyncMessage(msgs []docSyncEnvelope, typ string) *docSyncEnvelope {
	for i := range msgs {
		if msgs[i].Type == typ {
			return &msgs[i]
		}
	}
	return nil
}

func TestDocSyncService_JoinReplaysHistoryAndAnnouncesPeers(t *testing.T) {
	updateRepo, svc := newDocSyncTestService(t)
	docID := uuid.New()
	stored := [][]byte{[]byte("update-one"), []byte("update-two")}
	require.NoError(t, updateRepo.AppendUpdates(context.Background(), docID, stored))

	first := docSyncJoinClient(t, svc, docID, true)
	msgs := drainDocSync(first)
	init := findDocSyncMessage(msgs, "init")
	require.NotNil(t, init)
	var initPayload struct {
		DocumentID string   `json:"document_id"`
		ClientID   string   `json:"client_id"`
		CanWrite   bool     `json:"can_write"`
		Updates    []string `json:"updates"`
	}
	require.NoError(t, json.Unmarshal(init.Payload, &initPayload))
	assert.Equal(t, docID.String(), initPayload.DocumentID)
	assert.True(t, initPayload.CanWrite)
	require.Len(t, initPayload.Updates, 2)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("update-one")), initPayload.Updates[0])

	second := docSyncJoinClient(t, svc, docID, true)
	join := findDocSyncMessage(drainDocSync(first), "peer.join")
	require.NotNil(t, join)
	peers := findDocSyncMessage(drainDocSync(second), "peers")
	require.NotNil(t, peers)

	svc.Leave(second)
	assert.NotNil(t, findDocSyncMessage(drainDocSync(first), "peer.leave"))
	svc.Leave(first)
}

func TestDocSyncService_RelayUpdateAndCursor(t *testing.T) {
	_, svc := newDocSyncTestService(t)
	docID := uuid.New()
	alice := docSyncJoinClient(t, svc, docID, true)
	bob := docSyncJoinClient(t, svc, docID, true)
	drainDocSync(alice)
	drainDocSync(bob)

	relay, _ := json.Marshal(docSyncEnvelope{Type: "update", Payload: mustDocSyncJSON(map[string]string{
		"update": base64.StdEncoding.EncodeToString([]byte("delta")),
	})})
	svc.HandleClientMessage(context.Background(), alice, relay)

	got := findDocSyncMessage(drainDocSync(bob), "update")
	require.NotNil(t, got)
	var payload struct {
		Update   string `json:"update"`
		ClientID string `json:"client_id"`
	}
	require.NoError(t, json.Unmarshal(got.Payload, &payload))
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("delta")), payload.Update)
	assert.Equal(t, alice.PeerID().String(), payload.ClientID)
	// The author must not receive its own update back.
	assert.Nil(t, findDocSyncMessage(drainDocSync(alice), "update"))

	cursor, _ := json.Marshal(docSyncEnvelope{Type: "cursor", Payload: mustDocSyncJSON(map[string]string{
		"anchor": "YQ==", "head": "Yg==",
	})})
	svc.HandleClientMessage(context.Background(), alice, cursor)
	c := findDocSyncMessage(drainDocSync(bob), "cursor")
	require.NotNil(t, c)

	svc.Leave(alice)
	svc.Leave(bob)
}

func TestDocSyncService_ReadOnlyClientCannotPushUpdates(t *testing.T) {
	_, svc := newDocSyncTestService(t)
	docID := uuid.New()
	guest := docSyncJoinClient(t, svc, docID, false)
	editor := docSyncJoinClient(t, svc, docID, true)
	drainDocSync(guest)
	drainDocSync(editor)

	update, _ := json.Marshal(docSyncEnvelope{Type: "update", Payload: mustDocSyncJSON(map[string]string{
		"update": base64.StdEncoding.EncodeToString([]byte("evil")),
	})})
	svc.HandleClientMessage(context.Background(), guest, update)

	assert.Nil(t, findDocSyncMessage(drainDocSync(editor), "update"))
	assert.NotNil(t, findDocSyncMessage(drainDocSync(guest), "error"))

	svc.Leave(guest)
	svc.Leave(editor)
}

func TestDocSyncService_FlushesAndCompacts(t *testing.T) {
	updateRepo, svc := newDocSyncTestService(t)
	docID := uuid.New()
	client := docSyncJoinClient(t, svc, docID, true)
	drainDocSync(client)

	for i := 0; i < 3; i++ {
		update, _ := json.Marshal(docSyncEnvelope{Type: "update", Payload: mustDocSyncJSON(map[string]string{
			"update": base64.StdEncoding.EncodeToString([]byte{byte(i)}),
		})})
		svc.HandleClientMessage(context.Background(), client, update)
	}

	// The flusher ticks at 700ms; wait for the updates to land.
	deadline := time.Now().Add(3 * time.Second)
	for updateRepo.len() < 3 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	require.GreaterOrEqual(t, updateRepo.len(), 3)

	snapshot, _ := json.Marshal(docSyncEnvelope{Type: "snapshot", Payload: mustDocSyncJSON(map[string]string{
		"update": base64.StdEncoding.EncodeToString([]byte("full-state")),
	})})
	// A snapshot without a pending compaction request is ignored.
	svc.HandleClientMessage(context.Background(), client, snapshot)

	svc.Leave(client)
}

func TestDocSyncService_ConcurrentUpdatesDuringCompactionSurvive(t *testing.T) {
	updateRepo, svc := newDocSyncTestService(t)
	docID := uuid.New()
	client := docSyncJoinClient(t, svc, docID, true)
	drainDocSync(client)

	// Push the room past the compaction threshold and request a snapshot
	// the way the flush loop does.
	svc.mu.RLock()
	room := svc.rooms[docID]
	svc.mu.RUnlock()
	require.NotNil(t, room)
	room.mu.Lock()
	room.rowCount = docSyncCompactRows
	room.mu.Unlock()
	svc.maybeRequestCompaction(room)
	require.NotNil(t, findDocSyncMessage(drainDocSync(client), "compact.request"))

	// Hold the compaction open while more updates keep flowing in.
	inCompact := make(chan struct{})
	release := make(chan struct{})
	updateRepo.onCompact = func() {
		close(inCompact)
		<-release
	}
	snapshotDone := make(chan struct{})
	snapshot, _ := json.Marshal(docSyncEnvelope{Type: "snapshot", Payload: mustDocSyncJSON(map[string]string{
		"update": base64.StdEncoding.EncodeToString([]byte("full-state")),
	})})
	go func() {
		defer close(snapshotDone)
		svc.HandleClientMessage(context.Background(), client, snapshot)
	}()
	<-inCompact

	const concurrent = 8
	for i := 0; i < concurrent; i++ {
		update, _ := json.Marshal(docSyncEnvelope{Type: "update", Payload: mustDocSyncJSON(map[string]string{
			"update": base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("during-compact-%d", i))),
		})})
		svc.HandleClientMessage(context.Background(), client, update)
	}
	close(release)
	<-snapshotDone

	// Updates that arrived while the compaction was running must be
	// persisted after it commits, in order.
	deadline := time.Now().Add(5 * time.Second)
	for updateRepo.len() < 1+concurrent && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	stored, err := updateRepo.ListUpdates(context.Background(), docID)
	require.NoError(t, err)
	require.Len(t, stored, 1+concurrent)
	assert.Equal(t, []byte("full-state"), stored[0])
	for i := 0; i < concurrent; i++ {
		assert.Equal(t, []byte(fmt.Sprintf("during-compact-%d", i)), stored[1+i])
	}

	svc.Leave(client)
}

func TestDocSyncService_ResetDocumentDropsStateAndKicksPeers(t *testing.T) {
	updateRepo, svc := newDocSyncTestService(t)
	docID := uuid.New()
	closed := make(chan struct{}, 2)
	join := func() *DocSyncClient {
		client, _, _, err := svc.Join(context.Background(), docID, uuid.New(), "Tester", "#112233", true, func() { closed <- struct{}{} })
		require.NoError(t, err)
		return client
	}
	first, second := join(), join()
	drainDocSync(first)
	drainDocSync(second)

	// One already-persisted row plus one update still awaiting its flush.
	require.NoError(t, updateRepo.AppendUpdates(context.Background(), docID, [][]byte{[]byte("stale-persisted")}))
	update, _ := json.Marshal(docSyncEnvelope{Type: "update", Payload: mustDocSyncJSON(map[string]string{
		"update": base64.StdEncoding.EncodeToString([]byte("stale-buffered")),
	})})
	svc.HandleClientMessage(context.Background(), first, update)

	svc.ResetDocument(docID)

	assert.NotNil(t, findDocSyncMessage(drainDocSync(first), "reset"))
	assert.NotNil(t, findDocSyncMessage(drainDocSync(second), "reset"))
	for i := 0; i < 2; i++ {
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatal("peer sockets were not closed by the reset")
		}
	}
	stored, err := updateRepo.ListUpdates(context.Background(), docID)
	require.NoError(t, err)
	assert.Empty(t, stored)

	// A fresh join replays an empty log: nothing stale survived the reset.
	third := docSyncJoinClient(t, svc, docID, true)
	msgs := drainDocSync(third)
	init := findDocSyncMessage(msgs, "init")
	require.NotNil(t, init)
	var initPayload struct {
		Updates []string `json:"updates"`
	}
	require.NoError(t, json.Unmarshal(init.Payload, &initPayload))
	assert.Empty(t, initPayload.Updates)

	svc.Leave(third)
}

func TestDocSyncService_ShutdownClosesClients(t *testing.T) {
	_, svc := newDocSyncTestService(t)
	docID := uuid.New()
	closed := 0
	// The close callback mirrors the handler: closing the socket unblocks
	// its read loop, which then unregisters the client.
	var client *DocSyncClient
	client, _, _, err := svc.Join(context.Background(), docID, uuid.New(), "T", "#000000", true, func() {
		closed++
		go svc.Leave(client)
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, svc.Shutdown(ctx))
	assert.Equal(t, 1, closed)
}

func TestDocSyncSafeColor(t *testing.T) {
	assert.Equal(t, "#aabbcc", docSyncSafeColor("#aabbcc"))
	assert.Equal(t, "#71717a", docSyncSafeColor("red"))
	assert.Equal(t, "#71717a", docSyncSafeColor("javascript:alert(1)"))
}
