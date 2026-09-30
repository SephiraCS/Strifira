package machine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/agent"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestAgentRequiredTestsControlRunSuccessAndPublication(t *testing.T) {
	for _, test := range []struct {
		name       string
		execution  *CommandExecution
		err        error
		wantStatus domain.DevMachineAgentRunStatus
	}{
		{"passes", &CommandExecution{ExitCode: 0}, nil, domain.DevMachineAgentRunStatusSucceeded},
		{"nonzero exit", &CommandExecution{ExitCode: 2, Stdout: "test assertion failed"}, nil, domain.DevMachineAgentRunStatusFailed},
		{"missing executable", nil, errors.New("command unavailable"), domain.DevMachineAgentRunStatusFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, store, runtime, run := verificationFixture(t)
			run.PushBranch = true
			manager.globalForgejo = &GlobalForgejoCredentials{URL: "https://git.example.com", Token: "scoped-publish-test-token"}
			store.linkedRepository = &domain.ForgejoRepoModel{InstanceURL: "https://git.example.com"}
			command := json.RawMessage(`["go","test","./..."]`)
			run.TestCommand = &command
			runtime.workspaceExecution, runtime.workspaceCommandErr = test.execution, test.err
			require.NoError(t, manager.runAgent(t.Context(), store.machine, &domain.DevMachineOperation{AgentRunID: &run.ID, Generation: 1}))
			require.Equal(t, [][]string{{"go", "test", "./..."}}, runtime.workspaceCommands)
			require.Equal(t, test.wantStatus, store.completedRun.Status)
			if test.wantStatus == domain.DevMachineAgentRunStatusSucceeded {
				require.Equal(t, "passed", store.completedRun.TestStatus)
				require.Equal(t, 1, runtime.publishedBranches)
			} else {
				require.Equal(t, "failed", store.completedRun.TestStatus)
				require.Zero(t, runtime.publishedBranches)
			}
			require.NotContains(t, runtime.agentSecrets, "FORGEJO_TOKEN")
		})
	}
}

func TestAgentVerificationBlocksForbiddenChangesAndUnexpectedBranchBeforePublishing(t *testing.T) {
	for _, test := range []struct {
		name, branch string
		files        []string
		wantError    string
	}{
		{"forbidden dirty file", "ku/task", []string{"src/main.go", "secrets/token"}, "forbidden paths changed"},
		{"forbidden new environment file", "ku/task", []string{".env"}, "forbidden paths changed"},
		{"wrong branch", "main", []string{"src/main.go"}, "changed the working branch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, store, runtime, run := verificationFixture(t)
			run.PushBranch = true
			run.ForbiddenPaths = json.RawMessage(`[".env","secrets/"]`)
			runtime.gitState = GitState{Branch: test.branch, ChangedFiles: test.files, Dirty: true}
			require.NoError(t, manager.runAgent(t.Context(), store.machine, &domain.DevMachineOperation{AgentRunID: &run.ID, Generation: 1}))
			require.Equal(t, domain.DevMachineAgentRunStatusFailed, store.completedRun.Status)
			require.Contains(t, *store.completedRun.ErrorMessage, test.wantError)
			require.Zero(t, runtime.publishedBranches)
			require.NotContains(t, runtime.agentSecrets, "FORGEJO_TOKEN")
		})
	}
}

func TestAgentPublishesOnlyVerifiedSelectedBranch(t *testing.T) {
	manager, store, runtime, run := verificationFixture(t)
	manager.globalForgejo = &GlobalForgejoCredentials{URL: "https://git.example.com", Token: "scoped-publish-test-token"}
	store.linkedRepository = &domain.ForgejoRepoModel{InstanceURL: "https://git.example.com"}
	run.PushBranch = true
	run.ForbiddenPaths = json.RawMessage(`[".env","secrets/"]`)
	runtime.gitState = GitState{Branch: "ku/task", ChangedFiles: []string{"src/main.go"}}
	require.NoError(t, manager.runAgent(t.Context(), store.machine, &domain.DevMachineOperation{AgentRunID: &run.ID, Generation: 1}))
	require.Equal(t, domain.DevMachineAgentRunStatusSucceeded, store.completedRun.Status)
	require.Equal(t, 1, runtime.publishedBranches)
	require.NotContains(t, runtime.agentSecrets, "FORGEJO_TOKEN", "publishing credentials must never be passed to a model process")
	require.Equal(t, "not_run", store.completedRun.TestStatus)
}

func TestAgentTraceIsPersistedWhileProviderIsRunningWithoutDuplicateFinalLogs(t *testing.T) {
	manager, store, runtime, run := verificationFixture(t)
	runtime.onAgentRun = func(ctx context.Context) {
		observer, ok := ctx.Value(agentLogObserverKey{}).(agentLogObserver)
		require.True(t, ok)
		observer("test-agent", "stdout", runtime.agentExecution.Stdout+"\n")
		require.Nil(t, store.completedRun, "run has not completed yet")
		require.Len(t, store.logChunks, 1)
		require.Len(t, store.events, 1)
		observer("test-agent", "stderr", "still running\n")
	}
	require.NoError(t, manager.runAgent(t.Context(), store.machine, &domain.DevMachineOperation{AgentRunID: &run.ID, Generation: 1}))
	require.Len(t, store.logChunks, 2, "terminal capture must not duplicate active log chunks")
	require.Equal(t, int64(1), store.logChunks[0].Sequence)
	require.Equal(t, int64(1), store.logChunks[1].Sequence)
	providerEvents := 0
	for _, event := range store.events {
		if event.EventType == "text" {
			providerEvents++
		}
	}
	require.Equal(t, 1, providerEvents)
}

func TestRequestedPullRequestFailureIsNotReportedAsRunSuccess(t *testing.T) {
	manager, store, runtime, run := verificationFixture(t)
	manager.globalForgejo = &GlobalForgejoCredentials{URL: "https://git.example.com", Token: "scoped-publish-test-token"}
	manager.forgejo = forgejoAPIFake{pullRequestErr: errors.New("Forgejo unavailable")}
	store.linkedRepository = &domain.ForgejoRepoModel{InstanceURL: "https://git.example.com"}
	run.PushBranch, run.OpenPullRequest = true, true
	require.NoError(t, manager.runAgent(t.Context(), store.machine, &domain.DevMachineOperation{AgentRunID: &run.ID, Generation: 1}))
	require.Equal(t, 1, runtime.publishedBranches)
	require.Equal(t, domain.DevMachineAgentRunStatusFailed, store.completedRun.Status)
	require.ErrorContains(t, errors.New(*store.completedRun.ErrorMessage), "Pull request creation failed")
}

func TestForbiddenPathMatchingUsesDirectoriesAndGlobs(t *testing.T) {
	violations, err := forbiddenChangedPaths(json.RawMessage(`["secrets/","*.pem",".env"]`), []string{"src/secrets.go", "secrets/key", "key.pem", ".env", ".environment"})
	require.NoError(t, err)
	require.Equal(t, []string{"secrets/key", "key.pem", ".env"}, violations)
	_, err = forbiddenChangedPaths(json.RawMessage(`["["]`), []string{"file"})
	require.Error(t, err)
}

func TestRootWorkspaceTaskCanRunWithoutGitAndDoesNotClaimPathVerification(t *testing.T) {
	manager, store, runtime, run := verificationFixture(t)
	run.ForbiddenPaths = json.RawMessage(`[".env","secrets/"]`)
	runtime.gitStateErr = errors.New("workspace is not a Git repository")
	require.NoError(t, manager.runAgent(t.Context(), store.machine, &domain.DevMachineOperation{AgentRunID: &run.ID, Generation: 1}))
	require.Equal(t, domain.DevMachineAgentRunStatusSucceeded, store.completedRun.Status)
	require.Contains(t, string(store.completedRun.RiskNotes), "could not be verified")
	require.Zero(t, runtime.publishedBranches)
}

func TestWorkspaceCommandPreservesArgvAndActualExitStatus(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "command.pid")
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", workspaceCommandScript, "ku-command", pidFile, "/bin/sh", "-c", `printf '%s' "$1"; exit 7`, "test", "spaces; $(not executed)")
	output, err := cmd.Output()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 7, exitErr.ExitCode())
	require.Equal(t, "spaces; $(not executed)", string(output))
	require.NoFileExists(t, pidFile)
}

func TestWorkspaceCommandCancellationTerminatesItsProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "command.pid")
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", workspaceCommandScript, "ku-command", pidFile, "/bin/sh", "-c", "sleep 30 & wait")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	require.Eventually(t, func() bool { _, err := os.Stat(pidFile); return err == nil }, 2*time.Second, 10*time.Millisecond)
	stop := exec.CommandContext(t.Context(), "/bin/sh", "-c", cancelWorkspaceCommandScript, "ku-stop", pidFile)
	require.NoError(t, stop.Run())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		require.Error(t, err, "the test should exit on its termination signal")
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled command process group is still running")
	}
	require.NoFileExists(t, pidFile)
}

func verificationFixture(t *testing.T) (*Manager, *managerStoreFake, *runtimeFake, *domain.DevMachineAgentRun) {
	t.Helper()
	machineID, workspaceID, runID := uuid.New(), uuid.New(), uuid.New()
	run := &domain.DevMachineAgentRun{ID: runID, MachineID: machineID, WorkspaceID: workspaceID, ProviderID: "opencode", Mode: "autonomous", Status: domain.DevMachineAgentRunStatusQueued, MaxRuntimeSeconds: 30}
	store := &managerStoreFake{machine: &domain.DevMachine{ID: machineID, WorkspaceID: workspaceID, DesiredStatus: domain.DevMachineStatusRunning, Generation: 1, WorkingBranch: "ku/task", RepoOwner: "octo", RepoName: "repo"}, agentRun: run, provider: &domain.DevMachineAgentProvider{ProviderID: "opencode", ImageRef: "ku/opencode:test"}}
	runtime := &runtimeFake{agentExecution: &AgentExecution{ContainerID: "test-agent", Stdout: `{"type":"text","part":{"type":"text","text":"Completed requested changes"}}`}, gitState: GitState{Branch: "ku/task"}}
	manager := NewManager(store, runtime, agent.NewRegistry(agent.NewOpenCodeProvider("ku/opencode:test")), forgejoAPIFake{}, nil, make([]byte, 32), make([]byte, 32), "test")
	return manager, store, runtime, run
}
