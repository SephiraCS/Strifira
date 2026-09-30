package machine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/agent"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

const workspaceCommandScript = `set -eu
pid_file=$1
shift
umask 077
export KU_COMMAND_PID_FILE="$pid_file"
setsid /bin/sh -c 'set -eu; printf "%s\n" "$$" > "$KU_COMMAND_PID_FILE"; exec "$@"' ku-command "$@" &
child=$!
cleanup() {
  if [ -f "$pid_file" ]; then
    pid=$(cat "$pid_file")
    case "$pid" in ''|*[!0-9]*) ;; *) kill -TERM -- "-$pid" 2>/dev/null || true ;; esac
    rm -f "$pid_file"
  fi
}
trap cleanup EXIT
trap 'cleanup; exit 130' INT
trap 'cleanup; exit 143' TERM
set +e
wait "$child"
status=$?
set -e
cleanup
exit "$status"
`

const cancelWorkspaceCommandScript = `set -eu
pid_file=$1
[ -f "$pid_file" ] || exit 0
pid=$(cat "$pid_file")
case "$pid" in ''|*[!0-9]*) exit 1 ;; esac
kill -TERM -- "-$pid" 2>/dev/null || true
sleep 0.2
kill -KILL -- "-$pid" 2>/dev/null || true
rm -f "$pid_file"
`

func (r *DockerRuntime) RunWorkspaceCommand(ctx context.Context, services []domain.DevMachineService, checkout *domain.DevMachineCheckout, argv []string) (*CommandExecution, error) {
	containerID := developerContainerID(services)
	if containerID == "" || len(argv) == 0 {
		return nil, errors.New("developer container and command are required")
	}
	workspacePath := "/workspace"
	if checkout != nil {
		workspacePath = checkout.WorkspacePath
	}
	pidFile := "/tmp/ku-command-" + uuid.NewString() + ".pid"
	command := append([]string{"/bin/sh", "-c", workspaceCommandScript, "ku-command", pidFile}, argv...)
	created, err := r.client.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		User: "1000:1000", WorkingDir: workspacePath, Cmd: command,
		AttachStdout: true, AttachStderr: true,
		Env: []string{"FORGEJO_TOKEN=", "GITHUB_TOKEN="},
	})
	if err != nil {
		return nil, err
	}
	attached, err := r.client.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return nil, err
	}
	defer attached.Close()
	// Exec attachment does not otherwise stop a stalled reader on cancellation.
	cleanupDone := make(chan struct{})
	stopClose := context.AfterFunc(ctx, func() {
		defer close(cleanupDone)
		attached.Close()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = r.execOutput(cleanupCtx, containerID, []string{"/bin/sh", "-c", cancelWorkspaceCommandScript, "ku-command-stop", pidFile})
	})
	defer func() {
		if !stopClose() {
			<-cleanupDone
		}
	}()
	stdout, stderr := newCappedBuffer(maxCapturedLogBytes), newCappedBuffer(maxCapturedLogBytes)
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attached.Reader); err != nil {
		return nil, err
	}
	inspection, err := r.client.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		return nil, err
	}
	return &CommandExecution{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: inspection.ExitCode}, nil
}

// Publishing runs after independent verification. Never let repository hooks
// inherit the token, and push to the checked URL rather than a mutable origin.
const publishAgentBranchScript = `set -eu
workspace_path=$1
repository_url=$2
working_branch=$3
expected_host=$4
expected_repository=$5
git -C "$workspace_path" -c core.hooksPath=/dev/null add --all
if ! git -C "$workspace_path" diff --cached --quiet; then
  git -C "$workspace_path" -c core.hooksPath=/dev/null -c user.name='Ku Agent' -c user.email='ku-agent@localhost' commit -m 'Complete delegated task'
fi
IFS= read -r FORGEJO_TOKEN
[ -n "$FORGEJO_TOKEN" ] || exit 1
export FORGEJO_TOKEN KU_FORGEJO_HOST="$expected_host" KU_GIT_REPOSITORY="$expected_repository"
helper='!f() {
protocol=; host=; path=
while IFS= read -r line && [ -n "$line" ]; do
  case "$line" in protocol=*) protocol=${line#protocol=} ;; host=*) host=${line#host=} ;; path=*) path=${line#path=} ;; esac
done
if [ "$protocol" = https ] && [ "$host" = "$KU_FORGEJO_HOST" ] && [ "${path%.git}" = "$KU_GIT_REPOSITORY" ]; then
  printf "%s\n" username=oauth2 "password=$FORGEJO_TOKEN"
fi
}; f'
GIT_TERMINAL_PROMPT=0 GIT_ASKPASS=/bin/false git -C "$workspace_path" -c core.hooksPath=/dev/null -c credential.helper= -c credential.helper="$helper" -c credential.useHttpPath=true push -- "$repository_url" "HEAD:refs/heads/$working_branch"
`

func (r *DockerRuntime) PublishAgentBranch(ctx context.Context, machine *domain.DevMachine, services []domain.DevMachineService, checkout *domain.DevMachineCheckout, repositoryURL, token string) error {
	parsed, err := url.Parse(repositoryURL)
	repository := checkoutRepositoryFullName(machine, checkout)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git") != repository {
		return errors.New("publishing requires a credential-free HTTPS URL for the selected repository")
	}
	workspacePath, branch := "/workspace", machine.WorkingBranch
	if checkout != nil {
		workspacePath, branch = checkout.WorkspacePath, checkout.WorkingBranch
	}
	if branch == "" || token == "" {
		return errors.New("publishing requires a working branch and credential")
	}
	return r.execWithInput(ctx, developerContainerID(services), []string{
		"/bin/sh", "-c", publishAgentBranchScript, "ku-publish", workspacePath, repositoryURL, branch, parsed.Host, repository,
	}, token+"\n")
}

func forbiddenChangedPaths(raw json.RawMessage, files []string) ([]string, error) {
	var forbidden []string
	if len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, &forbidden) != nil {
		return nil, errors.New("invalid forbidden path rules")
	}
	var violations []string
	for _, file := range files {
		file = strings.TrimPrefix(path.Clean(file), "./")
		for _, rule := range forbidden {
			rule = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(rule), "./"), "/")
			if rule == "" {
				continue
			}
			matched, err := path.Match(rule, file)
			if err != nil {
				return nil, fmt.Errorf("invalid forbidden path pattern %q", rule)
			}
			if matched || file == rule || strings.HasPrefix(file, rule+"/") {
				violations = append(violations, file)
				break
			}
		}
	}
	return violations, nil
}

func (m *Manager) verifyAgentRun(ctx context.Context, machine *domain.DevMachine, run *domain.DevMachineAgentRun, checkout *domain.DevMachineCheckout, services []domain.DevMachineService, result *agent.RunResult, secrets map[string]string) error {
	// Provider claims about tests are advisory. Only this executed argv can set
	// the persisted test status to passed.
	result.TestsRun, result.TestStatus = []string{}, "not_run"
	if run.TestCommand != nil && string(*run.TestCommand) != "null" {
		var argv []string
		if json.Unmarshal(*run.TestCommand, &argv) != nil {
			return errors.New("invalid test command")
		}
		if len(argv) > 0 {
			result.TestsRun = []string{strings.Join(argv, " ")}
			_ = m.createEvent(ctx, machine, &run.ID, "agent", "agent_run.tests_started", map[string]any{"argv": argv})
			execution, err := m.runtime.RunWorkspaceCommand(ctx, services, checkout, argv)
			result.TestStatus = "failed"
			if execution != nil {
				output := redact(execution.Stdout+execution.Stderr, secrets)
				if output != "" {
					_ = m.store.CreateLogChunk(ctx, &domain.DevMachineLogChunk{WorkspaceID: machine.WorkspaceID, MachineID: machine.ID, AgentRunID: &run.ID, Stream: "tests", Sequence: 1, Content: output})
				}
				_ = m.createEvent(ctx, machine, &run.ID, "agent", "agent_run.tests_completed", map[string]any{"argv": argv, "exit_code": execution.ExitCode})
			}
			if err != nil {
				return fmt.Errorf("test command execution failed: %w", err)
			}
			if execution == nil || execution.ExitCode != 0 {
				return errors.New("required test command failed; branch was not published")
			}
			result.TestStatus = "passed"
		}
	}
	state, err := m.runtime.GitState(ctx, machine, services, checkout)
	if err != nil {
		if run.PushBranch || run.OpenPullRequest {
			return fmt.Errorf("repository verification failed: %w", err)
		}
		result.RiskNotes = append(result.RiskNotes, "Git changes and forbidden paths could not be verified; no branch publication was requested.")
		return nil
	}
	result.Branch, result.Commits, result.ChangedFiles = state.Branch, state.Commits, state.ChangedFiles
	violations, err := forbiddenChangedPaths(run.ForbiddenPaths, state.ChangedFiles)
	if err != nil {
		return err
	}
	if len(violations) > 0 {
		return fmt.Errorf("forbidden paths changed: %s; branch was not published", strings.Join(violations, ", "))
	}
	if !run.PushBranch && !run.OpenPullRequest {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	expectedBranch := machine.WorkingBranch
	if checkout != nil {
		expectedBranch = checkout.WorkingBranch
	}
	if state.Branch == "" || state.Branch != expectedBranch {
		return errors.New("agent changed the working branch; branch was not published")
	}
	instanceURL, token, err := m.forgejoCredential(ctx, machine, checkoutRepositoryFullName(machine, checkout))
	if err != nil {
		return fmt.Errorf("publishing credential unavailable: %w", err)
	}
	// Derive the destination from the authorized instance, not a provider output
	// or a remote URL the model could change.
	repositoryURL := strings.TrimRight(instanceURL, "/") + "/" + checkoutRepositoryFullName(machine, checkout)
	secrets["FORGEJO_TOKEN"] = token
	if err := m.runtime.PublishAgentBranch(ctx, machine, services, checkout, repositoryURL, token); err != nil {
		return fmt.Errorf("branch publication failed: %s", redact(safeError(err), secrets))
	}
	if state, err := m.runtime.GitState(ctx, machine, services, checkout); err == nil {
		result.Commits = state.Commits
	}
	return m.createEvent(ctx, machine, &run.ID, "git", "agent_run.branch_published", map[string]any{"branch": result.Branch, "occurred_at": time.Now().UTC()})
}
