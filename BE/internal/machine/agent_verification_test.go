package machine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
			run.TestCommand = new(json.RawMessage(`["go","test","./..."]`))
			runtime.candidate.GitState = GitState{Branch: test.branch, ChangedFiles: test.files, Dirty: true}
			require.NoError(t, manager.runAgent(t.Context(), store.machine, &domain.DevMachineOperation{AgentRunID: &run.ID, Generation: 1}))
			require.Equal(t, domain.DevMachineAgentRunStatusFailed, store.completedRun.Status)
			require.Contains(t, *store.completedRun.ErrorMessage, test.wantError)
			require.Zero(t, runtime.publishedBranches)
			require.Empty(t, runtime.workspaceCommands, "authorization checks must precede executing candidate tests")
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
	runtime.candidate.GitState = GitState{Branch: "ku/task", ChangedFiles: []string{"src/main.go"}}
	require.NoError(t, manager.runAgent(t.Context(), store.machine, &domain.DevMachineOperation{AgentRunID: &run.ID, Generation: 1}))
	require.Equal(t, domain.DevMachineAgentRunStatusSucceeded, store.completedRun.Status)
	require.Equal(t, 1, runtime.publishedBranches)
	require.NotContains(t, runtime.agentSecrets, "FORGEJO_TOKEN", "publishing credentials must never be passed to a model process")
	require.Equal(t, "not_run", store.completedRun.TestStatus)
}

func TestAgentTestsAndPublishesRetainedCandidateDespiteLaterSourceChanges(t *testing.T) {
	manager, store, runtime, run := verificationFixture(t)
	run.PushBranch = true
	run.TestCommand = new(json.RawMessage(`["go","test","./..."]`))
	runtime.workspaceExecution = &CommandExecution{}
	runtime.candidate.Commits = []string{runtime.candidate.Commit}
	runtime.candidate.ChangedFiles = []string{"src/main.go"}
	retained := runtime.candidate
	runtime.onCandidateCommand = func() {
		// A later workspace snapshot has another branch, commit and file list.
		// Neither publication nor the persisted result may re-read it.
		runtime.candidate = RepositoryCandidate{GitState: GitState{Branch: "later-work", ChangedFiles: []string{".env"}}, Commit: strings.Repeat("e", 40)}
	}
	require.NoError(t, manager.runAgent(t.Context(), store.machine, &domain.DevMachineOperation{AgentRunID: &run.ID, Generation: 1}))
	require.Equal(t, domain.DevMachineAgentRunStatusSucceeded, store.completedRun.Status)
	require.Equal(t, []string{"prepare", "agent", "retain", "test", "publish", "sync"}, runtime.repositoryCalls)
	require.Equal(t, retained, runtime.testedCandidate)
	require.Equal(t, retained, runtime.publishedCandidate)
	require.Equal(t, retained, runtime.synchronizedCandidate)
	require.Equal(t, run.ID, runtime.preparedRunID)
	require.Equal(t, run.ID, runtime.testedRunID)
	require.Equal(t, run.ID, runtime.publishedRunID)
	require.JSONEq(t, `["`+retained.Commit+`"]`, string(store.completedRun.Commits))
	require.JSONEq(t, `["src/main.go"]`, string(store.completedRun.ChangedFiles))
	require.Equal(t, "/workspace/repository", runtime.agentCheckout.WorkspacePath)
	require.NotContains(t, runtime.agentSecrets, "FORGEJO_TOKEN")
}

func TestAgentAttestationRefusalPreservesActualLogsAndBlocksPublication(t *testing.T) {
	manager, store, runtime, run := verificationFixture(t)
	run.PushBranch = true
	run.TestCommand = new(json.RawMessage(`["go","test","./..."]`))
	runtime.workspaceExecution = &CommandExecution{Stdout: "completed test output\n", Stderr: "test diagnostic\n"}
	runtime.workspaceCommandErr = errors.New("tests changed tracked candidate content")
	require.NoError(t, manager.runAgent(t.Context(), store.machine, &domain.DevMachineOperation{AgentRunID: &run.ID, Generation: 1}))
	require.Equal(t, domain.DevMachineAgentRunStatusFailed, store.completedRun.Status)
	require.Equal(t, "failed", store.completedRun.TestStatus)
	require.Contains(t, *store.completedRun.ErrorMessage, "tests changed tracked candidate content")
	require.Equal(t, []string{"prepare", "agent", "retain", "test"}, runtime.repositoryCalls)
	require.Zero(t, runtime.publishedBranches)
	var testLogs string
	for _, chunk := range store.logChunks {
		if chunk.Stream == "tests" {
			testLogs += chunk.Content
		}
	}
	require.Equal(t, "completed test output\ntest diagnostic\n", testLogs)
	for _, event := range store.events {
		if event.EventType == "agent_run.tests_completed" {
			require.Contains(t, string(event.Payload), `"verified":false`)
		}
	}
}

func TestAgentRepositoryPreparationFailureNeverStartsProvider(t *testing.T) {
	manager, store, runtime, run := verificationFixture(t)
	runtime.prepareRepositoryErr = errors.New("denied scoped-publish-test-token")
	require.NoError(t, manager.runAgent(t.Context(), store.machine, &domain.DevMachineOperation{AgentRunID: &run.ID, Generation: 1}))
	require.Equal(t, domain.DevMachineAgentRunStatusFailed, store.completedRun.Status)
	require.Equal(t, []string{"prepare"}, runtime.repositoryCalls)
	require.Zero(t, runtime.agentRuns)
	require.NotContains(t, *store.completedRun.ErrorMessage, "scoped-publish-test-token")
}

func TestAgentCandidateRetentionFailureCannotFallBackToUnverifiedSuccess(t *testing.T) {
	manager, store, runtime, run := verificationFixture(t)
	run.TestCommand = new(json.RawMessage(`["go","test","./..."]`))
	runtime.retainCandidateErr = errors.New("retained objects are unavailable")
	require.NoError(t, manager.runAgent(t.Context(), store.machine, &domain.DevMachineOperation{AgentRunID: &run.ID, Generation: 1}))
	require.Equal(t, domain.DevMachineAgentRunStatusFailed, store.completedRun.Status)
	require.Contains(t, *store.completedRun.ErrorMessage, "retained objects are unavailable")
	require.Equal(t, []string{"prepare", "agent", "retain"}, runtime.repositoryCalls)
	require.Empty(t, runtime.workspaceCommands)
	require.Zero(t, runtime.publishedBranches)
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
	store.machine.RepoOwner, store.machine.RepoName = "", ""
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

func TestWorkspaceCommandPreservesInputInItsProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "command.pid")
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", workspaceCommandScript, "ku-command", pidFile, "/bin/sh", "-c", `IFS= read -r input; printf '%s' "$input"`)
	cmd.Stdin = strings.NewReader("disposable-token\n")
	output, err := cmd.Output()
	require.NoError(t, err)
	require.Equal(t, "disposable-token", string(output))
}

func TestWorkspaceCommandCancellationBeforePIDPublicationCannotBeLost(t *testing.T) {
	dir := t.TempDir()
	pidFile, modified := filepath.Join(dir, "command.pid"), filepath.Join(dir, "modified")
	stop := exec.CommandContext(t.Context(), "/bin/sh", "-c", cancelWorkspaceCommandScript, "ku-stop", pidFile)
	require.NoError(t, stop.Run())
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", workspaceCommandScript, "ku-command", pidFile, "/bin/sh", "-c", `printf changed > "$1"`, "command", modified)
	err := cmd.Run()
	require.Error(t, err)
	require.NoFileExists(t, modified, "cancelled test code must not start")
	require.NoFileExists(t, pidFile)
	require.NoFileExists(t, pidFile+".cancel")
}

func verificationFixture(t *testing.T) (*Manager, *managerStoreFake, *runtimeFake, *domain.DevMachineAgentRun) {
	t.Helper()
	machineID, workspaceID, runID := uuid.New(), uuid.New(), uuid.New()
	run := &domain.DevMachineAgentRun{ID: runID, MachineID: machineID, WorkspaceID: workspaceID, ProviderID: "opencode", Mode: "autonomous", Status: domain.DevMachineAgentRunStatusQueued, MaxRuntimeSeconds: 30}
	store := &managerStoreFake{machine: &domain.DevMachine{ID: machineID, WorkspaceID: workspaceID, DesiredStatus: domain.DevMachineStatusRunning, Generation: 1, WorkingBranch: "ku/task", BaseBranch: "main", RepoOwner: "octo", RepoName: "repo", RepoURL: "https://git.example.com/octo/repo"}, agentRun: run, provider: &domain.DevMachineAgentProvider{ProviderID: "opencode", ImageRef: "ku/opencode:test"}}
	runtime := &runtimeFake{agentExecution: &AgentExecution{ContainerID: "test-agent", Stdout: `{"type":"text","part":{"type":"text","text":"Completed requested changes"}}`}, candidate: RepositoryCandidate{GitState: GitState{Branch: "ku/task"}, Commit: strings.Repeat("a", 40), BaseCommit: strings.Repeat("b", 40), SourceCommit: strings.Repeat("c", 40), SourceIndexHash: strings.Repeat("d", 64)}}
	store.linkedRepository = &domain.ForgejoRepoModel{InstanceURL: "https://git.example.com"}
	manager := NewManager(store, runtime, agent.NewRegistry(agent.NewOpenCodeProvider("ku/opencode:test")), forgejoAPIFake{}, &GlobalForgejoCredentials{URL: "https://git.example.com", Token: "scoped-publish-test-token"}, make([]byte, 32), make([]byte, 32), "test")
	return manager, store, runtime, run
}

func TestAgentVerificationTimeoutEstablishesFenceBeforeCompletion(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		t.Run(fmt.Sprint(cleanupFails), func(t *testing.T) {
			manager, store, runtime, run := verificationFixture(t)
			run.MaxRuntimeSeconds = 1
			run.PushBranch = true
			command := json.RawMessage(`["true"]`)
			run.TestCommand = &command
			tested := false
			runtime.onCandidateCommandContext = func(ctx context.Context) {
				tested = true
				<-ctx.Done()
				runtime.workspaceCommandErr = ctx.Err()
			}
			cleanupCalls := 0
			runtime.onCancelAgent = func(ctx context.Context, machine *domain.DevMachine, current *domain.DevMachineAgentRun) error {
				cleanupCalls++
				require.Nil(t, ctx.Err(), "cleanup must receive a fresh bounded context after execution expires")
				require.Equal(t, store.machine.ID, machine.ID)
				require.Equal(t, run.ID, current.ID)
				require.Equal(t, domain.DevMachineAgentRunStatusTimeout, current.Status)
				require.Nil(t, store.completedRun, "cleanup must establish its fence before persisting timeout")
				if cleanupFails {
					return errors.New("daemon cleanup failed")
				}
				return nil
			}
			err := manager.runAgent(t.Context(), store.machine, &domain.DevMachineOperation{AgentRunID: &run.ID, MachineID: store.machine.ID, WorkspaceID: store.machine.WorkspaceID, Generation: store.machine.Generation})
			require.True(t, tested)
			require.Equal(t, 1, cleanupCalls)
			require.Zero(t, runtime.publishedBranches)
			if cleanupFails {
				require.ErrorContains(t, err, "agent_timeout_cleanup_failed")
				require.Nil(t, store.completedRun)
			} else {
				require.NoError(t, err)
				require.NotNil(t, store.completedRun)
				require.Equal(t, domain.DevMachineAgentRunStatusTimeout, store.completedRun.Status)
			}
		})
	}
}

func TestAgentRepositorySelectionIsPersistedBeforeAnyHelperAndSurvivesMachineChanges(t *testing.T) {
	manager, store, runtime, run := verificationFixture(t)
	checkout, err := manager.prepareAgentRepository(t.Context(), store.machine, nil, nil, run.ID)
	require.NoError(t, err)
	require.NotNil(t, store.pinnedRepository)
	require.Equal(t, checkout.RepositoryURL, store.pinnedRepository.RepositoryURL)
	selected := *checkout
	store.machine.BaseBranch, store.machine.WorkingBranch = "later-base", "later-issue"
	store.machine.RepoOwner, store.machine.RepoName = "another", "repo"
	store.machine.RepoURL = "https://git.example.com/another/repo"
	checkout, err = manager.prepareAgentRepository(t.Context(), store.machine, nil, nil, run.ID)
	require.NoError(t, err)
	require.Equal(t, selected, *checkout)
	require.Equal(t, []string{"prepare", "prepare"}, runtime.repositoryCalls)

	store.pinnedRepository = nil
	store.pinRepositoryError = errors.New("selection write failed")
	_, err = manager.prepareAgentRepository(t.Context(), store.machine, nil, nil, run.ID)
	require.ErrorContains(t, err, "selection could not be persisted")
	require.Equal(t, []string{"prepare", "prepare"}, runtime.repositoryCalls, "no helper may leave artifacts without a durable selection")
}

func TestAgentPinsExplicitCheckoutWhoseCanonicalURLIsNotStoredInCheckoutTable(t *testing.T) {
	manager, store, _, run := verificationFixture(t)
	checkout := &domain.DevMachineCheckout{ID: uuid.New(), MachineID: store.machine.ID, WorkspaceID: store.machine.WorkspaceID,
		RepositoryFullName: "ku/api", BaseBranch: "main", WorkingBranch: "ku/task", WorkspacePath: "/workspace/tasks/pin-1"}
	run.CheckoutID = &checkout.ID
	selected, err := manager.prepareAgentRepository(t.Context(), store.machine, checkout, nil, run.ID)
	require.NoError(t, err)
	require.Equal(t, "https://git.example.com/ku/api", selected.RepositoryURL)
	require.Equal(t, selected.RepositoryURL, store.pinnedRepository.RepositoryURL)
	require.Empty(t, checkout.RepositoryURL, "the ordinary checkout row does not store this derived URL")
}
