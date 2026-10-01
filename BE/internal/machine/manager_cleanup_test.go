package machine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/stretchr/testify/require"
)

type cleanupManagerStoreFake struct {
	managerStoreFake
	run            *domain.DevMachineAgentRun
	claims         []string
	completed      int
	retried        int
	retryMessage   string
	eventError     error
	acknowledgeErr error
	ackContextErr  error
}

func (f *cleanupManagerStoreFake) LeaseAgentRunCleanup(_ context.Context, owner string, lease time.Duration) (*domain.DevMachineAgentRun, error) {
	f.claims = append(f.claims, owner)
	return f.run, nil
}

func (f *cleanupManagerStoreFake) CompleteAgentRunCleanup(ctx context.Context, _ uuid.UUID, _ string) error {
	f.completed++
	f.ackContextErr = ctx.Err()
	return f.acknowledgeErr
}

func (f *cleanupManagerStoreFake) RetryAgentRunCleanup(ctx context.Context, _ uuid.UUID, _, message string) error {
	f.retried++
	f.retryMessage = message
	f.ackContextErr = ctx.Err()
	return f.acknowledgeErr
}

func (f *cleanupManagerStoreFake) CreateEvent(ctx context.Context, event *domain.DevMachineEvent) error {
	if f.eventError != nil {
		return f.eventError
	}
	return f.managerStoreFake.CreateEvent(ctx, event)
}

type cleanupRuntimeFake struct {
	runtimeFake
	calls    int
	result   RepositorySourceSync
	checkout *domain.DevMachineCheckout
	onCall   func(context.Context) error
}

func (f *cleanupRuntimeFake) CleanupAgentRun(ctx context.Context, _ *domain.DevMachine, _ []domain.DevMachineService, checkout *domain.DevMachineCheckout, _ *domain.DevMachineAgentRun) (RepositorySourceSync, error) {
	f.calls++
	f.checkout = checkout
	if f.onCall != nil {
		return f.result, f.onCall(ctx)
	}
	return f.result, nil
}

func TestManagerTerminalCleanupRetriesFailuresAndFencesAcknowledgements(t *testing.T) {
	for _, mode := range []string{"success", "conflict", "runtime-error", "event-error", "context-cancelled", "wrong-machine", "wrong-checkout", "nonterminal", "destroyed", "ack-error"} {
		t.Run(mode, func(t *testing.T) {
			machine := fixtureHelperMachine()
			run := &domain.DevMachineAgentRun{ID: uuid.New(), MachineID: machine.ID, WorkspaceID: machine.WorkspaceID, Status: domain.DevMachineAgentRunStatusTimeout}
			store := &cleanupManagerStoreFake{managerStoreFake: managerStoreFake{machine: machine}, run: run}
			runtime := &cleanupRuntimeFake{result: RepositorySourceSync{Status: "unchanged"}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "conflict", "event-error":
				runtime.result = RepositorySourceSync{Status: "conflict", Reason: "later owner index has precedence"}
				if mode == "event-error" {
					store.eventError = errors.New("database event unavailable")
				}
			case "runtime-error":
				runtime.onCall = func(context.Context) error { return errors.New("owned container is still removing") }
			case "context-cancelled":
				runtime.onCall = func(ctx context.Context) error { cancel(); return ctx.Err() }
			case "wrong-machine":
				machine.WorkspaceID = uuid.New()
			case "wrong-checkout":
				run.CheckoutID = new(uuid.New())
				store.checkout = &domain.DevMachineCheckout{ID: *run.CheckoutID, MachineID: uuid.New(), WorkspaceID: machine.WorkspaceID}
			case "nonterminal":
				run.Status = domain.DevMachineAgentRunStatusRunning
			case "destroyed":
				machine.Status = domain.DevMachineStatusDestroyed
			case "ack-error":
				store.acknowledgeErr = errors.New("database acknowledgement unavailable")
			}
			manager := NewManager(store, runtime, nil, nil, nil, nil, nil, "backend-owner")
			err := manager.reconcileAgentRunCleanup(ctx)
			if mode == "success" || mode == "conflict" || mode == "destroyed" {
				require.NoError(t, err)
				require.Equal(t, 1, store.completed)
				require.Zero(t, store.retried)
			} else if mode == "ack-error" {
				require.ErrorContains(t, err, "acknowledgement")
			} else {
				require.Error(t, err)
				require.Equal(t, 1, store.retried)
				require.NotEmpty(t, store.retryMessage)
				require.Zero(t, store.completed)
			}
			require.NoError(t, store.ackContextErr, "cleanup failure must not cancel retry persistence")
			if mode == "conflict" {
				require.Len(t, store.events, 1)
				require.Equal(t, "agent_run.cleanup_source_conflict", store.events[0].EventType)
			}
			if mode == "destroyed" || mode == "wrong-machine" || mode == "wrong-checkout" || mode == "nonterminal" {
				require.Zero(t, runtime.calls)
			}
			if mode != "context-cancelled" {
				require.NoError(t, ctx.Err())
			}
		})
	}
}

func TestManagerTerminalCleanupClaimIdentityChangesOnEveryAttempt(t *testing.T) {
	store := &cleanupManagerStoreFake{}
	manager := NewManager(store, &cleanupRuntimeFake{}, nil, nil, nil, nil, nil, "backend-owner")
	require.NoError(t, manager.reconcileAgentRunCleanup(t.Context()))
	require.NoError(t, manager.reconcileAgentRunCleanup(t.Context()))
	require.Len(t, store.claims, 2)
	require.NotEqual(t, store.claims[0], store.claims[1])
}

func TestManagerCleanupDoesNotBlockOperationPollingOrStartDuplicateWorkers(t *testing.T) {
	machine := fixtureHelperMachine()
	run := &domain.DevMachineAgentRun{ID: uuid.New(), MachineID: machine.ID, WorkspaceID: machine.WorkspaceID, Status: domain.DevMachineAgentRunStatusTimeout}
	store := &cleanupManagerStoreFake{managerStoreFake: managerStoreFake{machine: machine}, run: run}
	started, release := make(chan struct{}), make(chan struct{})
	runtime := &cleanupRuntimeFake{result: RepositorySourceSync{Status: "unchanged"}, onCall: func(ctx context.Context) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	manager := NewManager(store, runtime, nil, nil, nil, nil, nil, "backend-owner")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	defer manager.operations.Wait()
	defer close(release)
	manager.startAgentRunCleanup(ctx)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cleanup worker did not start")
	}
	for range 20 {
		manager.startAgentRunCleanup(ctx)
	}
	require.Len(t, store.claims, 1, "a held cleanup cannot consume another lease")
	require.NoError(t, manager.processBatch(ctx), "new operations can still be leased")
}

func TestManagerTerminalCleanupUsesPinnedDefaultRepositoryWithoutCredentials(t *testing.T) {
	for _, mode := range []string{"unchanged", "changed-machine-and-link", "no-selection", "wrong-authority"} {
		t.Run(mode, func(t *testing.T) {
			machine := fixtureHelperMachine()
			machine.RepoOwner, machine.RepoName = "ku", "api"
			machine.RepoURL = "https://git.example.com/ku/api"
			machine.BaseBranch, machine.WorkingBranch = "main", "ku/task"
			checkout, err := defaultRepositoryCheckout(machine, "https://git.example.com")
			require.NoError(t, err)
			run := &domain.DevMachineAgentRun{ID: uuid.New(), MachineID: machine.ID, WorkspaceID: machine.WorkspaceID, Status: domain.DevMachineAgentRunStatusTimeout}
			store := &cleanupManagerStoreFake{managerStoreFake: managerStoreFake{machine: machine, pinnedRepository: checkout}, run: run}
			switch mode {
			case "changed-machine-and-link":
				machine.RepoOwner, machine.RepoName = "later", "repo"
				machine.BaseBranch, machine.WorkingBranch = "new-base", "new-issue"
			case "no-selection":
				store.pinnedRepository = nil
			case "wrong-authority":
				checkout.WorkspaceID = uuid.New()
			}
			runtime := &cleanupRuntimeFake{result: RepositorySourceSync{Status: "unchanged"}}
			// Neither a link/connection nor an encryption key is available.
			manager := NewManager(store, runtime, nil, nil, nil, nil, nil, "backend-owner")
			err = manager.reconcileAgentRunCleanup(t.Context())
			if mode == "wrong-authority" {
				require.Error(t, err)
				require.Zero(t, runtime.calls)
				require.Equal(t, 1, store.retried)
			} else {
				require.NoError(t, err)
				require.Equal(t, 1, runtime.calls)
				require.Equal(t, 1, store.completed)
				if mode == "no-selection" {
					require.Nil(t, runtime.checkout, "a later repository assignment cannot invent selection for this run")
				} else {
					require.Equal(t, machine.ID, runtime.checkout.ID)
					require.Equal(t, "/workspace/repository", runtime.checkout.WorkspacePath)
					require.Equal(t, "https://git.example.com/ku/api", runtime.checkout.RepositoryURL)
					require.Equal(t, "ku/task", runtime.checkout.WorkingBranch)
				}
			}
			require.Empty(t, store.runtimeCredentials)
			require.Empty(t, store.installationFullNameCalls, "cleanup must not resolve/decrypt publishing credentials")
			require.Nil(t, run.CheckoutID)
		})
	}
}
