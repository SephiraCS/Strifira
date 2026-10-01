package machine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestAgentRepositoryAPIsRejectForeignAuthorityBeforeCallingDocker(t *testing.T) {
	machine := fixtureHelperMachine()
	checkout := domain.DevMachineCheckout{ID: uuid.New(), MachineID: machine.ID, WorkspaceID: machine.WorkspaceID,
		RepositoryURL: "https://git.example.test/owner/repo", RepositoryFullName: "owner/repo",
		BaseBranch: "main", WorkingBranch: "ku/task", WorkspacePath: "/workspace/repository"}
	runID := uuid.New()
	for _, test := range []struct {
		name   string
		change func(*domain.DevMachineCheckout)
	}{
		{"foreign machine", func(c *domain.DevMachineCheckout) { c.MachineID = uuid.New() }},
		{"foreign workspace", func(c *domain.DevMachineCheckout) { c.WorkspaceID = uuid.New() }},
		{"foreign URL", func(c *domain.DevMachineCheckout) { c.RepositoryURL = "https://git.example.test/foreign/repo" }},
		{"URL credential", func(c *domain.DevMachineCheckout) { c.RepositoryURL = "https://token@git.example.test/owner/repo" }},
		{"external worktree", func(c *domain.DevMachineCheckout) { c.WorkspacePath = "/repositories/owned" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected := checkout
			test.change(&selected)
			runtime := &DockerRuntime{} // Any Docker access would panic.
			require.Error(t, runtime.PrepareAgentRepository(t.Context(), machine, nil, &selected, runID, "fixture-token"))
			_, err := runtime.RetainAgentCandidate(t.Context(), machine, nil, &selected, runID)
			require.Error(t, err)
			candidate := RepositoryCandidate{GitState: GitState{Branch: checkout.WorkingBranch}, Commit: strings.Repeat("a", 40)}
			require.Error(t, runtime.PublishAgentCandidate(t.Context(), machine, nil, &selected, runID, candidate, "fixture-token"))
			_, err = runtime.SynchronizeAgentCandidate(t.Context(), machine, nil, &selected, runID, candidate)
			require.Error(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			_, err = runtime.RunCandidateCommand(ctx, machine, nil, &selected, runID, candidate, []string{"true"})
			require.Error(t, err)
		})
	}
}

func TestAgentRepositoryPreparationCannotUseMissingAuthority(t *testing.T) {
	machine := fixtureHelperMachine()
	checkout := &domain.DevMachineCheckout{ID: uuid.New(), RepositoryURL: "https://git.example.test/owner/repo", RepositoryFullName: "owner/repo",
		BaseBranch: "main", WorkingBranch: "ku/task", WorkspacePath: "/workspace/repository"}
	runtime := &DockerRuntime{}
	require.Error(t, runtime.PrepareAgentRepository(t.Context(), nil, nil, checkout, uuid.New(), "fixture-token"))
	require.Error(t, runtime.PrepareAgentRepository(t.Context(), machine, nil, checkout, uuid.Nil, "fixture-token"))
	require.Error(t, runtime.PrepareAgentRepository(t.Context(), machine, nil, checkout, uuid.New(), ""))
}
