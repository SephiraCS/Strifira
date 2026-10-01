package machine

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

const fixtureHelperImageID = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func fixtureHelperMachine() *domain.DevMachine {
	return &domain.DevMachine{ID: uuid.New(), WorkspaceID: uuid.New(), RoutingKey: "helper-fixture",
		WorkspaceVolumeName: new("fixture-volume"), DockerNetworkName: new("fixture-network"),
		DiskGB: 1, MemoryMB: 4096, CPUMillis: 4000, PidsLimit: 1024}
}

func TestRepositoryHelperRejectsLegacyWritableMounts(t *testing.T) {
	for _, subpath := range []string{"", "repositories", "workspace/..", "workspace/../repositories"} {
		config := &container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: "volume", Target: "/workspace", VolumeOptions: &mount.VolumeOptions{Subpath: subpath}}}}
		require.False(t, protectedRepositoryMounts(config, "volume", false), subpath)
	}
	workspace := mount.Mount{Type: mount.TypeVolume, Source: "volume", Target: "/workspace", VolumeOptions: &mount.VolumeOptions{Subpath: "workspace"}}
	store := mount.Mount{Type: mount.TypeVolume, Source: "volume", Target: "/repositories", ReadOnly: true, VolumeOptions: &mount.VolumeOptions{Subpath: "repositories"}}
	config := &container.HostConfig{Mounts: []mount.Mount{workspace, store}}
	require.True(t, protectedRepositoryMounts(config, "volume", false))
	config.Mounts[1].ReadOnly = false
	require.False(t, protectedRepositoryMounts(config, "volume", false), "a writable protected store cannot be trusted by its subpath alone")
	config.Mounts[0].ReadOnly = true
	require.True(t, protectedRepositoryMounts(config, "volume", true), "other backend helpers can serialize on repository flock")
	config.Binds = []string{"volume:/leak"}
	require.False(t, protectedRepositoryMounts(config, "volume", true))
	config.Binds = nil
	config.Mounts = append(config.Mounts, mount.Mount{Type: mount.TypeVolume, Source: "volume", Target: "/leak"})
	require.False(t, protectedRepositoryMounts(config, "volume", true), "all mounts, including additional aliases, must be safe")
}

// The HTTP fixture covers daemon failure boundaries, not Git behavior. Real Git
// history/publication and real Docker mount/resource acceptance have separate
// integration fixtures.
func TestRepositoryHelperDaemonBoundaries(t *testing.T) {
	t.Run("repository", func(t *testing.T) { testGitHelperDaemonBoundaries(t, false) })
	t.Run("checkout", func(t *testing.T) { testGitHelperDaemonBoundaries(t, true) })
}

func testGitHelperDaemonBoundaries(t *testing.T, checkout bool) {
	t.Helper()
	for _, mode := range []string{"success", "create-response-lost", "attach-failed", "start-failed", "early-eof", "nonzero", "stdout-error", "oversized", "cancel", "cleanup-failed", "foreign-create", "legacy-mount", "nil-consumer-config", "nil-cleanup-config", "missing-layout", "foreign-layout", "layout-not-ready"} {
		t.Run(mode, func(t *testing.T) {
			machine := fixtureHelperMachine()
			credential := "helper-disposable-secret"
			if checkout {
				credential = ""
			}
			var removeCalls, createCalls atomic.Int32
			var mu sync.Mutex
			var config container.Config
			var hostConfig container.HostConfig
			var received string
			removed := make(chan struct{})
			removeOnce := sync.OnceFunc(func() { close(removed) })
			attachedReady := make(chan struct{})
			attachOnce := sync.OnceFunc(func() { close(attachedReady) })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				path := strings.TrimPrefix(r.URL.Path, "/v1.47")
				switch {
				case path == "/images/collector/json", path == "/images/"+fixtureHelperImageID+"/json":
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": fixtureHelperImageID})
				case path == "/volumes/fixture-volume":
					options, err := workspaceVolumeCreateOptions("fixture-volume", machineLabels(machine), 1)
					if err != nil {
						t.Error(err)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Name": options.Name, "Driver": options.Driver, "Labels": options.Labels, "Options": options.DriverOpts})
				case path == "/containers/json":
					if mode == "legacy-mount" || mode == "nil-consumer-config" {
						_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": "legacy", "Mounts": []map[string]any{{"Name": "fixture-volume", "RW": true}}}})
					} else {
						_, _ = io.WriteString(w, `[]`)
					}
				case path == "/containers/legacy/json":
					if mode == "nil-consumer-config" {
						_, _ = io.WriteString(w, `{"Id":"legacy","Config":null}`)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "legacy", "Config": &container.Config{}, "HostConfig": &container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: "fixture-volume", Target: "/workspace"}}}})
				case path == "/containers/"+workspaceLayoutName(machine)+"/json":
					if mode == "missing-layout" {
						http.NotFound(w, r)
						return
					}
					record := fixtureLayoutRecord(machine)
					if mode == "foreign-layout" {
						record.Config.Labels["com.ku.machine-id"] = uuid.NewString()
					}
					_ = json.NewEncoder(w).Encode(record)
				case path == "/containers/create":
					var body struct {
						container.Config
						HostConfig container.HostConfig
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						return
					}
					if body.Labels["com.ku.service"] == "volume-init" {
						require.True(t, body.HostConfig.Mounts[0].ReadOnly)
						require.Empty(t, body.HostConfig.CapAdd)
						w.WriteHeader(http.StatusCreated)
						_, _ = io.WriteString(w, `{"Id":"layout-verifier"}`)
						return
					}
					mu.Lock()
					config, hostConfig = body.Config, body.HostConfig
					mu.Unlock()
					createCalls.Add(1)
					if mode == "create-response-lost" || mode == "foreign-create" || mode == "nil-cleanup-config" {
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = io.WriteString(w, `{"message":"create response lost"}`)
					} else {
						w.WriteHeader(http.StatusCreated)
						_, _ = io.WriteString(w, `{"Id":"helper"}`)
					}
				case strings.HasSuffix(path, "/json") && strings.HasPrefix(path, "/containers/ku-helper-fixture-git-"):
					mu.Lock()
					copiedConfig := config
					mu.Unlock()
					if mode == "nil-cleanup-config" {
						_, _ = io.WriteString(w, `{"Id":"helper","Config":null}`)
						return
					}
					if mode == "foreign-create" {
						copiedConfig.Labels = map[string]string{"com.ku.managed": "true", "com.ku.machine-id": uuid.NewString()}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "helper", "Config": copiedConfig})
				case path == "/containers/helper/attach":
					if mode == "attach-failed" {
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = io.WriteString(w, `{"message":"attachment failed"}`)
						return
					}
					conn, buffer, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					_, _ = buffer.WriteString("HTTP/1.1 101 UPGRADED\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
					_ = buffer.Flush()
					attachOnce()
					input, _ := io.ReadAll(conn)
					mu.Lock()
					received = string(input)
					mu.Unlock()
					if mode == "cancel" {
						<-removed
						return
					}
					if mode == "stdout-error" {
						writeHelperFrame(conn, stdcopy.Stdout, "object-check-failed\x00 "+credential)
					} else if mode == "oversized" {
						writeHelperFrame(conn, stdcopy.Stdout, strings.Repeat("x", maxCapturedLogBytes+1))
					} else {
						writeHelperFrame(conn, stdcopy.Stdout, "candidate-metadata\x00")
					}
					if mode == "nonzero" {
						writeHelperFrame(conn, stdcopy.Stderr, "rejected "+credential)
					}
				case path == "/containers/helper/start":
					if mode == "start-failed" {
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = io.WriteString(w, `{"message":"start failed"}`)
					} else {
						w.WriteHeader(http.StatusNoContent)
					}
				case path == "/containers/helper/json":
					state := &container.State{Status: container.StateExited}
					if mode == "early-eof" {
						state.Status, state.Running = container.StateRunning, true
					}
					if mode == "nonzero" || mode == "stdout-error" {
						state.ExitCode = 23
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "helper", "State": state})
				case path == "/containers/layout-verifier/start", path == "/containers/layout-verifier" && r.Method == http.MethodDelete:
					w.WriteHeader(http.StatusNoContent)
				case path == "/containers/layout-verifier/wait":
					status := 0
					if mode == "layout-not-ready" {
						status = 76
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"StatusCode": status})
				case path == "/containers/helper" && r.Method == http.MethodDelete:
					removeCalls.Add(1)
					removeOnce()
					if mode == "cleanup-failed" {
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = io.WriteString(w, `{"message":"remove failed"}`)
					} else {
						w.WriteHeader(http.StatusNoContent)
					}
				default:
					t.Errorf("unexpected daemon request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			api, err := client.NewClientWithOpts(client.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")), client.WithAPIVersion("1.47"))
			require.NoError(t, err)
			t.Cleanup(func() { _ = api.Close() })
			runtime := &DockerRuntime{client: api}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			if mode == "cancel" {
				go func() {
					select {
					case <-attachedReady:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			services := []domain.DevMachineService{{ServiceType: "collector", ImageRef: "collector"}}
			command := isolatedGitCommand("cat", "fixture")
			var output string
			if checkout {
				output, err = runtime.runCheckoutHelper(ctx, machine, services, command)
			} else {
				output, err = runtime.runRepositoryHelper(ctx, machine, services, command, credential, repositoryHelperInvocation{})
			}
			if mode == "success" {
				require.NoError(t, err)
				require.Equal(t, "candidate-metadata\x00", output)
			} else {
				require.Error(t, err)
				require.Empty(t, output)
			}
			if mode == "cancel" {
				require.True(t, errors.Is(err, context.Canceled), err)
			}
			if mode == "stdout-error" {
				require.ErrorContains(t, err, "object-check-failed")
				require.NotContains(t, err.Error(), "\x00", "diagnostics must remain valid persisted text")
			}
			if mode == "legacy-mount" || mode == "nil-consumer-config" || mode == "missing-layout" || mode == "foreign-layout" || mode == "layout-not-ready" {
				require.Zero(t, createCalls.Load())
				require.Zero(t, removeCalls.Load())
				return
			}
			if mode == "foreign-create" || mode == "nil-cleanup-config" {
				require.Zero(t, removeCalls.Load())
				require.ErrorContains(t, err, "ownership")
				return
			}
			require.EqualValues(t, 1, removeCalls.Load(), "cleanup must cover every create/attach/start/EOF/cancellation boundary")
			mu.Lock()
			defer mu.Unlock()
			require.Equal(t, fixtureHelperImageID, config.Image)
			require.NotContains(t, strings.Join(config.Env, " ")+strings.Join(config.Entrypoint, " "), "helper-disposable-secret")
			require.Equal(t, "/repositories", config.WorkingDir)
			require.True(t, hostConfig.ReadonlyRootfs)
			require.Equal(t, "none", hostConfig.LogConfig.Type)
			require.Equal(t, hostConfig.Memory, hostConfig.MemorySwap)
			require.Positive(t, hostConfig.Memory)
			require.LessOrEqual(t, hostConfig.Memory, int64(512*1024*1024))
			require.Equal(t, []string{"ALL"}, hostConfig.CapDrop)
			require.EqualValues(t, 64, *hostConfig.PidsLimit)
			require.Len(t, hostConfig.Mounts, 2)
			require.Equal(t, !checkout, hostConfig.Mounts[0].ReadOnly)
			require.Equal(t, "workspace", hostConfig.Mounts[0].VolumeOptions.Subpath)
			require.Equal(t, checkout, hostConfig.Mounts[1].ReadOnly)
			require.Equal(t, "repositories", hostConfig.Mounts[1].VolumeOptions.Subpath)
			if checkout {
				require.Equal(t, "git-checkout", config.Labels["com.ku.service"])
				require.Equal(t, container.NetworkMode("none"), hostConfig.NetworkMode)
				require.Empty(t, received, "checkout installer must never receive a credential")
			} else if mode == "success" {
				require.Equal(t, credential+"\n", received)
			}
			if err != nil && credential != "" {
				require.NotContains(t, err.Error(), credential)
			}
		})
	}
}

func writeHelperFrame(writer io.Writer, stream stdcopy.StdType, value string) {
	header := [8]byte{byte(stream)}
	binary.BigEndian.PutUint32(header[4:], uint32(len(value)))
	_, _ = writer.Write(header[:])
	_, _ = io.WriteString(writer, value)
}
