package machine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/agent"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/stretchr/testify/require"
)

type reconciliationStore struct {
	*managerStoreFake
	logFailures             map[string]int
	eventFailures           map[string]int
	eventCommitErrors       map[string]int
	logAttempts             map[string]int
	eventAttempts           map[string]int
	eventAttemptOccurrences map[string][]uuid.UUID
	nextEventID             int64
}

func (s *reconciliationStore) CreateLogChunk(ctx context.Context, chunk *domain.DevMachineLogChunk) error {
	key := fmt.Sprintf("%s/%d", chunk.Stream, chunk.Sequence)
	s.logAttempts[key]++
	if s.logFailures[key] > 0 {
		s.logFailures[key]--
		return errors.New("log write unavailable")
	}
	for _, saved := range s.logChunks {
		if saved.Stream == chunk.Stream && saved.Sequence == chunk.Sequence {
			if saved.Content != chunk.Content {
				return errors.New("log sequence reused with different content")
			}
			return nil
		}
	}
	return s.managerStoreFake.CreateLogChunk(ctx, chunk)
}

func (s *reconciliationStore) CreateEvent(ctx context.Context, event *domain.DevMachineEvent) error {
	if event.OccurrenceID == uuid.Nil {
		event.OccurrenceID = uuid.New()
	}
	key := string(event.Payload)
	if event.Source == "agent" && !strings.HasPrefix(event.EventType, "agent_run.") {
		s.eventAttempts[key]++
		s.eventAttemptOccurrences[key] = append(s.eventAttemptOccurrences[key], event.OccurrenceID)
		if s.eventFailures[key] > 0 {
			s.eventFailures[key]--
			return errors.New("event write unavailable")
		}
	}
	for _, saved := range s.events {
		if saved.OccurrenceID == event.OccurrenceID {
			event.ID, event.CreatedAt = saved.ID, saved.CreatedAt
			return nil
		}
	}
	s.nextEventID++
	saved := *event
	saved.ID = s.nextEventID
	if err := s.managerStoreFake.CreateEvent(ctx, &saved); err != nil {
		return err
	}
	if s.eventCommitErrors[key] > 0 {
		s.eventCommitErrors[key]--
		return errors.New("event committed but response was lost")
	}
	event.ID, event.CreatedAt = saved.ID, saved.CreatedAt
	return nil
}

func newReconciliationRun(t *testing.T, stdout, stderr string) (*Manager, *reconciliationStore, *runtimeFake, *domain.DevMachineOperation) {
	t.Helper()
	machineID, workspaceID, runID := uuid.New(), uuid.New(), uuid.New()
	store := &reconciliationStore{managerStoreFake: &managerStoreFake{
		machine: &domain.DevMachine{ID: machineID, WorkspaceID: workspaceID, RoutingKey: "0123456789abcdef0123",
			Status: domain.DevMachineStatusRunning, DesiredStatus: domain.DevMachineStatusRunning, Generation: 1},
		agentRun: &domain.DevMachineAgentRun{ID: runID, MachineID: machineID, WorkspaceID: workspaceID,
			ProviderID: "opencode", Mode: "autonomous", Status: domain.DevMachineAgentRunStatusQueued, MaxRuntimeSeconds: 30},
		provider: &domain.DevMachineAgentProvider{MachineID: machineID, ProviderID: "opencode", ImageRef: "ku/opencode:test", Enabled: true},
	}, logFailures: make(map[string]int), eventFailures: make(map[string]int), eventCommitErrors: make(map[string]int),
		logAttempts: make(map[string]int), eventAttempts: make(map[string]int), eventAttemptOccurrences: make(map[string][]uuid.UUID)}
	runtime := &runtimeFake{agentExecution: &AgentExecution{ContainerID: "test-agent", Stdout: stdout, Stderr: stderr}}
	manager := NewManager(store, runtime, agent.NewRegistry(agent.NewOpenCodeProvider("ku/opencode:test")), forgejoAPIFake{}, nil, make([]byte, 32), make([]byte, 32), "test")
	operation := &domain.DevMachineOperation{MachineID: machineID, WorkspaceID: workspaceID, AgentRunID: new(runID), Action: domain.DevMachineOpRunAgent, Generation: 1}
	return manager, store, runtime, operation
}

func persistedStream(store *reconciliationStore, stream string) string {
	var chunks []domain.DevMachineLogChunk
	for _, chunk := range store.logChunks {
		if chunk.Stream == stream {
			chunks = append(chunks, chunk)
		}
	}
	var output strings.Builder
	for _, chunk := range chunks {
		output.WriteString(chunk.Content)
	}
	return output.String()
}

func providerEvents(store *reconciliationStore) []domain.DevMachineEvent {
	var events []domain.DevMachineEvent
	for _, event := range store.events {
		if event.Source == "agent" && !strings.HasPrefix(event.EventType, "agent_run.") {
			events = append(events, event)
		}
	}
	return events
}

func TestManagerReconcilesPartialFollowStreams(t *testing.T) {
	first := `{"type":"message","message":"first"}` + "\n"
	last := `{"type":"text","part":{"type":"text","text":"terminal result"}}`
	stdout, stderr := first+last, "diagnostic one\ndiagnostic two\n"
	for _, stream := range []string{"stdout", "stderr", "both"} {
		t.Run(stream, func(t *testing.T) {
			manager, store, runtime, operation := newReconciliationRun(t, stdout, stderr)
			runtime.onAgentRun = func(ctx context.Context) {
				observer := ctx.Value(agentLogObserverKey{}).(agentLogObserver)
				if stream != "stderr" {
					observer("test-agent", "stdout", first)
					observer("test-agent", "stdout", last[:19])
				}
				if stream != "stdout" {
					observer("test-agent", "stderr", "diagnostic one\n")
				}
			}
			require.NoError(t, manager.runAgent(t.Context(), store.machine, operation))
			require.Equal(t, stdout, persistedStream(store, "stdout"))
			require.Equal(t, stderr, persistedStream(store, "stderr"))
			require.Len(t, providerEvents(store), 2)
			require.Equal(t, "terminal result", *store.completedRun.Summary)
		})
	}
}

func TestManagerReconcilesFailedLogWritesWithoutDuplicates(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			output := "first line\nsecond line\nlast line"
			stdout, stderr := output, "only terminal stderr"
			if stream == "stderr" {
				stdout, stderr = "only terminal stdout", output
			}
			manager, store, runtime, operation := newReconciliationRun(t, stdout, stderr)
			store.logFailures[stream+"/1"] = 1
			runtime.onAgentRun = func(ctx context.Context) {
				observer := ctx.Value(agentLogObserverKey{}).(agentLogObserver)
				observer("test-agent", stream, "first line\n")
				observer("test-agent", stream, "second line\n")
			}
			require.NoError(t, manager.runAgent(t.Context(), store.machine, operation))
			require.Equal(t, stdout, persistedStream(store, "stdout"))
			require.Equal(t, stderr, persistedStream(store, "stderr"))
			require.Equal(t, 2, store.logAttempts[stream+"/1"])
			require.Equal(t, 1, store.logAttempts[stream+"/2"])
			require.Equal(t, 1, store.logAttempts[stream+"/3"])
			require.Len(t, store.logChunks, 4)
		})
	}
}

func TestManagerRetriesFailedEventsOnceAndPreservesRepeatedEvents(t *testing.T) {
	first := `{"type":"message","message":"repeat"}`
	last := `{"type":"text","part":{"type":"text","text":"terminal result"}}`
	stdout := first + "\n" + first + "\n" + last
	manager, store, runtime, operation := newReconciliationRun(t, stdout, "")
	store.eventFailures[first] = 1
	runtime.onAgentRun = func(ctx context.Context) {
		ctx.Value(agentLogObserverKey{}).(agentLogObserver)("test-agent", "stdout", first+"\n"+first+"\n")
	}
	require.NoError(t, manager.runAgent(t.Context(), store.machine, operation))
	require.Len(t, providerEvents(store), 3)
	require.Equal(t, 3, store.eventAttempts[first], "two occurrences and one bounded final retry")
	require.Equal(t, 1, store.eventAttempts[last])
	require.Equal(t, stdout, persistedStream(store, "stdout"))
}

func TestManagerReconcilesCommittedEventsAfterLostReplies(t *testing.T) {
	repeated := `{"type":"message","message":"repeat"}`
	terminal := `{"type":"text","part":{"type":"text","text":"terminal result"}}`
	stdout := repeated + "\n" + repeated + "\n" + terminal
	manager, store, runtime, operation := newReconciliationRun(t, stdout, "")
	store.eventCommitErrors[repeated] = 2
	store.eventCommitErrors[terminal] = 1
	runtime.onAgentRun = func(ctx context.Context) {
		ctx.Value(agentLogObserverKey{}).(agentLogObserver)("test-agent", "stdout", repeated+"\n"+repeated+"\n")
	}
	require.NoError(t, manager.runAgent(t.Context(), store.machine, operation))
	events := providerEvents(store)
	require.Len(t, events, 3, "retries must not duplicate a committed occurrence")
	require.Equal(t, repeated, string(events[0].Payload))
	require.Equal(t, repeated, string(events[1].Payload))
	require.NotEqual(t, events[0].ID, events[1].ID, "identical occurrences must keep distinct identities")
	require.Equal(t, []uuid.UUID{events[0].OccurrenceID, events[1].OccurrenceID, events[0].OccurrenceID, events[1].OccurrenceID}, store.eventAttemptOccurrences[repeated])
	require.Equal(t, []uuid.UUID{events[2].OccurrenceID, events[2].OccurrenceID}, store.eventAttemptOccurrences[terminal])
	require.Equal(t, "terminal result", *store.completedRun.Summary)
	require.NotContains(t, string(store.completedRun.RiskNotes), "Agent output could not be fully saved.")
}

func TestManagerBuffersPartialJSONLAndRedactsBeforePersistence(t *testing.T) {
	line := `{"type":"message","message":"secret-token"}`
	manager, store, _, _ := newReconciliationRun(t, line+"\n", "")
	logs := &agentRunLogs{manager: manager, machine: store.machine, run: store.agentRun,
		service: &domain.DevMachineService{ID: uuid.New()}, provider: agent.NewOpenCodeProvider("ku/opencode:test"),
		secrets: map[string]string{"API_KEY": "secret-token"}, streams: make(map[string]*agentRunLogStream), eventOccurrences: make(map[[32]byte]int)}
	logs.observe(t.Context(), "test-agent", "stdout", `{"type":"message","message":"secret-`)
	require.Empty(t, store.logChunks)
	require.Empty(t, providerEvents(store))
	logs.observe(t.Context(), "test-agent", "stdout", "token\"}\n")
	require.Len(t, store.logChunks, 1)
	require.Len(t, providerEvents(store), 1)
	require.NoError(t, logs.finish(t.Context(), line+"\n", ""))
	require.Len(t, store.logChunks, 1)
	require.Len(t, providerEvents(store), 1)
	require.NotContains(t, store.logChunks[0].Content, "secret-token")
	require.Contains(t, store.logChunks[0].Content, "[REDACTED]")
	require.NotContains(t, string(providerEvents(store)[0].Payload), "secret-token")

	logs.observe(t.Context(), "test-agent", "stdout", "late callback\n")
	require.Len(t, store.logChunks, 1, "callbacks after reconciliation must not append output")

	logs = &agentRunLogs{manager: manager, machine: store.machine, run: store.agentRun,
		service: &domain.DevMachineService{ID: uuid.New()}, provider: agent.NewOpenCodeProvider("ku/opencode:test"),
		secrets: map[string]string{"API_KEY": "secret-token"}, streams: make(map[string]*agentRunLogStream), eventOccurrences: make(map[[32]byte]int)}
	logs.observe(t.Context(), "test-agent", "stderr", "diagnostic secret-")
	require.NoError(t, logs.finish(t.Context(), "", ""))
	require.Equal(t, "diagnostic [REDACTED]", persistedStream(store, "stderr"))
}

func TestManagerReportsPersistentEventFailureWithoutChangingProviderResult(t *testing.T) {
	line := `{"type":"text","part":{"type":"text","text":"terminal result"}}`
	manager, store, runtime, operation := newReconciliationRun(t, line, "")
	store.eventFailures[line] = 10
	runtime.onAgentRun = func(ctx context.Context) {
		ctx.Value(agentLogObserverKey{}).(agentLogObserver)("test-agent", "stdout", line+"\n")
	}
	require.NoError(t, manager.runAgent(t.Context(), store.machine, operation))
	require.Equal(t, 2, store.eventAttempts[line])
	require.Empty(t, providerEvents(store))
	require.Equal(t, "terminal result", *store.completedRun.Summary)
	require.Equal(t, domain.DevMachineAgentRunStatusSucceeded, store.completedRun.Status)
	require.Contains(t, string(store.completedRun.RiskNotes), "Agent output could not be fully saved.")
}

func TestManagerReportsPersistentLogFailureAndStillSavesRemainingOutput(t *testing.T) {
	manager, store, runtime, operation := newReconciliationRun(t, "first\nsecond\nlast", "terminal stderr")
	store.logFailures["stdout/1"] = 10
	runtime.onAgentRun = func(ctx context.Context) {
		observer := ctx.Value(agentLogObserverKey{}).(agentLogObserver)
		observer("test-agent", "stdout", "first\n")
		observer("test-agent", "stdout", "second\n")
	}
	require.NoError(t, manager.runAgent(t.Context(), store.machine, operation))
	require.Equal(t, 2, store.logAttempts["stdout/1"])
	require.Equal(t, "second\nlast", persistedStream(store, "stdout"))
	require.Equal(t, "terminal stderr", persistedStream(store, "stderr"))
	require.Equal(t, domain.DevMachineAgentRunStatusSucceeded, store.completedRun.Status)
	require.Contains(t, string(store.completedRun.RiskNotes), "Agent output could not be fully saved.")
}

func TestManagerReconcilesObservedEOFWhenTerminalCaptureIsShorter(t *testing.T) {
	manager, store, runtime, operation := newReconciliationRun(t, "first\n", "")
	runtime.onAgentRun = func(ctx context.Context) {
		observer := ctx.Value(agentLogObserverKey{}).(agentLogObserver)
		observer("test-agent", "stdout", "first\nunfinished diagnostic")
	}
	require.NoError(t, manager.runAgent(t.Context(), store.machine, operation))
	require.Equal(t, "first\nunfinished diagnostic", persistedStream(store, "stdout"))
	require.Equal(t, "first", *store.completedRun.Summary, "provider result must still use terminal capture")
}
