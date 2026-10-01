package machine

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
)

func authorizedRunRepository(machine *domain.DevMachine, checkout *domain.DevMachineCheckout, runID uuid.UUID) (string, string, error) {
	if machine == nil || machine.ID == uuid.Nil || runID == uuid.Nil {
		return "", "", errors.New("repository run identity is unavailable")
	}
	if checkout != nil && (checkout.MachineID != uuid.Nil && checkout.MachineID != machine.ID || checkout.WorkspaceID != uuid.Nil && checkout.WorkspaceID != machine.WorkspaceID) {
		return "", "", errors.New("repository checkout belongs to another machine or workspace")
	}
	repository, parsed, err := protectedCheckoutRepository(checkout)
	if err != nil {
		return "", "", err
	}
	return repository, parsed.Host, nil
}

// Pin the authorized base and parent once per run, before starting a provider.
// Only this immutable helper receives repository credentials.
func (r *DockerRuntime) PrepareAgentRepository(ctx context.Context, machine *domain.DevMachine, services []domain.DevMachineService, checkout *domain.DevMachineCheckout, runID uuid.UUID, token string) error {
	repository, host, err := authorizedRunRepository(machine, checkout, runID)
	if err != nil {
		return err
	}
	if token == "" {
		return errors.New("repository credential is unavailable")
	}
	_, err = r.runRepositoryHelper(ctx, machine, services, isolatedGitCommand(prepareProtectedRepositoryScript,
		"ku-pin-agent-repository", repository, strings.TrimSuffix(checkout.RepositoryURL, ".git"), checkout.BaseBranch, checkout.WorkingBranch,
		host, checkout.RepositoryFullName, "http://"+machine.RoutingKey+"-egress:3128", runID.String()), token, repositoryHelperInvocation{RunID: runID})
	return err
}

func (r *DockerRuntime) RetainAgentCandidate(ctx context.Context, machine *domain.DevMachine, services []domain.DevMachineService, checkout *domain.DevMachineCheckout, runID uuid.UUID) (RepositoryCandidate, error) {
	repository, _, err := authorizedRunRepository(machine, checkout, runID)
	if err != nil {
		return RepositoryCandidate{}, err
	}
	output, err := r.runRepositoryHelper(ctx, machine, services, isolatedGitCommand(retainRepositoryCandidateScript,
		"ku-retain-agent-candidate", checkout.WorkspacePath, repository, runID.String()), "", repositoryHelperInvocation{RunID: runID})
	if err != nil {
		return RepositoryCandidate{}, err
	}
	return parseRepositoryCandidate(output)
}

func (r *DockerRuntime) PublishAgentCandidate(ctx context.Context, machine *domain.DevMachine, services []domain.DevMachineService, checkout *domain.DevMachineCheckout, runID uuid.UUID, candidate RepositoryCandidate, token string) error {
	repository, host, err := authorizedRunRepository(machine, checkout, runID)
	if err != nil {
		return err
	}
	if token == "" || !validGitObjectID(candidate.Commit) || candidate.Branch != checkout.WorkingBranch {
		return errors.New("publication requires the selected candidate, branch and credential")
	}
	_, err = r.runRepositoryHelper(ctx, machine, services, isolatedGitCommand(publishRetainedRepositoryCandidateScript,
		"ku-publish-agent-candidate", repository, strings.TrimSuffix(checkout.RepositoryURL, ".git"), checkout.WorkingBranch,
		host, checkout.RepositoryFullName, "http://"+machine.RoutingKey+"-egress:3128", runID.String(), candidate.Commit), token, repositoryHelperInvocation{RunID: runID})
	return err
}
