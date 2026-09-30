package machine

import (
	"bytes"
	"context"
	"io"

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
