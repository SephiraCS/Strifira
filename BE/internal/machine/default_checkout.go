package machine

import (
	"context"
	"errors"
	"strings"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
)

// The implicit repository has one identity shared by provisioning, agent runs
// and terminal cleanup. No writable workspace metadata selects its authority.
func defaultRepositoryCheckout(machine *domain.DevMachine, instanceURL string) (*domain.DevMachineCheckout, error) {
	if machine == nil || machine.ID == uuid.Nil || machine.WorkspaceID == uuid.Nil {
		return nil, errors.New("default repository machine authority is unavailable")
	}
	checkout := &domain.DevMachineCheckout{ID: machine.ID, MachineID: machine.ID, WorkspaceID: machine.WorkspaceID,
		RepositoryFullName: checkoutRepositoryFullName(machine, nil),
		BaseBranch:         machine.BaseBranch, WorkingBranch: machine.WorkingBranch, WorkspacePath: "/workspace/repository"}
	checkout.RepositoryURL = strings.TrimRight(instanceURL, "/") + "/" + checkout.RepositoryFullName
	if machine.RepoURL == "" || strings.TrimSuffix(machine.RepoURL, ".git") != checkout.RepositoryURL {
		return nil, errors.New("default repository no longer matches its original GitHub URL; use a new machine for a different repository")
	}
	if _, _, err := protectedCheckoutRepository(checkout); err != nil {
		return nil, err
	}
	return checkout, nil
}

func (m *Manager) defaultRepositoryProvision(ctx context.Context, machine *domain.DevMachine) (*RepositoryProvision, error) {
	if machine.RepoOwner == "" && machine.RepoName == "" {
		return nil, nil
	}
	instanceURL, token, err := m.githubCredential(ctx, machine, checkoutRepositoryFullName(machine, nil))
	if err != nil {
		return nil, err
	}
	checkout, err := defaultRepositoryCheckout(machine, instanceURL)
	if err != nil {
		return nil, err
	}
	return &RepositoryProvision{Checkout: checkout, Token: token}, nil
}
