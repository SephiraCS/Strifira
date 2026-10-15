package machine

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/agent"
	"github.com/SephiraCS/strifira-backend/internal/domain"
	cryptoutil "github.com/SephiraCS/strifira-backend/pkg/crypto"
	githubclient "github.com/SephiraCS/strifira-backend/pkg/github"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type managerStoreFake struct {
	machine                   *domain.DevMachine
	services                  []domain.DevMachineService
	states                    []domain.DevMachineStatus
	events                    []domain.DevMachineEvent
	idle                      []domain.DevMachine
	permanentDeleteMachines   []domain.DevMachine
	permanentDeleteRequests   int
	purgedMachineIDs          []uuid.UUID
	desired                   []domain.DevMachineStatus
	actions                   []domain.DevMachineOperationAction
	checkout                  *domain.DevMachineCheckout
	pinnedRepository          *domain.DevMachineCheckout
	pinRepositoryError        error
	checkoutStatus            string
	checkoutError             *string
	terminalSession           *domain.DevMachineTerminalSession
	completedTerminalIDs      []uuid.UUID
	failedTerminalIDs         []uuid.UUID
	environment               *domain.DevMachineEnvironment
	deleteRequestedEnvs       []domain.DevMachineEnvironment
	deletedEnvironmentIDs     []uuid.UUID
	installationFullNameCalls []string
	linkedRepository          *domain.GitHubRepoModel
	githubConnection          *domain.GitHubConnection
	githubWorkspaceDisable    *domain.GitHubWorkspaceDisable
	agentRun                  *domain.DevMachineAgentRun
	provider                  *domain.DevMachineAgentProvider
	completedRun              *domain.DevMachineAgentRun
	completeAgentRunErr       error
	runtimeCredentials        []domain.DevMachineRuntimeCredential
	purgedCredentialNow       *time.Time
	purgedAccessLogBefore     *time.Time
	purgedAccessLogLimit      int
	revokedMachineIDs         []uuid.UUID
	failedOperationCode       string
	failedOperationMessage    string
	failedOperationRetryable  *bool
	failOperationWillRetry    *bool
	completedOperations       int
	orphanedEnvironmentCount  int
	orphanReconcileCalls      int
	orphanedCheckoutCount     int
	checkoutReconcileCalls    int
	updateAgentRunStarted     func() error
	envVarListCalls           int
	runtimeServicesCreated    int
	timedOutRuns              []domain.DevMachineAgentRun
	logChunks                 []domain.DevMachineLogChunk
}

func (f *managerStoreFake) LeaseOperations(context.Context, string, int, time.Duration) ([]domain.DevMachineOperation, error) {
	return nil, nil
}
func (f *managerStoreFake) RenewOperationLease(context.Context, uuid.UUID, string, time.Duration) error {
	return nil
}
func (f *managerStoreFake) CompleteOperation(context.Context, uuid.UUID, string) error {
	f.completedOperations++
	return nil
}
func (f *managerStoreFake) FailOperation(_ context.Context, _ uuid.UUID, _ string, code, message string, retryable bool) (bool, error) {
	f.failedOperationCode = code
	f.failedOperationMessage = message
	f.failedOperationRetryable = &retryable
	if f.failOperationWillRetry != nil {
		return *f.failOperationWillRetry, nil
	}
	return retryable, nil
}
func (f *managerStoreFake) GetMachineInternal(context.Context, uuid.UUID) (*domain.DevMachine, error) {
	return f.machine, nil
}
func (f *managerStoreFake) GetProvider(context.Context, uuid.UUID, uuid.UUID, string) (*domain.DevMachineAgentProvider, error) {
	return f.provider, nil
}
func (f *managerStoreFake) GetAgentRunInternal(context.Context, uuid.UUID) (*domain.DevMachineAgentRun, error) {
	return f.agentRun, nil
}
func (f *managerStoreFake) GetAgentRunRepository(context.Context, uuid.UUID) (*domain.DevMachineCheckout, error) {
	return f.pinnedRepository, nil
}
func (f *managerStoreFake) PinAgentRunRepository(_ context.Context, _ uuid.UUID, checkout *domain.DevMachineCheckout) error {
	if f.pinRepositoryError != nil {
		return f.pinRepositoryError
	}
	copy := *checkout
	f.pinnedRepository = &copy
	return nil
}
func (f *managerStoreFake) ListServices(context.Context, uuid.UUID, uuid.UUID) ([]domain.DevMachineService, error) {
	return f.services, nil
}
func (f *managerStoreFake) ListEnvVarsInternal(context.Context, uuid.UUID, *string, string) ([]domain.DevMachineEnvVar, error) {
	f.envVarListCalls++
	return nil, nil
}
func (f *managerStoreFake) SetMachineState(_ context.Context, _ uuid.UUID, status domain.DevMachineStatus, network, volume, _, _ *string) error {
	f.machine.Status = status
	if network != nil {
		f.machine.DockerNetworkName = network
	}
	if volume != nil {
		f.machine.WorkspaceVolumeName = volume
	}
	f.states = append(f.states, status)
	return nil
}
func (f *managerStoreFake) SetMachineStateForOperation(_ context.Context, _ uuid.UUID, generation int64, allowStale bool, status domain.DevMachineStatus, network, volume, _, _ *string) (bool, error) {
	if !allowStale && f.machine.Generation != generation {
		return false, nil
	}
	_ = f.SetMachineState(context.Background(), f.machine.ID, status, network, volume, nil, nil)
	return true, nil
}
func (f *managerStoreFake) UpdateServiceRuntime(_ context.Context, serviceID uuid.UUID, containerID, status, health string, _ *string) error {
	for i := range f.services {
		if f.services[i].ID == serviceID {
			f.services[i].ContainerID = &containerID
			f.services[i].Status = status
			f.services[i].HealthStatus = health
		}
	}
	return nil
}
func (f *managerStoreFake) CreateRuntimeService(context.Context, *domain.DevMachineService) error {
	f.runtimeServicesCreated++
	return nil
}
func (f *managerStoreFake) StartAgentRun(context.Context, uuid.UUID) (time.Time, error) {
	if f.updateAgentRunStarted != nil {
		if err := f.updateAgentRunStarted(); err != nil {
			return time.Time{}, err
		}
	}
	if f.agentRun != nil {
		f.agentRun.Status = domain.DevMachineAgentRunStatusRunning
		if f.agentRun.StartedAt == nil {
			f.agentRun.StartedAt = new(time.Now())
		}
		return *f.agentRun.StartedAt, nil
	}
	return time.Now(), nil
}

func (f *managerStoreFake) CompleteAgentRun(_ context.Context, run *domain.DevMachineAgentRun) error {
	if f.completeAgentRunErr != nil {
		return f.completeAgentRunErr
	}
	copy := *run
	f.completedRun = &copy
	return nil
}
func (f *managerStoreFake) CreateEvent(_ context.Context, event *domain.DevMachineEvent) error {
	f.events = append(f.events, *event)
	return nil
}
func (f *managerStoreFake) CreateLogChunk(_ context.Context, chunk *domain.DevMachineLogChunk) error {
	f.logChunks = append(f.logChunks, *chunk)
	return nil
}
func (f *managerStoreFake) RevokeMachineAccess(_ context.Context, machineID uuid.UUID) error {
	f.revokedMachineIDs = append(f.revokedMachineIDs, machineID)
	return nil
}
func (f *managerStoreFake) ListExpiredMachines(context.Context, int) ([]domain.DevMachine, error) {
	return nil, nil
}
func (f *managerStoreFake) ListTimedOutAgentRuns(context.Context, int) ([]domain.DevMachineAgentRun, error) {
	return f.timedOutRuns, nil
}
func (f *managerStoreFake) LeaseAgentRunCleanup(context.Context, string, time.Duration) (*domain.DevMachineAgentRun, error) {
	return nil, nil
}
func (f *managerStoreFake) CompleteAgentRunCleanup(context.Context, uuid.UUID, string) error {
	return nil
}
func (f *managerStoreFake) RetryAgentRunCleanup(context.Context, uuid.UUID, string, string) error {
	return nil
}
func (f *managerStoreFake) ListRuntimeMachines(context.Context, int, int) ([]domain.DevMachine, error) {
	return nil, nil
}

func (f *managerStoreFake) SetDesiredAndEnqueue(_ context.Context, _ uuid.UUID, _ uuid.UUID, desired domain.DevMachineStatus, operation *domain.DevMachineOperation) error {
	f.desired = append(f.desired, desired)
	f.actions = append(f.actions, operation.Action)
	return nil
}

func (f *managerStoreFake) RequestPermanentDelete(_ context.Context, workspaceID, machineID uuid.UUID, _ *uuid.UUID) (*domain.DevMachineOperation, error) {
	f.permanentDeleteRequests++
	var machine *domain.DevMachine
	if f.machine != nil && f.machine.ID == machineID && f.machine.WorkspaceID == workspaceID {
		machine = f.machine
	} else {
		for i := range f.permanentDeleteMachines {
			if f.permanentDeleteMachines[i].ID == machineID && f.permanentDeleteMachines[i].WorkspaceID == workspaceID {
				machine = &f.permanentDeleteMachines[i]
				break
			}
		}
	}
	if machine == nil {
		return nil, nil
	}
	if machine.DeleteRequestedAt == nil {
		now := time.Now().UTC()
		machine.DeleteRequestedAt = &now
	}
	if domain.DevMachineSafelyPurgeable(machine) {
		return nil, nil
	}
	operation := &domain.DevMachineOperation{
		ID: uuid.New(), MachineID: machineID, WorkspaceID: workspaceID,
		Action: domain.DevMachineOpTeardown, Status: domain.DevMachineOpStatusPending,
		Generation: machine.Generation + 1, IdempotencyKey: "permanent-delete:test", MaxAttempts: 10,
	}
	f.desired = append(f.desired, domain.DevMachineStatusDestroyed)
	f.actions = append(f.actions, operation.Action)
	machine.DesiredStatus = domain.DevMachineStatusDestroyed
	machine.Generation = operation.Generation
	return operation, nil
}

func (f *managerStoreFake) ListPermanentDeleteRequests(context.Context, int) ([]domain.DevMachine, error) {
	return f.permanentDeleteMachines, nil
}

func (f *managerStoreFake) PurgePermanentDeleteRequest(_ context.Context, _ uuid.UUID, machineID uuid.UUID) error {
	f.purgedMachineIDs = append(f.purgedMachineIDs, machineID)
	return nil
}

func (f *managerStoreFake) CreateResourceSample(context.Context, *domain.DevMachineResourceSample) error {
	return nil
}
func (f *managerStoreFake) UpdateVolumeUsage(context.Context, uuid.UUID, int64) error    { return nil }
func (f *managerStoreFake) CreateGitRef(context.Context, *domain.DevMachineGitRef) error { return nil }
func (f *managerStoreFake) GetGitHubConnection(context.Context, uuid.UUID) (*domain.GitHubConnection, error) {
	return f.githubConnection, nil
}
func (f *managerStoreFake) GetWorkspaceDisable(context.Context, uuid.UUID) (*domain.GitHubWorkspaceDisable, error) {
	return f.githubWorkspaceDisable, nil
}
func (f *managerStoreFake) GetLinkedRepositoryByFullName(_ context.Context, _ uuid.UUID, fullName string) (*domain.GitHubRepoModel, error) {
	f.installationFullNameCalls = append(f.installationFullNameCalls, fullName)
	return f.linkedRepository, nil
}
func (f *managerStoreFake) GetCheckoutInternal(context.Context, uuid.UUID) (*domain.DevMachineCheckout, error) {
	return f.checkout, nil
}
func (f *managerStoreFake) UpdateCheckoutState(_ context.Context, _ uuid.UUID, status string, lastError *string) error {
	f.checkoutStatus = status
	f.checkoutError = lastError
	if f.checkout != nil {
		f.checkout.Status = status
		f.checkout.LastError = lastError
	}
	return nil
}
func (f *managerStoreFake) GetTerminalSessionInternal(context.Context, uuid.UUID) (*domain.DevMachineTerminalSession, error) {
	return f.terminalSession, nil
}
func (f *managerStoreFake) CompleteTerminalSessionClose(_ context.Context, sessionID uuid.UUID) error {
	f.completedTerminalIDs = append(f.completedTerminalIDs, sessionID)
	if f.terminalSession != nil {
		f.terminalSession.Status = "closed"
	}
	return nil
}
func (f *managerStoreFake) FailTerminalSessionClose(_ context.Context, sessionID uuid.UUID) error {
	f.failedTerminalIDs = append(f.failedTerminalIDs, sessionID)
	if f.terminalSession != nil {
		f.terminalSession.Status = "close_failed"
	}
	return nil
}
func (f *managerStoreFake) GetEnvironment(context.Context, uuid.UUID, uuid.UUID) (*domain.DevMachineEnvironment, error) {
	return f.environment, nil
}
func (f *managerStoreFake) UpdateEnvironmentState(_ context.Context, _ uuid.UUID, status, imageRef string, digest *string) error {
	if f.environment != nil {
		f.environment.Status = status
		if imageRef != "" {
			f.environment.ImageRef = imageRef
		}
		f.environment.ImageDigest = digest
	}
	return nil
}
func (f *managerStoreFake) ReconcileOrphanedEnvironments(context.Context, int) (int, error) {
	f.orphanReconcileCalls++
	return f.orphanedEnvironmentCount, nil
}
func (f *managerStoreFake) ReconcileOrphanedCheckouts(context.Context, int) (int, error) {
	f.checkoutReconcileCalls++
	return f.orphanedCheckoutCount, nil
}
func (f *managerStoreFake) ListDeleteRequestedEnvironments(context.Context, int) ([]domain.DevMachineEnvironment, error) {
	return f.deleteRequestedEnvs, nil
}
func (f *managerStoreFake) DeleteEnvironment(_ context.Context, _ uuid.UUID, environmentID uuid.UUID) error {
	f.deletedEnvironmentIDs = append(f.deletedEnvironmentIDs, environmentID)
	return nil
}
func (f *managerStoreFake) ListIdleMachines(context.Context, int) ([]domain.DevMachine, error) {
	return f.idle, nil
}
func (f *managerStoreFake) UpsertRuntimeCredential(_ context.Context, credential *domain.DevMachineRuntimeCredential) error {
	f.runtimeCredentials = append(f.runtimeCredentials, *credential)
	return nil
}
func (f *managerStoreFake) PurgeExpiredRuntimeCredentials(_ context.Context, now time.Time) (int, error) {
	f.purgedCredentialNow = &now
	return 0, nil
}
func (f *managerStoreFake) PurgeAccessLogs(_ context.Context, before time.Time, limit int) (int, error) {
	f.purgedAccessLogBefore = &before
	f.purgedAccessLogLimit = limit
	return 0, nil
}

type githubAPIFake struct{ pullRequestErr error }

func (f githubAPIFake) CreatePullRequest(string, string, string, string, string, string, string, string) (*githubclient.PullRequest, error) {
	if f.pullRequestErr != nil {
		return nil, f.pullRequestErr
	}
	return &githubclient.PullRequest{HTMLURL: "https://git.example.com/strifira/api/pull/1", Number: 1}, nil
}

type runtimeFake struct {
	spawned, paused, stopped, tornDown int
	snapshots                          int
	agentRuns                          int
	terminatedTerminals                int
	terminatedSession                  *domain.DevMachineTerminalSession
	terminateTerminalErr               error
	deletedEnvironmentIDs              []uuid.UUID
	deleteEnvironmentImageErr          error
	environmentDeleteAttempts          int
	onSpawn                            func()
	spawnProvision                     *RepositoryProvision
	agentExecution                     *AgentExecution
	workspaceExecution                 *CommandExecution
	workspaceCommandErr                error
	workspaceCommands                  [][]string
	candidate                          RepositoryCandidate
	retainCandidateErr                 error
	prepareRepositoryErr               error
	publishCandidateErr                error
	repositoryCalls                    []string
	testedCandidate                    RepositoryCandidate
	publishedCandidate                 RepositoryCandidate
	synchronizedCandidate              RepositoryCandidate
	sourceSyncResult                   RepositorySourceSync
	sourceSyncErr                      error
	preparedRunID                      uuid.UUID
	testedRunID                        uuid.UUID
	publishedRunID                     uuid.UUID
	onCandidateCommand                 func()
	onCandidateCommandContext          func(context.Context)
	agentCheckout                      *domain.DevMachineCheckout
	publishedBranches                  int
	agentSecrets                       map[string]string
	onAgentRun                         func(context.Context)
	onCancelAgent                      func(context.Context, *domain.DevMachine, *domain.DevMachineAgentRun) error
}

func (f *runtimeFake) Spawn(_ context.Context, _ *domain.DevMachine, services []domain.DevMachineService, _ map[string]map[string]string, provision *RepositoryProvision) (string, string, map[string]string, error) {
	f.spawned++
	f.spawnProvision = provision
	if f.onSpawn != nil {
		f.onSpawn()
	}
	containers := make(map[string]string)
	for _, service := range services {
		containers[service.ServiceKey] = "container-" + service.ServiceKey
	}
	return "machine-network", "workspace-volume", containers, nil
}
func (f *runtimeFake) Start(context.Context, *domain.DevMachine, []domain.DevMachineService, map[string]map[string]string) error {
	return nil
}
func (f *runtimeFake) Pause(context.Context, *domain.DevMachine, []domain.DevMachineService) error {
	f.paused++
	return nil
}
func (f *runtimeFake) Stop(context.Context, *domain.DevMachine, []domain.DevMachineService) error {
	f.stopped++
	return nil
}
func (f *runtimeFake) Teardown(context.Context, *domain.DevMachine, []domain.DevMachineService) error {
	f.tornDown++
	return nil
}
func (f *runtimeFake) RunAgent(ctx context.Context, _ *domain.DevMachine, _ *domain.DevMachineAgentRun, _ *domain.DevMachineAgentProvider, checkout *domain.DevMachineCheckout, secrets map[string]string) (*AgentExecution, error) {
	f.agentCheckout = checkout
	f.repositoryCalls = append(f.repositoryCalls, "agent")
	f.agentRuns++
	f.agentSecrets = maps.Clone(secrets)
	if f.onAgentRun != nil {
		f.onAgentRun(ctx)
	}
	return f.agentExecution, nil
}
func (f *runtimeFake) CancelAgent(ctx context.Context, machine *domain.DevMachine, run *domain.DevMachineAgentRun) error {
	if f.onCancelAgent != nil {
		return f.onCancelAgent(ctx, machine, run)
	}
	return nil
}

func (f *runtimeFake) CleanupAgentRun(context.Context, *domain.DevMachine, []domain.DevMachineService, *domain.DevMachineCheckout, *domain.DevMachineAgentRun) (RepositorySourceSync, error) {
	return RepositorySourceSync{Status: "unchanged"}, nil
}
func (f *runtimeFake) TerminateTerminal(_ context.Context, _ *domain.DevMachine, _ []domain.DevMachineService, session *domain.DevMachineTerminalSession) error {
	f.terminatedTerminals++
	f.terminatedSession = session
	return f.terminateTerminalErr
}
func (f *runtimeFake) Inspect(context.Context, *domain.DevMachine, []domain.DevMachineService) (RuntimeInspection, error) {
	return RuntimeInspection{NetworkExists: true, VolumeExists: true, GatewayAttached: true}, nil
}
func (f *runtimeFake) Stats(context.Context, *domain.DevMachine, []domain.DevMachineService) (ResourceUsage, error) {
	return ResourceUsage{}, nil
}
func (f *runtimeFake) PrepareAgentRepository(_ context.Context, _ *domain.DevMachine, _ []domain.DevMachineService, _ *domain.DevMachineCheckout, runID uuid.UUID, _ string) error {
	f.repositoryCalls = append(f.repositoryCalls, "prepare")
	f.preparedRunID = runID
	return f.prepareRepositoryErr
}
func (f *runtimeFake) RetainAgentCandidate(_ context.Context, _ *domain.DevMachine, _ []domain.DevMachineService, _ *domain.DevMachineCheckout, _ uuid.UUID) (RepositoryCandidate, error) {
	f.repositoryCalls = append(f.repositoryCalls, "retain")
	return f.candidate, f.retainCandidateErr
}
func (f *runtimeFake) RunCandidateCommand(ctx context.Context, _ *domain.DevMachine, _ []domain.DevMachineService, _ *domain.DevMachineCheckout, runID uuid.UUID, candidate RepositoryCandidate, argv []string) (*CommandExecution, error) {
	f.repositoryCalls = append(f.repositoryCalls, "test")
	f.workspaceCommands = append(f.workspaceCommands, argv)
	f.testedRunID, f.testedCandidate = runID, candidate
	if f.onCandidateCommandContext != nil {
		f.onCandidateCommandContext(ctx)
	}
	if f.onCandidateCommand != nil {
		f.onCandidateCommand()
	}
	return f.workspaceExecution, f.workspaceCommandErr
}
func (f *runtimeFake) RunWorkspaceCommand(ctx context.Context, _ *domain.DevMachine, _ []domain.DevMachineService, runID uuid.UUID, argv []string) (*CommandExecution, error) {
	f.repositoryCalls = append(f.repositoryCalls, "workspace-test")
	f.workspaceCommands = append(f.workspaceCommands, argv)
	f.testedRunID = runID
	if f.onCandidateCommandContext != nil {
		f.onCandidateCommandContext(ctx)
	}
	return f.workspaceExecution, f.workspaceCommandErr
}
func (f *runtimeFake) PublishAgentCandidate(_ context.Context, _ *domain.DevMachine, _ []domain.DevMachineService, _ *domain.DevMachineCheckout, runID uuid.UUID, candidate RepositoryCandidate, _ string) error {
	f.repositoryCalls = append(f.repositoryCalls, "publish")
	f.publishedBranches++
	f.publishedRunID, f.publishedCandidate = runID, candidate
	return f.publishCandidateErr
}
func (f *runtimeFake) SynchronizeAgentCandidate(_ context.Context, _ *domain.DevMachine, _ []domain.DevMachineService, _ *domain.DevMachineCheckout, _ uuid.UUID, candidate RepositoryCandidate) (RepositorySourceSync, error) {
	f.repositoryCalls = append(f.repositoryCalls, "sync")
	f.synchronizedCandidate = candidate
	result := f.sourceSyncResult
	if result.Status == "" {
		result.Status = "updated"
	}
	return result, f.sourceSyncErr
}
func (f *runtimeFake) PrepareCheckout(context.Context, *domain.DevMachine, []domain.DevMachineService, *domain.DevMachineCheckout, string) error {
	return nil
}
func (f *runtimeFake) SnapshotEnvironment(context.Context, *domain.DevMachine, []domain.DevMachineService, *domain.DevMachineEnvironment) (string, error) {
	f.snapshots++
	return "sha256:test", nil
}
func (f *runtimeFake) DeleteEnvironmentImage(_ context.Context, environment *domain.DevMachineEnvironment) error {
	f.environmentDeleteAttempts++
	if f.deleteEnvironmentImageErr != nil {
		return f.deleteEnvironmentImageErr
	}
	f.deletedEnvironmentIDs = append(f.deletedEnvironmentIDs, environment.ID)
	return nil
}
func (f *runtimeFake) Ping(context.Context) error { return nil }

func TestAgentToolchainFailureOverridesOuterSuccess(t *testing.T) {
	machineID, workspaceID, runID := uuid.New(), uuid.New(), uuid.New()
	run := &domain.DevMachineAgentRun{
		ID: runID, MachineID: machineID, WorkspaceID: workspaceID,
		ProviderID: "opencode", Mode: "autonomous", Status: domain.DevMachineAgentRunStatusQueued,
		MaxRuntimeSeconds: 30,
	}
	store := &managerStoreFake{
		machine: &domain.DevMachine{
			ID: machineID, WorkspaceID: workspaceID, RoutingKey: "0123456789abcdef0123",
			Status: domain.DevMachineStatusRunning, DesiredStatus: domain.DevMachineStatusRunning, Generation: 1,
		},
		agentRun: run,
		provider: &domain.DevMachineAgentProvider{
			MachineID: machineID, ProviderID: "opencode", ImageRef: "strifira/opencode:test", Enabled: true,
		},
	}
	runtime := &runtimeFake{agentExecution: &AgentExecution{
		ContainerID: "agent-container", ExitCode: 0,
		Stdout: `{"type":"tool_use","part":{"type":"tool","tool":"bash","state":{"status":"completed","title":"Build project","output":"go: command not found\n","metadata":{"exit":127}}}}` + "\n" +
			`{"type":"text","part":{"type":"text","text":"Unable to verify the build"}}`,
	}}
	manager := NewManager(store, runtime, agent.NewRegistry(agent.NewOpenCodeProvider("strifira/opencode:test")), githubAPIFake{}, nil, make([]byte, 32), make([]byte, 32), "test")

	err := manager.runAgent(context.Background(), store.machine, &domain.DevMachineOperation{
		MachineID: machineID, WorkspaceID: workspaceID, AgentRunID: &runID,
		Action: domain.DevMachineOpRunAgent, Generation: 1,
	})

	require.NoError(t, err)
	require.NotNil(t, store.completedRun)
	require.Equal(t, domain.DevMachineAgentRunStatusFailed, store.completedRun.Status)
	require.Equal(t, "Unable to verify the build", *store.completedRun.Summary)
	require.Contains(t, string(store.completedRun.RiskNotes), "Build project")
}

func TestManagerDoesNotLaunchAgentCancelledBeforeStart(t *testing.T) {
	machineID, workspaceID, runID := uuid.New(), uuid.New(), uuid.New()
	run := &domain.DevMachineAgentRun{
		ID: runID, MachineID: machineID, WorkspaceID: workspaceID,
		ProviderID: "opencode", Mode: "autonomous", Status: domain.DevMachineAgentRunStatusQueued,
		AllowedSecrets: json.RawMessage(`["API_KEY"]`), MaxRuntimeSeconds: 30,
	}
	store := &managerStoreFake{
		machine: &domain.DevMachine{
			ID: machineID, WorkspaceID: workspaceID, RoutingKey: "0123456789abcdef0123",
			Status: domain.DevMachineStatusRunning, DesiredStatus: domain.DevMachineStatusRunning, Generation: 1,
		},
		agentRun: run,
		provider: &domain.DevMachineAgentProvider{
			MachineID: machineID, ProviderID: "opencode", ImageRef: "strifira/opencode:test", Enabled: true,
		},
	}
	store.updateAgentRunStarted = func() error {
		store.agentRun.Status = domain.DevMachineAgentRunStatusCancelled
		return sql.ErrNoRows
	}
	runtime := &runtimeFake{}
	manager := NewManager(store, runtime, agent.NewRegistry(agent.NewOpenCodeProvider("strifira/opencode:test")), githubAPIFake{}, nil, make([]byte, 32), nil, "test")

	err := manager.runAgent(context.Background(), store.machine, &domain.DevMachineOperation{
		MachineID: machineID, WorkspaceID: workspaceID, AgentRunID: &runID,
		Action: domain.DevMachineOpRunAgent, Generation: 1,
	})

	require.NoError(t, err)
	require.Equal(t, domain.DevMachineAgentRunStatusCancelled, store.agentRun.Status)
	require.Zero(t, store.envVarListCalls)
	require.Zero(t, store.runtimeServicesCreated)
	require.Zero(t, runtime.agentRuns)
}

func TestReconcileQueuesIdlePause(t *testing.T) {
	machine := domain.DevMachine{
		ID: uuid.New(), WorkspaceID: uuid.New(), Status: domain.DevMachineStatusRunning,
		DesiredStatus: domain.DevMachineStatusRunning, Generation: 4,
	}
	store := &managerStoreFake{machine: &machine, idle: []domain.DevMachine{machine}}
	manager := NewManager(store, &runtimeFake{}, agent.NewRegistry(), githubAPIFake{}, nil, nil, nil, "test")

	require.NoError(t, manager.reconcile(context.Background()))
	require.Equal(t, []domain.DevMachineStatus{domain.DevMachineStatusPaused}, store.desired)
	require.Equal(t, []domain.DevMachineOperationAction{domain.DevMachineOpPause}, store.actions)
}

func TestManagerLifecycleSpawnPauseAndTeardown(t *testing.T) {
	machineID, workspaceID := uuid.New(), uuid.New()
	store := &managerStoreFake{
		machine:  &domain.DevMachine{ID: machineID, WorkspaceID: workspaceID, RoutingKey: "0123456789abcdef0123", Status: domain.DevMachineStatusQueued},
		services: []domain.DevMachineService{{ID: uuid.New(), MachineID: machineID, ServiceKey: "ide", ServiceType: "ide"}},
	}
	runtime := &runtimeFake{}
	manager := NewManager(store, runtime, agent.NewRegistry(), githubAPIFake{}, nil, make([]byte, 32), make([]byte, 32), "test")
	err := manager.processOperation(context.Background(), &domain.DevMachineOperation{MachineID: machineID, WorkspaceID: workspaceID, Action: domain.DevMachineOpSpawn})
	require.NoError(t, err)
	require.Equal(t, domain.DevMachineStatusRunning, store.machine.Status)
	require.Equal(t, 1, runtime.spawned)
	err = manager.processOperation(context.Background(), &domain.DevMachineOperation{MachineID: machineID, WorkspaceID: workspaceID, Action: domain.DevMachineOpPause})
	require.NoError(t, err)
	require.Equal(t, domain.DevMachineStatusPaused, store.machine.Status)
	err = manager.processOperation(context.Background(), &domain.DevMachineOperation{MachineID: machineID, WorkspaceID: workspaceID, Action: domain.DevMachineOpTeardown})
	require.NoError(t, err)
	require.Equal(t, domain.DevMachineStatusDestroyed, store.machine.Status)
	require.Equal(t, 1, runtime.tornDown)
}

func TestManagerPauseRevokesMachineAccess(t *testing.T) {
	machineID, workspaceID := uuid.New(), uuid.New()
	containerID := "container-ide"
	store := &managerStoreFake{
		machine: &domain.DevMachine{
			ID: machineID, WorkspaceID: workspaceID, RoutingKey: "0123456789abcdef0123",
			Status: domain.DevMachineStatusRunning, DesiredStatus: domain.DevMachineStatusPaused, Generation: 3,
		},
		services: []domain.DevMachineService{{
			ID: uuid.New(), MachineID: machineID, ServiceKey: "ide", ServiceType: "ide",
			ContainerID: &containerID, Status: "running",
		}},
	}
	runtime := &runtimeFake{}
	manager := NewManager(store, runtime, agent.NewRegistry(), githubAPIFake{}, nil, make([]byte, 32), make([]byte, 32), "test")

	err := manager.processOperation(context.Background(), &domain.DevMachineOperation{
		MachineID: machineID, WorkspaceID: workspaceID, Action: domain.DevMachineOpPause, Generation: 3,
	})

	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{machineID}, store.revokedMachineIDs)
	require.Equal(t, 1, runtime.paused)
	require.Equal(t, domain.DevMachineStatusPaused, store.machine.Status)
}

func TestManagerSkipsStaleOperationGeneration(t *testing.T) {
	machineID, workspaceID := uuid.New(), uuid.New()
	store := &managerStoreFake{
		machine:  &domain.DevMachine{ID: machineID, WorkspaceID: workspaceID, RoutingKey: "0123456789abcdef0123", Status: domain.DevMachineStatusQueued, Generation: 2},
		services: []domain.DevMachineService{{ID: uuid.New(), MachineID: machineID, ServiceKey: "ide", ServiceType: "ide"}},
	}
	runtime := &runtimeFake{}
	manager := NewManager(store, runtime, agent.NewRegistry(), githubAPIFake{}, nil, make([]byte, 32), make([]byte, 32), "test")
	err := manager.processOperation(context.Background(), &domain.DevMachineOperation{
		MachineID: machineID, WorkspaceID: workspaceID, Action: domain.DevMachineOpSpawn, Generation: 1,
	})
	require.NoError(t, err)
	require.Zero(t, runtime.spawned)
}

func TestManagerStartResumesSpawnAfterRetryableFailure(t *testing.T) {
	machineID, workspaceID := uuid.New(), uuid.New()
	store := &managerStoreFake{
		machine:  &domain.DevMachine{ID: machineID, WorkspaceID: workspaceID, RoutingKey: "0123456789abcdef0123", Status: domain.DevMachineStatusSpawning},
		services: []domain.DevMachineService{{ID: uuid.New(), MachineID: machineID, ServiceKey: "ide", ServiceType: "ide"}},
	}
	runtime := &runtimeFake{}
	manager := NewManager(store, runtime, agent.NewRegistry(), githubAPIFake{}, nil, make([]byte, 32), make([]byte, 32), "test")

	err := manager.processOperation(context.Background(), &domain.DevMachineOperation{
		MachineID: machineID, WorkspaceID: workspaceID, Action: domain.DevMachineOpStart,
	})

	require.NoError(t, err)
	require.Equal(t, domain.DevMachineStatusRunning, store.machine.Status)
	require.Equal(t, 1, runtime.spawned)
}

func TestManagerCheckoutTokenUsesCheckoutRepository(t *testing.T) {
	machineID, workspaceID, checkoutID := uuid.New(), uuid.New(), uuid.New()
	store := &managerStoreFake{
		machine:  &domain.DevMachine{ID: machineID, WorkspaceID: workspaceID, RoutingKey: "0123456789abcdef0123", Status: domain.DevMachineStatusRunning, Generation: 2, RepoOwner: "legacy", RepoName: "old"},
		services: []domain.DevMachineService{{ID: uuid.New(), MachineID: machineID, ServiceKey: "ide", ServiceType: "ide"}},
		checkout: &domain.DevMachineCheckout{ID: checkoutID, WorkspaceID: workspaceID, MachineID: machineID, RepositoryFullName: "selected/repo", BaseBranch: "main", WorkingBranch: "strifira/test", WorkspacePath: "/workspace/tasks/eng-1", Status: "queued"},
	}
	manager := NewManager(store, &runtimeFake{}, agent.NewRegistry(), githubAPIFake{}, nil, make([]byte, 32), nil, "test")

	err := manager.processOperation(context.Background(), &domain.DevMachineOperation{MachineID: machineID, WorkspaceID: workspaceID, Action: domain.DevMachineOpCheckoutIssue, CheckoutID: &checkoutID, Generation: 2})

	require.Error(t, err)
	require.Equal(t, []string{"selected/repo"}, store.installationFullNameCalls)
}

func TestManagerKeepsCheckoutPreparingWhileOperationRetries(t *testing.T) {
	machineID, workspaceID, checkoutID := uuid.New(), uuid.New(), uuid.New()
	store := &managerStoreFake{
		machine:  &domain.DevMachine{ID: machineID, WorkspaceID: workspaceID, Status: domain.DevMachineStatusRunning, DesiredStatus: domain.DevMachineStatusRunning, Generation: 2},
		checkout: &domain.DevMachineCheckout{ID: checkoutID, WorkspaceID: workspaceID, MachineID: machineID, RepositoryFullName: "selected/repo", Status: "queued"},
	}
	manager := NewManager(store, &runtimeFake{}, agent.NewRegistry(), githubAPIFake{}, nil, make([]byte, 32), nil, "test")

	manager.operationSlots <- struct{}{}
	manager.operations.Add(1)
	manager.processLeasedOperation(context.Background(), domain.DevMachineOperation{
		ID: uuid.New(), MachineID: machineID, WorkspaceID: workspaceID, Action: domain.DevMachineOpCheckoutIssue,
		CheckoutID: &checkoutID, Generation: 2,
	})

	require.Equal(t, "preparing", store.checkoutStatus)
	require.Nil(t, store.checkoutError)
	require.NotNil(t, store.failedOperationRetryable)
	require.True(t, *store.failedOperationRetryable)
}

func TestManagerTerminatesStaleTerminalSessionAndCompletesClose(t *testing.T) {
	machineID, workspaceID, sessionID := uuid.New(), uuid.New(), uuid.New()
	store := &managerStoreFake{
		machine: &domain.DevMachine{
			ID: machineID, WorkspaceID: workspaceID, Status: domain.DevMachineStatusRunning,
			DesiredStatus: domain.DevMachineStatusRunning, Generation: 4,
		},
		terminalSession: &domain.DevMachineTerminalSession{
			ID: sessionID, MachineID: machineID, WorkspaceID: workspaceID, RuntimeSessionName: "term-test", Status: "closing",
		},
	}
	runtime := &runtimeFake{}
	manager := NewManager(store, runtime, agent.NewRegistry(), githubAPIFake{}, nil, nil, nil, "test")

	err := manager.processOperation(context.Background(), &domain.DevMachineOperation{
		MachineID: machineID, WorkspaceID: workspaceID, Action: domain.DevMachineOpTerminateTerminal,
		TerminalSessionID: &sessionID, Generation: 3,
	})

	require.NoError(t, err)
	require.Equal(t, 1, runtime.terminatedTerminals)
	require.Same(t, store.terminalSession, runtime.terminatedSession)
	require.Equal(t, []uuid.UUID{sessionID}, store.completedTerminalIDs)
	require.Equal(t, "closed", store.terminalSession.Status)
}

func TestManagerTerminalCloseFailureRemainsExplicitAfterRetriesExhausted(t *testing.T) {
	machineID, workspaceID, sessionID := uuid.New(), uuid.New(), uuid.New()
	willRetry := false
	store := &managerStoreFake{
		machine: &domain.DevMachine{
			ID: machineID, WorkspaceID: workspaceID, Status: domain.DevMachineStatusRunning,
			DesiredStatus: domain.DevMachineStatusRunning, Generation: 1,
		},
		terminalSession: &domain.DevMachineTerminalSession{
			ID: sessionID, MachineID: machineID, WorkspaceID: workspaceID, RuntimeSessionName: "term-test", Status: "closing",
		},
		failOperationWillRetry: &willRetry,
	}
	runtime := &runtimeFake{terminateTerminalErr: errors.New("docker unavailable")}
	manager := NewManager(store, runtime, agent.NewRegistry(), githubAPIFake{}, nil, nil, nil, "test")
	manager.operationSlots <- struct{}{}
	manager.operations.Add(1)

	manager.processLeasedOperation(context.Background(), domain.DevMachineOperation{
		ID: uuid.New(), MachineID: machineID, WorkspaceID: workspaceID, Action: domain.DevMachineOpTerminateTerminal,
		TerminalSessionID: &sessionID, Generation: 1,
	})

	require.NotNil(t, store.failedOperationRetryable)
	require.True(t, *store.failedOperationRetryable)
	require.Equal(t, []uuid.UUID{sessionID}, store.failedTerminalIDs)
	require.Equal(t, "close_failed", store.terminalSession.Status)
	require.Empty(t, store.completedTerminalIDs)
}

func TestGitHubCredentialRegistersRuntimeCredentialBeforeReturning(t *testing.T) {
	machineID, workspaceID := uuid.New(), uuid.New()
	token := "fjt_" + strings.Repeat("runtime-token", 4)
	key := cryptoutil.DeriveKey("manager-runtime-credential-test")
	githubKey := cryptoutil.DeriveKey("manager-runtime-credential-github")
	encrypted, err := cryptoutil.Encrypt(token, githubKey)
	require.NoError(t, err)
	store := &managerStoreFake{
		machine: &domain.DevMachine{ID: machineID, WorkspaceID: workspaceID, RepoOwner: "strifira", RepoName: "api"},
		linkedRepository: &domain.GitHubRepoModel{ID: uuid.New(), WorkspaceID: workspaceID,
			FullName: "strifira/api", InstanceURL: "https://git.example.com"},
		githubConnection: &domain.GitHubConnection{WorkspaceID: workspaceID,
			InstanceURL: "https://git.example.com", Token: encrypted},
	}
	manager := NewManager(store, &runtimeFake{}, agent.NewRegistry(), githubAPIFake{}, nil, key, githubKey, "test")

	instanceURL, returnedToken, err := manager.githubCredential(context.Background(), store.machine, "strifira/api")

	require.NoError(t, err)
	require.Equal(t, "https://git.example.com", instanceURL)
	require.Equal(t, token, returnedToken)
	require.Equal(t, []string{"strifira/api"}, store.installationFullNameCalls)
	require.Len(t, store.runtimeCredentials, 1)
	credential := store.runtimeCredentials[0]
	require.Equal(t, machineID, credential.MachineID)
	require.Equal(t, domain.DevMachineRuntimeCredentialScopeMachine, credential.Scope)
	require.Equal(t, domain.DevMachineRuntimeCredentialTypeGitHubToken, credential.CredentialType)
	require.True(t, credential.ExpiresAt.After(time.Now()))
	require.NotEmpty(t, credential.EncryptedValue)
	require.NotEqual(t, token, credential.EncryptedValue)
	decrypted, err := cryptoutil.Decrypt(credential.EncryptedValue, key)
	require.NoError(t, err)
	require.Equal(t, token, decrypted)
	fingerprint := sha256.Sum256([]byte(token))
	require.Equal(t, hex.EncodeToString(fingerprint[:]), credential.FingerprintSHA256)
	encoded, err := json.Marshal(credential)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), credential.EncryptedValue)
	require.NotContains(t, string(encoded), credential.FingerprintSHA256)
	require.NotContains(t, string(encoded), token)
}

func TestGitHubCredentialRejectsRepositoryLinkedToAnotherInstance(t *testing.T) {
	store := &managerStoreFake{machine: &domain.DevMachine{ID: uuid.New(), WorkspaceID: uuid.New(), RepoOwner: "octo", RepoName: "repo"}, linkedRepository: &domain.GitHubRepoModel{InstanceURL: "https://old.example.com"}}
	manager := NewManager(store, &runtimeFake{}, agent.NewRegistry(), githubAPIFake{}, &GlobalGitHubCredentials{URL: "https://git.example.com", Token: "must-not-be-returned"}, make([]byte, 32), nil, "test")
	_, token, err := manager.githubCredential(t.Context(), store.machine, "octo/repo")
	require.ErrorContains(t, err, "different GitHub instance")
	require.Empty(t, token)
	require.Empty(t, store.runtimeCredentials)
}

func TestGitHubCredentialPrecedence(t *testing.T) {
	machineID, workspaceID := uuid.New(), uuid.New()
	rowToken := "fjt_" + strings.Repeat("row-token", 4)
	githubKey := cryptoutil.DeriveKey("manager-github-precedence-test")
	encrypted, err := cryptoutil.Encrypt(rowToken, githubKey)
	require.NoError(t, err)
	linkedRepository := &domain.GitHubRepoModel{ID: uuid.New(), WorkspaceID: workspaceID,
		FullName: "strifira/api", InstanceURL: "https://git.example.com"}
	connection := &domain.GitHubConnection{WorkspaceID: workspaceID,
		InstanceURL: "https://git.example.com", Token: encrypted}

	t.Run("row overrides global", func(t *testing.T) {
		store := &managerStoreFake{machine: &domain.DevMachine{ID: machineID, WorkspaceID: workspaceID, RepoOwner: "strifira", RepoName: "api"},
			linkedRepository: linkedRepository, githubConnection: connection}
		manager := NewManager(store, &runtimeFake{}, agent.NewRegistry(), githubAPIFake{},
			&GlobalGitHubCredentials{URL: "https://git.example.com", Token: "global-must-not-win"}, make([]byte, 32), githubKey, "test")

		instanceURL, token, err := manager.githubCredential(t.Context(), store.machine, "strifira/api")

		require.NoError(t, err)
		require.Equal(t, "https://git.example.com", instanceURL)
		require.Equal(t, rowToken, token)
	})

	t.Run("disable marker yields no credentials even with row and global", func(t *testing.T) {
		store := &managerStoreFake{machine: &domain.DevMachine{ID: machineID, WorkspaceID: workspaceID, RepoOwner: "strifira", RepoName: "api"},
			linkedRepository: linkedRepository, githubConnection: connection,
			githubWorkspaceDisable: &domain.GitHubWorkspaceDisable{WorkspaceID: workspaceID}}
		manager := NewManager(store, &runtimeFake{}, agent.NewRegistry(), githubAPIFake{},
			&GlobalGitHubCredentials{URL: "https://git.example.com", Token: "global-must-not-win"}, make([]byte, 32), githubKey, "test")

		_, token, err := manager.githubCredential(t.Context(), store.machine, "strifira/api")

		require.ErrorIs(t, err, sql.ErrNoRows)
		require.Empty(t, token)
		require.Empty(t, store.runtimeCredentials, "no credential audit trail for a paused workspace")
	})
}

func TestReconcilePurgesExpiredRuntimeCredentials(t *testing.T) {
	store := &managerStoreFake{orphanedEnvironmentCount: 2, orphanedCheckoutCount: 3}
	manager := NewManager(store, &runtimeFake{}, agent.NewRegistry(), githubAPIFake{}, nil, nil, nil, "test")

	require.NoError(t, manager.reconcile(context.Background()))
	require.NotNil(t, store.purgedCredentialNow)
	require.NotNil(t, store.purgedAccessLogBefore)
	require.WithinDuration(t, store.purgedCredentialNow.Add(-gatewayAccessLogRetention), *store.purgedAccessLogBefore, time.Second)
	require.Equal(t, gatewayAccessLogPurgeBatch, store.purgedAccessLogLimit)
	require.Equal(t, 1, store.orphanReconcileCalls)
	require.Equal(t, 1, store.checkoutReconcileCalls)
}

func TestReconcileRetriesEnvironmentImageCleanupFailure(t *testing.T) {
	environmentID, workspaceID := uuid.New(), uuid.New()
	store := &managerStoreFake{deleteRequestedEnvs: []domain.DevMachineEnvironment{{
		ID: environmentID, WorkspaceID: workspaceID, Status: "delete_requested", ImageRef: "strifira/environment:test",
	}}}
	runtime := &runtimeFake{deleteEnvironmentImageErr: errors.New("image is busy")}
	manager := NewManager(store, runtime, agent.NewRegistry(), githubAPIFake{}, nil, nil, nil, "test")

	require.NoError(t, manager.reconcile(context.Background()))
	require.Equal(t, 1, runtime.environmentDeleteAttempts)
	require.Empty(t, store.deletedEnvironmentIDs)

	runtime.deleteEnvironmentImageErr = nil
	require.NoError(t, manager.reconcile(context.Background()))
	require.Equal(t, 2, runtime.environmentDeleteAttempts)
	require.Equal(t, []uuid.UUID{environmentID}, store.deletedEnvironmentIDs)
}

func TestManagerMarksStaleCheckoutFailed(t *testing.T) {
	machineID, workspaceID, checkoutID := uuid.New(), uuid.New(), uuid.New()
	store := &managerStoreFake{
		machine:  &domain.DevMachine{ID: machineID, WorkspaceID: workspaceID, Status: domain.DevMachineStatusRunning, Generation: 2},
		checkout: &domain.DevMachineCheckout{ID: checkoutID, Status: "queued"},
	}
	manager := NewManager(store, &runtimeFake{}, agent.NewRegistry(), githubAPIFake{}, nil, make([]byte, 32), nil, "test")

	err := manager.processOperation(context.Background(), &domain.DevMachineOperation{MachineID: machineID, WorkspaceID: workspaceID, Action: domain.DevMachineOpCheckoutIssue, CheckoutID: &checkoutID, Generation: 1})

	require.NoError(t, err)
	require.Equal(t, "failed", store.checkoutStatus)
	require.NotNil(t, store.checkoutError)
	require.Contains(t, *store.checkoutError, "state changed")
}

func TestManagerRejectsRepositoryAgentRunWithoutCheckout(t *testing.T) {
	machineID, workspaceID, runID, repositoryID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	machine := &domain.DevMachine{
		ID: machineID, WorkspaceID: workspaceID, Status: domain.DevMachineStatusRunning,
		DesiredStatus: domain.DevMachineStatusRunning, Generation: 1, RepositoryAffinityID: &repositoryID,
	}
	store := &managerStoreFake{
		machine: machine,
		agentRun: &domain.DevMachineAgentRun{
			ID: runID, MachineID: machineID, WorkspaceID: workspaceID, Status: domain.DevMachineAgentRunStatusQueued,
		},
	}
	runtime := &runtimeFake{}
	manager := NewManager(store, runtime, agent.NewRegistry(), githubAPIFake{}, nil, make([]byte, 32), nil, "test")

	err := manager.runAgent(context.Background(), machine, &domain.DevMachineOperation{AgentRunID: &runID, Generation: 1})

	var terminalErr *terminalOperationError
	require.ErrorAs(t, err, &terminalErr)
	require.Equal(t, "checkout_not_ready", terminalErr.code)
	require.Zero(t, runtime.agentRuns)
	require.Zero(t, store.envVarListCalls)
}

func TestManagerSnapshotEnvironmentTransitionsThroughBuilding(t *testing.T) {
	machineID, workspaceID, environmentID := uuid.New(), uuid.New(), uuid.New()
	environment := &domain.DevMachineEnvironment{ID: environmentID, WorkspaceID: workspaceID, Name: "base", ImageRef: "strifira/dev-environment-test:snapshot", Status: "pending"}
	store := &managerStoreFake{
		machine: &domain.DevMachine{
			ID: machineID, WorkspaceID: workspaceID, RoutingKey: "0123456789abcdef0123",
			Status: domain.DevMachineStatusPaused, DesiredStatus: domain.DevMachineStatusPaused, Generation: 3,
		},
		services:    []domain.DevMachineService{{ID: uuid.New(), MachineID: machineID, ServiceKey: "ide", ServiceType: "ide"}},
		environment: environment,
	}
	manager := NewManager(store, &runtimeFake{}, agent.NewRegistry(), githubAPIFake{}, nil, make([]byte, 32), nil, "test")

	err := manager.processOperation(context.Background(), &domain.DevMachineOperation{MachineID: machineID, WorkspaceID: workspaceID, Action: domain.DevMachineOpSnapshotEnvironment, EnvironmentID: &environmentID, Generation: 3})

	require.NoError(t, err)
	require.Equal(t, "ready", environment.Status)
	require.NotNil(t, environment.ImageDigest)
	require.Equal(t, "sha256:test", *environment.ImageDigest)
	require.Equal(t, "sha256:test", environment.ImageRef)
}

func TestManagerFailsGenerationMismatchedSnapshotsWithoutRetry(t *testing.T) {
	for _, generation := range []int64{2, 4} {
		t.Run(fmt.Sprintf("operation generation %d", generation), func(t *testing.T) {
			machineID, workspaceID, environmentID := uuid.New(), uuid.New(), uuid.New()
			environment := &domain.DevMachineEnvironment{
				ID: environmentID, WorkspaceID: workspaceID, Name: "base",
				ImageRef: "strifira/dev-environment-test:snapshot", Status: "pending",
			}
			store := &managerStoreFake{
				machine: &domain.DevMachine{
					ID: machineID, WorkspaceID: workspaceID, Status: domain.DevMachineStatusPaused,
					DesiredStatus: domain.DevMachineStatusPaused, Generation: 3,
				},
				environment: environment,
			}
			runtime := &runtimeFake{}
			manager := NewManager(store, runtime, agent.NewRegistry(), githubAPIFake{}, nil, make([]byte, 32), nil, "test")
			operation := domain.DevMachineOperation{
				ID: uuid.New(), MachineID: machineID, WorkspaceID: workspaceID,
				Action: domain.DevMachineOpSnapshotEnvironment, EnvironmentID: &environmentID,
				Status: domain.DevMachineOpStatusLeased, Generation: generation, MaxAttempts: 2,
			}
			manager.operationSlots <- struct{}{}
			manager.operations.Add(1)

			manager.processLeasedOperation(context.Background(), operation)

			require.Equal(t, "failed", environment.Status)
			require.Zero(t, runtime.snapshots)
			require.NotNil(t, store.failedOperationRetryable)
			require.False(t, *store.failedOperationRetryable)
			require.Equal(t, "environment_snapshot_stale", store.failedOperationCode)
			require.Contains(t, store.failedOperationMessage, "machine generation changed")
			require.Zero(t, store.completedOperations)
		})
	}
}

func TestManagerFailsSnapshotWhenMachineStateChanged(t *testing.T) {
	machineID, workspaceID, environmentID := uuid.New(), uuid.New(), uuid.New()
	environment := &domain.DevMachineEnvironment{
		ID: environmentID, WorkspaceID: workspaceID, Name: "base",
		ImageRef: "strifira/dev-environment-test:snapshot", Status: "building",
	}
	store := &managerStoreFake{
		machine: &domain.DevMachine{
			ID: machineID, WorkspaceID: workspaceID, Status: domain.DevMachineStatusPaused,
			DesiredStatus: domain.DevMachineStatusRunning, Generation: 3,
		},
		environment: environment,
	}
	runtime := &runtimeFake{}
	manager := NewManager(store, runtime, agent.NewRegistry(), githubAPIFake{}, nil, make([]byte, 32), nil, "test")
	operation := domain.DevMachineOperation{
		ID: uuid.New(), MachineID: machineID, WorkspaceID: workspaceID,
		Action: domain.DevMachineOpSnapshotEnvironment, EnvironmentID: &environmentID,
		Status: domain.DevMachineOpStatusLeased, Generation: 3, MaxAttempts: 2,
	}
	manager.operationSlots <- struct{}{}
	manager.operations.Add(1)

	manager.processLeasedOperation(context.Background(), operation)

	require.Equal(t, "failed", environment.Status)
	require.Zero(t, runtime.snapshots)
	require.NotNil(t, store.failedOperationRetryable)
	require.False(t, *store.failedOperationRetryable)
	require.Equal(t, "environment_snapshot_state_changed", store.failedOperationCode)
	require.Contains(t, store.failedOperationMessage, "desired state running")
	require.Zero(t, store.completedOperations)
}

func TestManagerSnapshotRetryDoesNotUndoEnvironmentDeletion(t *testing.T) {
	machineID, workspaceID, environmentID := uuid.New(), uuid.New(), uuid.New()
	environment := &domain.DevMachineEnvironment{
		ID: environmentID, WorkspaceID: workspaceID, Name: "base",
		ImageRef: "strifira/dev-environment-test:snapshot", Status: "delete_requested",
	}
	store := &managerStoreFake{
		machine:     &domain.DevMachine{ID: machineID, WorkspaceID: workspaceID, Status: domain.DevMachineStatusPaused, Generation: 3},
		services:    []domain.DevMachineService{{ID: uuid.New(), MachineID: machineID, ServiceKey: "ide", ServiceType: "ide"}},
		environment: environment,
	}
	manager := NewManager(store, &runtimeFake{}, agent.NewRegistry(), githubAPIFake{}, nil, make([]byte, 32), nil, "test")

	err := manager.processOperation(context.Background(), &domain.DevMachineOperation{
		MachineID: machineID, WorkspaceID: workspaceID, Action: domain.DevMachineOpSnapshotEnvironment,
		EnvironmentID: &environmentID, Generation: 3,
	})

	require.NoError(t, err)
	require.Equal(t, "delete_requested", environment.Status)
	require.Nil(t, environment.ImageDigest)
}

func TestReconcileDeletesRequestedEnvironmentImagesBeforeRecords(t *testing.T) {
	environment := domain.DevMachineEnvironment{ID: uuid.New(), WorkspaceID: uuid.New(), ImageRef: "strifira/dev-environment-test:snapshot", Status: "delete_requested"}
	store := &managerStoreFake{deleteRequestedEnvs: []domain.DevMachineEnvironment{environment}}
	runtime := &runtimeFake{}
	manager := NewManager(store, runtime, agent.NewRegistry(), githubAPIFake{}, nil, nil, nil, "test")

	require.NoError(t, manager.reconcile(context.Background()))
	require.Equal(t, []uuid.UUID{environment.ID}, runtime.deletedEnvironmentIDs)
	require.Equal(t, []uuid.UUID{environment.ID}, store.deletedEnvironmentIDs)
}

func TestReconcilePermanentDeletePurgesOnlySafeRowsAndQueuesUnsafeRows(t *testing.T) {
	now := time.Now().UTC()
	networkName := "machine-network"
	volumeName := "workspace-volume"
	running := domain.DevMachine{
		ID: uuid.New(), WorkspaceID: uuid.New(), Status: domain.DevMachineStatusRunning,
		DesiredStatus: domain.DevMachineStatusRunning, Generation: 3, DeleteRequestedAt: &now,
		DockerNetworkName: &networkName, WorkspaceVolumeName: &volumeName,
	}
	destroyed := domain.DevMachine{
		ID: uuid.New(), WorkspaceID: uuid.New(), Status: domain.DevMachineStatusDestroyed,
		DesiredStatus: domain.DevMachineStatusDestroyed, Generation: 4, DeleteRequestedAt: &now,
	}
	failedWithResources := domain.DevMachine{
		ID: uuid.New(), WorkspaceID: uuid.New(), Status: domain.DevMachineStatusFailed,
		DesiredStatus: domain.DevMachineStatusFailed, Generation: 5, DeleteRequestedAt: &now,
		DockerNetworkName: &networkName, WorkspaceVolumeName: &volumeName,
	}
	store := &managerStoreFake{permanentDeleteMachines: []domain.DevMachine{running, destroyed, failedWithResources}}
	manager := NewManager(store, &runtimeFake{}, agent.NewRegistry(), githubAPIFake{}, nil, nil, nil, "test")

	require.NoError(t, manager.reconcile(context.Background()))
	require.Equal(t, []uuid.UUID{destroyed.ID}, store.purgedMachineIDs)
	require.Equal(t, 2, store.permanentDeleteRequests)
	require.Equal(t, []domain.DevMachineStatus{domain.DevMachineStatusDestroyed, domain.DevMachineStatusDestroyed}, store.desired)
	require.Equal(t, []domain.DevMachineOperationAction{domain.DevMachineOpTeardown, domain.DevMachineOpTeardown}, store.actions)
}

func TestManagerDoesNotPublishRunningAfterGenerationChanges(t *testing.T) {
	machineID, workspaceID := uuid.New(), uuid.New()
	store := &managerStoreFake{
		machine:  &domain.DevMachine{ID: machineID, WorkspaceID: workspaceID, RoutingKey: "0123456789abcdef0123", Status: domain.DevMachineStatusQueued, Generation: 1},
		services: []domain.DevMachineService{{ID: uuid.New(), MachineID: machineID, ServiceKey: "ide", ServiceType: "ide"}},
	}
	runtime := &runtimeFake{onSpawn: func() {
		store.machine.Generation = 2
		store.machine.DesiredStatus = domain.DevMachineStatusDestroyed
	}}
	manager := NewManager(store, runtime, agent.NewRegistry(), githubAPIFake{}, nil, make([]byte, 32), make([]byte, 32), "test")

	err := manager.processOperation(context.Background(), &domain.DevMachineOperation{
		MachineID: machineID, WorkspaceID: workspaceID, Action: domain.DevMachineOpSpawn, Generation: 1,
	})

	require.NoError(t, err)
	require.NotEqual(t, domain.DevMachineStatusRunning, store.machine.Status)
	require.Equal(t, 1, runtime.tornDown)
}

func TestWorkspaceMountDisablesImageCopyUp(t *testing.T) {
	runtime := &DockerRuntime{}
	machine := &domain.DevMachine{MemoryMB: 4096, CPUMillis: 2000, PidsLimit: 512}

	hostConfig := runtime.secureHostConfig(machine, "ide", "machine-network", "workspace-volume")

	require.Len(t, hostConfig.Mounts, 2)
	for _, mounted := range hostConfig.Mounts {
		require.NotNil(t, mounted.VolumeOptions)
		require.True(t, mounted.VolumeOptions.NoCopy)
	}
	require.Equal(t, "workspace", hostConfig.Mounts[0].VolumeOptions.Subpath)
	require.Equal(t, "repositories", hostConfig.Mounts[1].VolumeOptions.Subpath)
	require.True(t, hostConfig.Mounts[1].ReadOnly)
	require.Equal(t, "json-file", hostConfig.LogConfig.Type)
	require.Equal(t, "10m", hostConfig.LogConfig.Config["max-size"])
}

func TestOwnedByMachineRequiresManagedLabel(t *testing.T) {
	machineID := uuid.New()
	require.True(t, ownedByMachine(map[string]string{"com.strifira.managed": "true", "com.strifira.machine-id": machineID.String()}, machineID))
	require.False(t, ownedByMachine(map[string]string{"com.strifira.machine-id": machineID.String()}, machineID))
	require.False(t, ownedByMachine(map[string]string{"com.strifira.managed": "true", "com.strifira.machine-id": uuid.NewString()}, machineID))
}

func TestUniqueContainerIDsDeduplicatesSharedDeveloperServices(t *testing.T) {
	containerID := "developer-container"
	ids := uniqueContainerIDs([]domain.DevMachineService{
		{ServiceType: "ide", ServiceKey: "ide", ContainerID: &containerID},
		{ServiceType: "terminal", ServiceKey: "terminal", ContainerID: &containerID},
	})
	require.Equal(t, []string{containerID}, ids)
}

func TestManagerTimeoutReconciliationCancelsContextAndWaitsForOwnedCleanup(t *testing.T) {
	for _, mode := range []string{"success", "cleanup-error", "completion-error"} {
		t.Run(mode, func(t *testing.T) {
			machine := fixtureHelperMachine()
			run := domain.DevMachineAgentRun{ID: uuid.New(), MachineID: machine.ID, WorkspaceID: machine.WorkspaceID, Status: domain.DevMachineAgentRunStatusRunning}
			store := &managerStoreFake{machine: machine, agentRun: &run, timedOutRuns: []domain.DevMachineAgentRun{run}}
			if mode == "completion-error" {
				store.completeAgentRunErr = errors.New("database unavailable")
			}
			runtime := &runtimeFake{}
			manager := NewManager(store, runtime, agent.NewRegistry(), githubAPIFake{}, nil, make([]byte, 32), make([]byte, 32), "test")
			active, cancel := context.WithCancel(t.Context())
			defer cancel()
			manager.registerActiveRun(machine.ID, run.ID, cancel)
			otherRun := uuid.New()
			other, otherCancel := context.WithCancel(t.Context())
			defer otherCancel()
			manager.registerActiveRun(machine.ID, otherRun, otherCancel)
			runtime.onCancelAgent = func(_ context.Context, actualMachine *domain.DevMachine, actualRun *domain.DevMachineAgentRun) error {
				require.Equal(t, machine.ID, actualMachine.ID)
				require.Equal(t, run.ID, actualRun.ID)
				require.ErrorIs(t, active.Err(), context.Canceled)
				require.Nil(t, other.Err(), "an expired run must not cancel another active run")
				require.Nil(t, store.completedRun, "terminal completion must follow successful cleanup")
				if mode == "cleanup-error" {
					return errors.New("daemon unavailable")
				}
				return nil
			}
			require.NoError(t, manager.reconcile(t.Context()))
			if mode == "success" {
				require.NotNil(t, store.completedRun)
				require.Equal(t, domain.DevMachineAgentRunStatusTimeout, store.completedRun.Status)
				require.True(t, slices.ContainsFunc(store.events, func(event domain.DevMachineEvent) bool { return event.EventType == "agent_run.timeout" }))
			} else {
				require.Nil(t, store.completedRun)
				require.False(t, slices.ContainsFunc(store.events, func(event domain.DevMachineEvent) bool { return event.EventType == "agent_run.timeout" }))
			}
		})
	}
}

func TestManagerAgentRetryKeepsOriginalDeadline(t *testing.T) {
	machine := fixtureHelperMachine()
	machine.DesiredStatus = domain.DevMachineStatusRunning
	machine.Generation = 1
	started := time.Now().Add(-15 * time.Second)
	run := &domain.DevMachineAgentRun{ID: uuid.New(), MachineID: machine.ID, WorkspaceID: machine.WorkspaceID, ProviderID: "opencode", Mode: "autonomous", Status: domain.DevMachineAgentRunStatusRunning, MaxRuntimeSeconds: 30, StartedAt: &started}
	store := &managerStoreFake{machine: machine, agentRun: run, provider: &domain.DevMachineAgentProvider{MachineID: machine.ID, ProviderID: "opencode", ImageRef: "strifira/opencode:test", Enabled: true}}
	runtime := &runtimeFake{agentExecution: &AgentExecution{ExitCode: 0}}
	runtime.onAgentRun = func(ctx context.Context) {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.Equal(t, started.Add(30*time.Second), deadline, "retry must not grant another full runtime")
	}
	manager := NewManager(store, runtime, agent.NewRegistry(agent.NewOpenCodeProvider("strifira/opencode:test")), githubAPIFake{}, nil, make([]byte, 32), make([]byte, 32), "test")
	require.NoError(t, manager.runAgent(t.Context(), machine, &domain.DevMachineOperation{AgentRunID: &run.ID, MachineID: machine.ID, WorkspaceID: machine.WorkspaceID, Generation: 1}))
	require.Equal(t, started, *run.StartedAt)
}
