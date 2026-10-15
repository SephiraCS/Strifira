package machine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/agent"
	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
)

const workspaceCommandScript = `set -eu
pid_file=$1
shift
umask 077
export STRIFIRA_COMMAND_PID_FILE="$pid_file"
export STRIFIRA_COMMAND_CANCEL_FILE="$pid_file.cancel"
exec 3<&0
setsid /bin/sh -c 'set -eu; [ ! -e "$STRIFIRA_COMMAND_CANCEL_FILE" ] || exit 130; printf "%s\n" "$$" > "$STRIFIRA_COMMAND_PID_FILE"; [ ! -e "$STRIFIRA_COMMAND_CANCEL_FILE" ] || exit 130; exec "$@"' strifira-command "$@" <&3 &
child=$!
exec 3<&-
cleanup() {
  if [ -f "$pid_file" ]; then
    pid=$(cat "$pid_file")
    case "$pid" in ''|*[!0-9]*) ;; *) /usr/bin/env kill -TERM -- "-$pid" 2>/dev/null || true ;; esac
    rm -f "$pid_file"
  fi
  rm -f "$STRIFIRA_COMMAND_CANCEL_FILE"
}
cancel_command() {
  : > "$STRIFIRA_COMMAND_CANCEL_FILE"
  /usr/bin/env kill -TERM -- "-$child" 2>/dev/null || true
  /usr/bin/env kill -TERM -- "$child" 2>/dev/null || true
  wait "$child" 2>/dev/null || true
}
trap cleanup EXIT
trap 'cancel_command; exit 130' INT
trap 'cancel_command; exit 143' TERM
set +e
wait "$child"
status=$?
set -e
cleanup
exit "$status"
`

const cancelWorkspaceCommandScript = `set -eu
pid_file=$1
# Cancellation may arrive before setsid's child has published its PID. Leave a
# tombstone so both sides of that startup window reject execution.
umask 077
: > "$pid_file.cancel"
[ -f "$pid_file" ] || exit 0
pid=$(cat "$pid_file")
case "$pid" in ''|*[!0-9]*) exit 1 ;; esac
/usr/bin/env kill -TERM -- "-$pid" 2>/dev/null || true
sleep 0.2
/usr/bin/env kill -KILL -- "-$pid" 2>/dev/null || true
rm -f "$pid_file"
`

func (r *DockerRuntime) commandCancellation(ctx context.Context, containerID, pidFile string) (func(), func() bool) {
	cancelCommand := sync.OnceFunc(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = r.execOutput(cleanupCtx, containerID, []string{"/bin/sh", "-c", cancelWorkspaceCommandScript, "strifira-command-stop", pidFile})
	})
	return cancelCommand, context.AfterFunc(ctx, cancelCommand)
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

// Repository identity comes from the selected checkout or machine and the
// authorized GitHub instance. Source remotes and provider claims have no say.
func (m *Manager) prepareAgentRepository(ctx context.Context, machine *domain.DevMachine, checkout *domain.DevMachineCheckout, services []domain.DevMachineService, runID uuid.UUID) (*domain.DevMachineCheckout, error) {
	pinned, err := m.store.GetAgentRunRepository(ctx, runID)
	if err != nil {
		return nil, err
	}
	if pinned != nil {
		checkout = pinned
	}
	if checkout == nil && machine.RepoOwner == "" && machine.RepoName == "" {
		return nil, nil
	}
	repositoryFullName := checkoutRepositoryFullName(machine, checkout)
	instanceURL, token, err := m.githubCredential(ctx, machine, repositoryFullName)
	if err != nil {
		return nil, fmt.Errorf("repository credential unavailable: %w", err)
	}
	repositoryURL := strings.TrimRight(instanceURL, "/") + "/" + repositoryFullName
	var selected domain.DevMachineCheckout
	if checkout == nil {
		defaultCheckout, err := defaultRepositoryCheckout(machine, instanceURL)
		if err != nil {
			return nil, err
		}
		selected = *defaultCheckout
	} else {
		selected = *checkout
		if selected.RepositoryURL != "" && strings.TrimSuffix(selected.RepositoryURL, ".git") != repositoryURL {
			return nil, errors.New("checkout repository does not match the authorized GitHub instance")
		}
		selected.RepositoryURL = repositoryURL
	}
	if _, _, err := authorizedRunRepository(machine, &selected, runID); err != nil {
		return nil, err
	}
	if err := m.store.PinAgentRunRepository(ctx, runID, &selected); err != nil {
		return nil, fmt.Errorf("repository selection could not be persisted: %w", err)
	}
	if err := m.runtime.PrepareAgentRepository(ctx, machine, services, &selected, runID, token); err != nil {
		return nil, fmt.Errorf("repository preparation failed: %s", redact(safeError(err), map[string]string{"GITHUB_TOKEN": token}))
	}
	return &selected, nil
}

func (m *Manager) verifyAgentRun(ctx context.Context, machine *domain.DevMachine, run *domain.DevMachineAgentRun, checkout *domain.DevMachineCheckout, services []domain.DevMachineService, result *agent.RunResult, secrets map[string]string) error {
	// Provider claims are advisory. Persist only backend-observed Git and tests.
	result.Branch, result.Commits, result.ChangedFiles = "", nil, nil
	result.TestsRun, result.TestStatus = []string{}, "not_run"
	var argv []string
	if run.TestCommand != nil && string(*run.TestCommand) != "null" {
		if json.Unmarshal(*run.TestCommand, &argv) != nil {
			return errors.New("invalid test command")
		}
	}
	if checkout == nil {
		if run.PushBranch || run.OpenPullRequest {
			return errors.New("repository verification failed: no selected repository")
		}
		if len(argv) > 0 {
			if err := m.verifyAgentTests(ctx, machine, run, result, secrets, argv, "", func() (*CommandExecution, error) {
				return m.runtime.RunWorkspaceCommand(ctx, machine, services, run.ID, argv)
			}); err != nil {
				return err
			}
		}
		result.RiskNotes = append(result.RiskNotes, "Git changes and forbidden paths could not be verified; no branch publication was requested.")
		return nil
	}
	candidate, err := m.runtime.RetainAgentCandidate(ctx, machine, services, checkout, run.ID)
	if err != nil {
		return fmt.Errorf("repository verification failed: %w", err)
	}
	if !validGitObjectID(candidate.Commit) || !validGitObjectID(candidate.BaseCommit) {
		return errors.New("repository verification returned an invalid retained candidate")
	}
	result.Branch, result.Commits, result.ChangedFiles = candidate.Branch, candidate.Commits, candidate.ChangedFiles
	violations, err := forbiddenChangedPaths(run.ForbiddenPaths, candidate.ChangedFiles)
	if err != nil {
		return err
	}
	if len(violations) > 0 {
		return fmt.Errorf("forbidden paths changed: %s; branch was not published", strings.Join(violations, ", "))
	}
	if candidate.Branch == "" || candidate.Branch != checkout.WorkingBranch {
		return errors.New("agent changed the working branch; branch was not published")
	}
	if len(argv) > 0 {
		if err := m.verifyAgentTests(ctx, machine, run, result, secrets, argv, candidate.Commit, func() (*CommandExecution, error) {
			return m.runtime.RunCandidateCommand(ctx, machine, services, checkout, run.ID, candidate, argv)
		}); err != nil {
			return err
		}
	}
	if !run.PushBranch && !run.OpenPullRequest {
		m.synchronizeAgentCandidate(ctx, machine, run, checkout, services, candidate, result, secrets)
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	instanceURL, token, err := m.githubCredential(ctx, machine, checkout.RepositoryFullName)
	if err != nil {
		return fmt.Errorf("publishing credential unavailable: %w", err)
	}
	if strings.TrimRight(instanceURL, "/")+"/"+checkout.RepositoryFullName != checkout.RepositoryURL {
		return errors.New("authorized GitHub instance changed during the run; branch was not published")
	}
	secrets["GITHUB_TOKEN"] = token
	if err := m.runtime.PublishAgentCandidate(ctx, machine, services, checkout, run.ID, candidate, token); err != nil {
		return fmt.Errorf("branch publication failed: %s", redact(safeError(err), secrets))
	}
	if err := m.createEvent(ctx, machine, &run.ID, "git", "agent_run.branch_published", map[string]any{"branch": result.Branch, "commit": candidate.Commit, "occurred_at": time.Now().UTC()}); err != nil {
		return err
	}
	m.synchronizeAgentCandidate(ctx, machine, run, checkout, services, candidate, result, secrets)
	return nil
}

func (m *Manager) verifyAgentTests(ctx context.Context, machine *domain.DevMachine, run *domain.DevMachineAgentRun, result *agent.RunResult, secrets map[string]string, argv []string, commit string, execute func() (*CommandExecution, error)) error {
	result.TestsRun = []string{strings.Join(argv, " ")}
	result.TestStatus = "failed"
	_ = m.createEvent(ctx, machine, &run.ID, "agent", "agent_run.tests_started", map[string]any{"argv": argv, "commit": commit})
	execution, err := execute()
	if execution != nil {
		stdout := redactLogFragment(execution.Stdout, secrets, execution.StdoutTruncated)
		stderr := redactLogFragment(execution.Stderr, secrets, execution.StderrTruncated)
		if execution.StdoutTruncated {
			stdout += "\n[Strifira: stdout truncated after the 4 MiB capture limit]\n"
		}
		if execution.StderrTruncated {
			stderr += "\n[Strifira: stderr truncated after the 4 MiB capture limit]\n"
		}
		truncated := execution.StdoutTruncated || execution.StderrTruncated
		output := stdout + stderr
		if output != "" {
			if logErr := m.store.CreateLogChunk(ctx, &domain.DevMachineLogChunk{WorkspaceID: machine.WorkspaceID, MachineID: machine.ID, AgentRunID: &run.ID, Stream: "tests", Sequence: 1, Content: output, Truncated: truncated}); logErr != nil {
				result.RiskNotes = append(result.RiskNotes, "Test output could not be fully saved.")
			}
		}
		if truncated {
			result.RiskNotes = append(result.RiskNotes, "Test output exceeded the capture limit and was truncated; the process exit status was verified.")
		}
		_ = m.createEvent(ctx, machine, &run.ID, "agent", "agent_run.tests_completed", map[string]any{"argv": argv, "commit": commit, "exit_code": execution.ExitCode, "verified": err == nil && execution.ExitCode == 0, "output_truncated": truncated})
	}
	if err != nil {
		return fmt.Errorf("test command execution failed: %w", err)
	}
	if execution == nil || execution.ExitCode != 0 {
		return errors.New("required test command failed")
	}
	result.TestStatus = "passed"
	return nil
}

// A verified/published candidate stays successful when later user work prevents
// local metadata synchronization. The conflict is visible and no edits reset.
func (m *Manager) synchronizeAgentCandidate(ctx context.Context, machine *domain.DevMachine, run *domain.DevMachineAgentRun, checkout *domain.DevMachineCheckout, services []domain.DevMachineService, candidate RepositoryCandidate, result *agent.RunResult, secrets map[string]string) {
	sync, err := m.runtime.SynchronizeAgentCandidate(ctx, machine, services, checkout, run.ID, candidate)
	if err != nil {
		sync.Status, sync.Reason = "failed", redact(safeError(err), secrets)
	}
	if sync.Status == "conflict" || sync.Status == "failed" {
		result.RiskNotes = append(result.RiskNotes, "Verified candidate could not be synchronized with the editable checkout: "+sync.Reason)
	}
	_ = m.createEvent(ctx, machine, &run.ID, "git", "agent_run.source_synchronized", map[string]any{"commit": candidate.Commit, "status": sync.Status, "reason": sync.Reason})
}
