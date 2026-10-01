package machine

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

func TestInputExecutionCannotReportSuccessAfterAmbiguousAttachment(t *testing.T) {
	for _, attachFailure := range []bool{false, true} {
		name := "input/" + map[bool]string{false: "eof-while-running", true: "attach-failure"}[attachFailure]
		t.Run(name, func(t *testing.T) {
			var cleanupCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/containers/developer/exec"):
					var body struct{ Cmd []string }
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						http.Error(w, "invalid fixture request", http.StatusBadRequest)
						return
					}
					id := "command"
					if len(body.Cmd) > 2 && body.Cmd[2] == cancelWorkspaceCommandScript {
						cleanupCalls.Add(1)
						id = "cleanup"
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(map[string]string{"Id": id})
				case strings.HasSuffix(r.URL.Path, "/exec/command/start"), strings.HasSuffix(r.URL.Path, "/exec/cleanup/start"):
					primary := strings.HasSuffix(r.URL.Path, "/exec/command/start")
					_, _ = io.Copy(io.Discard, r.Body)
					if primary && attachFailure {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = io.WriteString(w, `{"message":"ambiguous attachment failure"}`)
						return
					}
					conn, buffer, err := w.(http.Hijacker).Hijack()
					if err != nil {
						return
					}
					defer conn.Close()
					_, _ = buffer.WriteString("HTTP/1.1 101 UPGRADED\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
					_ = buffer.Flush()
					if primary {
						_, _ = io.Copy(io.Discard, conn)
					}
					// Attachment EOF deliberately precedes authoritative exit.
				case strings.HasSuffix(r.URL.Path, "/exec/command/json"), strings.HasSuffix(r.URL.Path, "/exec/cleanup/json"):
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"Running": strings.HasSuffix(r.URL.Path, "/exec/command/json"), "ExitCode": 0})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			api, err := client.NewClientWithOpts(client.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")), client.WithAPIVersion("1.47"))
			require.NoError(t, err)
			defer api.Close()
			runtime := &DockerRuntime{client: api}
			err = runtime.execWithInput(t.Context(), "developer", []string{"/bin/sh", "-c", "cat >/dev/null"}, "disposable-input\n")
			require.Error(t, err)
			if !attachFailure {
				require.ErrorContains(t, err, "before execution completed")
			}
			require.EqualValues(t, 1, cleanupCalls.Load(), "an ambiguous running execution must be stopped exactly once")
		})
	}
}
