package devboxworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/go-playground/validator/v10"
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

func TestNewWorkerInjectsTokenIntoVersionAndExecutionRedaction(t *testing.T) {
	config := testConfig(t)
	token := "strifira_" + strings.Repeat("d", 64)
	provider := filepath.Join(t.TempDir(), "provider")
	require.NoError(t, os.WriteFile(provider, []byte("#!/bin/sh\nprintf '%s' '"+token+"'\n"), 0700))
	config.Providers["claude-code"] = provider
	worker, err := New(config, NewClient(config, token))
	require.NoError(t, err)
	require.Equal(t, "[REDACTED]", worker.Heartbeat.Providers[0].Version)
	executor, ok := worker.Executor.(*LocalExecutor)
	require.True(t, ok)
	require.Equal(t, token, executor.redactionToken)
}

func TestProviderVersionNormalizationAllowsContractHeartbeatAndClaim(t *testing.T) {
	for _, test := range []struct {
		name, output, expected string
	}{
		{name: "ASCII banner over100", output: strings.Repeat("v", 101), expected: strings.Repeat("v", 100)},
		{name: "Unicode rune limit", output: strings.Repeat("é", 101), expected: strings.Repeat("é", 100)},
		{name: "multiline ANSI wrapper", output: "\x1b[32mClaude Code1.2.3\x1b[0m\nWrapper ready\r\n", expected: "Claude Code1.2.3 Wrapper ready"},
		{name: "NUL and Unicode controls", output: "\x00\t\x7f\u0085\v", expected: "unknown"},
		{name: "empty output", expected: "unknown"},
		{name: "invalid UTF8", output: "\xff1.2.3", expected: "�1.2.3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var claims atomic.Int32
			versions := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/claim") {
					claims.Add(1)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				var request dto.DevBoxHeartbeatRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if err := validator.New().Struct(request); err != nil || len(request.Providers) != 1 {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				version := request.Providers[0].Version
				if version == "" || utf8.RuneCountInString(version) > 100 || strings.ContainsFunc(version, unicode.IsControl) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				versions <- version
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			config := testConfig(t)
			config.ServerURL, config.LocalhostHTTP = server.URL, true
			output := filepath.Join(t.TempDir(), "version-output")
			require.NoError(t, os.WriteFile(output, []byte(test.output), 0600))
			provider := filepath.Join(t.TempDir(), "provider")
			script := "#!/bin/sh\ncat -- '" + strings.ReplaceAll(output, "'", "'\\''") + "'\n"
			require.NoError(t, os.WriteFile(provider, []byte(script), 0700))
			config.Providers["claude-code"] = provider
			worker, err := New(config, NewClient(config, "strifira_"+strings.Repeat("a", 64)))
			require.NoError(t, err)
			require.NoError(t, worker.Run(t.Context(), true))
			require.Equal(t, int32(1), claims.Load(), "valid normalized heartbeat must allow the next claim")
			require.Equal(t, test.expected, <-versions)
		})
	}
}

type isolatedHeartbeatAPI struct {
	*fakeAPI
	err       error
	heartbeat atomic.Int32
	renewals  atomic.Int32
	renewed   chan struct{}
}

func (a *isolatedHeartbeatAPI) Heartbeat(ctx context.Context, body Heartbeat) error {
	if a.heartbeat.Add(1) == 1 {
		return a.err
	}
	return a.fakeAPI.Heartbeat(ctx, body)
}

func (a *isolatedHeartbeatAPI) Renew(ctx context.Context, id, token string) (LeaseState, error) {
	state, err := a.fakeAPI.Renew(ctx, id, token)
	if err != nil {
		return state, err
	}
	lifetime := state.ExpiresAt.Sub(state.ServerTime)
	state.ServerTime = time.Now().UTC()
	state.ExpiresAt = state.ServerTime.Add(lifetime)
	if a.renewals.Add(1) == 2 {
		close(a.renewed)
	}
	return state, nil
}

func TestIsolatedHeartbeatFailurePreservesRenewedLeaseAndCompletion(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "service unavailable", err: &HTTPError{Status: http.StatusServiceUnavailable}},
		{name: "rate limited", err: &HTTPError{Status: http.StatusTooManyRequests}},
		{name: "transport failure", err: &transportError{err: io.EOF}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			renewed := make(chan struct{})
			worker, api, lease := leasedWorker(executorFunc(func(ctx context.Context, _ Job, _ func(string, string)) Result {
				select {
				case <-ctx.Done():
					return Result{Status: "failed", Summary: "valid lease was stopped"}
				case <-renewed:
					return Result{Status: "succeeded", Summary: "completed under renewed lease"}
				}
			}))
			calls := &isolatedHeartbeatAPI{fakeAPI: api, err: test.err, renewed: renewed}
			worker.API = calls
			var warnings bytes.Buffer
			worker.Logger = slog.New(slog.NewTextHandler(&warnings, nil))
			require.NoError(t, worker.RunLease(t.Context(), lease))
			require.GreaterOrEqual(t, calls.renewals.Load(), int32(2))
			require.Len(t, api.completed, 1)
			require.Equal(t, "succeeded", api.completed[0].Status)
			require.Contains(t, warnings.String(), "worker heartbeat failed during job")
		})
	}
}

func TestHeartbeatAuthorityFailureStillStopsLeaseAndCompletion(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			worker, api, lease := leasedWorker(executorFunc(func(ctx context.Context, _ Job, _ func(string, string)) Result {
				<-ctx.Done()
				return Result{Status: "succeeded"}
			}))
			calls := &isolatedHeartbeatAPI{fakeAPI: api, err: &HTTPError{Status: status}, renewed: make(chan struct{})}
			worker.API = calls
			require.ErrorIs(t, worker.RunLease(t.Context(), lease), ErrLeaseLost)
			require.Equal(t, int32(1), calls.renewals.Load())
			require.Empty(t, api.completed)
		})
	}
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

func TestAuthorityLossCancelsInFlightCompletionRetry(t *testing.T) {
	worker, api, lease := leasedWorker(executorFunc(func(context.Context, Job, func(string, string)) Result { return Result{Status: "succeeded"} }))
	api.renewError = &HTTPError{Status: http.StatusForbidden}
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

type completionDeadlineAPI struct{ *fakeAPI }

func (a *completionDeadlineAPI) Complete(ctx context.Context, id, token string, result Result) error {
	if err := a.fakeAPI.Complete(ctx, id, token, result); err != nil {
		return err
	}
	<-ctx.Done()
	return &transportError{err: ctx.Err()}
}

func TestShutdownCompletionRetriesRemainBoundedBeyondExecutionLeaseExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		worker, api, lease := leasedWorker(executorFunc(func(context.Context, Job, func(string, string)) Result {
			cancel()
			return Result{Status: "succeeded"}
		}))
		worker.API = &completionDeadlineAPI{fakeAPI: api}
		started := time.Now()
		require.ErrorIs(t, worker.RunLease(ctx, lease), context.DeadlineExceeded)
		// Three 3-second attempts and 250/500 ms backoffs remain within the
		// independent shutdown budget, even though the execution lease expires.
		require.Equal(t, 9750*time.Millisecond, time.Since(started))
		require.Less(t, time.Since(started), 12*time.Second)
		require.Len(t, api.completed, 3)
		require.Equal(t, "cancelled", api.completed[0].Status)
		require.Equal(t, api.completed[0], api.completed[1])
		require.Equal(t, api.completed[0], api.completed[2])
	})
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

type cappedRuntimeAPI struct {
	*fakeAPI
	deadline time.Time
}

func (a *cappedRuntimeAPI) Renew(context.Context, string, string) (LeaseState, error) {
	return LeaseState{Status: "running", ServerTime: time.Now(), ExpiresAt: a.deadline, DeadlineAt: a.deadline}, nil
}

func TestRuntimeCappedLeaseReportsTimeoutMetadataBeforeServerDeadline(t *testing.T) {
	for _, capAtStart := range []bool{true, false} {
		t.Run(fmt.Sprintf("capped at start %t", capAtStart), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				worker, api, lease := leasedWorker(executorFunc(func(ctx context.Context, _ Job, _ func(string, string)) Result {
					<-ctx.Done()
					return Result{Status: "failed", Summary: "provider execution details", Patch: "retained patch", ChangedFiles: []string{"README.md"}, TestStatus: "failed", TestExitCode: new(7), TestsRun: []string{"go test ./..."}, RiskNotes: []string{"test details retained"}}
				}))
				start := time.Now()
				deadline := start.Add(10 * time.Second)
				api.start.ServerTime, api.start.DeadlineAt = start, deadline
				api.start.ExpiresAt = deadline
				if !capAtStart {
					api.start.ExpiresAt = start.Add(8 * time.Second)
				}
				lease.Job.MaxRuntimeSeconds = 10
				worker.API = &cappedRuntimeAPI{fakeAPI: api, deadline: deadline}
				require.NoError(t, worker.RunLease(t.Context(), lease))
				require.Equal(t, 7*time.Second, time.Since(start))
				require.True(t, time.Now().Before(deadline), "timeout delivery starts while completion remains allowed")
				require.Len(t, api.completed, 1)
				result := api.completed[0]
				require.Equal(t, "timeout", result.Status)
				require.Contains(t, result.Summary, "provider execution details")
				require.Equal(t, "retained patch", result.Patch)
				require.Equal(t, []string{"README.md"}, result.ChangedFiles)
				require.Equal(t, []string{"go test ./..."}, result.TestsRun)
				require.Equal(t, 7, *result.TestExitCode)
				require.Contains(t, result.RiskNotes, "test details retained")
			})
		})
	}
}

type terminalRenewalReplayAPI struct {
	*fakeAPI
	reportStarted chan struct{}
	renewReturned chan struct{}
	renewStatus   int
	terminalState string
	responseDelay time.Duration
	renewCalls    atomic.Int32
	completeCalls atomic.Int32
}

func (a *terminalRenewalReplayAPI) Renew(ctx context.Context, _, _ string) (LeaseState, error) {
	select {
	case <-a.reportStarted:
	case <-ctx.Done():
		return LeaseState{}, ctx.Err()
	}
	select {
	case <-time.After(a.renewDelay):
	case <-ctx.Done():
		return LeaseState{}, ctx.Err()
	}
	if a.renewCalls.Add(1) == 1 {
		close(a.renewReturned)
		if a.renewStatus == http.StatusOK {
			state := a.start
			lifetime := state.ExpiresAt.Sub(state.ServerTime)
			state.ServerTime = time.Now()
			state.ExpiresAt = state.ServerTime.Add(lifetime)
			return state, nil
		}
		if a.terminalState != "" {
			return LeaseState{Status: a.terminalState}, nil
		}
	}
	if a.renewStatus == http.StatusOK {
		return LeaseState{}, &HTTPError{Status: http.StatusConflict}
	}
	return LeaseState{}, &HTTPError{Status: a.renewStatus}
}

func (a *terminalRenewalReplayAPI) Complete(ctx context.Context, id, token string, result Result) error {
	if a.completeCalls.Add(1) == 1 {
		if err := a.fakeAPI.Complete(ctx, id, token, result); err != nil {
			return err
		}
		close(a.reportStarted)
		select {
		case <-a.renewReturned:
		case <-ctx.Done():
			return ctx.Err()
		}
		if a.renewStatus == http.StatusUnauthorized || a.renewStatus == http.StatusForbidden {
			<-ctx.Done()
			return ctx.Err()
		}
		select {
		case <-time.After(a.responseDelay):
			return &transportError{err: io.EOF}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return a.fakeAPI.Complete(ctx, id, token, result)
}

func TestTerminalRenewalDuringReportingAllowsCommittedResultReplay(t *testing.T) {
	for _, test := range []struct {
		name          string
		status        int
		renewDelay    time.Duration
		terminalState string
	}{
		{name: "conflict before expiry", status: http.StatusConflict},
		{name: "conflict after expiry", status: http.StatusConflict, renewDelay: 3 * time.Second},
		{name: "renewed lease during reporting", status: http.StatusOK},
		{name: "terminal state after expiry", terminalState: "succeeded", renewDelay: 3 * time.Second},
		{name: "unauthorized", status: http.StatusUnauthorized},
		{name: "unauthorized after expiry", status: http.StatusUnauthorized, renewDelay: 3 * time.Second},
		{name: "forbidden", status: http.StatusForbidden},
		{name: "forbidden after expiry", status: http.StatusForbidden, renewDelay: 3 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var executed atomic.Int32
				worker, api, lease := leasedWorker(executorFunc(func(context.Context, Job, func(string, string)) Result {
					executed.Add(1)
					return Result{Status: "succeeded", TestStatus: "passed", Patch: "immutable artifact"}
				}))
				started := time.Now()
				deadline := started.Add(10 * time.Second)
				api.start.DeadlineAt = deadline
				api.renewDelay = test.renewDelay
				lease.Job.MaxRuntimeSeconds = 10
				calls := &terminalRenewalReplayAPI{fakeAPI: api, reportStarted: make(chan struct{}), renewReturned: make(chan struct{}), renewStatus: test.status, terminalState: test.terminalState, responseDelay: 4 * time.Second}
				worker.API = calls
				err := worker.RunLease(t.Context(), lease)
				require.Equal(t, int32(1), executed.Load(), "reporting never reruns execution")
				if test.status != http.StatusUnauthorized && test.status != http.StatusForbidden {
					require.NoError(t, err)
					require.Equal(t, 5250*time.Millisecond+test.renewDelay, time.Since(started))
					require.Greater(t, time.Since(started), 2*time.Second, "replay outlasts the original execution lease timer")
					require.True(t, time.Now().Before(deadline), "the server runtime deadline remains valid")
					require.Equal(t, int32(2), calls.completeCalls.Load())
					require.Len(t, api.completed, 2)
					require.Equal(t, api.completed[0], api.completed[1], "committed result replay stays immutable")
				} else {
					require.ErrorIs(t, err, ErrLeaseLost)
					require.Equal(t, time.Second+test.renewDelay, time.Since(started), "only the real authority response stops reporting")
					require.Equal(t, int32(1), calls.completeCalls.Load(), "real authority loss still prevents replay")
				}
			})
		})
	}
}

func TestLateRenewDoesNotExtendExpiredServerLease(t *testing.T) {
	worker, api, lease := leasedWorker(executorFunc(func(ctx context.Context, _ Job, _ func(string, string)) Result { <-ctx.Done(); return Result{} }))
	api.renew.ExpiresAt = api.renew.ServerTime.Add(1100 * time.Millisecond)
	api.renewDelay = 150 * time.Millisecond
	require.ErrorIs(t, worker.RunLease(t.Context(), lease), ErrLeaseLost)
	require.Empty(t, api.completed)
}
