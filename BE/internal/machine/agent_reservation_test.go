package machine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func reservationFixture(t *testing.T, sourceSync bool) (*domain.DevMachine, client.ContainerCreateOptions, container.InspectResponse, uuid.UUID) {
	t.Helper()
	machine := fixtureHelperMachine()
	run := uuid.New()
	var options client.ContainerCreateOptions
	var err error
	if sourceSync {
		options, err = repositoryHelperOptions(machine, fixtureHelperImageID, uuid.NewString(), []string{"true"})
		options.Name = "strifira-" + machine.RoutingKey + "-source-sync-" + run.String()
		options.Config.Labels["com.strifira.source-sync-run"] = run.String()
		options.HostConfig.NetworkMode = "none"
		options.HostConfig.Mounts[0].ReadOnly = false
		options.NetworkingConfig = nil
	} else {
		checkout := &domain.DevMachineCheckout{ID: uuid.New(), RepositoryURL: "https://git.example.test/owner/repo", RepositoryFullName: "owner/repo", BaseBranch: "main", WorkingBranch: "strifira/task", WorkspacePath: "/workspace/repository"}
		candidate := RepositoryCandidate{GitState: GitState{Branch: checkout.WorkingBranch}, Commit: strings.Repeat("a", 40)}
		options, err = candidateVerificationOptions(machine, fixtureHelperImageID, uuid.NewString(), checkout, run, candidate, []string{"true"})
	}
	require.NoError(t, err)
	// A serialization boundary makes the abandoned container independent from
	// the retry, matching Docker inspection rather than shared Go pointers.
	encoded, err := json.Marshal(container.InspectResponse{ID: "old-immutable-id", Name: "/" + options.Name, Config: options.Config, HostConfig: options.HostConfig, State: &container.State{Status: container.StateExited}})
	require.NoError(t, err)
	var old container.InspectResponse
	require.NoError(t, json.Unmarshal(encoded, &old))
	old.Config.Labels["com.strifira.repository-operation"] = uuid.NewString()
	old.Config.Labels[repositoryReservationVersionLabel] = "1"
	old.Config.Labels[repositoryReservationDeadlineLabel] = strconv.FormatInt(time.Now().Add(-time.Minute).UnixNano(), 10)
	return machine, options, old, run
}

func TestRepositoryReservationRecoveryPolicy(t *testing.T) {
	for _, sourceSync := range []bool{false, true} {
		kind := "verification"
		if sourceSync {
			kind = "source-sync"
		}
		t.Run(kind, func(t *testing.T) {
			for _, mode := range []string{"exited", "created", "running", "paused", "restarting", "dead", "removing", "unexpired", "grace", "missing-deadline", "missing-version", "invalid-operation", "foreign-machine", "foreign-workspace", "foreign-routing", "wrong-name", "missing-id", "wrong-run", "wrong-image", "wrong-user", "wrong-directory", "wrong-candidate", "wrong-mount", "writable-root", "privileged", "cap-added", "cap-policy", "security-policy", "bind", "volumes-from", "network", "tmpfs", "nil-state", "nil-config", "nil-host"} {
				t.Run(mode, func(t *testing.T) {
					machine, options, old, run := reservationFixture(t, sourceSync)
					switch mode {
					case "created":
						old.State.Status = container.StateCreated
					case "running":
						old.State.Running = true
						old.State.Status = container.StateRunning
					case "paused":
						old.State.Paused = true
					case "restarting":
						old.State.Restarting = true
					case "dead":
						old.State.Status = container.StateDead
					case "removing":
						old.State.Status = container.StateRemoving
					case "unexpired":
						old.Config.Labels[repositoryReservationDeadlineLabel] = strconv.FormatInt(time.Now().Add(time.Minute).UnixNano(), 10)
					case "grace":
						old.Config.Labels[repositoryReservationDeadlineLabel] = strconv.FormatInt(time.Now().Add(-time.Second).UnixNano(), 10)
					case "missing-deadline":
						delete(old.Config.Labels, repositoryReservationDeadlineLabel)
					case "missing-version":
						delete(old.Config.Labels, repositoryReservationVersionLabel)
					case "invalid-operation":
						old.Config.Labels["com.strifira.repository-operation"] = "invalid"
					case "foreign-machine":
						old.Config.Labels["com.strifira.machine-id"] = uuid.NewString()
					case "foreign-workspace":
						old.Config.Labels["com.strifira.workspace-id"] = uuid.NewString()
					case "foreign-routing":
						old.Config.Labels["com.strifira.routing-key"] = "other"
					case "wrong-name":
						old.Name = "/other"
					case "missing-id":
						old.ID = ""
					case "wrong-run":
						old.Config.Labels["com.strifira.agent-run-id"] = uuid.NewString()
						old.Config.Labels["com.strifira.source-sync-run"] = uuid.NewString()
					case "wrong-image":
						old.Config.Image = "mutable:latest"
					case "wrong-user":
						old.Config.User = "0:0"
					case "wrong-directory":
						old.Config.WorkingDir = "/other"
					case "wrong-candidate":
						old.Config.Labels["com.strifira.verification-commit"] = strings.Repeat("b", 40)
					case "wrong-mount":
						old.HostConfig.Mounts[0].Source = "foreign-volume"
					case "writable-root":
						old.HostConfig.ReadonlyRootfs = false
					case "privileged":
						old.HostConfig.Privileged = true
					case "cap-added":
						old.HostConfig.CapAdd = []string{"SYS_ADMIN"}
					case "cap-policy":
						old.HostConfig.CapDrop = nil
					case "security-policy":
						old.HostConfig.SecurityOpt = nil
					case "bind":
						old.HostConfig.Binds = []string{"/:/host"}
					case "volumes-from":
						old.HostConfig.VolumesFrom = []string{"other"}
					case "network":
						old.HostConfig.NetworkMode = "host"
					case "tmpfs":
						old.HostConfig.Tmpfs = map[string]string{"/unsafe": "rw"}
					case "nil-state":
						old.State = nil
					case "nil-config":
						old.Config = nil
					case "nil-host":
						old.HostConfig = nil
					}
					err := validateExpiredRepositoryReservation(machine, options, run, old, time.Now())
					if mode == "exited" || mode == "created" {
						require.NoError(t, err)
					} else {
						require.Error(t, err)
					}
				})
			}
		})
	}
}

func TestWorkspaceVerificationReservationRecoveryPolicy(t *testing.T) {
	for _, mode := range []string{"expired", "running", "unexpired", "other-run", "other-service", "live-workspace-mount"} {
		t.Run(mode, func(t *testing.T) {
			machine, _, previous, run := reservationFixture(t, false)
			options, err := workspaceVerificationOptions(machine, fixtureHelperImageID, uuid.NewString(), run, []string{"true"})
			require.NoError(t, err)
			encoded, err := json.Marshal(container.InspectResponse{
				ID: previous.ID, Name: "/" + options.Name, Config: options.Config, HostConfig: options.HostConfig, State: previous.State,
			})
			require.NoError(t, err)
			var old container.InspectResponse
			require.NoError(t, json.Unmarshal(encoded, &old))
			old.Config.Labels["com.strifira.repository-operation"] = uuid.NewString()
			old.Config.Labels[repositoryReservationVersionLabel] = "1"
			old.Config.Labels[repositoryReservationDeadlineLabel] = strconv.FormatInt(time.Now().Add(-time.Minute).UnixNano(), 10)
			switch mode {
			case "running":
				old.State.Running = true
			case "unexpired":
				old.Config.Labels[repositoryReservationDeadlineLabel] = strconv.FormatInt(time.Now().Add(time.Minute).UnixNano(), 10)
			case "other-run":
				old.Config.Labels["com.strifira.agent-run-id"] = uuid.NewString()
			case "other-service":
				old.Config.Labels["com.strifira.service"] = "git-verification"
			case "live-workspace-mount":
				old.HostConfig.Mounts[0].VolumeOptions.Subpath = "workspace"
			}
			err = validateExpiredRepositoryReservation(machine, options, run, old, time.Now())
			if mode == "expired" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestRepositoryReservationDaemonRecovery(t *testing.T) {
	for _, sourceSync := range []bool{false, true} {
		for _, mode := range []string{"expired", "racing-start", "running", "missing", "inspect-error"} {
			t.Run(strconv.FormatBool(sourceSync)+"/"+mode, func(t *testing.T) {
				machine, options, old, _ := reservationFixture(t, sourceSync)
				if mode == "running" {
					old.State.Status = container.StateRunning
					old.State.Running = true
				}
				var removes, inspections atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					path := strings.TrimPrefix(r.URL.Path, "/v1.47")
					if r.Method == http.MethodGet && path == "/containers/"+options.Name+"/json" {
						inspections.Add(1)
						if mode == "missing" {
							w.WriteHeader(http.StatusNotFound)
							return
						}
						if mode == "inspect-error" {
							w.WriteHeader(http.StatusInternalServerError)
							return
						}
						require.NoError(t, json.NewEncoder(w).Encode(old))
						return
					}
					if r.Method == http.MethodDelete && path == "/containers/old-immutable-id" {
						removes.Add(1)
						require.NotEqual(t, "1", r.URL.Query().Get("force"))
						require.NotEqual(t, "true", r.URL.Query().Get("force"))
						require.NotEqual(t, "1", r.URL.Query().Get("v"))
						if mode == "racing-start" {
							w.WriteHeader(http.StatusConflict)
						} else {
							w.WriteHeader(http.StatusNoContent)
						}
						return
					}
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
					w.WriteHeader(http.StatusInternalServerError)
				}))
				t.Cleanup(server.Close)
				api, err := client.NewClientWithOpts(client.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")), client.WithAPIVersion("1.47"))
				require.NoError(t, err)
				t.Cleanup(func() { _ = api.Close() })
				runtime := &DockerRuntime{client: api}
				ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
				defer cancel()
				release, err := runtime.acquireRepositoryReservation(ctx, machine, &options)
				if mode == "expired" || mode == "missing" {
					require.NoError(t, err)
					defer release()
					require.NotEmpty(t, options.Config.Labels[repositoryReservationDeadlineLabel])
					second, err := runtime.acquireRepositoryReservation(ctx, machine, &options)
					require.ErrorContains(t, err, "active invocation")
					require.Nil(t, second)
					require.EqualValues(t, 1, inspections.Load(), "local overlap must not inspect or delete the reserved container")
					release()
					release()
				} else {
					require.Error(t, err)
					require.Nil(t, release)
				}
				runtime.reservationMu.Lock()
				require.Empty(t, runtime.activeReservations)
				runtime.reservationMu.Unlock()
				expected := int32(0)
				if mode == "expired" || mode == "racing-start" {
					expected = 1
				}
				require.Equal(t, expected, removes.Load())
			})
		}
	}
}
