package machine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

func fixtureCancellationRecord(machine *domain.DevMachine, runID uuid.UUID) container.InspectResponse {
	labels := machineLabels(machine)
	labels["com.strifira.service"], labels["com.strifira.cancellation-version"], labels["com.strifira.agent-run-id"] = "agent-cancellation", "1", runID.String()
	return container.InspectResponse{ID: "cancellation-record", Name: "/" + agentCancellationName(machine, runID), Config: &container.Config{Image: fixtureHelperImageID, User: "1000:1000", Labels: labels}, HostConfig: &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true}, State: &container.State{Status: container.StateCreated}}
}

func TestAgentContainerRemovalWaitsForImmutableIDDisappearance(t *testing.T) {
	for _, mode := range []string{"eventual-removal", "still-removing"} {
		t.Run(mode, func(t *testing.T) {
			var inspections atomic.Int32
			api := cancellationFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/v1.47/containers/original-id/json", r.URL.Path)
				count := inspections.Add(1)
				if mode == "eventual-removal" && count >= 3 {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(container.InspectResponse{ID: "original-id", State: &container.State{Status: container.StateRemoving}}))
			})
			ctx, cancel := context.WithTimeout(t.Context(), 120*time.Millisecond)
			defer cancel()
			err := (&DockerRuntime{client: api}).waitAgentContainerRemoved(ctx, "original-id")
			if mode == "eventual-removal" {
				require.NoError(t, err)
				require.EqualValues(t, 3, inspections.Load())
			} else {
				require.ErrorIs(t, err, context.DeadlineExceeded, "private data must be retained until this ID disappears")
			}
		})
	}
}

func cancellationFixtureClient(t *testing.T, handler http.HandlerFunc) *client.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	api, err := client.NewClientWithOpts(client.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")), client.WithAPIVersion("1.47"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = api.Close() })
	return api
}

func TestAgentCancellationStopsEveryOwnedRunContainerAndPreservesForeignRuns(t *testing.T) {
	for _, mode := range []string{"success", "lost-marker-response", "delete-error", "foreign-target", "changed-inspection", "already-gone"} {
		t.Run(mode, func(t *testing.T) {
			machine := fixtureHelperMachine()
			run := &domain.DevMachineAgentRun{ID: uuid.New(), MachineID: machine.ID, WorkspaceID: machine.WorkspaceID}
			inspections := map[string]container.InspectResponse{}
			operation := uuid.NewString()
			for _, role := range []string{"agent", "git-verification", "workspace-verification", "git-helper", "git-checkout", "source-sync"} {
				labels := machineLabels(machine)
				labels["com.strifira.service"], labels["com.strifira.agent-run-id"] = role, run.ID.String()
				labels["com.strifira.repository-operation"] = operation
				name := "strifira-" + machine.RoutingKey + "-git-" + operation
				if role == "agent" {
					name = "strifira-" + machine.RoutingKey + "-agent-" + run.ID.String()
				}
				if verificationService(role) {
					name = "strifira-" + machine.RoutingKey + "-verification-" + run.ID.String()
				}
				if role == "source-sync" {
					labels["com.strifira.service"], labels["com.strifira.source-sync-run"] = "git-helper", run.ID.String()
					delete(labels, "com.strifira.agent-run-id")
					name = "strifira-" + machine.RoutingKey + "-source-sync-" + run.ID.String()
				}
				inspections[role] = container.InspectResponse{ID: role, Name: "/" + name, Config: &container.Config{Image: fixtureHelperImageID, Labels: labels}}
			}
			if mode == "foreign-target" {
				inspections["agent"].Config.Labels["com.strifira.workspace-id"] = uuid.NewString()
			}
			if mode == "changed-inspection" {
				changed := inspections["agent"]
				changed.ID = "foreign-replacement"
				inspections["agent"] = changed
			}
			var marker container.InspectResponse
			var markerPresent bool
			deleted := map[string]bool{}
			api := cancellationFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				path := strings.TrimPrefix(r.URL.Path, "/v1.47")
				switch {
				case path == "/containers/"+agentCancellationName(machine, run.ID)+"/json":
					if !markerPresent {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					require.NoError(t, json.NewEncoder(w).Encode(marker))
				case path == "/containers/"+workspaceLayoutName(machine)+"/json":
					labels := machineLabels(machine)
					labels["com.strifira.service"], labels["com.strifira.layout-version"], labels["com.strifira.layout-volume"], labels["com.strifira.layout-key"] = "workspace-layout", workspaceLayoutVersion, *machine.WorkspaceVolumeName, uuid.NewString()
					require.NoError(t, json.NewEncoder(w).Encode(container.InspectResponse{ID: "layout", Config: &container.Config{Image: fixtureHelperImageID, Labels: labels}, HostConfig: &container.HostConfig{}}))
				case path == "/containers/create":
					require.Equal(t, agentCancellationName(machine, run.ID), r.URL.Query().Get("name"))
					var options struct {
						container.Config
						HostConfig *container.HostConfig
					}
					require.NoError(t, json.NewDecoder(r.Body).Decode(&options))
					require.Empty(t, options.HostConfig.Mounts)
					require.Empty(t, options.Volumes)
					require.Equal(t, container.NetworkMode("none"), options.HostConfig.NetworkMode)
					marker = fixtureCancellationRecord(machine, run.ID)
					marker.Config = &options.Config
					marker.HostConfig = options.HostConfig
					markerPresent = true
					if mode == "lost-marker-response" {
						w.WriteHeader(http.StatusInternalServerError)
					} else {
						w.WriteHeader(http.StatusCreated)
						require.NoError(t, json.NewEncoder(w).Encode(map[string]string{"Id": marker.ID}))
					}
				case path == "/containers/json":
					var items []container.Summary
					for id, inspection := range inspections {
						items = append(items, container.Summary{ID: id, Labels: inspection.Config.Labels})
					}
					other := machineLabels(machine)
					other["com.strifira.agent-run-id"] = uuid.NewString()
					cleanup := machineLabels(machine)
					cleanup["com.strifira.agent-run-id"], cleanup["com.strifira.service"] = run.ID.String(), "git-cleanup"
					items = append(items, container.Summary{ID: "other-run", Labels: other}, container.Summary{ID: marker.ID, Labels: marker.Config.Labels}, container.Summary{ID: "cleanup", Labels: cleanup})
					require.NoError(t, json.NewEncoder(w).Encode(items))
				case r.Method == http.MethodGet && strings.HasSuffix(path, "/json"):
					id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json")
					inspection, ok := inspections[id]
					require.True(t, ok, "foreign/cleanup/record must never be a cancellation target")
					if deleted[id] || mode == "already-gone" && id == "agent" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					require.NoError(t, json.NewEncoder(w).Encode(inspection))
				case r.Method == http.MethodDelete:
					id := strings.TrimPrefix(path, "/containers/")
					require.Contains(t, inspections, id)
					deleted[id] = true
					require.True(t, r.URL.Query().Get("force") == "1" || r.URL.Query().Get("force") == "true")
					require.NotEqual(t, "1", r.URL.Query().Get("v"))
					require.NotEqual(t, "true", r.URL.Query().Get("v"))
					if mode == "delete-error" && id == "agent" {
						w.WriteHeader(http.StatusInternalServerError)
					} else {
						w.WriteHeader(http.StatusNoContent)
					}
				default:
					t.Errorf("unexpected daemon request: %s %s", r.Method, r.URL.String())
					w.WriteHeader(http.StatusInternalServerError)
				}
			})
			runtime := &DockerRuntime{client: api}
			err := runtime.CancelAgent(t.Context(), machine, run)
			if mode == "delete-error" || mode == "foreign-target" || mode == "changed-inspection" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.True(t, markerPresent)
			for _, id := range []string{"git-verification", "workspace-verification", "git-helper", "git-checkout", "source-sync"} {
				require.True(t, deleted[id], "all owned helpers must be attempted even after another target refuses or fails")
			}
			require.Equal(t, mode != "foreign-target" && mode != "changed-inspection" && mode != "already-gone", deleted["agent"])
			// A new runtime has no in-memory state, but the cancellation is durable.
			restarted := &DockerRuntime{client: api}
			require.ErrorIs(t, restarted.requireAgentRunActive(t.Context(), machine, run.ID), context.Canceled)
			require.NoError(t, restarted.recordAgentCancellation(t.Context(), machine, run.ID), "repeated cancellation retains its original record")
		})
	}
}

func TestAgentCancellationFenceRejectsRacingCreateBeforeStart(t *testing.T) {
	machine, options, _, runID := reservationFixture(t, false)
	var markerPresent bool
	var creates, starts, removes atomic.Int32
	api := cancellationFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := strings.TrimPrefix(r.URL.Path, "/v1.47")
		switch {
		case path == "/containers/"+agentCancellationName(machine, runID)+"/json":
			if !markerPresent {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			require.NoError(t, json.NewEncoder(w).Encode(fixtureCancellationRecord(machine, runID)))
		case path == "/containers/"+options.Name+"/json":
			w.WriteHeader(http.StatusNotFound)
		case path == "/containers/create":
			// Cancellation has already completed while a prior create was in flight.
			// Its late response must cause owned removal, with no attach/start.
			creates.Add(1)
			markerPresent = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Id":"late-created-id"}`))
		case path == "/containers/late-created-id" && r.Method == http.MethodDelete:
			removes.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(path, "/start"):
			starts.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	runtime := &DockerRuntime{client: api}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// Reservations require a bounded execution context.
	ctx, deadlineCancel := context.WithTimeout(ctx, repositoryHelperTimeout)
	defer deadlineCancel()
	execution, err := runtime.executeRepositoryContainer(ctx, machine, options, "", repositoryContainerHooks{})
	require.Nil(t, execution)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualValues(t, 1, creates.Load())
	require.EqualValues(t, 1, removes.Load())
	require.Zero(t, starts.Load())
	execution, err = runtime.executeRepositoryContainer(ctx, machine, options, "", repositoryContainerHooks{})
	require.Nil(t, execution)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualValues(t, 1, creates.Load(), "an acknowledged cancellation must refuse future creates")
}

func TestAgentCancellationRejectsUnrelatedAuthority(t *testing.T) {
	machine := fixtureHelperMachine()
	runID := uuid.New()
	record := fixtureCancellationRecord(machine, runID)
	raw, err := json.Marshal(record)
	require.NoError(t, err)
	for _, mode := range []string{"foreign-machine", "foreign-workspace", "wrong-run", "wrong-name", "wrong-version", "running", "mount", "writable", "mutable-image", "missing-config", "missing-host", "missing-state"} {
		t.Run(mode, func(t *testing.T) {
			var record container.InspectResponse
			require.NoError(t, json.Unmarshal(raw, &record))
			switch mode {
			case "foreign-machine":
				record.Config.Labels["com.strifira.machine-id"] = uuid.NewString()
			case "foreign-workspace":
				record.Config.Labels["com.strifira.workspace-id"] = uuid.NewString()
			case "wrong-run":
				record.Config.Labels["com.strifira.agent-run-id"] = uuid.NewString()
			case "wrong-name":
				record.Name = "/foreign"
			case "wrong-version":
				record.Config.Labels["com.strifira.cancellation-version"] = ""
			case "running":
				record.State.Running = true
			case "mount":
				record.HostConfig.Binds = []string{"/:/host"}
			case "writable":
				record.HostConfig.ReadonlyRootfs = false
			case "mutable-image":
				record.Config.Image = "mutable:latest"
			case "missing-config":
				record.Config = nil
			case "missing-host":
				record.HostConfig = nil
			case "missing-state":
				record.State = nil
			}
			require.Error(t, validateAgentCancellation(record, machine, runID))
		})
	}
}

func TestCandidateVerificationCleanupWaitsForConcurrentRemoval(t *testing.T) {
	for _, mode := range []string{"removed", "conflict-removed", "exited", "created", "running", "paused", "restarting", "dead", "nil-state", "wrong-id", "inspect-error", "stop-error", "removal-timeout"} {
		t.Run(mode, func(t *testing.T) {
			var inspections atomic.Int32
			api := cancellationFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				path := strings.TrimPrefix(r.URL.Path, "/v1.47")
				if path == "/containers/verification/stop" {
					if mode == "stop-error" {
						w.WriteHeader(http.StatusInternalServerError)
					} else if mode == "conflict-removed" {
						w.WriteHeader(http.StatusConflict)
					} else {
						w.WriteHeader(http.StatusNoContent)
					}
					return
				}
				require.Equal(t, "/containers/verification/json", path)
				n := inspections.Add(1)
				state := &container.State{Status: container.StateExited}
				id := "verification"
				switch mode {
				case "removed", "conflict-removed":
					if n == 1 {
						state.Status = container.StateRemoving
					} else {
						w.WriteHeader(http.StatusNotFound)
						return
					}
				case "created":
					state.Status = container.StateCreated
				case "running":
					state.Status = container.StateRunning
					state.Running = true
				case "paused":
					state.Status = container.StatePaused
					state.Paused = true
				case "restarting":
					state.Status = container.StateRestarting
					state.Restarting = true
				case "dead":
					state.Status = container.StateDead
				case "nil-state":
					state = nil
				case "wrong-id":
					id = "other"
				case "inspect-error":
					w.WriteHeader(http.StatusInternalServerError)
					return
				case "removal-timeout":
					state.Status = container.StateRemoving
				}
				require.NoError(t, json.NewEncoder(w).Encode(container.InspectResponse{ID: id, State: state}))
			})
			runtime := &DockerRuntime{client: api}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if mode == "removal-timeout" {
				var shortCancel context.CancelFunc
				ctx, shortCancel = context.WithTimeout(ctx, 10*time.Millisecond)
				defer shortCancel()
			}
			allowed, err := runtime.waitCandidateVerificationStopped(ctx, "verification")
			if mode == "removed" || mode == "conflict-removed" || mode == "exited" {
				require.NoError(t, err)
				require.True(t, allowed)
			} else if mode == "created" {
				require.NoError(t, err)
				require.False(t, allowed)
			} else {
				require.Error(t, err)
				require.False(t, allowed)
			}
			if mode == "removed" || mode == "conflict-removed" {
				require.EqualValues(t, 2, inspections.Load(), "cleanup must wait until removal completes")
			}
		})
	}
}
