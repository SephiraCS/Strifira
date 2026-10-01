package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

func fixtureLayoutRecord(machine *domain.DevMachine) container.InspectResponse {
	labels := machineLabels(machine)
	labels["com.ku.service"] = "workspace-layout"
	labels["com.ku.layout-version"] = workspaceLayoutVersion
	labels["com.ku.layout-volume"] = *machine.WorkspaceVolumeName
	labels["com.ku.layout-key"] = uuid.NewString()
	return container.InspectResponse{ID: "record", Config: &container.Config{Labels: labels}, HostConfig: &container.HostConfig{}, State: &container.State{Status: container.StateCreated}}
}

func TestWorkspaceLayoutRecordAuthority(t *testing.T) {
	machine := fixtureHelperMachine()
	for _, mode := range []string{"valid", "missing-config", "missing-host", "foreign", "wrong-volume", "old-version", "invalid-key", "nil-key", "noncanonical-key", "mount", "bind", "image-volume"} {
		t.Run(mode, func(t *testing.T) {
			record := fixtureLayoutRecord(machine)
			switch mode {
			case "missing-config":
				record.Config = nil
			case "missing-host":
				record.HostConfig = nil
			case "foreign":
				record.Config.Labels["com.ku.machine-id"] = uuid.NewString()
			case "wrong-volume":
				record.Config.Labels["com.ku.layout-volume"] = "other"
			case "old-version":
				record.Config.Labels["com.ku.layout-version"] = "1"
			case "invalid-key":
				record.Config.Labels["com.ku.layout-key"] = "marker"
			case "nil-key":
				record.Config.Labels["com.ku.layout-key"] = uuid.Nil.String()
			case "noncanonical-key":
				record.Config.Labels["com.ku.layout-key"] = strings.ToUpper(record.Config.Labels["com.ku.layout-key"])
			case "mount":
				record.HostConfig.Mounts = []mount.Mount{{Source: "volume"}}
			case "bind":
				record.HostConfig.Binds = []string{"volume:/workspace"}
			case "image-volume":
				record.Config.Volumes = map[string]struct{}{"/data": {}}
			}
			key, err := validateWorkspaceLayoutRecord(record, machine, *machine.WorkspaceVolumeName)
			if mode == "valid" {
				require.NoError(t, err)
				require.Equal(t, record.Config.Labels["com.ku.layout-key"], key)
			} else {
				require.Error(t, err)
				require.Empty(t, key)
			}
		})
	}
}

func TestWorkspaceLayoutServiceMountPolicies(t *testing.T) {
	runtime := &DockerRuntime{}
	machine := fixtureHelperMachine()
	for _, service := range []string{"ide", "agent", "collector", "terminal", "browser", "egress"} {
		t.Run(service, func(t *testing.T) {
			config := runtime.secureHostConfig(machine, service, "network", "volume")
			require.True(t, serviceWorkspaceMountsMatch(config, service, "volume"))
			if len(config.Mounts) == 0 {
				config.Mounts = []mount.Mount{{Type: mount.TypeVolume, Source: "volume", Target: "/leak"}}
				require.False(t, serviceWorkspaceMountsMatch(config, service, "volume"))
				return
			}
			require.Len(t, config.Mounts, 2)
			require.True(t, config.Mounts[1].ReadOnly)
			config.Mounts[1].ReadOnly = false
			require.False(t, serviceWorkspaceMountsMatch(config, service, "volume"))
			config.Mounts[1].ReadOnly = true
			config.Mounts[0].ReadOnly = !config.Mounts[0].ReadOnly
			require.False(t, serviceWorkspaceMountsMatch(config, service, "volume"))
			config.Mounts[0].ReadOnly = !config.Mounts[0].ReadOnly
			config.Mounts = append(config.Mounts, mount.Mount{Type: mount.TypeVolume, Source: "volume", Target: "/leak"})
			require.False(t, serviceWorkspaceMountsMatch(config, service, "volume"))
		})
	}
}

func TestWorkspaceLayoutDaemonBoundaries(t *testing.T) {
	for _, mode := range []string{"ready", "migrate", "invalid-marker", "wait-failed", "start-failed", "create-lost", "foreign-create", "cleanup-failed", "cancel", "legacy-stopped", "legacy-running", "legacy-paused", "legacy-foreign", "legacy-restarted", "modern-running-missing", "modern-running-ready", "foreign-modern", "helper-running-ready"} {
		t.Run(mode, func(t *testing.T) {
			machine := fixtureHelperMachine()
			record := fixtureLayoutRecord(machine)
			var mu sync.Mutex
			var modes []string
			var removed []string
			var createdConfig container.Config
			var createdHost container.HostConfig
			var legacyInspects int
			consumerPresent := strings.HasPrefix(mode, "legacy-") || strings.HasPrefix(mode, "modern-") || mode == "foreign-modern" || mode == "helper-running-ready"
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				path := strings.TrimPrefix(req.URL.Path, "/v1.47")
				switch {
				case path == "/images/collector/json":
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": fixtureHelperImageID})
				case path == "/volumes/fixture-volume":
					options, err := workspaceVolumeCreateOptions("fixture-volume", machineLabels(machine), machine.DiskGB)
					require.NoError(t, err)
					_ = json.NewEncoder(w).Encode(map[string]any{"Name": options.Name, "Driver": options.Driver, "Labels": options.Labels, "Options": options.DriverOpts})
				case path == "/containers/"+workspaceLayoutName(machine)+"/json":
					if mode == "migrate" && len(modes) == 0 {
						http.Error(w, `{"message":"absent"}`, http.StatusNotFound)
					} else {
						_ = json.NewEncoder(w).Encode(record)
					}
				case path == "/containers/json":
					items := []map[string]any{}
					if consumerPresent {
						labels := machineLabels(machine)
						if mode == "legacy-foreign" || mode == "foreign-modern" {
							labels["com.ku.machine-id"] = uuid.NewString()
						}
						items = append(items, map[string]any{"Id": "consumer", "Labels": labels, "Mounts": []map[string]any{{"Name": "fixture-volume"}}})
					}
					_ = json.NewEncoder(w).Encode(items)
				case path == "/containers/consumer/json":
					legacyInspects++
					labels := machineLabels(machine)
					host := &container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: "fixture-volume", Target: "/workspace"}}}
					state := &container.State{Status: container.StateExited}
					if mode == "legacy-foreign" || mode == "foreign-modern" {
						labels["com.ku.machine-id"] = uuid.NewString()
					}
					if strings.HasPrefix(mode, "modern-") || mode == "foreign-modern" || mode == "helper-running-ready" {
						host = (&DockerRuntime{}).secureHostConfig(machine, "ide", "network", "fixture-volume")
					}
					config := &container.Config{Labels: labels}
					if mode == "helper-running-ready" {
						host.Mounts[0].ReadOnly, host.Mounts[1].ReadOnly = true, false
						config.Image, config.User = fixtureHelperImageID, "1000:1000"
						labels["com.ku.service"], labels["com.ku.repository-operation"] = "git-helper", uuid.NewString()
					}
					state.Running = mode == "legacy-running" || strings.HasPrefix(mode, "modern-running") || mode == "helper-running-ready" || (mode == "legacy-restarted" && legacyInspects > 1)
					state.Paused = mode == "legacy-paused"
					_ = json.NewEncoder(w).Encode(container.InspectResponse{ID: "consumer", Config: config, HostConfig: host, State: state})
				case path == "/containers/consumer" && req.Method == http.MethodDelete:
					require.NotEqual(t, "1", req.URL.Query().Get("force"))
					consumerPresent = false
					removed = append(removed, "consumer")
					w.WriteHeader(http.StatusNoContent)
				case path == "/containers/create":
					var body struct {
						container.Config
						HostConfig container.HostConfig
					}
					require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
					if body.Labels["com.ku.service"] == "workspace-layout" {
						require.Empty(t, body.HostConfig.Mounts)
						require.Empty(t, body.Volumes)
						record.Config = &body.Config
						record.HostConfig = &body.HostConfig
						w.WriteHeader(http.StatusCreated)
						_, _ = io.WriteString(w, `{"Id":"record"}`)
						return
					}
					createdConfig, createdHost = body.Config, body.HostConfig
					operationMode := body.Entrypoint[len(body.Entrypoint)-1]
					modes = append(modes, operationMode)
					require.Equal(t, fixtureHelperImageID, body.Image)
					require.Equal(t, "0:0", body.User)
					require.Equal(t, "none", string(body.HostConfig.NetworkMode))
					require.True(t, body.HostConfig.ReadonlyRootfs)
					require.EqualValues(t, 32, *body.HostConfig.PidsLimit)
					require.EqualValues(t, 64*1024*1024, body.HostConfig.Memory)
					require.Equal(t, body.HostConfig.Memory, body.HostConfig.MemorySwap)
					require.Equal(t, []string{"ALL"}, body.HostConfig.CapDrop)
					require.Equal(t, []string{"no-new-privileges=true"}, body.HostConfig.SecurityOpt)
					require.Len(t, body.HostConfig.Mounts, 1)
					require.Equal(t, operationMode == "verify", body.HostConfig.Mounts[0].ReadOnly)
					if operationMode == "verify" {
						require.Empty(t, body.HostConfig.CapAdd)
					} else {
						require.ElementsMatch(t, []string{"CAP_CHOWN", "CAP_FOWNER", "CAP_DAC_OVERRIDE"}, body.HostConfig.CapAdd)
					}
					if mode == "create-lost" || mode == "foreign-create" {
						http.Error(w, `{"message":"lost create response"}`, http.StatusInternalServerError)
					} else {
						w.WriteHeader(http.StatusCreated)
						_, _ = io.WriteString(w, `{"Id":"initializer"}`)
					}
				case strings.HasPrefix(path, "/containers/ku-helper-fixture-layout-init-") && strings.HasSuffix(path, "/json"):
					config := createdConfig
					config.Labels = maps.Clone(config.Labels)
					if mode == "foreign-create" {
						config.Labels["com.ku.machine-id"] = uuid.NewString()
					}
					_ = json.NewEncoder(w).Encode(container.InspectResponse{ID: "initializer", Config: &config, HostConfig: &createdHost})
				case path == "/containers/initializer/start":
					if mode == "start-failed" {
						http.Error(w, `{"message":"cannot start"}`, http.StatusInternalServerError)
						return
					}
					w.WriteHeader(http.StatusNoContent)
					if mode == "cancel" {
						cancel()
					}
				case path == "/containers/initializer/wait":
					if mode == "wait-failed" {
						http.Error(w, `{"message":"wait failed"}`, http.StatusInternalServerError)
						return
					}
					status := 0
					if createdConfig.Entrypoint[len(createdConfig.Entrypoint)-1] == "verify" && (mode == "migrate" || mode == "legacy-stopped" || mode == "cleanup-failed" || mode == "modern-running-missing" || mode == "foreign-modern") {
						status = 76
					}
					if mode == "invalid-marker" {
						status = 1
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"StatusCode": status})
				case path == "/containers/initializer/logs":
					writeHelperFrame(w, 2, "invalid layout")
				case path == "/containers/initializer" && req.Method == http.MethodDelete:
					removed = append(removed, "initializer")
					if mode == "cleanup-failed" {
						http.Error(w, `{"message":"cannot remove"}`, http.StatusInternalServerError)
					} else {
						w.WriteHeader(http.StatusNoContent)
					}
				default:
					t.Errorf("unexpected daemon request %s %s", req.Method, path)
					http.NotFound(w, req)
				}
			}))
			t.Cleanup(server.Close)
			api, err := client.NewClientWithOpts(client.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")), client.WithAPIVersion("1.47"))
			require.NoError(t, err)
			t.Cleanup(func() { _ = api.Close() })
			err = (&DockerRuntime{client: api}).prepareProtectedWorkspaceVolume(ctx, machine, "fixture-volume", "collector")
			mu.Lock()
			defer mu.Unlock()
			switch mode {
			case "ready", "modern-running-ready", "helper-running-ready":
				require.NoError(t, err)
				require.Equal(t, []string{"verify"}, modes)
			case "migrate", "legacy-stopped":
				require.NoError(t, err)
				require.Equal(t, []string{"verify", "migrate"}, modes)
			default:
				require.Error(t, err)
				require.NotContains(t, modes, "migrate", "only a missing ready marker and a quiescent volume permit writes")
			}
			if mode == "cancel" {
				require.ErrorIs(t, err, context.Canceled)
			}
			if mode == "cleanup-failed" || mode == "foreign-create" {
				require.ErrorIs(t, err, errWorkspaceLayoutCleanup)
			}
			if strings.HasPrefix(mode, "legacy-") && mode != "legacy-stopped" || mode == "foreign-create" {
				require.Empty(t, removed, fmt.Sprint(removed))
			}
		})
	}
}

func TestWorkspaceLayoutTeardownPreservesAuthorityUntilVolumeRemoval(t *testing.T) {
	for _, mode := range []string{"success", "volume-failed", "foreign-record"} {
		t.Run(mode, func(t *testing.T) {
			machine := fixtureHelperMachine()
			record := fixtureLayoutRecord(machine)
			record.Name = "/" + workspaceLayoutName(machine)
			if mode == "foreign-record" {
				record.Config.Labels["com.ku.layout-volume"] = "foreign-volume"
			}
			var mu sync.Mutex
			var removals []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				path := strings.TrimPrefix(req.URL.Path, "/v1.47")
				switch {
				case path == "/containers/json":
					_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": record.ID, "Labels": record.Config.Labels}})
				case path == "/containers/record/json", path == "/containers/"+workspaceLayoutName(machine)+"/json":
					_ = json.NewEncoder(w).Encode(record)
				case path == "/networks/fixture-network/disconnect":
					w.WriteHeader(http.StatusNoContent)
				case path == "/networks/fixture-network":
					http.Error(w, `{"message":"absent"}`, http.StatusNotFound)
				case path == "/volumes/fixture-volume" && req.Method == http.MethodGet:
					_ = json.NewEncoder(w).Encode(map[string]any{"Name": "fixture-volume", "Labels": machineLabels(machine)})
				case path == "/volumes/fixture-volume" && req.Method == http.MethodDelete:
					removals = append(removals, "volume")
					if mode == "volume-failed" {
						http.Error(w, `{"message":"volume is still in use"}`, http.StatusConflict)
					} else {
						w.WriteHeader(http.StatusNoContent)
					}
				case path == "/containers/record" && req.Method == http.MethodDelete:
					removals = append(removals, "record")
					require.NotEqual(t, "1", req.URL.Query().Get("force"))
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected teardown request %s %s", req.Method, path)
					http.NotFound(w, req)
				}
			}))
			t.Cleanup(server.Close)
			api, err := client.NewClientWithOpts(client.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")), client.WithAPIVersion("1.47"))
			require.NoError(t, err)
			t.Cleanup(func() { _ = api.Close() })
			err = (&DockerRuntime{client: api}).Teardown(t.Context(), machine, nil)
			mu.Lock()
			defer mu.Unlock()
			switch mode {
			case "success":
				require.NoError(t, err)
				require.Equal(t, []string{"volume", "record"}, removals)
			case "volume-failed":
				require.Error(t, err)
				require.Equal(t, []string{"volume"}, removals)
			case "foreign-record":
				require.Error(t, err)
				require.Empty(t, removals)
			}
		})
	}
}
