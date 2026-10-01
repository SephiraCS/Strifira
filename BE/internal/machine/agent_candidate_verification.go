package machine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// Verification lives below the protected store, outside every IDE/agent mount.
// Its writable mount exposes only this run's worktree; objects stay read-only.
// Both the worktree and its temporary staging data consume the machine quota.
const candidateVerificationSetup = protectedRepositoryFunctions + `
repository_dir=$1
run_key=$2
expected_commit=$3
case "$run_key" in ''|*[!A-Za-z0-9-]*) exit 1 ;; esac
case "$expected_commit" in *[!a-f0-9]*) exit 1 ;; esac
[ "${#expected_commit}" -eq 40 ] || [ "${#expected_commit}" -eq 64 ] || exit 1
[ "$(realpath -e "$repository_dir")" = "$repository_dir" ]
cd "$repository_dir"
exec 9> "$repository_dir/ku.lock"
flock -w 30 9
candidate=$(repository_git rev-parse --verify "refs/ku/runs/$run_key/candidate^{commit}")
[ "$candidate" = "$expected_commit" ]
run_dir="$repository_dir/ku-runs/$run_key"
[ -d "$run_dir" ] && [ ! -L "$run_dir" ] || exit 1
[ "$(realpath -e "$run_dir")" = "$run_dir" ]
verification_dir="$run_dir/verification"
scratch_dir="$run_dir/verification-scratch"
stage_dir="$run_dir/verification-stage"
` + privateVerificationDirectoryFunctions

const candidateVerificationActiveGuard = `
[ ! -e "$run_dir/terminal-cleanup" ] || { echo 'agent run is terminal' >&2; exit 1; }
`

const candidateVerificationOwnerValidation = `
owner=$4
owner_epoch=${owner%%:*}
owner_operation=${owner#*:}
case "$owner_epoch" in ''|*[!0-9]*) exit 1 ;; esac
case "$owner_operation" in ''|*[!a-f0-9-]*) exit 1 ;; esac
[ "${#owner_operation}" -eq 36 ]
[ "$owner_epoch" -gt 0 ]
owner_file="$run_dir/verification.owner"
[ ! -L "$owner_file" ]
`

const candidateVerificationFunctions = candidateVerificationSetup + candidateVerificationOwnerValidation + `
[ -f "$owner_file" ] && [ "$(cat "$owner_file")" = "$owner" ] || { echo 'verification reservation ownership changed' >&2; exit 1; }
`

// Docker creation timestamps order reservation generations. A delayed helper
// from a reclaimed invocation cannot replace a newer protected owner or touch
// its worktree. Clock regression refuses takeover rather than resetting data.
const claimCandidateVerificationScript = candidateVerificationSetup + candidateVerificationActiveGuard + candidateVerificationOwnerValidation + `
` + claimVerificationOwnerScript

// Only a trusted helper prepares/reclaims these exact directories. Never run
// this while a verification container still mounts them. Retrying starts from
// the retained commit, even after tests rewrote every byte of their Git config.
const prepareCandidateVerificationScript = candidateVerificationFunctions + candidateVerificationActiveGuard + `
working_branch=$5
git check-ref-format "refs/heads/$working_branch"
assert_owned_directory "$verification_dir"
assert_owned_directory "$scratch_dir"
assert_owned_directory "$stage_dir"
rm -rf -- "$stage_dir"
if [ ! -d "$verification_dir" ]; then mkdir "$verification_dir"; fi
if [ ! -d "$scratch_dir" ]; then mkdir "$scratch_dir"; fi
mkdir "$stage_dir"
trap 'rm -rf -- "$stage_dir"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
git -c protocol.file.allow=always clone --quiet --local --shared --no-checkout --template= -- "$repository_dir" "$stage_dir/worktree"
git_dir="$stage_dir/worktree/.git"
cat > "$git_dir/config" <<'CONFIG'
` + protectedRepositoryConfig + `CONFIG
git config --file "$git_dir/config" core.bare false
git --git-dir="$git_dir" --work-tree="$stage_dir/worktree" checkout --quiet -B "$working_branch" "$candidate"
# Keep the directory inode reserved by Docker. Remove/move only children of
# these exact authorized private directories; no live workspace is involved.
clear_private_directory "$verification_dir"
clear_private_directory "$scratch_dir"
for entry in "$stage_dir/worktree"/* "$stage_dir/worktree"/.[!.]* "$stage_dir/worktree"/..?*; do
  if [ -e "$entry" ] || [ -L "$entry" ]; then mv -- "$entry" "$verification_dir/"; fi
done
`

// Container creation may validate a subpath before the test starts. This
// idempotent step creates only its empty directory; it never resets existing
// contents, including when another invocation already holds the reservation.
const ensureCandidateVerificationDirectoryScript = candidateVerificationSetup + candidateVerificationActiveGuard + `
assert_owned_directory "$verification_dir"
assert_owned_directory "$scratch_dir"
if [ ! -d "$verification_dir" ]; then mkdir "$verification_dir"; fi
if [ ! -d "$scratch_dir" ]; then mkdir "$scratch_dir"; fi
`

// Tests control their worktree, including .git. Validate through the immutable
// helper's configuration, protected objects and a fresh, private index instead
// of trusting test-provided refs, index entries, hooks, filters or alternates.
// Untracked build output is allowed; every tracked path must still encode the
// candidate tree. This check runs only after the test container is removed.
const attestCandidateVerificationScript = candidateVerificationFunctions + candidateVerificationActiveGuard + `
assert_owned_directory "$verification_dir"
assert_owned_directory "$scratch_dir"
[ -d "$verification_dir" ]
assert_owned_directory "$stage_dir"
rm -rf -- "$stage_dir"
mkdir "$stage_dir"
trap 'rm -rf -- "$stage_dir"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export GIT_INDEX_FILE="$stage_dir/index" GIT_ATTR_SOURCE="$candidate"
verification_git() {
  git --git-dir="$repository_dir" --work-tree="$verification_dir" -c core.bare=false -c core.hooksPath=/dev/null -c core.fsmonitor=false -c core.attributesFile=/dev/null "$@"
}
verification_git read-tree "$candidate"
verification_git add --update -- .
expected_tree=$(repository_git rev-parse "$candidate^{tree}")
actual_tree=$(verification_git write-tree)
[ "$actual_tree" = "$expected_tree" ] || { echo 'tests changed tracked candidate content; branch was not published' >&2; exit 1; }
printf '%s\n' "$candidate"
`

const cleanupCandidateVerificationScript = candidateVerificationFunctions + `
assert_owned_directory "$verification_dir"
assert_owned_directory "$scratch_dir"
assert_owned_directory "$stage_dir"
clear_private_directory "$verification_dir"
clear_private_directory "$scratch_dir"
rm -rf -- "$verification_dir" "$scratch_dir" "$stage_dir"
`

func candidateVerificationOptions(machine *domain.DevMachine, imageID, operationID string, checkout *domain.DevMachineCheckout, runID uuid.UUID, candidate RepositoryCandidate, argv []string) (client.ContainerCreateOptions, error) {
	repository, _, err := authorizedRunRepository(machine, checkout, runID)
	if err != nil {
		return client.ContainerCreateOptions{}, err
	}
	operation, err := uuid.Parse(operationID)
	if err != nil || operation == uuid.Nil || runID == uuid.Nil || !validGitObjectID(candidate.Commit) || candidate.Branch != checkout.WorkingBranch || len(argv) == 0 || argv[0] == "" {
		return client.ContainerCreateOptions{}, errors.New("candidate verification requires a selected run, commit, branch and test command")
	}
	for _, argument := range argv {
		if strings.ContainsRune(argument, '\x00') {
			return client.ContainerCreateOptions{}, errors.New("candidate test command contains a NUL byte")
		}
	}
	options, err := repositoryHelperOptions(machine, imageID, operationID, argv)
	if err != nil {
		return client.ContainerCreateOptions{}, err
	}
	options.Config.Labels["com.ku.service"] = "git-verification"
	options.Config.Labels["com.ku.agent-run-id"] = runID.String()
	options.Config.Labels["com.ku.verification-commit"] = candidate.Commit
	options.Name = "ku-" + machine.RoutingKey + "-verification-" + runID.String()
	options.Config.WorkingDir = checkout.WorkspacePath
	options.Config.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/tmp", "LANG=C.UTF-8", "CI=true"}
	options.Config.OpenStdin, options.Config.StdinOnce, options.Config.AttachStdin = false, false, false
	options.HostConfig.Mounts[0].ReadOnly = false
	options.HostConfig.Mounts[0].Target = checkout.WorkspacePath
	options.HostConfig.Mounts[0].VolumeOptions.Subpath = strings.TrimPrefix(repository, "/") + "/ku-runs/" + runID.String() + "/verification"
	options.HostConfig.Mounts[1].ReadOnly = true
	options.HostConfig.Tmpfs = nil
	scratch := options.HostConfig.Mounts[0]
	scratch.Target = "/tmp"
	scratch.VolumeOptions = &mount.VolumeOptions{NoCopy: true, Subpath: strings.TrimSuffix(options.HostConfig.Mounts[0].VolumeOptions.Subpath, "/verification") + "/verification-scratch"}
	options.HostConfig.Mounts = append(options.HostConfig.Mounts, scratch)
	options.HostConfig.Resources.Memory = int64(machine.MemoryMB) * 1024 * 1024
	options.HostConfig.Resources.MemorySwap = options.HostConfig.Resources.Memory
	options.HostConfig.Resources.NanoCPUs = int64(machine.CPUMillis) * 1_000_000
	options.HostConfig.Resources.PidsLimit = new(int64(machine.PidsLimit))
	if machine.PidsLimit <= 0 {
		return client.ContainerCreateOptions{}, errors.New("candidate verification requires a process budget")
	}
	return options, nil
}

// A verification writer is confined to one backend-created run directory.
// Recognizing it never authorizes writes to repository objects or another run.
func protectedVerificationMounts(inspection container.InspectResponse, machine *domain.DevMachine) bool {
	config, host := inspection.Config, inspection.HostConfig
	if config == nil || host == nil || machine == nil || machine.WorkspaceVolumeName == nil || !ownedByMachine(config.Labels, machine.ID) || config.Labels["com.ku.service"] != "git-verification" || config.User != "1000:1000" || !isImmutableLocalImageID(config.Image) || !validGitObjectID(config.Labels["com.ku.verification-commit"]) {
		return false
	}
	for _, key := range []string{"com.ku.repository-operation", "com.ku.agent-run-id"} {
		id, err := uuid.Parse(config.Labels[key])
		if err != nil || id == uuid.Nil || id.String() != config.Labels[key] {
			return false
		}
	}
	path := config.WorkingDir
	if path != "/workspace/repository" {
		segment, ok := strings.CutPrefix(path, "/workspace/tasks/")
		if !ok || segment == "" || strings.Contains(segment, "..") {
			return false
		}
		for _, char := range segment {
			if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-", char) {
				return false
			}
		}
	}
	if len(host.Binds) != 0 || len(host.VolumesFrom) != 0 || len(host.Mounts) != 3 || len(host.Tmpfs) != 0 || host.Privileged || !host.ReadonlyRootfs || len(host.CapAdd) != 0 || !slices.Contains(host.CapDrop, "ALL") || !slices.Contains(host.SecurityOpt, "no-new-privileges=true") {
		return false
	}
	worktree, objects := host.Mounts[0], host.Mounts[1]
	if worktree.Type != mount.TypeVolume || objects.Type != mount.TypeVolume || worktree.Source != *machine.WorkspaceVolumeName || objects.Source != *machine.WorkspaceVolumeName || worktree.ReadOnly || !objects.ReadOnly || worktree.Target != path || objects.Target != "/repositories" || worktree.VolumeOptions == nil || objects.VolumeOptions == nil || !worktree.VolumeOptions.NoCopy || !objects.VolumeOptions.NoCopy || objects.VolumeOptions.Subpath != "repositories" {
		return false
	}
	parts := strings.Split(worktree.VolumeOptions.Subpath, "/")
	if len(parts) != 5 || parts[0] != "repositories" || parts[2] != "ku-runs" || parts[3] != config.Labels["com.ku.agent-run-id"] || parts[4] != "verification" {
		return false
	}
	key, ok := strings.CutSuffix(parts[1], ".git")
	if !ok || len(key) != 64 || !validGitObjectID(key) {
		return false
	}
	scratch := host.Mounts[2]
	return scratch.Type == mount.TypeVolume && scratch.Source == *machine.WorkspaceVolumeName && !scratch.ReadOnly && scratch.Target == "/tmp" && scratch.VolumeOptions != nil && scratch.VolumeOptions.NoCopy && scratch.VolumeOptions.Subpath == strings.TrimSuffix(worktree.VolumeOptions.Subpath, "/verification")+"/verification-scratch"
}

// Test commands inherit tooling paths from the selected immutable IDE image,
// not the live IDE's environment. All credentials and shell startup variables
// are excluded. Proxy endpoints come only from backend-owned machine routing.
func candidateToolingEnvironment(machine *domain.DevMachine, imageEnvironment []string) ([]string, error) {
	values := map[string]string{"PATH": "/usr/local/bin:/usr/bin:/bin"}
	for _, entry := range imageEnvironment {
		key, value, _ := strings.Cut(entry, "=")
		if key == "" {
			return nil, errors.New("candidate tooling image has an invalid environment key")
		}
		for index, char := range key {
			if !(char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char == '_' || index > 0 && char >= '0' && char <= '9') {
				return nil, errors.New("candidate tooling image has an invalid environment key")
			}
		}
		// Docker merges image defaults even when Config.Env is supplied.
		// Override every unapproved image key explicitly, without its value.
		values[key] = ""
		if key != "PATH" && key != "GOROOT" && key != "RUSTUP_HOME" {
			continue
		}
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return nil, errors.New("candidate tooling image has an invalid environment path")
		}
		values[key] = value
	}
	for key, value := range map[string]string{"HOME": "/tmp", "LANG": "C.UTF-8", "CI": "true", "GOPATH": "/tmp/go", "GOCACHE": "/tmp/go-cache", "CARGO_HOME": "/tmp/cargo"} {
		values[key] = value
	}
	proxy := "http://" + machine.RoutingKey + "-egress:3128"
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		values[key] = proxy
	}
	values["NO_PROXY"], values["no_proxy"] = "localhost,127.0.0.1", "localhost,127.0.0.1"
	result := make([]string, 0, len(values))
	for _, key := range slices.Sorted(maps.Keys(values)) {
		result = append(result, key+"="+values[key])
	}
	return result, nil
}

func (r *DockerRuntime) verificationToolingImage(ctx context.Context, machine *domain.DevMachine, services []domain.DevMachineService) (string, []string, error) {
	if machine == nil {
		return "", nil, errors.New("verification machine is unavailable")
	}
	var toolingRef string
	for _, service := range services {
		if service.ServiceType == "ide" {
			toolingRef = service.ImageRef
			break
		}
	}
	if toolingRef == "" {
		return "", nil, errors.New("verification tooling image is unavailable")
	}
	if machine.EnvironmentID != nil {
		if err := r.ensureEnvironmentImage(ctx, toolingRef, machine.WorkspaceID, *machine.EnvironmentID); err != nil {
			return "", nil, err
		}
	} else if err := r.ensureImage(ctx, toolingRef); err != nil {
		return "", nil, err
	}
	image, err := r.client.ImageInspect(ctx, toolingRef)
	if err != nil {
		return "", nil, err
	}
	if image.Config == nil || len(image.Config.Volumes) != 0 {
		return "", nil, errors.New("verification tooling image has unavailable configuration or unmanaged volumes")
	}
	environment, err := candidateToolingEnvironment(machine, image.Config.Env)
	return image.ID, environment, err
}

func (r *DockerRuntime) RunCandidateCommand(ctx context.Context, machine *domain.DevMachine, services []domain.DevMachineService, checkout *domain.DevMachineCheckout, runID uuid.UUID, candidate RepositoryCandidate, argv []string) (*CommandExecution, error) {
	if _, bounded := ctx.Deadline(); !bounded {
		return nil, errors.New("candidate verification requires a run deadline")
	}
	repository, _, err := authorizedRunRepository(machine, checkout, runID)
	if err != nil {
		return nil, err
	}
	imageID, environment, err := r.verificationToolingImage(ctx, machine, services)
	if err != nil {
		return nil, err
	}
	options, err := candidateVerificationOptions(machine, imageID, uuid.NewString(), checkout, runID, candidate, argv)
	if err != nil {
		return nil, err
	}
	options.Config.Env = environment
	helperImage, err := r.repositoryHelperImage(ctx, services)
	if err != nil {
		return nil, err
	}
	if err := r.verifyRepositoryHelperWorkspace(ctx, machine, helperImage); err != nil {
		return nil, err
	}
	_, err = r.runRepositoryHelper(ctx, machine, services, isolatedGitCommand(ensureCandidateVerificationDirectoryScript, "ku-verify-directory", repository, runID.String(), candidate.Commit), "", repositoryHelperInvocation{RunID: runID})
	if err != nil {
		return nil, err
	}
	// Creation reserves the run name before touching any staging data. Docker
	// receives a stable directory before preparation changes its contents.
	prepared := false
	owner := ""
	hooks := repositoryContainerHooks{
		afterCreate: func(ctx context.Context, containerID string) error {
			inspection, err := r.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
			if err != nil {
				return err
			}
			if inspection.Container.ID != containerID {
				return errors.New("verification reservation container changed")
			}
			owner, err = verificationReservationOwner(inspection.Container, options.Config.Labels["com.ku.repository-operation"])
			return err
		},
		beforeStart: func(ctx context.Context) error {
			_, err := r.runRepositoryHelper(ctx, machine, services, isolatedGitCommand(claimCandidateVerificationScript, "ku-verify-claim", repository, runID.String(), candidate.Commit, owner), "", repositoryHelperInvocation{RunID: runID})
			if err != nil {
				return err
			}
			prepared = true
			_, err = r.runRepositoryHelper(ctx, machine, services, isolatedGitCommand(prepareCandidateVerificationScript, "ku-verify-prepare", repository, runID.String(), candidate.Commit, owner, checkout.WorkingBranch), "", repositoryHelperInvocation{RunID: runID})
			return err
		},
		afterExit: func(ctx context.Context, execution *CommandExecution) error {
			if execution.ExitCode != 0 {
				return nil
			}
			output, err := r.runRepositoryHelper(ctx, machine, services, isolatedGitCommand(attestCandidateVerificationScript, "ku-verify-attest", repository, runID.String(), candidate.Commit, owner), "", repositoryHelperInvocation{RunID: runID})
			if err != nil {
				return err
			}
			if strings.TrimSpace(output) != candidate.Commit {
				return errors.New("candidate attestation returned a different commit")
			}
			return nil
		},
		beforeRemove: func(ctx context.Context, containerID string) error {
			if !prepared {
				return nil
			}
			canClean, err := r.waitCandidateVerificationStopped(ctx, containerID)
			if err != nil {
				return err
			}
			if !canClean {
				// A created container can have a late start in flight. Its
				// final forced removal settles that race, so retain the exact
				// private directories for the next reserved preparation.
				return nil
			}

			_, err = r.runRepositoryHelper(ctx, machine, services, isolatedGitCommand(cleanupCandidateVerificationScript, "ku-verify-cleanup", repository, runID.String(), candidate.Commit, owner), "", repositoryHelperInvocation{RunID: runID, Cleanup: true})
			if err != nil {
				return fmt.Errorf("private candidate cleanup failed: %w", err)
			}
			return nil
		},
	}
	return r.executeRepositoryContainer(ctx, machine, options, "", hooks)
}

// Docker may close attachment before a concurrent forced removal finishes.
// Never clear mounted data in that interval; wait for this immutable ID to
// disappear or prove that its process is stopped. Running/paused/ambiguous
// states still refuse cleanup rather than guessing from attachment EOF.
func (r *DockerRuntime) waitCandidateVerificationStopped(ctx context.Context, containerID string) (bool, error) {
	_, err := r.client.ContainerStop(ctx, containerID, client.ContainerStopOptions{Timeout: new(0)})
	if err != nil && !errdefs.IsNotModified(err) && !errdefs.IsNotFound(err) && !errdefs.IsConflict(err) {
		return false, err
	}
	for {
		inspection, err := r.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
		if errdefs.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		state := inspection.Container.State
		if inspection.Container.ID != containerID || state == nil {
			return false, errors.New("candidate verification stop authority is unavailable; private files were retained")
		}
		if state.Status != container.StateRemoving {
			if state.Running || state.Paused || state.Restarting || state.Status != container.StateCreated && state.Status != container.StateExited {
				return false, fmt.Errorf("candidate verification container has not stopped (status=%s); private files were retained", state.Status)
			}
			return state.Status == container.StateExited, nil
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-timer.C:
		}
	}
}
