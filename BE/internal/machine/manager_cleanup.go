package machine

import (
	"context"
	"errors"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

// Reclamation can wait on Docker/Git locks. Keep it outside the operation poll
// loop while bounding concurrency to one cleanup per manager process.
func (m *Manager) startAgentRunCleanup(ctx context.Context) {
	if ctx.Err() != nil || !m.cleanupActive.CompareAndSwap(false, true) {
		return
	}
	m.operations.Go(func() {
		defer m.cleanupActive.Store(false)
		if err := m.reconcileAgentRunCleanup(ctx); err != nil {
			log.WithError(err).Warn("terminal agent cleanup reconciliation failed")
		}
	})
}

func (m *Manager) reconcileAgentRunCleanup(ctx context.Context) error {
	// A unique claim token fences a delayed acknowledgement from this same
	// manager after its previous lease expired and another attempt acquired it.
	owner := m.owner + ":" + uuid.NewString()
	run, err := m.store.LeaseAgentRunCleanup(ctx, owner, 2*time.Minute)
	if err != nil || run == nil {
		return err
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	err = m.cleanupTerminalRun(cleanupCtx, run)
	// Persist retry with an independent bounded context, including when the
	// execution deadline expired. A failed acknowledgement leaves the lease
	// recoverable, never a permanent in-progress row.
	ackCtx, ackCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer ackCancel()
	if err != nil {
		return errors.Join(err, m.store.RetryAgentRunCleanup(ackCtx, run.ID, owner, "terminal agent cleanup failed; inspect the manager log for details"))
	}
	return m.store.CompleteAgentRunCleanup(ackCtx, run.ID, owner)
}

func (m *Manager) cleanupTerminalRun(ctx context.Context, run *domain.DevMachineAgentRun) error {
	if !terminalAgentStatus(run.Status) {
		return errors.New("cleanup queue returned a nonterminal agent run")
	}
	machine, err := m.store.GetMachineInternal(ctx, run.MachineID)
	if err != nil {
		return err
	}
	if machine == nil || machine.ID != run.MachineID || machine.WorkspaceID != run.WorkspaceID {
		return errors.New("terminal agent cleanup machine authority changed")
	}
	if machine.Status == domain.DevMachineStatusDestroyed {
		return nil
	}
	services, err := m.store.ListServices(ctx, machine.WorkspaceID, machine.ID)
	if err != nil {
		return err
	}
	var checkout *domain.DevMachineCheckout
	if run.CheckoutID != nil {
		checkout, err = m.store.GetCheckoutInternal(ctx, *run.CheckoutID)
		if err != nil {
			return err
		}
		if checkout == nil || checkout.ID != *run.CheckoutID || checkout.MachineID != machine.ID || checkout.WorkspaceID != machine.WorkspaceID {
			return errors.New("terminal agent cleanup checkout authority changed")
		}
	}
	// A run without a persisted selection never started a repository helper.
	// Current machine branches, links and writable remotes cannot select one.
	checkout, err = m.store.GetAgentRunRepository(ctx, run.ID)
	if err != nil {
		return err
	}
	run.RepositorySelection = checkout
	if err := validateTerminalCleanupCheckout(machine, checkout, run); err != nil {
		return err
	}

	m.cancelActiveRunContexts(machine.ID, &run.ID)
	result, err := m.runtime.CleanupAgentRun(ctx, machine, services, checkout, run)
	if err != nil {
		return err
	}
	if result.Status == "conflict" {
		// A later owner edit has precedence. Persist its explanation before
		// acknowledging cleanup; failure requeues this idempotent recovery.
		return m.createEvent(ctx, machine, &run.ID, "agent", "agent_run.cleanup_source_conflict", map[string]any{"reason": result.Reason})
	}
	return nil
}
