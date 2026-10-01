package devboxworker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/google/uuid"
)

var (
	ErrLeaseLost          = errors.New("job lease lost; execution stopped")
	ErrRemoteCancellation = errors.New("job cancellation requested")
	ErrRuntimeExceeded    = errors.New("job runtime exceeded")
	versionANSI           = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]")
)

func normalizeProviderVersion(value string) string {
	value = versionANSI.ReplaceAllString(value, "")
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, strings.ToValidUTF8(value, "�"))
	characters := []rune(strings.Join(strings.Fields(value), " "))
	if len(characters) == 0 {
		return "unknown"
	}
	return string(characters[:min(len(characters), 100)])
}

type Worker struct {
	Config    Config
	API       API
	Executor  Executor
	Logger    *slog.Logger
	Heartbeat Heartbeat
}

func New(config Config, api API) (*Worker, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(config.WorkRoot, 0700); err != nil {
		return nil, fmt.Errorf("create work_root: %w", err)
	}
	info, err := os.Lstat(config.WorkRoot)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("work_root must be a private directory with mode 0700")
	}
	hostname, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	heartbeat := Heartbeat{Hostname: hostname, OS: runtime.GOOS, Arch: runtime.GOARCH, Version: Version, Providers: []ProviderInfo{}}
	var redactionToken string
	if client, ok := api.(*Client); ok {
		redactionToken = client.token
	}
	ids := make([]string, 0, len(config.Providers))
	for id := range config.Providers {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		versionCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		output := &boundedBuffer{limit: 1000, token: redactionToken}
		_, err := runProcess(versionCtx, "", childEnvironment(""), []string{config.Providers[id], "--version"}, output, output)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("provider %s version check failed: %w", id, err)
		}
		heartbeat.Providers = append(heartbeat.Providers, ProviderInfo{ID: id, Version: normalizeProviderVersion(output.String())})
	}
	return &Worker{Config: config, API: api, Executor: &LocalExecutor{Config: config, redactionToken: redactionToken}, Logger: slog.Default(), Heartbeat: heartbeat}, nil
}

func (w *Worker) Run(ctx context.Context, once bool) error {
	interval := w.Config.PollInterval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if err := w.API.Heartbeat(ctx, w.Heartbeat); err != nil {
			if once || fatalHTTP(err) {
				return err
			}
			w.Logger.Warn("worker heartbeat failed", "error", err)
		} else {
			lease, err := w.API.Claim(ctx)
			if err != nil {
				if once || fatalHTTP(err) {
					return err
				}
				w.Logger.Warn("job claim failed", "error", err)
			} else if lease != nil {
				if err := w.RunLease(ctx, *lease); err != nil {
					if once || fatalHTTP(err) {
						return err
					}
					w.Logger.Warn("job stopped", "job_id", lease.Job.ID, "error", err)
				}
				if once {
					return nil
				}
				continue
			} else if once {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func fatalHTTP(err error) bool {
	value, ok := errors.AsType[*HTTPError](err)
	return ok && (value.Status == 401 || value.Status == 403)
}

func retryableCompletion(err error) bool {
	if value, ok := errors.AsType[*HTTPError](err); ok {
		return value.Status == http.StatusRequestTimeout || value.Status == http.StatusTooManyRequests || (value.Status >= 500 && value.Status <= 599)
	}
	_, ok := errors.AsType[*transportError](err)
	return ok
}

func (w *Worker) completeResult(ctx, shutdown context.Context, lease Lease, result Result) error {
	var err error
	for attempt := range 3 {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		attemptLimit := 10 * time.Second
		if shutdown.Err() != nil {
			attemptLimit = 3 * time.Second
		}
		attemptCtx, cancel := context.WithTimeout(ctx, attemptLimit)
		err = w.API.Complete(attemptCtx, lease.Job.ID, lease.Token, result)
		cancel()
		if err == nil {
			return nil
		}
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		if !retryableCompletion(err) || attempt == 2 {
			return err
		}
		// Replay the immutable result and lease, never rerun the executor. The
		// authenticated completion endpoint also accepts an already committed result.
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(time.Duration(1<<attempt) * 250 * time.Millisecond):
		}
	}
	return err
}

func leaseDuration(state LeaseState) (time.Duration, error) {
	if state.ServerTime.IsZero() || state.ExpiresAt.IsZero() {
		return 0, errors.New("lease response omitted server time or deadline")
	}
	lifetime := state.ExpiresAt.Sub(state.ServerTime)
	if lifetime <= 0 || lifetime > 2*time.Minute+time.Second {
		return 0, errors.New("lease response has an invalid lifetime")
	}
	duration := lifetime - time.Second
	if runtimeCappedLease(state) {
		return max(duration, time.Nanosecond), nil
	}
	if duration <= 0 {
		return 0, errors.New("lease response has an invalid lifetime")
	}
	return duration, nil
}

func runtimeCappedLease(state LeaseState) bool {
	return !state.DeadlineAt.IsZero() && !state.ExpiresAt.Before(state.DeadlineAt)
}

func (w *Worker) RunLease(parent context.Context, lease Lease) error {
	if _, err := uuid.Parse(lease.Job.ID); err != nil || lease.Token == "" {
		return errors.New("invalid claimed job or lease token")
	}
	if lease.Job.MaxRuntimeSeconds < 1 || lease.Job.MaxRuntimeSeconds > 24*60*60 {
		return errors.New("invalid job runtime limit")
	}
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	// Delivery survives execution cancellation but stays within the service's
	// shutdown window. Start its shutdown budget before subprocess cleanup so a
	// parent cancellation during execution or reporting cannot extend that window.
	deliveryCtx, stopDelivery := context.WithCancelCause(context.Background())
	defer stopDelivery(nil)
	stopShutdown := context.AfterFunc(parent, func() {
		timer := time.NewTimer(12 * time.Second)
		defer timer.Stop()
		select {
		case <-deliveryCtx.Done():
		case <-timer.C:
			stopDelivery(context.DeadlineExceeded)
		}
	})
	defer stopShutdown()
	stopLease := func() {
		stopDelivery(ErrLeaseLost)
		cancel(ErrLeaseLost)
	}
	startRequestedAt := time.Now()
	state, err := w.API.Start(ctx, lease.Job.ID, lease.Token)
	startElapsed := time.Since(startRequestedAt)
	if err != nil {
		return err
	}
	duration, err := leaseDuration(state)
	if err != nil {
		return err
	}
	duration -= startElapsed
	if state.Status != "running" || (duration <= 0 && !runtimeCappedLease(state)) {
		return ErrLeaseLost
	}
	if state.CancelRequested {
		cancel(ErrRemoteCancellation)
	}
	runtimeLimit := time.Duration(lease.Job.MaxRuntimeSeconds)*time.Second - startElapsed
	if !state.DeadlineAt.IsZero() {
		runtimeLimit = min(runtimeLimit, state.DeadlineAt.Sub(state.ServerTime)-startElapsed)
	}
	// Reserve time for process cleanup and the first completion upload while the
	// server can still accept a rich timeout result instead of sweeping the job.
	runtimeLimit -= min(3*time.Second, max(runtimeLimit/2, 0))
	if runtimeLimit <= 0 {
		cancel(ErrRuntimeExceeded)
	}
	runtimeTimer := time.AfterFunc(max(runtimeLimit, time.Nanosecond), func() { cancel(ErrRuntimeExceeded) })
	defer runtimeTimer.Stop()
	// The lease clock governs execution. Once the immutable result is ready,
	// only the authenticated completion endpoint and delivery budgets govern
	// reporting. Serialize that transition with expiry and renewal responses.
	var leaseMu sync.Mutex
	reporting := false
	runtimeCapped := runtimeCappedLease(state)
	leaseTimer := time.AfterFunc(max(duration, time.Nanosecond), func() {
		leaseMu.Lock()
		defer leaseMu.Unlock()
		if reporting {
			return
		}
		if runtimeCapped {
			cancel(ErrRuntimeExceeded)
		} else {
			stopLease()
		}
	})
	defer leaseTimer.Stop()
	interval := time.Duration(lease.HeartbeatIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 15 * time.Second
	}
	interval = min(interval, max(duration/2, time.Nanosecond))
	logs := make(chan LogEntry, 128)
	var dropped atomic.Bool
	var logBytes atomic.Int64
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
				renewRequestedAt := time.Now()
				next, err := w.API.Renew(ctx, lease.Job.ID, lease.Token)
				renewElapsed := time.Since(renewRequestedAt)
				keepRenewing := func() bool {
					leaseMu.Lock()
					defer leaseMu.Unlock()
					if err != nil {
						if status, ok := errors.AsType[*HTTPError](err); ok && status.Status == http.StatusConflict && reporting {
							// Completion may already have committed its terminal state.
							// Let the authenticated completion replay resolve ambiguity.
							return false
						}
						if ctx.Err() == nil || fatalHTTP(err) {
							stopLease()
						}
						return false
					}
					if next.CancelRequested {
						cancel(ErrRemoteCancellation)
						return false
					}
					if next.Status != "running" {
						if reporting && (next.Status == "succeeded" || next.Status == "failed" || next.Status == "cancelled" || next.Status == "timeout") {
							return false
						}
						stopLease()
						return false
					}
					lifetime, err := leaseDuration(next)
					if err != nil {
						stopLease()
						return false
					}
					lifetime -= renewElapsed
					if lifetime <= 0 && !runtimeCappedLease(next) {
						stopLease()
						return false
					}
					if !reporting {
						runtimeCapped = runtimeCappedLease(next)
						leaseTimer.Reset(max(lifetime, time.Nanosecond))
					}
					return true
				}()
				if !keepRenewing {
					return
				}
				if err := w.API.Heartbeat(ctx, w.Heartbeat); err != nil {
					if ctx.Err() != nil {
						return
					}
					if fatalHTTP(err) {
						stopLease()
						return
					}
					w.Logger.Warn("worker heartbeat failed during job", "job_id", lease.Job.ID, "error", err)
				}
			}
		}
	})
	logDone := make(chan struct{})
	wg.Go(func() {
		defer close(logDone)
		var sequence int64
		for entry := range logs {
			if ctx.Err() != nil {
				return
			}
			sequence++
			entry.Sequence, entry.LeaseToken = sequence, lease.Token
			response, err := w.API.Log(ctx, lease.Job.ID, entry)
			if err != nil {
				// Retry the identical sequence once, so an ambiguous response cannot duplicate logs.
				if !fatalHTTP(err) {
					response, err = w.API.Log(ctx, lease.Job.ID, entry)
				}
				if err != nil {
					if ctx.Err() == nil {
						stopLease()
					}
					return
				}
			}
			if response.Truncated {
				dropped.Store(true)
			}
		}
	})
	result := Result{Status: "failed", ExitCode: -1, TestStatus: "not_run", ChangedFiles: []string{}, TestsRun: []string{}, RiskNotes: []string{}}
	if ctx.Err() == nil {
		result = w.Executor.Execute(ctx, lease.Job, func(stream, content string) {
			if logBytes.Add(int64(len(content))) > maxOutputBytes {
				dropped.Store(true)
				return
			}
			select {
			case logs <- LogEntry{Stream: stream, Content: content}:
			default:
				dropped.Store(true)
			}
		})
	}
	close(logs)
	<-logDone
	if dropped.Load() {
		result.RiskNotes = append(result.RiskNotes, "Execution logs were truncated by the local or server limit.")
	}
	leaseMu.Lock()
	cause := context.Cause(ctx)
	if !errors.Is(cause, ErrLeaseLost) {
		reporting = true
		leaseTimer.Stop()
	}
	leaseMu.Unlock()
	if errors.Is(cause, ErrLeaseLost) {
		cancel(ErrLeaseLost)
		wg.Wait()
		return ErrLeaseLost
	}
	if cause != nil {
		result.Status, result.ExitCode = "cancelled", -1
		summary := cause.Error()
		if result.Summary != "" && result.Summary != summary {
			summary += "\n" + result.Summary
		}
		result.Summary = boundedText(summary, maxSummaryBytes)
		if errors.Is(cause, ErrRuntimeExceeded) {
			result.Status = "timeout"
		}
	}
	// Three bounded attempts fit within this separate delivery budget, including
	// backoff, while cancellation/shutdown can still be recorded after processes stop.
	completeCtx, budgetCancel := context.WithTimeout(deliveryCtx, 35*time.Second)
	defer budgetCancel()
	err = w.completeResult(completeCtx, parent, lease, result)
	cancel(nil)
	wg.Wait()
	return err
}
