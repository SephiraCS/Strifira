package machine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
)

// Database terminal authority and the persistent Docker cancellation fence
// replace a vanished caller's authority. Retained objects/refs and checkout
// files are never removed. Failed cleanup is safe to retry after a restart.
func (r *DockerRuntime) CleanupAgentRun(ctx context.Context, machine *domain.DevMachine, services []domain.DevMachineService, checkout *domain.DevMachineCheckout, run *domain.DevMachineAgentRun) (RepositorySourceSync, error) {
	if machine == nil || run == nil || !terminalAgentStatus(run.Status) || run.ID == uuid.Nil || run.MachineID != machine.ID || run.WorkspaceID != machine.WorkspaceID {
		return RepositorySourceSync{}, errors.New("agent cleanup requires terminal authority for the exact machine, workspace and run")
	}
	if err := validateTerminalCleanupCheckout(machine, checkout, run); err != nil {
		return RepositorySourceSync{}, err
	}
	if err := r.CancelAgent(ctx, machine, run); err != nil {
		return RepositorySourceSync{}, err
	}
	if machine.WorkspaceVolumeName == nil || *machine.WorkspaceVolumeName == "" {
		return RepositorySourceSync{Status: "unchanged"}, nil
	}
	if checkout == nil {
		_, err := r.runRepositoryHelper(ctx, machine, services, isolatedGitCommand(cleanupTerminalWorkspaceVerificationScript,
			"ku-terminal-workspace-cleanup", "/repositories", run.ID.String(), "/workspace"), "", repositoryHelperInvocation{RunID: run.ID, Cleanup: true})
		return RepositorySourceSync{Status: "unchanged"}, err
	}
	repository, _, err := authorizedRunRepository(machine, checkout, run.ID)
	if err != nil {
		return RepositorySourceSync{}, err
	}
	output, err := r.runRepositoryHelper(ctx, machine, services, isolatedGitCommand(cleanupTerminalAgentRunScript,
		"ku-terminal-cleanup", repository, run.ID.String()), "", repositoryHelperInvocation{RunID: run.ID, Cleanup: true})
	if err != nil {
		return RepositorySourceSync{}, err
	}
	if output == "" {
		return RepositorySourceSync{Status: "unchanged"}, nil
	}
	candidate, err := parseRepositoryCandidate(output)
	if err != nil {
		return RepositorySourceSync{}, err
	}
	if candidate.Branch != checkout.WorkingBranch {
		return RepositorySourceSync{}, errors.New("terminal cleanup candidate branch changed")
	}
	stageKey, err := r.checkoutStageKey(ctx, machine, run.ID)
	if err != nil {
		return RepositorySourceSync{}, err
	}
	output, err = r.runRepositoryHelper(ctx, machine, services, isolatedGitCommand(synchronizeRepositoryCandidateScript,
		"ku-recover-terminal-source", checkout.WorkspacePath, repository, run.ID.String(), stageKey, candidate.Branch, candidate.Commit, candidate.SourceCommit, candidate.SourceIndexHash, "true"), "", repositoryHelperInvocation{RunID: run.ID, Checkout: true, SourceSync: true, Cleanup: true, Recovery: true})
	if err != nil {
		return RepositorySourceSync{}, fmt.Errorf("terminal source synchronization recovery failed: %w", err)
	}
	status, reason, _ := strings.Cut(strings.TrimSpace(output), "\n")
	if status != "updated" && status != "unchanged" && status != "conflict" || status == "conflict" && reason == "" || status != "conflict" && reason != "" {
		return RepositorySourceSync{}, errors.New("terminal recovery returned an invalid result")
	}
	return RepositorySourceSync{Status: status, Reason: reason}, nil
}

// A nil CheckoutID authorizes only the run's persisted implicit checkout.
// It cannot turn an arbitrary issue checkout or workspace path into authority.
func validateTerminalCleanupCheckout(machine *domain.DevMachine, checkout *domain.DevMachineCheckout, run *domain.DevMachineAgentRun) error {
	selected := run.RepositorySelection
	if selected == nil {
		if checkout != nil {
			return errors.New("agent cleanup repository selection is unavailable")
		}
		return nil
	}
	if checkout == nil || checkout.ID != selected.ID || checkout.MachineID != machine.ID || checkout.WorkspaceID != machine.WorkspaceID ||
		checkout.MachineID != selected.MachineID || checkout.WorkspaceID != selected.WorkspaceID ||
		checkout.RepositoryFullName != selected.RepositoryFullName || checkout.RepositoryURL != selected.RepositoryURL ||
		checkout.BaseBranch != selected.BaseBranch || checkout.WorkingBranch != selected.WorkingBranch || checkout.WorkspacePath != selected.WorkspacePath {
		return errors.New("agent cleanup repository selection changed")
	}
	if run.CheckoutID != nil {
		if checkout.ID != *run.CheckoutID {
			return errors.New("agent cleanup checkout authority changed")
		}
	} else if checkout.ID != machine.ID || checkout.WorkspacePath != "/workspace/repository" {
		return errors.New("agent cleanup default checkout authority changed")
	}
	_, _, err := protectedCheckoutRepository(checkout)
	return err
}

const cleanupTerminalAgentRunScript = `set -eu
umask 077
repository_dir=$1
run_key=$2
case "$run_key" in ''|*[!a-f0-9-]*) exit 1 ;; esac
[ "${#run_key}" -eq 36 ]
[ ! -L "$repository_dir" ]
if [ ! -e "$repository_dir" ]; then exit 0; fi
[ -d "$repository_dir" ] && [ "$(realpath -e "$repository_dir")" = "$repository_dir" ]
exec 9> "$repository_dir/ku.lock"
flock -w 30 9
run_parent="$repository_dir/ku-runs"
[ ! -L "$run_parent" ]
if [ ! -e "$run_parent" ]; then mkdir -m 0700 "$run_parent"; fi
[ -d "$run_parent" ] && [ "$(realpath -e "$run_parent")" = "$run_parent" ]
run_dir="$run_parent/$run_key"
[ ! -L "$run_dir" ]
if [ ! -e "$run_dir" ]; then mkdir -m 0700 "$run_dir"; fi
[ -d "$run_dir" ] && [ "$(realpath -e "$run_dir")" = "$run_dir" ]
[ ! -L "$run_dir/terminal-cleanup" ]
printf '%s\n' "$run_key" > "$run_dir/terminal-cleanup"
sync -f "$run_dir"
# Only exact directories or private snapshot names inside this protected run
# namespace may be reclaimed. Keep candidate.info, owner fences and all refs.
for directory in "$run_dir/verification" "$run_dir/verification-scratch" "$run_dir/verification-stage" "$run_dir"/snapshot.*; do
  [ ! -L "$directory" ] || exit 1
  if [ -e "$directory" ]; then
    [ -d "$directory" ] && [ "$(realpath -e "$directory")" = "$directory" ] || exit 1
    chmod -R u+rwX -- "$directory"
    rm -rf -- "$directory"
  fi
done
[ ! -L "$run_dir/candidate.info" ]
if [ -f "$run_dir/candidate.info" ]; then cat "$run_dir/candidate.info"; fi
`
