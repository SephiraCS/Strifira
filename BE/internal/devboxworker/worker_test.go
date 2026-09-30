package devboxworker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeAPI struct {
	mu                sync.Mutex
	start             LeaseState
	renew             LeaseState
	renewError        error
	startDelay        time.Duration
	renewDelay        time.Duration
	logErrorOnce      bool
	logs              []LogEntry
	completed         []Result
	lease             *Lease
	heartbeats        int
	completeErrorOnce error
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
	if a.completeErrorOnce != nil {
		err := a.completeErrorOnce
		a.completeErrorOnce = nil
		return err
	}
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

func TestCancellationAndShutdownCompletionRetriesIdenticalResult(t *testing.T) {
	for _, cause := range []error{ErrRemoteCancellation, ErrRuntimeExceeded, context.Canceled} {
		t.Run(cause.Error(), func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			worker, api, lease := leasedWorker(executorFunc(func(context.Context, Job, func(string, string)) Result {
				cancel(cause)
				return Result{Status: "succeeded"}
			}))
			api.completeErrorOnce = &HTTPError{Status: http.StatusServiceUnavailable}
			require.NoError(t, worker.RunLease(ctx, lease))
			require.Len(t, api.completed, 2)
			require.Equal(t, api.completed[0], api.completed[1])
			expected := "cancelled"
			if errors.Is(cause, ErrRuntimeExceeded) {
				expected = "timeout"
			}
			require.Equal(t, expected, api.completed[0].Status)
		})
	}
}

type shutdownAPI struct {
	*fakeAPI
	action  string
	started chan struct{}
	once    sync.Once
}

func (a *shutdownAPI) interrupted(ctx context.Context) error {
	a.once.Do(func() { close(a.started) })
	<-ctx.Done()
	return ctx.Err()
}

func (a *shutdownAPI) Renew(ctx context.Context, id, token string) (LeaseState, error) {
	if a.action == "renew" {
		return LeaseState{}, a.interrupted(ctx)
	}
	return a.fakeAPI.Renew(ctx, id, token)
}

func (a *shutdownAPI) Heartbeat(ctx context.Context, body Heartbeat) error {
	if a.action == "heartbeat" {
		return a.interrupted(ctx)
	}
	return a.fakeAPI.Heartbeat(ctx, body)
}

func (a *shutdownAPI) Log(ctx context.Context, id string, entry LogEntry) (LogResponse, error) {
	if a.action == "log" {
		return LogResponse{}, a.interrupted(ctx)
	}
	return a.fakeAPI.Log(ctx, id, entry)
}

func (a *shutdownAPI) Complete(ctx context.Context, id, token string, result Result) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(50 * time.Millisecond):
		return a.fakeAPI.Complete(ctx, id, token, result)
	}
}

func TestShutdownInterruptingLeaseOrLogRequestsStillReportsCancellation(t *testing.T) {
	for _, action := range []string{"renew", "heartbeat", "log"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			started := make(chan struct{})
			worker, api, lease := leasedWorker(executorFunc(func(_ context.Context, _ Job, log func(string, string)) Result {
				if action == "log" {
					log("stdout", "execution output")
				}
				<-started
				cancel()
				return Result{Status: "succeeded"}
			}))
			worker.API = &shutdownAPI{fakeAPI: api, action: action, started: started}
			require.NoError(t, worker.RunLease(ctx, lease))
			require.Len(t, api.completed, 1)
			require.Equal(t, "cancelled", api.completed[0].Status)
		})
	}
}

func TestShutdownCompletionFitsServiceStopWindow(t *testing.T) {
	for _, scenario := range []string{"before reporting", "during reporting"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			shutdownAt := make(chan time.Time, 1)
			shutdown := func() {
				shutdownAt <- time.Now()
				cancel()
			}
			worker, api, lease := leasedWorker(executorFunc(func(context.Context, Job, func(string, string)) Result {
				if scenario == "before reporting" {
					shutdown()
					// Subprocess cleanup consumes part of the same shutdown budget.
					time.Sleep(time.Second)
				}
				return Result{Status: "succeeded"}
			}))
			api.start.ExpiresAt = api.start.ServerTime.Add(time.Minute)
			api.renew = api.start
			var attempts atomic.Int32
			var durationsMu sync.Mutex
			var durations []time.Duration
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				started := time.Now()
				if attempts.Add(1) == 1 && scenario == "during reporting" {
					shutdown()
				}
				<-r.Context().Done()
				durationsMu.Lock()
				durations = append(durations, time.Since(started))
				durationsMu.Unlock()
			}))
			defer server.Close()
			worker.API = &completionClientAPI{fakeAPI: api, client: NewClient(Config{ServerURL: server.URL}, "worker-registration")}
			require.ErrorIs(t, worker.RunLease(ctx, lease), context.DeadlineExceeded)
			require.Less(t, time.Since(<-shutdownAt), 14*time.Second, "delivery and cleanup must leave margin within TimeoutStopSec=20s")
			server.Close() // Join handlers before inspecting completed request lifetimes.
			durationsMu.Lock()
			defer durationsMu.Unlock()
			if scenario == "before reporting" {
				require.Equal(t, int32(3), attempts.Load())
				require.Len(t, durations, 3)
				for _, duration := range durations {
					require.Less(t, duration, 4*time.Second, "new shutdown attempts have their own shorter request budget")
				}
			} else {
				require.Equal(t, int32(2), attempts.Load(), "an in-flight normal request cannot retain the normal overall delivery window")
				require.Len(t, durations, 2)
			}
		})
	}
}

type completionClientAPI struct {
	*fakeAPI
	client *Client
}

func (a *completionClientAPI) Complete(ctx context.Context, id, token string, result Result) error {
	return a.client.Complete(ctx, id, token, result)
}

func TestCompletionTransientHTTPRetriesKeepExecutionAndResultOnce(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var executed atomic.Int32
			worker, api, lease := leasedWorker(executorFunc(func(context.Context, Job, func(string, string)) Result {
				executed.Add(1)
				return Result{Status: "succeeded", Summary: "Reviewed change", Patch: "unchanged artifact", TestStatus: "passed"}
			}))
			bodies := make(chan []byte, 3)
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				bodies <- body
				if attempts.Add(1) == 1 {
					w.WriteHeader(status)
					return
				}
				if status == http.StatusServiceUnavailable {
					time.Sleep(1100 * time.Millisecond)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			worker.API = &completionClientAPI{fakeAPI: api, client: NewClient(Config{ServerURL: server.URL}, "worker-registration")}
			require.NoError(t, worker.RunLease(t.Context(), lease))
			require.Equal(t, int32(1), executed.Load())
			require.Equal(t, int32(2), attempts.Load())
			require.Equal(t, <-bodies, <-bodies, "result and immutable lease must replay byte for byte")
			if status == http.StatusServiceUnavailable {
				api.mu.Lock()
				defer api.mu.Unlock()
				require.Positive(t, api.heartbeats, "lease maintenance must continue during delivery")
			}
		})
	}
}

func TestCompletionAmbiguousCommittedTransportResponseReplaysSameResult(t *testing.T) {
	var executed atomic.Int32
	worker, api, lease := leasedWorker(executorFunc(func(context.Context, Job, func(string, string)) Result {
		executed.Add(1)
		return Result{Status: "succeeded", Summary: "Committed once", Patch: "retained patch"}
	}))
	bodies := make(chan []byte, 3)
	var attempts, commits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		bodies <- body
		if attempts.Add(1) == 1 {
			commits.Add(1)
			connection, _, err := w.(http.Hijacker).Hijack()
			require.NoError(t, err)
			require.NoError(t, connection.Close(), "lose response after storing the terminal result")
			return
		}
		w.WriteHeader(http.StatusNoContent) // Idempotent server replay acknowledges the saved result.
	}))
	defer server.Close()
	worker.API = &completionClientAPI{fakeAPI: api, client: NewClient(Config{ServerURL: server.URL}, "worker-registration")}
	require.NoError(t, worker.RunLease(t.Context(), lease))
	require.Equal(t, int32(1), executed.Load())
	require.Equal(t, int32(1), commits.Load())
	require.Equal(t, int32(2), attempts.Load())
	require.Equal(t, <-bodies, <-bodies)
}

func TestCompletionPermanentHTTPRejectionDoesNotRetry(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				w.WriteHeader(status)
			}))
			defer server.Close()
			worker, api, lease := leasedWorker(executorFunc(func(context.Context, Job, func(string, string)) Result { return Result{Status: "succeeded"} }))
			worker.API = &completionClientAPI{fakeAPI: api, client: NewClient(Config{ServerURL: server.URL}, "worker-registration")}
			err := worker.RunLease(t.Context(), lease)
			require.EqualError(t, err, (&HTTPError{Status: status}).Error())
			require.Equal(t, int32(1), attempts.Load())
		})
	}
}

func TestCompletionBudgetAndAttemptLimits(t *testing.T) {
	for _, scenario := range []string{"attempt limit", "budget exhausted during request", "budget exhausted during backoff"} {
		t.Run(scenario, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				if scenario == "budget exhausted during request" {
					_, _ = io.Copy(io.Discard, r.Body)
					<-r.Context().Done()
					return
				}
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer server.Close()
			worker := &Worker{API: NewClient(Config{ServerURL: server.URL}, "worker-registration")}
			budget := 2 * time.Second
			if scenario != "attempt limit" {
				budget = 50 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(t.Context(), budget)
			defer cancel()
			started := time.Now()
			err := worker.completeResult(ctx, t.Context(), Lease{Job: Job{ID: uuid.NewString()}, Token: "immutable-lease"}, Result{Status: "succeeded"})
			if scenario == "attempt limit" {
				require.EqualError(t, err, "worker API returned HTTP 503")
				require.Equal(t, int32(3), attempts.Load())
			} else {
				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.Equal(t, int32(1), attempts.Load())
			}
			require.Less(t, time.Since(started), 1500*time.Millisecond)
		})
	}
}

func TestLeaseLossCancelsInFlightCompletionRetry(t *testing.T) {
	worker, api, lease := leasedWorker(executorFunc(func(context.Context, Job, func(string, string)) Result { return Result{Status: "succeeded"} }))
	api.renewError = &HTTPError{Status: http.StatusConflict}
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	worker.API = &completionClientAPI{fakeAPI: api, client: NewClient(Config{ServerURL: server.URL}, "worker-registration")}
	started := time.Now()
	require.ErrorIs(t, worker.RunLease(t.Context(), lease), ErrLeaseLost)
	require.Equal(t, int32(2), attempts.Load())
	require.Less(t, time.Since(started), 2*time.Second)
}

func TestLeaseExpiryAfterShutdownStopsCompletionRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	worker, api, lease := leasedWorker(executorFunc(func(context.Context, Job, func(string, string)) Result {
		cancel()
		return Result{Status: "succeeded"}
	}))
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	worker.API = &completionClientAPI{fakeAPI: api, client: NewClient(Config{ServerURL: server.URL}, "worker-registration")}
	started := time.Now()
	require.ErrorIs(t, worker.RunLease(ctx, lease), ErrLeaseLost)
	require.Equal(t, int32(2), attempts.Load())
	require.Less(t, time.Since(started), 3*time.Second)
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
