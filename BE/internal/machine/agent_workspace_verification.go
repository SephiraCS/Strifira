package machine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

const privateVerificationDirectoryFunctions = `
assert_owned_directory() {
  [ ! -L "$1" ] || exit 1
  if [ -e "$1" ]; then
    [ -d "$1" ] && [ "$(realpath -e "$1")" = "$1" ] || exit 1
  fi
}
clear_private_directory() {
  assert_owned_directory "$1"
  [ -d "$1" ] || return 0
  chmod u+rwx -- "$1"
  for entry in "$1"/* "$1"/.[!.]* "$1"/..?*; do
    if [ -e "$entry" ] || [ -L "$entry" ]; then
      if [ -d "$entry" ] && [ ! -L "$entry" ]; then chmod -R u+rwX -- "$entry"; fi
      rm -rf -- "$entry"
    fi
  done
}
`

const claimVerificationOwnerScript = `
if [ -e "$owner_file" ]; then
  previous=$(cat "$owner_file")
  if [ "$previous" = "$owner" ]; then exit 0; fi
  previous_epoch=${previous%%:*}
  case "$previous_epoch" in ''|*[!0-9]*) exit 1 ;; esac
  [ "$owner_epoch" -gt "$previous_epoch" ] || { echo 'verification reservation generation did not advance' >&2; exit 1; }
fi
[ ! -L "$owner_file.new" ]
printf '%s\n' "$owner" > "$owner_file.new"
mv -T -f -- "$owner_file.new" "$owner_file"
sync -f "$run_dir"
`

// Filesystem verification uses the protected store solely for run-private
// copies. Test code mounts neither the live workspace nor repository objects.
// Copies and compiler scratch files consume the existing machine disk quota.
const workspaceVerificationSetup = `set -eu
umask 077
store=$1
run_key=$2
source_dir=$3
case "$run_key" in ''|*[!a-f0-9-]*) exit 1 ;; esac
[ "${#run_key}" -eq 36 ]
[ -d "$store" ] && [ ! -L "$store" ] && [ "$(realpath -e "$store")" = "$store" ]
` + privateVerificationDirectoryFunctions + `
run_parent="$store/filesystem-runs"
assert_owned_directory "$run_parent"
if [ ! -d "$run_parent" ]; then mkdir -p -- "$run_parent"; fi
run_dir="$run_parent/$run_key"
assert_owned_directory "$run_dir"
if [ ! -d "$run_dir" ]; then mkdir -p -- "$run_dir"; fi
[ ! -L "$run_dir/verification.lock" ]
exec 9> "$run_dir/verification.lock"
flock -w 30 9
verification_dir="$run_dir/verification"
scratch_dir="$run_dir/verification-scratch"
`

const ensureWorkspaceVerificationDirectoryScript = workspaceVerificationSetup + candidateVerificationActiveGuard + `
assert_owned_directory "$verification_dir"
assert_owned_directory "$scratch_dir"
if [ ! -d "$verification_dir" ]; then mkdir "$verification_dir"; fi
if [ ! -d "$scratch_dir" ]; then mkdir "$scratch_dir"; fi
`

const claimWorkspaceVerificationScript = workspaceVerificationSetup + candidateVerificationActiveGuard + candidateVerificationOwnerValidation + `
` + claimVerificationOwnerScript

const workspaceVerificationFunctions = workspaceVerificationSetup + candidateVerificationOwnerValidation + `
[ -f "$owner_file" ] && [ "$(cat "$owner_file")" = "$owner" ] || { echo 'verification reservation ownership changed' >&2; exit 1; }
`

// cp never follows source symlinks or hard-links a file to the live workspace.
// Special files are refused, rather than copying sockets or blocking on FIFOs.
// Reset children only: Docker reserves these exact directory inodes first.
const prepareWorkspaceVerificationScript = workspaceVerificationFunctions + candidateVerificationActiveGuard + `
[ -d "$source_dir" ] && [ ! -L "$source_dir" ] && [ "$(realpath -e "$source_dir")" = "$source_dir" ]
special=$(find "$source_dir" ! -type f ! -type d ! -type l -print -quit)
[ -z "$special" ] || { echo 'workspace snapshot contains an unsupported special file' >&2; exit 1; }
clear_private_directory "$verification_dir"
clear_private_directory "$scratch_dir"
[ -d "$verification_dir" ] && [ -d "$scratch_dir" ]
cp -a --no-preserve=ownership -- "$source_dir/." "$verification_dir/"
`

const cleanupWorkspaceVerificationScript = workspaceVerificationFunctions + `
clear_private_directory "$verification_dir"
clear_private_directory "$scratch_dir"
rm -rf -- "$verification_dir" "$scratch_dir"
`

// Invoked after the durable cancellation fence has stopped every run writer.
// The terminal marker prevents a delayed preparer from recreating the copy.
const cleanupTerminalWorkspaceVerificationScript = workspaceVerificationSetup + `
[ ! -L "$run_dir/terminal-cleanup" ]
printf '%s\n' "$run_key" > "$run_dir/terminal-cleanup"
sync -f "$run_dir"
clear_private_directory "$verification_dir"
clear_private_directory "$scratch_dir"
rm -rf -- "$verification_dir" "$scratch_dir"
`

func verificationService(service string) bool {
	return service == "git-verification" || service == "workspace-verification"
}

func workspaceVerificationOptions(machine *domain.DevMachine, imageID, operationID string, runID uuid.UUID, argv []string) (client.ContainerCreateOptions, error) {
	operation, err := uuid.Parse(operationID)
	if err != nil || operation == uuid.Nil || operation.String() != operationID || runID == uuid.Nil || len(argv) == 0 || argv[0] == "" {
		return client.ContainerCreateOptions{}, errors.New("workspace verification requires a run, operation and test command")
	}
	for _, argument := range argv {
		if strings.ContainsRune(argument, '\x00') {
			return client.ContainerCreateOptions{}, errors.New("workspace test command contains a NUL byte")
		}
	}
	options, err := repositoryHelperOptions(machine, imageID, operationID, argv)
	if err != nil {
		return client.ContainerCreateOptions{}, err
	}
	if machine.PidsLimit <= 0 {
		return client.ContainerCreateOptions{}, errors.New("workspace verification requires a process budget")
	}
	options.Config.Labels["com.strifira.service"] = "workspace-verification"
	options.Config.Labels["com.strifira.agent-run-id"] = runID.String()
	options.Name = "strifira-" + machine.RoutingKey + "-verification-" + runID.String()
	options.Config.WorkingDir = "/workspace"
	options.Config.OpenStdin, options.Config.StdinOnce, options.Config.AttachStdin = false, false, false
	options.HostConfig.Tmpfs = nil
	options.HostConfig.Mounts = []mount.Mount{
		{Type: mount.TypeVolume, Source: *machine.WorkspaceVolumeName, Target: "/workspace", VolumeOptions: &mount.VolumeOptions{NoCopy: true, Subpath: "repositories/filesystem-runs/" + runID.String() + "/verification"}},
		{Type: mount.TypeVolume, Source: *machine.WorkspaceVolumeName, Target: "/tmp", VolumeOptions: &mount.VolumeOptions{NoCopy: true, Subpath: "repositories/filesystem-runs/" + runID.String() + "/verification-scratch"}},
	}
	options.HostConfig.Resources.Memory = int64(machine.MemoryMB) * 1024 * 1024
	options.HostConfig.Resources.MemorySwap = options.HostConfig.Resources.Memory
	options.HostConfig.Resources.NanoCPUs = int64(machine.CPUMillis) * 1_000_000
	options.HostConfig.Resources.PidsLimit = new(int64(machine.PidsLimit))
	return options, nil
}

func protectedWorkspaceVerificationMounts(inspection container.InspectResponse, machine *domain.DevMachine) bool {
	config, host := inspection.Config, inspection.HostConfig
	if machine == nil || machine.WorkspaceVolumeName == nil || machine.DockerNetworkName == nil || config == nil || host == nil || !ownedByMachine(config.Labels, machine.ID) || config.Labels["com.strifira.workspace-id"] != machine.WorkspaceID.String() || config.Labels["com.strifira.routing-key"] != machine.RoutingKey || config.Labels["com.strifira.service"] != "workspace-verification" || config.User != "1000:1000" || !isImmutableLocalImageID(config.Image) || config.WorkingDir != "/workspace" || len(config.Volumes) != 0 {
		return false
	}
	for _, key := range []string{"com.strifira.repository-operation", "com.strifira.agent-run-id"} {
		id, err := uuid.Parse(config.Labels[key])
		if err != nil || id == uuid.Nil || id.String() != config.Labels[key] {
			return false
		}
	}
	if len(host.Binds) != 0 || len(host.VolumesFrom) != 0 || len(host.Mounts) != 2 || len(host.Tmpfs) != 0 || host.Privileged || !host.ReadonlyRootfs || len(host.CapAdd) != 0 || !slices.Contains(host.CapDrop, "ALL") || !slices.Contains(host.SecurityOpt, "no-new-privileges=true") || string(host.NetworkMode) != *machine.DockerNetworkName {
		return false
	}
	for i, path := range []string{"verification", "verification-scratch"} {
		mounted := host.Mounts[i]
		target := "/workspace"
		if i == 1 {
			target = "/tmp"
		}
		if mounted.Type != mount.TypeVolume || mounted.Source != *machine.WorkspaceVolumeName || mounted.Target != target || mounted.ReadOnly || mounted.VolumeOptions == nil || !mounted.VolumeOptions.NoCopy || mounted.VolumeOptions.Subpath != "repositories/filesystem-runs/"+config.Labels["com.strifira.agent-run-id"]+"/"+path {
			return false
		}
	}
	return true
}

func (r *DockerRuntime) RunWorkspaceCommand(ctx context.Context, machine *domain.DevMachine, services []domain.DevMachineService, runID uuid.UUID, argv []string) (*CommandExecution, error) {
	if _, bounded := ctx.Deadline(); !bounded {
		return nil, errors.New("workspace verification requires a run deadline")
	}
	imageID, environment, err := r.verificationToolingImage(ctx, machine, services)
	if err != nil {
		return nil, err
	}
	options, err := workspaceVerificationOptions(machine, imageID, uuid.NewString(), runID, argv)
	if err != nil {
		return nil, err
	}
	options.Config.Env = environment
	runHelper := func(ctx context.Context, script, owner string, cleanup bool) error {
		_, err := r.runRepositoryHelper(ctx, machine, services, isolatedGitCommand(script, "strifira-workspace-verify", "/repositories", runID.String(), "/workspace", owner), "", repositoryHelperInvocation{RunID: runID, Cleanup: cleanup})
		return err
	}
	if err := runHelper(ctx, ensureWorkspaceVerificationDirectoryScript, "", false); err != nil {
		return nil, err
	}
	prepared := false
	owner := ""
	hooks := repositoryContainerHooks{
		afterCreate: func(ctx context.Context, containerID string) error {
			inspection, err := r.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
			if err != nil {
				return err
			}
			if inspection.Container.ID != containerID {
				return errors.New("workspace verification reservation container changed")
			}
			owner, err = verificationReservationOwner(inspection.Container, options.Config.Labels["com.strifira.repository-operation"])
			return err
		},
		beforeStart: func(ctx context.Context) error {
			if err := runHelper(ctx, claimWorkspaceVerificationScript, owner, false); err != nil {
				return err
			}
			prepared = true
			return runHelper(ctx, prepareWorkspaceVerificationScript, owner, false)
		},
		beforeRemove: func(ctx context.Context, containerID string) error {
			if !prepared {
				return nil
			}
			canClean, err := r.waitCandidateVerificationStopped(ctx, containerID)
			if err != nil || !canClean {
				return err
			}
			if err := runHelper(ctx, cleanupWorkspaceVerificationScript, owner, true); err != nil {
				return fmt.Errorf("private workspace verification cleanup failed: %w", err)
			}
			return nil
		},
	}
	return r.executeRepositoryContainer(ctx, machine, options, "", hooks)
}
