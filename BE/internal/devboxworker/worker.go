package devboxworker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

var (
	ErrLeaseLost          = errors.New("job lease lost; execution stopped")
	ErrRemoteCancellation = errors.New("job cancellation requested")
	ErrRuntimeExceeded    = errors.New("job runtime exceeded")
)

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
	ids := make([]string, 0, len(config.Providers))
	for id := range config.Providers {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		versionCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		output := &boundedBuffer{limit: 1000}
		_, err := runProcess(versionCtx, "", childEnvironment(""), []string{config.Providers[id], "--version"}, output, output)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("provider %s version check failed: %w", id, err)
		}
		heartbeat.Providers = append(heartbeat.Providers, ProviderInfo{ID: id, Version: boundedText(strings.TrimSpace(output.String()), 200)})
	}
	return &Worker{Config: config, API: api, Executor: &LocalExecutor{Config: config}, Logger: slog.Default(), Heartbeat: heartbeat}, nil
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

func leaseDuration(state LeaseState) (time.Duration, error) {
	if state.ServerTime.IsZero() || state.ExpiresAt.IsZero() {
		return 0, errors.New("lease response omitted server time or deadline")
	}
	duration := state.ExpiresAt.Sub(state.ServerTime) - time.Second
	if duration <= 0 || duration > 2*time.Minute {
		return 0, errors.New("lease response has an invalid lifetime")
	}
	return duration, nil
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
	if duration <= 0 || state.Status != "running" {
		return ErrLeaseLost
	}
	if state.CancelRequested {
		cancel(ErrRemoteCancellation)
	}
	runtimeLimit := time.Duration(lease.Job.MaxRuntimeSeconds)*time.Second - startElapsed
	if !state.DeadlineAt.IsZero() {
		runtimeLimit = min(runtimeLimit, state.DeadlineAt.Sub(state.ServerTime)-startElapsed)
	}
	if runtimeLimit <= 0 {
		cancel(ErrRuntimeExceeded)
	}
	runtimeTimer := time.AfterFunc(max(runtimeLimit, time.Nanosecond), func() { cancel(ErrRuntimeExceeded) })
	defer runtimeTimer.Stop()
	leaseTimer := time.AfterFunc(duration, func() { cancel(ErrLeaseLost) })
	defer leaseTimer.Stop()
	interval := time.Duration(lease.HeartbeatIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 15 * time.Second
	}
	interval = min(interval, duration/2)
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
				if err != nil {
					cancel(ErrLeaseLost)
					return
				}
				if next.CancelRequested {
					cancel(ErrRemoteCancellation)
					return
				}
				if next.Status != "running" {
					cancel(ErrLeaseLost)
					return
				}
				lifetime, err := leaseDuration(next)
				if err != nil {
					cancel(ErrLeaseLost)
					return
				}
				lifetime -= renewElapsed
				if lifetime <= 0 {
					cancel(ErrLeaseLost)
					return
				}
				leaseTimer.Reset(lifetime)
				if err := w.API.Heartbeat(ctx, w.Heartbeat); err != nil {
					cancel(ErrLeaseLost)
					return
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
					cancel(ErrLeaseLost)
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
	cause := context.Cause(ctx)
	if errors.Is(cause, ErrLeaseLost) {
		cancel(ErrLeaseLost)
		wg.Wait()
		return ErrLeaseLost
	}
	if cause != nil {
		result.Status, result.ExitCode = "cancelled", -1
		result.Summary = boundedText(cause.Error(), maxSummaryBytes)
		if errors.Is(cause, ErrRuntimeExceeded) {
			result.Status = "timeout"
		}
	}
	// Use a separate short context to record cancellation/shutdown after all subprocesses stop.
	completeCtx, completeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer completeCancel()
	err = w.API.Complete(completeCtx, lease.Job.ID, lease.Token, result)
	cancel(nil)
	wg.Wait()
	return err
}
