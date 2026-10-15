package machine

import (
	"context"
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

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

func TestCandidateExecutionDaemonBoundaries(t *testing.T) {
	for _, mode := range []string{"success", "nonzero", "prepare-failed", "attest-failed", "stage-cleanup-failed", "attach-failed", "start-failed", "early-eof", "cancel", "externally-cancelled", "cancelled-inspection-missing", "create-response-lost", "foreign-create", "nil-cleanup-config", "cleanup-failed", "oversized", "inspect-failed"} {
		t.Run(mode, func(t *testing.T) {
			machine := fixtureHelperMachine()
			checkout := &domain.DevMachineCheckout{ID: uuid.New(), RepositoryURL: "https://git.example.test/owner/repo", RepositoryFullName: "owner/repo", BaseBranch: "main", WorkingBranch: "strifira/task", WorkspacePath: "/workspace/repository"}
			candidate := RepositoryCandidate{GitState: GitState{Branch: checkout.WorkingBranch}, Commit: strings.Repeat("a", 40)}
			options, err := candidateVerificationOptions(machine, fixtureHelperImageID, uuid.NewString(), checkout, uuid.New(), candidate, []string{"/custom/tool", "test", "argument with spaces"})
			require.NoError(t, err)
			var config container.Config
			var mu sync.Mutex
			var phases []string
			phase := func(value string) { mu.Lock(); phases = append(phases, value); mu.Unlock() }
			var removes, cleanups, attestations atomic.Int32
			var externallyCancelled atomic.Bool
			removed, attached := make(chan struct{}), make(chan struct{})
			closeRemoved, closeAttached := sync.OnceFunc(func() { close(removed) }), sync.OnceFunc(func() { close(attached) })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				path := strings.TrimPrefix(r.URL.Path, "/v1.47")
				switch {
				case strings.Contains(path, "-cancel-") && r.Method == http.MethodGet:
					if externallyCancelled.Load() {
						_ = json.NewEncoder(w).Encode(fixtureCancellationRecord(machine, uuid.MustParse(options.Config.Labels["com.strifira.agent-run-id"])))
					} else {
						http.NotFound(w, r)
					}
				case path == "/containers/create":
					require.NoError(t, json.NewDecoder(r.Body).Decode(&config))
					phase("reserved")
					if mode == "create-response-lost" || mode == "foreign-create" || mode == "nil-cleanup-config" {
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = io.WriteString(w, `{"message":"create response lost"}`)
						return
					}
					w.WriteHeader(http.StatusCreated)
					_, _ = io.WriteString(w, `{"Id":"verification"}`)
				case path == "/containers/"+options.Name+"/json":
					if config.Image == "" {
						http.NotFound(w, r)
						return
					}
					if mode == "nil-cleanup-config" {
						_, _ = io.WriteString(w, `{"Id":"verification","Config":null}`)
						return
					}
					if mode == "foreign-create" {
						config.Labels["com.strifira.repository-operation"] = uuid.NewString()
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "verification", "Config": config})
				case path == "/containers/verification/attach":
					if mode == "attach-failed" {
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = io.WriteString(w, `{"message":"attachment failed"}`)
						return
					}
					conn, buffer, err := w.(http.Hijacker).Hijack()
					require.NoError(t, err)
					defer conn.Close()
					_, _ = buffer.WriteString("HTTP/1.1 101 UPGRADED\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
					_ = buffer.Flush()
					closeAttached()
					input, _ := io.ReadAll(conn)
					require.Empty(t, input, "tests must never receive repository credentials")
					if mode == "cancel" {
						<-removed
						return
					}
					if mode == "oversized" {
						writeHelperFrame(conn, stdcopy.Stdout, strings.Repeat("x", maxCapturedLogBytes+1))
					} else {
						writeHelperFrame(conn, stdcopy.Stdout, "tests completed\n")
						writeHelperFrame(conn, stdcopy.Stderr, "test diagnostic\n")
					}
				case path == "/containers/verification/start":
					phase("started")
					if mode == "start-failed" {
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = io.WriteString(w, `{"message":"start failed"}`)
					} else {
						w.WriteHeader(http.StatusNoContent)
					}
				case path == "/containers/verification/json":
					if mode == "externally-cancelled" || mode == "cancelled-inspection-missing" {
						externallyCancelled.Store(true)
					}
					if mode == "cancelled-inspection-missing" {
						http.NotFound(w, r)
						return
					}
					if mode == "inspect-failed" {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					state := &container.State{Status: container.StateExited}
					if mode == "early-eof" {
						state.Status, state.Running = container.StateRunning, true
					}
					if mode == "nonzero" {
						state.ExitCode = 23
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "verification", "State": state})
				case path == "/containers/verification" && r.Method == http.MethodDelete:
					removes.Add(1)
					phase("released")
					closeRemoved()
					if mode == "cleanup-failed" {
						w.WriteHeader(http.StatusInternalServerError)
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
					case <-attached:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			execution, err := runtime.executeRepositoryContainer(ctx, machine, options, "", repositoryContainerHooks{
				beforeStart: func(context.Context) error {
					phase("prepared")
					if mode == "prepare-failed" {
						return errors.New("preparation failed")
					}
					return nil
				},
				afterExit: func(_ context.Context, execution *CommandExecution) error {
					_, overlapErr := runtime.acquireRepositoryReservation(ctx, machine, &options)
					require.ErrorContains(t, overlapErr, "active invocation", "terminal process must retain its reservation through attestation")
					if execution.ExitCode != 0 {
						return nil
					}
					attestations.Add(1)
					phase("attested")
					if mode == "attest-failed" {
						return errors.New("attestation failed")
					}
					return nil
				},
				beforeRemove: func(context.Context, string) error {
					_, overlapErr := runtime.acquireRepositoryReservation(ctx, machine, &options)
					require.ErrorContains(t, overlapErr, "active invocation", "cleanup must finish before the local reservation releases")
					cleanups.Add(1)
					phase("cleaned")
					if mode == "stage-cleanup-failed" {
						return errors.New("stage cleanup failed")
					}
					return nil
				},
			})
			if mode == "success" || mode == "nonzero" || mode == "oversized" {
				require.NoError(t, err)
				if mode == "oversized" {
					require.Len(t, execution.Stdout, maxCapturedLogBytes)
					require.True(t, execution.StdoutTruncated)
					require.False(t, execution.StderrTruncated)
					require.EqualValues(t, 1, attestations.Load(), "truncated output still needs candidate attestation")
				} else {
					require.Equal(t, "tests completed\n", execution.Stdout)
					require.Equal(t, "test diagnostic\n", execution.Stderr)
				}
				if mode == "nonzero" {
					require.Equal(t, 23, execution.ExitCode)
					require.Zero(t, attestations.Load())
				}
			} else {
				require.Error(t, err)
				if mode == "attest-failed" {
					require.NotNil(t, execution)
				} else {
					require.Nil(t, execution)
				}
			}
			if mode == "cancel" || mode == "externally-cancelled" || mode == "cancelled-inspection-missing" {
				require.ErrorIs(t, err, context.Canceled)
				require.Zero(t, attestations.Load(), "cancelled results cannot reach attestation")
			}
			if mode == "foreign-create" || mode == "nil-cleanup-config" {
				require.Zero(t, removes.Load())
				require.Zero(t, cleanups.Load())
				return
			}
			require.EqualValues(t, 1, cleanups.Load())
			if mode == "stage-cleanup-failed" {
				require.Zero(t, removes.Load())
				return
			}
			require.EqualValues(t, 1, removes.Load())
			mu.Lock()
			defer mu.Unlock()
			if mode == "success" {
				require.Equal(t, []string{"reserved", "prepared", "started", "attested", "cleaned", "released"}, phases)
			}
		})
	}
}
