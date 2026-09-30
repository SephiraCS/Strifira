package devboxworker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeAPI struct {
	mu           sync.Mutex
	start        LeaseState
	renew        LeaseState
	renewError   error
	startDelay   time.Duration
	renewDelay   time.Duration
	logErrorOnce bool
	logs         []LogEntry
	completed    []Result
	lease        *Lease
	heartbeats   int
}

func (a *fakeAPI) Heartbeat(context.Context, Heartbeat) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.heartbeats++
	return nil
}
func (a *fakeAPI) Claim(context.Context) (*Lease, error) { return a.lease, nil }
func (a *fakeAPI) Start(context.Context, string, string) (LeaseState, error) {
	time.Sleep(a.startDelay)
	return a.start, nil
}
func (a *fakeAPI) Renew(context.Context, string, string) (LeaseState, error) {
	time.Sleep(a.renewDelay)
	return a.renew, a.renewError
}
func (a *fakeAPI) Log(_ context.Context, _ string, log LogEntry) (LogResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.logs = append(a.logs, log)
	if a.logErrorOnce {
		a.logErrorOnce = false
		return LogResponse{}, errors.New("ambiguous connection failure")
	}
	return LogResponse{Accepted: true}, nil
}
func (a *fakeAPI) Complete(_ context.Context, _, _ string, result Result) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.completed = append(a.completed, result)
	return nil
}

type executorFunc func(context.Context, Job, func(string, string)) Result

func (f executorFunc) Execute(ctx context.Context, job Job, log func(string, string)) Result {
	return f(ctx, job, log)
}

func leasedWorker(executor Executor) (*Worker, *fakeAPI, Lease) {
	now := time.Now().UTC()
	state := LeaseState{Status: "running", ServerTime: now, ExpiresAt: now.Add(3 * time.Second), DeadlineAt: now.Add(time.Minute)}
	lease := Lease{Job: Job{ID: uuid.NewString(), MaxRuntimeSeconds: 30}, Token: "immutable-lease", HeartbeatIntervalSeconds: 1}
	api := &fakeAPI{start: state, renew: state}
	return &Worker{API: api, Executor: executor, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, api, lease
}

func TestLeaseLossStopsExecutionAndDoesNotPublishCompletion(t *testing.T) {
	stopped := make(chan struct{})
	worker, api, lease := leasedWorker(executorFunc(func(ctx context.Context, _ Job, _ func(string, string)) Result {
		<-ctx.Done()
		close(stopped)
		return Result{Status: "succeeded"}
	}))
	api.renewError = &HTTPError{Status: 409}
	require.ErrorIs(t, worker.RunLease(t.Context(), lease), ErrLeaseLost)
	select {
	case <-stopped:
	default:
		t.Fatal("executor did not stop")
	}
	require.Empty(t, api.completed)
}

func TestRemoteCancellationAndRuntimeStopBeforeCompletion(t *testing.T) {
	for _, scenario := range []string{"remote cancellation", "runtime", "shutdown"} {
		t.Run(scenario, func(t *testing.T) {
			stopped := false
			worker, api, lease := leasedWorker(executorFunc(func(ctx context.Context, _ Job, _ func(string, string)) Result {
				<-ctx.Done()
				stopped = true
				return Result{Status: "succeeded"}
			}))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			expected := "cancelled"
			switch scenario {
			case "remote cancellation":
				api.renew.CancelRequested = true
			case "runtime":
				lease.Job.MaxRuntimeSeconds = 1
				expected = "timeout"
			case "shutdown":
				time.AfterFunc(50*time.Millisecond, cancel)
			}
			require.NoError(t, worker.RunLease(ctx, lease))
			require.True(t, stopped)
			require.Len(t, api.completed, 1)
			require.Equal(t, expected, api.completed[0].Status)
		})
	}
}

func TestLogRetryKeepsSequenceAndImmutableLease(t *testing.T) {
	worker, api, lease := leasedWorker(executorFunc(func(_ context.Context, _ Job, log func(string, string)) Result {
		log("stdout", "first\n")
		log("stderr", "second\n")
		return Result{Status: "succeeded", ExitCode: 0, TestStatus: "passed"}
	}))
	api.logErrorOnce = true
	require.NoError(t, worker.RunLease(t.Context(), lease))
	require.Len(t, api.logs, 3)
	require.Equal(t, api.logs[0], api.logs[1], "ambiguous log retry must be identical")
	require.Equal(t, int64(1), api.logs[0].Sequence)
	require.Equal(t, int64(2), api.logs[2].Sequence)
	for _, entry := range api.logs {
		require.Equal(t, lease.Token, entry.LeaseToken)
	}
	require.Len(t, api.completed, 1)
}

func TestOnceIdleDoesNotExecute(t *testing.T) {
	worker, api, _ := leasedWorker(executorFunc(func(context.Context, Job, func(string, string)) Result {
		t.Fatal("idle worker executed a job")
		return Result{}
	}))
	require.NoError(t, worker.Run(t.Context(), true))
	require.Equal(t, 1, api.heartbeats)
}

func TestLeaseExpiredInTransitNeverStartsExecution(t *testing.T) {
	worker, api, lease := leasedWorker(executorFunc(func(context.Context, Job, func(string, string)) Result {
		t.Fatal("expired lease invoked executor")
		return Result{}
	}))
	api.start.ExpiresAt = api.start.ServerTime.Add(1100 * time.Millisecond)
	api.startDelay = 150 * time.Millisecond
	require.ErrorIs(t, worker.RunLease(t.Context(), lease), ErrLeaseLost)
	require.Empty(t, api.completed)
}

func TestRuntimeExpiredInTransitNeverStartsExecution(t *testing.T) {
	worker, api, lease := leasedWorker(executorFunc(func(context.Context, Job, func(string, string)) Result {
		t.Fatal("expired runtime invoked executor")
		return Result{}
	}))
	api.start.DeadlineAt = api.start.ServerTime.Add(100 * time.Millisecond)
	api.startDelay = 150 * time.Millisecond
	require.NoError(t, worker.RunLease(t.Context(), lease))
	require.Len(t, api.completed, 1)
	require.Equal(t, "timeout", api.completed[0].Status)
}

func TestLateRenewDoesNotExtendExpiredServerLease(t *testing.T) {
	worker, api, lease := leasedWorker(executorFunc(func(ctx context.Context, _ Job, _ func(string, string)) Result { <-ctx.Done(); return Result{} }))
	api.renew.ExpiresAt = api.renew.ServerTime.Add(1100 * time.Millisecond)
	api.renewDelay = 150 * time.Millisecond
	require.ErrorIs(t, worker.RunLease(t.Context(), lease), ErrLeaseLost)
	require.Empty(t, api.completed)
}
