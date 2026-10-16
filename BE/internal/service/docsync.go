package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"sync"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/realtime"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

// DocSyncService relays Yjs document updates between clients editing the same
// document and persists the append-only update log. The server never decodes
// Yjs payloads — clients apply updates themselves — it only guarantees ordered
// delivery to every peer and durable storage. Cursor presence rides the same
// socket as small cursor messages with Yjs relative positions, so remote carets
// survive concurrent edits.

const (
	docSyncSendBuffer       = 256
	docSyncFlushInterval    = 700 * time.Millisecond
	docSyncCompactRows      = 600
	docSyncMaxUpdateBytes   = 1 << 20  // 1MB per incremental update
	docSyncMaxSnapshotBytes = 12 << 20 // 12MB per compaction snapshot
	docSyncMaxCursorBytes   = 8 << 10  // 8KB per encoded relative position
	// DocSyncJoinTimeout bounds the wait for the client's first "join" frame.
	DocSyncJoinTimeout = 15 * time.Second
)

var docSyncColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

type docSyncEnvelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type DocSyncClient struct {
	peerID   uuid.UUID
	userID   uuid.UUID
	docID    uuid.UUID
	name     string
	color    string
	canWrite bool
	send     chan []byte
	close    func()
	anchor   string
	head     string
}

func (c *DocSyncClient) peerPayload() map[string]any {
	return map[string]any{
		"client_id": c.peerID.String(),
		"user_id":   c.userID.String(),
		"name":      c.name,
		"color":     c.color,
		"anchor":    c.anchor,
		"head":      c.head,
	}
}

// Outbound exposes the client's outbound frame queue to the WebSocket
// handler's write pump.
func (c *DocSyncClient) Outbound() <-chan []byte {
	return c.send
}

// PeerID identifies this connection in cursor presence payloads.
func (c *DocSyncClient) PeerID() uuid.UUID {
	return c.peerID
}

type docRoom struct {
	mu             sync.RWMutex
	docID          uuid.UUID
	clients        map[*DocSyncClient]bool
	history        [][]byte // full update log known to this room
	buffered       [][]byte // updates pending persistence
	rowCount       int64
	compactPending bool
	stopFlusher    chan struct{}
	flusherDone    chan struct{}
	// persistMu serializes log writes (flushes and compactions). A
	// compaction deletes every row for the document, so a flush interleaved
	// with it would either be wiped by the delete or duplicate the tail.
	// Updates keep flowing while it is held: they append to room.buffered
	// and persist once the holder releases.
	persistMu sync.Mutex
}

type docRemoteEditor struct {
	payload  map[string]any
	lastSeen time.Time
}

type DocSyncService struct {
	mu         sync.RWMutex
	rooms      map[uuid.UUID]*docRoom
	docRepo    repository.DocumentRepo
	updateRepo repository.DocumentUpdateRepo
	closing    bool
	// fanout, when set, relays every room broadcast to the other cluster
	// nodes editing the same document and feeds this service the frames
	// from remote editors. Nil keeps doc-sync single-node (tests).
	fanout *realtime.Fanout
	// remote caches the editor peers other nodes announced for one
	// document, so joins show every editor cluster-wide. Entries refresh on
	// cursor traffic and are pruned when stale.
	remote map[uuid.UUID]map[string]*docRemoteEditor
}

func NewDocSyncService(docRepo repository.DocumentRepo, updateRepo repository.DocumentUpdateRepo) *DocSyncService {
	return &DocSyncService{
		rooms:      make(map[uuid.UUID]*docRoom),
		docRepo:    docRepo,
		updateRepo: updateRepo,
		remote:     make(map[uuid.UUID]map[string]*docRemoteEditor),
	}
}

// SetFanout wires the cluster relay before the service starts serving.
func (s *DocSyncService) SetFanout(f *realtime.Fanout) {
	s.fanout = f
	f.SetDocHandler(s.deliverRemote)
	f.SetDocHistoryResponder(s.historySnapshot)
}

// docRemotePeerTTL bounds how long a remote editor stays listed without
// cursor/join traffic; a dead node's editors vanish from the peer lists
// without explicit cleanup.
const docRemoteEditorTTL = 2 * time.Minute

// Join loads (or reuses) the document's room, hands the client the full
// update log and current peers, and announces it to the other editors.
func (s *DocSyncService) Join(ctx context.Context, docID, userID uuid.UUID, name, color string, canWrite bool, closeFn func()) (*DocSyncClient, [][]byte, []map[string]any, error) {
	// Load the replay log before the client can receive relays.
	updates, err := s.updateRepo.ListUpdates(ctx, docID)
	if err != nil {
		return nil, nil, nil, err
	}
	count, err := s.updateRepo.CountUpdates(ctx, docID)
	if err != nil {
		return nil, nil, nil, err
	}

	client := &DocSyncClient{
		peerID:   uuid.New(),
		userID:   userID,
		docID:    docID,
		name:     truncateDocSyncName(name),
		color:    docSyncSafeColor(color),
		canWrite: canWrite,
		send:     make(chan []byte, docSyncSendBuffer),
		close:    closeFn,
	}

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil, nil, nil, context.Canceled
	}
	room := s.rooms[docID]
	if room == nil {
		// The database log may lag the live rooms on the other nodes by one
		// flush interval; catch up from them before the room exists.
		remoteUpdates, remotePeers := s.collectRemoteState(ctx, docID)
		updates = mergeDocUpdates(updates, remoteUpdates)
		s.remote[docID] = docRemoteEditorsFromPayloads(remotePeers)
		room = &docRoom{
			docID:       docID,
			clients:     make(map[*DocSyncClient]bool),
			history:     updates,
			rowCount:    int64(count),
			stopFlusher: make(chan struct{}),
			flusherDone: make(chan struct{}),
		}
		s.rooms[docID] = room
		go s.flushLoop(room)
	}
	s.mu.Unlock()

	room.mu.Lock()
	// The live room history supersedes the DB log (it includes updates that
	// are still buffered for the next flush).
	history := room.history
	if len(history) == 0 && len(updates) > 0 {
		history = updates
	}
	room.clients[client] = true
	peers := make([]map[string]any, 0, len(room.clients)-1)
	for peer := range room.clients {
		if peer != client {
			peers = append(peers, peer.peerPayload())
		}
	}
	peers = append(peers, s.remoteEditors(docID)...)
	room.mu.Unlock()

	join, _ := json.Marshal(docSyncEnvelope{Type: "peer.join", Payload: mustDocSyncJSON(client.peerPayload())})
	s.broadcast(room, client, join)

	// The joiner gets the full replay log and the room's current peers.
	encoded := make([]string, len(history))
	for i, u := range history {
		encoded[i] = base64.StdEncoding.EncodeToString(u)
	}
	s.Send(client, "init", map[string]any{
		"document_id": docID.String(),
		"client_id":   client.PeerID().String(),
		"can_write":   canWrite,
		"updates":     encoded,
	})
	s.Send(client, "peers", map[string]any{"peers": peers})
	return client, history, peers, nil
}

// collectRemoteState asks the cluster's live rooms for their update log and
// editor peers of a document (no-op without a fanout).
func (s *DocSyncService) collectRemoteState(ctx context.Context, docID uuid.UUID) ([][]byte, []map[string]any) {
	if s.fanout == nil {
		return nil, nil
	}
	updates, peers := s.fanout.DocHistory(ctx, docID)
	s.pruneRemoteEditors(docID)
	return updates, peers
}

// mergeDocUpdates appends unseen updates to the database log. Yjs applies
// updates in any order (duplicate content is a no-op), so concatenation
// preserves the primary log's order and grafts the rest.
func mergeDocUpdates(primary, extras [][]byte) [][]byte {
	if len(extras) == 0 {
		return primary
	}
	seen := make(map[string]bool, len(primary)+len(extras))
	merged := make([][]byte, 0, len(primary)+len(extras))
	for _, update := range primary {
		seen[string(update)] = true
		merged = append(merged, update)
	}
	for _, update := range extras {
		if !seen[string(update)] {
			seen[string(update)] = true
			merged = append(merged, update)
		}
	}
	return merged
}

// docRemoteEditorsFromPayloads builds the remote editor cache from history
// replies.
func docRemoteEditorsFromPayloads(payloads []map[string]any) map[string]*docRemoteEditor {
	editors := make(map[string]*docRemoteEditor, len(payloads))
	now := time.Now()
	for _, payload := range payloads {
		if id, ok := payload["client_id"].(string); ok && id != "" {
			editors[id] = &docRemoteEditor{payload: payload, lastSeen: now}
		}
	}
	return editors
}

// remoteEditors snapshots the cached remote editors of a document.
func (s *DocSyncService) remoteEditors(docID uuid.UUID) []map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	peers := make([]map[string]any, 0, len(s.remote[docID]))
	for _, editor := range s.remote[docID] {
		peers = append(peers, editor.payload)
	}
	return peers
}

// pruneRemoteEditors drops cache entries past the TTL and whole caches for
// documents nobody edits locally.
func (s *DocSyncService) pruneRemoteEditors(docID uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, editors := range s.remote {
		if s.rooms[id] == nil && id != docID {
			delete(s.remote, id)
			continue
		}
		for peer, editor := range editors {
			if time.Since(editor.lastSeen) > docRemoteEditorTTL {
				delete(editors, peer)
			}
		}
		if len(editors) == 0 {
			delete(s.remote, id)
		}
	}
}

// Leave removes the client, tells the room's peers, and tears the room down
// (after a final flush) when the last editor disconnects.
func (s *DocSyncService) Leave(client *DocSyncClient) {
	s.mu.Lock()
	room := s.rooms[client.docID]
	if room == nil {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	room.mu.Lock()
	if !room.clients[client] {
		room.mu.Unlock()
		return
	}
	delete(room.clients, client)
	remaining := len(room.clients)
	room.mu.Unlock()

	left, _ := json.Marshal(docSyncEnvelope{Type: "peer.leave", Payload: mustDocSyncJSON(map[string]string{"client_id": client.peerID.String()})})
	s.broadcast(room, nil, left)

	if remaining == 0 {
		s.mu.Lock()
		if s.rooms[client.docID] == room {
			delete(s.rooms, client.docID)
		}
		delete(s.remote, client.docID)
		s.mu.Unlock()
		s.stopRoom(room)
	}
}

// HandleClientMessage processes one incoming JSON frame from an editor.
func (s *DocSyncService) HandleClientMessage(ctx context.Context, client *DocSyncClient, data []byte) {
	var msg docSyncEnvelope
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}
	switch msg.Type {
	case "update":
		s.handleUpdate(client, msg.Payload)
	case "cursor":
		s.handleCursor(client, msg.Payload)
	case "snapshot":
		s.handleSnapshot(ctx, client, msg.Payload)
	}
}

func (s *DocSyncService) handleUpdate(client *DocSyncClient, payload json.RawMessage) {
	var p struct {
		Update string `json:"update"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return
	}
	update, err := base64.StdEncoding.DecodeString(p.Update)
	if err != nil || len(update) == 0 || len(update) > docSyncMaxUpdateBytes {
		return
	}
	if !client.canWrite {
		s.sendError(client, "FORBIDDEN", "read-only session")
		return
	}

	s.mu.Lock()
	room := s.rooms[client.docID]
	s.mu.Unlock()
	if room == nil {
		return
	}

	relay, _ := json.Marshal(docSyncEnvelope{Type: "update", Payload: mustDocSyncJSON(map[string]string{
		"update":    p.Update,
		"client_id": client.peerID.String(),
	})})

	room.mu.Lock()
	room.history = append(room.history, update)
	room.buffered = append(room.buffered, update)
	room.mu.Unlock()

	s.broadcast(room, client, relay)
}

func (s *DocSyncService) handleCursor(client *DocSyncClient, payload json.RawMessage) {
	var p struct {
		Anchor string `json:"anchor"`
		Head   string `json:"head"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return
	}
	if len(p.Anchor) > docSyncMaxCursorBytes || len(p.Head) > docSyncMaxCursorBytes {
		return
	}

	s.mu.Lock()
	room := s.rooms[client.docID]
	s.mu.Unlock()
	if room == nil {
		return
	}

	room.mu.Lock()
	client.anchor = p.Anchor
	client.head = p.Head
	peer := client.peerPayload()
	room.mu.Unlock()

	cursor, _ := json.Marshal(docSyncEnvelope{Type: "cursor", Payload: mustDocSyncJSON(peer)})
	s.broadcast(room, client, cursor)
}

// handleSnapshot stores a full-state snapshot received in answer to a
// compaction request: the update log collapses to one row plus the tail of
// updates buffered when the snapshot arrived (overlap is harmless, Yjs
// applies duplicate content as a no-op). The buffer is swapped out under
// persistMu and the tail compacted while holding it; updates arriving in the
// meantime start a fresh buffer that is only flushed after the compaction
// commits, so nothing is lost and replay order stays correct.
func (s *DocSyncService) handleSnapshot(ctx context.Context, client *DocSyncClient, payload json.RawMessage) {
	var p struct {
		Update string `json:"update"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return
	}
	snapshot, err := base64.StdEncoding.DecodeString(p.Update)
	if err != nil || len(snapshot) == 0 || len(snapshot) > docSyncMaxSnapshotBytes {
		return
	}

	s.mu.Lock()
	room := s.rooms[client.docID]
	s.mu.Unlock()
	if room == nil {
		return
	}

	room.persistMu.Lock()
	defer room.persistMu.Unlock()

	room.mu.Lock()
	if !room.compactPending {
		room.mu.Unlock()
		return
	}
	tail := room.buffered
	room.buffered = nil
	room.mu.Unlock()

	flushCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.updateRepo.Compact(flushCtx, client.docID, snapshot, tail); err != nil {
		log.WithError(err).WithField("document_id", client.docID).Warn("doc sync compaction failed")
		// Undo the swap so the tail is still persisted by a later flush.
		room.mu.Lock()
		room.buffered = append(tail, room.buffered...)
		room.mu.Unlock()
		return
	}

	room.mu.Lock()
	room.rowCount = 1 + int64(len(tail))
	room.compactPending = false
	room.mu.Unlock()
}

func (s *DocSyncService) broadcast(room *docRoom, exclude *DocSyncClient, data []byte) {
	if s.fanout != nil {
		// Cluster path: every node — this one included — receives the frame
		// and relays it to its local room (deliverRemote); local membership
		// state was already updated synchronously above.
		excludePeer := ""
		if exclude != nil {
			excludePeer = exclude.peerID.String()
		}
		s.fanout.PublishDoc(room.docID, data, excludePeer)
		return
	}
	room.mu.RLock()
	defer room.mu.RUnlock()
	for client := range room.clients {
		if client == exclude {
			continue
		}
		select {
		case client.send <- data:
		default:
		}
	}
}

// Send queues a server-originated frame to one client.
func (s *DocSyncService) Send(client *DocSyncClient, typ string, payload any) {
	data, err := json.Marshal(docSyncEnvelope{Type: typ, Payload: mustDocSyncJSON(payload)})
	if err != nil {
		return
	}
	select {
	case client.send <- data:
	default:
	}
}

func (s *DocSyncService) sendError(client *DocSyncClient, code, message string) {
	s.Send(client, "error", map[string]string{"code": code, "message": message})
}

func (s *DocSyncService) flushLoop(room *docRoom) {
	defer close(room.flusherDone)
	ticker := time.NewTicker(docSyncFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-room.stopFlusher:
			s.flush(room)
			return
		case <-ticker.C:
			s.flush(room)
			s.maybeRequestCompaction(room)
		}
	}
}

func (s *DocSyncService) flush(room *docRoom) {
	// Hold persistMu across the swap and the append: a compaction running
	// concurrently would DELETE these rows out from under the insert (or the
	// compacted tail would be persisted twice).
	room.persistMu.Lock()
	defer room.persistMu.Unlock()

	room.mu.Lock()
	if len(room.buffered) == 0 {
		room.mu.Unlock()
		return
	}
	buf := room.buffered
	room.buffered = nil
	room.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.updateRepo.AppendUpdates(ctx, room.docID, buf); err != nil {
		log.WithError(err).WithField("document_id", room.docID).Warn("doc sync update flush failed; will retry")
		room.mu.Lock()
		room.buffered = append(buf, room.buffered...)
		room.mu.Unlock()
		return
	}
	room.mu.Lock()
	room.rowCount += int64(len(buf))
	room.mu.Unlock()
}

// maybeRequestCompaction asks one connected editor for a full-state snapshot
// once the row count grows past the threshold; the room keeps accepting
// updates meanwhile (they become the compacted row's tail).
func (s *DocSyncService) maybeRequestCompaction(room *docRoom) {
	room.mu.Lock()
	defer room.mu.Unlock()
	if room.compactPending || room.rowCount < docSyncCompactRows {
		return
	}
	for client := range room.clients {
		if !client.canWrite {
			continue
		}
		room.compactPending = true
		data, _ := json.Marshal(docSyncEnvelope{Type: "compact.request", Payload: json.RawMessage(`{}`)})
		select {
		case client.send <- data:
		default:
			room.compactPending = false
		}
		return
	}
}

func (s *DocSyncService) stopRoom(room *docRoom) {
	close(room.stopFlusher)
	<-room.flusherDone
}

// Shutdown closes every doc-sync socket and waits for the rooms to drain and
// flush. Hijacked WebSockets are invisible to http.Server.Shutdown, so the
// server must call this explicitly next to the realtime hub's Shutdown.
func (s *DocSyncService) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	rooms := make([]*docRoom, 0, len(s.rooms))
	for _, room := range s.rooms {
		rooms = append(rooms, room)
	}
	s.mu.Unlock()

	for _, room := range rooms {
		room.mu.RLock()
		conns := make([]*DocSyncClient, 0, len(room.clients))
		for client := range room.clients {
			conns = append(conns, client)
		}
		room.mu.RUnlock()
		for _, client := range conns {
			if client.close != nil {
				client.close()
			}
		}
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.mu.RLock()
		remaining := len(s.rooms)
		s.mu.RUnlock()
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

// ResetDocument discards a document's entire live and persisted Yjs state,
// used after a revision restore makes the stored HTML authoritative. Any
// live room is torn down without flushing its buffer, the update log is
// wiped, and every connected editor receives a "reset" frame followed by a
// socket close — clients reconnect with a fresh Y.Doc seeded from the
// restored content_html. The log wipe happens after the room's flusher has
// drained, so a final (stale) flush cannot resurrect rows afterwards.
// Cluster nodes without the originating request tear their rooms down the
// same way (without touching the shared database a second time).
func (s *DocSyncService) ResetDocument(docID uuid.UUID) {
	s.resetRoomLocalAndNotify(docID)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.updateRepo.DeleteByDocument(ctx, docID); err != nil {
		log.WithError(err).WithField("document_id", docID).Warn("doc sync reset failed to clear update log")
	}

	if s.fanout != nil {
		reset, _ := json.Marshal(docSyncEnvelope{Type: "reset", Payload: mustDocSyncJSON(map[string]any{})})
		s.fanout.PublishDoc(docID, reset, "")
	}
}

// resetRoomLocalAndNotify tears this node's room down without flushing and
// sends its clients the reset frame before closing them.
func (s *DocSyncService) resetRoomLocalAndNotify(docID uuid.UUID) {
	for _, client := range s.resetRoomLocal(docID) {
		s.Send(client, "reset", map[string]any{})
		if client.close != nil {
			client.close()
		}
	}
}

// resetRoomLocal tears the document's room down without flushing its
// buffer and returns the affected clients.
func (s *DocSyncService) resetRoomLocal(docID uuid.UUID) []*DocSyncClient {
	s.mu.Lock()
	room := s.rooms[docID]
	if room != nil {
		delete(s.rooms, docID)
	}
	delete(s.remote, docID)
	s.mu.Unlock()

	var clients []*DocSyncClient
	if room != nil {
		// Once the flusher has drained, nothing can persist updates for
		// this room again; updates from a racing handleUpdate only append
		// to a dead, unreachable room.
		s.stopRoom(room)

		room.mu.Lock()
		room.buffered = nil
		room.history = nil
		room.compactPending = false
		clients = make([]*DocSyncClient, 0, len(room.clients))
		for client := range room.clients {
			clients = append(clients, client)
		}
		room.mu.Unlock()
	}
	return clients
}

// deliverRemote processes one relayed doc-sync frame from the cluster:
// remote editor bookkeeping plus local relay. The origin's own deliveries
// ride the same path — its local relay excludes the origin editor, and the
// history/peer bookkeeping for its own editors already happened
// synchronously in the originating call.
func (s *DocSyncService) deliverRemote(env *realtime.DocEnvelope) {
	var frame docSyncEnvelope
	if json.Unmarshal(env.Frame, &frame) != nil {
		return
	}
	fromSelf := s.fanout != nil && env.Node == s.fanout.Self()

	s.mu.RLock()
	room := s.rooms[env.DocID]
	s.mu.RUnlock()

	switch frame.Type {
	case "peer.join":
		if !fromSelf {
			var payload map[string]any
			if json.Unmarshal(frame.Payload, &payload) == nil {
				s.trackRemoteEditor(env.DocID, payload)
			}
		}
	case "peer.leave":
		var payload struct {
			ClientID string `json:"client_id"`
		}
		if json.Unmarshal(frame.Payload, &payload) == nil {
			s.forgetRemoteEditor(env.DocID, payload.ClientID)
		}
	case "cursor":
		if !fromSelf {
			var payload map[string]any
			if json.Unmarshal(frame.Payload, &payload) == nil {
				s.trackRemoteEditor(env.DocID, payload)
			}
		}
	case "update":
		if !fromSelf && room != nil {
			// Remember relayed updates so later joins on this node replay
			// them without waiting for the origin's database flush. The
			// origin node persists; relays never write.
			var payload struct {
				Update string `json:"update"`
			}
			if json.Unmarshal(frame.Payload, &payload) == nil {
				if update, err := base64.StdEncoding.DecodeString(payload.Update); err == nil && len(update) > 0 {
					room.mu.Lock()
					room.history = append(room.history, update)
					room.mu.Unlock()
				}
			}
		}
	case "reset":
		s.resetRoomLocalAndNotify(env.DocID)
		return
	}

	// Relay the frame to the local room, excluding the origin editor.
	s.mu.RLock()
	room = s.rooms[env.DocID]
	s.mu.RUnlock()
	if room == nil {
		return
	}
	room.mu.RLock()
	defer room.mu.RUnlock()
	for client := range room.clients {
		if client.peerID.String() == env.ExcludePeer {
			continue
		}
		select {
		case client.send <- env.Frame:
		default:
		}
	}
}

func (s *DocSyncService) trackRemoteEditor(docID uuid.UUID, payload map[string]any) {
	id, _ := payload["client_id"].(string)
	if id == "" {
		return
	}
	s.mu.Lock()
	if s.remote[docID] == nil {
		s.remote[docID] = make(map[string]*docRemoteEditor)
	}
	s.remote[docID][id] = &docRemoteEditor{payload: payload, lastSeen: time.Now()}
	s.mu.Unlock()
}

func (s *DocSyncService) forgetRemoteEditor(docID uuid.UUID, clientID string) {
	if clientID == "" {
		return
	}
	s.mu.Lock()
	delete(s.remote[docID], clientID)
	if len(s.remote[docID]) == 0 {
		delete(s.remote, docID)
	}
	s.mu.Unlock()
}

// historySnapshot is the fanout responder callback: this node's live log
// and editor peers for a document.
func (s *DocSyncService) historySnapshot(docID uuid.UUID) ([][]byte, []map[string]any) {
	s.mu.RLock()
	room := s.rooms[docID]
	s.mu.RUnlock()
	if room == nil {
		return nil, nil
	}
	room.mu.RLock()
	history := make([][]byte, len(room.history))
	copy(history, room.history)
	peers := make([]map[string]any, 0, len(room.clients))
	for client := range room.clients {
		peers = append(peers, client.peerPayload())
	}
	room.mu.RUnlock()
	peers = append(peers, s.remoteEditors(docID)...)
	return history, peers
}

func truncateDocSyncName(name string) string {
	runes := []rune(name)
	if len(runes) > 80 {
		runes = runes[:80]
	}
	return string(runes)
}

func docSyncSafeColor(color string) string {
	if docSyncColorPattern.MatchString(color) {
		return color
	}
	return "#71717a"
}

func mustDocSyncJSON(payload any) json.RawMessage {
	data, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return data
}
