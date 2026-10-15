package machine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/agent"
	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

type agentLogObserverKey struct{}
type agentLogObserver func(containerID, stream, content string)

type agentLogWriter struct {
	containerID string
	stream      string
	observer    agentLogObserver
	pending     []byte
	captured    int
}

func (w *agentLogWriter) Write(data []byte) (int, error) {
	n := len(data)
	if w.captured >= maxCapturedLogBytes {
		return n, nil
	}
	data = data[:min(len(data), maxCapturedLogBytes-w.captured)]
	w.captured += len(data)
	w.pending = append(w.pending, data...)
	if end := bytes.LastIndexByte(w.pending, '\n'); end >= 0 {
		w.observer(w.containerID, w.stream, string(w.pending[:end+1]))
		w.pending = bytes.Clone(w.pending[end+1:])
	}
	return n, nil
}

func (w *agentLogWriter) flush() {
	if len(w.pending) > 0 {
		w.observer(w.containerID, w.stream, string(w.pending))
		w.pending = nil
	}
}

// Follow the same container, including when a manager resumes a durable run.
// Terminal stdout/stderr capture remains the authority for provider results.
func (r *DockerRuntime) followAgentLogs(ctx context.Context, containerID string, observer agentLogObserver) func(bool) {
	logCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		logs, err := r.client.ContainerLogs(logCtx, containerID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Follow: true})
		if err != nil {
			return
		}
		defer logs.Close()
		stopClose := context.AfterFunc(logCtx, func() { logs.Close() })
		defer stopClose()
		stdout := &agentLogWriter{containerID: containerID, stream: "stdout", observer: observer}
		stderr := &agentLogWriter{containerID: containerID, stream: "stderr", observer: observer}
		_, _ = stdcopy.StdCopy(stdout, stderr, logs)
		stdout.flush()
		stderr.flush()
	}()
	return func(drain bool) {
		if !drain {
			cancel()
		}
		// A normal container exit closes Follow naturally. Cancellation closes a
		// stalled reader; don't abandon a live stream merely due to an observation timeout.
		select {
		case <-done:
		case <-ctx.Done():
			cancel()
			<-done
		}
		cancel()
	}
}

var _ io.Writer = (*agentLogWriter)(nil)

type agentRunLogRecord struct {
	chunk     domain.DevMachineLogChunk
	attempted bool
	persisted bool
}

type agentRunLogStream struct {
	raw      strings.Builder
	consumed int
	records  []*agentRunLogRecord
	blocked  bool
}

type agentRunEventRecord struct {
	event     domain.DevMachineEvent
	persisted bool
}

// Streaming writes are best effort. Retain their exact identities so terminal
// capture can fill each stream independently and retry failed writes once.
type agentRunLogs struct {
	mu               sync.Mutex
	manager          *Manager
	machine          *domain.DevMachine
	run              *domain.DevMachineAgentRun
	service          *domain.DevMachineService
	provider         agent.Provider
	secrets          map[string]string
	streams          map[string]*agentRunLogStream
	eventOccurrences map[[32]byte]int
	eventRecords     []*agentRunEventRecord
	started          bool
	closed           bool
}

func redactLogFragment(content string, secrets map[string]string, incomplete bool) string {
	redacted := redact(content, secrets)
	// An interrupted reader can end inside a secret. Preserve the diagnostic
	// fragment while masking any suffix that could still be a secret prefix.
	if incomplete {
		for secret := range maps.Values(secrets) {
			for length := min(len(secret)-1, len(redacted)); length > 0; length-- {
				if strings.HasSuffix(redacted, secret[:length]) {
					redacted = redacted[:len(redacted)-length] + "[REDACTED]"
					break
				}
			}
		}
	}
	return redacted
}

func (l *agentRunLogs) writeLog(ctx context.Context, stream string, state *agentRunLogStream, content string) *agentRunLogRecord {
	redacted := redactLogFragment(content, l.secrets, !strings.HasSuffix(content, "\n"))
	record := &agentRunLogRecord{chunk: domain.DevMachineLogChunk{
		WorkspaceID: l.machine.WorkspaceID, MachineID: l.machine.ID, AgentRunID: &l.run.ID, ServiceID: &l.service.ID,
		Stream: stream, Sequence: int64(len(state.records) + 1), Content: redacted,
	}}
	state.records = append(state.records, record)
	if !state.blocked {
		record.attempted = true
		record.persisted = l.manager.store.CreateLogChunk(ctx, &record.chunk) == nil
		state.blocked = !record.persisted
	}
	return record
}

func (l *agentRunLogs) finishLog(ctx context.Context, record *agentRunLogRecord) error {
	attempts := 1
	if !record.attempted {
		attempts = 2
	}
	var err error
	for range attempts {
		record.attempted = true
		err = l.manager.store.CreateLogChunk(ctx, &record.chunk)
		if err == nil {
			record.persisted = true
			return nil
		}
	}
	return fmt.Errorf("save agent %s log sequence %d: %w", record.chunk.Stream, record.chunk.Sequence, err)
}

func (l *agentRunLogs) providerEvent(event agent.Event) domain.DevMachineEvent {
	payload := event.Payload
	if len(payload) == 0 {
		payload, _ = json.Marshal(map[string]any{"message": event.Message})
	}
	return domain.DevMachineEvent{
		WorkspaceID: l.machine.WorkspaceID, MachineID: l.machine.ID, AgentRunID: &l.run.ID,
		Source: "agent", EventType: event.Type, Payload: payload, OccurredAt: time.Now().UTC(),
	}
}

func agentRunEventKey(event domain.DevMachineEvent) [32]byte {
	return sha256.Sum256([]byte(event.EventType + "\x00" + string(event.Payload)))
}

func (l *agentRunLogs) writeEvent(ctx context.Context, event domain.DevMachineEvent) *agentRunEventRecord {
	record := &agentRunEventRecord{event: event}
	key := agentRunEventKey(event)
	l.eventOccurrences[key]++
	l.eventRecords = append(l.eventRecords, record)
	record.persisted = l.manager.store.CreateEvent(ctx, &record.event) == nil
	return record
}

func (l *agentRunLogs) finishEvent(ctx context.Context, record *agentRunEventRecord) error {
	if err := l.manager.store.CreateEvent(ctx, &record.event); err != nil {
		return fmt.Errorf("save agent event: %w", err)
	}
	record.persisted = true
	return nil
}

func (l *agentRunLogs) observe(ctx context.Context, containerID, stream, content string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || (stream != "stdout" && stream != "stderr") {
		return
	}
	if !l.started {
		_ = l.manager.store.UpdateServiceRuntime(ctx, l.service.ID, containerID, "running", "running", nil)
		l.started = true
	}
	state := l.streams[stream]
	if state == nil {
		state = &agentRunLogStream{}
		l.streams[stream] = state
	}
	state.raw.WriteString(content)
	raw := state.raw.String()
	end := strings.LastIndexByte(raw, '\n') + 1
	if end <= state.consumed {
		return
	}
	complete := raw[state.consumed:end]
	state.consumed = end
	l.writeLog(ctx, stream, state, complete)
	if stream == "stdout" {
		for _, event := range l.provider.ParseEvents([]byte(redact(complete, l.secrets))) {
			l.writeEvent(ctx, l.providerEvent(event))
		}
	}
}

func (l *agentRunLogs) finish(ctx context.Context, stdout, stderr string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	var failures []error
	for _, stream := range []string{"stdout", "stderr"} {
		state := l.streams[stream]
		if state == nil {
			state = &agentRunLogStream{}
			l.streams[stream] = state
		}
		state.blocked = false
		for _, record := range state.records {
			if !record.persisted {
				if err := l.finishLog(ctx, record); err != nil {
					failures = append(failures, err)
					state.blocked = true
				}
			}
		}
		terminal := stdout
		if stream == "stderr" {
			terminal = stderr
		}
		raw := state.raw.String()
		if len(raw) > len(terminal) && strings.HasPrefix(raw, terminal) {
			terminal = raw
		}
		observed := raw[:state.consumed]
		matched := 0
		for matched < min(len(observed), len(terminal)) && observed[matched] == terminal[matched] {
			matched++
		}
		// Complete streamed lines are already stored (or retried under the same
		// sequence). Incomplete Follow fragments remain in the terminal suffix.
		if matched < len(terminal) {
			record := l.writeLog(ctx, stream, state, terminal[matched:])
			if !record.persisted {
				if err := l.finishLog(ctx, record); err != nil {
					failures = append(failures, err)
				}
			}
		}
	}
	for _, record := range l.eventRecords {
		if !record.persisted {
			if err := l.finishEvent(ctx, record); err != nil {
				failures = append(failures, err)
			}
		}
	}
	occurrences := make(map[[32]byte]int)
	for _, providerEvent := range l.provider.ParseEvents([]byte(redact(stdout, l.secrets))) {
		event := l.providerEvent(providerEvent)
		key := agentRunEventKey(event)
		occurrence := occurrences[key]
		occurrences[key]++
		if occurrence < l.eventOccurrences[key] {
			continue
		}
		record := l.writeEvent(ctx, event)
		if !record.persisted {
			if err := l.finishEvent(ctx, record); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}
