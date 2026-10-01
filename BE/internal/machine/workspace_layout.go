package machine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

const workspaceLayoutVersion = "2"

var errWorkspaceLayoutNotReady = errors.New("workspace layout migration is not complete")
var errWorkspaceLayoutCleanup = errors.New("workspace layout initializer cleanup failed")

// The nonce is held by a mountless, backend-created Docker record. A marker in
// the legacy agent-writable volume alone is never evidence of a trusted layout.
// Renames retain all old names, metadata, symlinks, dirty files and Git indexes,
// and do not duplicate history or require free space for a second volume.
const workspaceLayoutScript = `set -eu
root=$1
nonce=$2
mode=$3
case "$nonce" in ''|*[!a-f0-9-]*) exit 1 ;; esac
root=$(realpath -e "$root")
state="$root/.ku-layout-$nonce"
# A missing Docker authority record must not rewrap a previously protected
# layout or expose its retained repositories inside the writable workspace.
# Old metadata is a reason to fail closed, never authority to adopt its nonce.
for previous in "$root"/.ku-layout-*; do
  [ -e "$previous" ] || [ -L "$previous" ] || continue
  [ "$previous" != "$state" ] || continue
  if [ "$(stat -c %u "$previous")" = 0 ]; then exit 77; fi
done
verify_ready() {
  [ -d "$state" ] && [ ! -L "$state" ] || exit 1
  [ "$(stat -c %u:%a "$state")" = 0:700 ]
  [ -f "$state/ready" ] && [ ! -L "$state/ready" ] || exit 1
  [ "$(stat -c %u:%a "$state/ready")" = 0:600 ]
  [ "$(cat "$state/ready")" = "$nonce" ]
  [ "$(stat -c %u:%a "$root")" = 0:755 ]
  [ -d "$root/workspace" ] && [ ! -L "$root/workspace" ] || exit 1
  [ -d "$root/repositories" ] && [ ! -L "$root/repositories" ] || exit 1
  [ "$(stat -c %u "$root/workspace")" = 1000 ]
  [ "$(stat -c %u:%a "$root/repositories")" = 1000:700 ]
}
if [ "$mode" = verify ]; then
  if [ ! -e "$state/ready" ] && [ ! -L "$state/ready" ]; then exit 76; fi
  verify_ready
  exit 0
fi
[ "$mode" = migrate ]
umask 077
# No legacy container can be started after the guarded removal phase. Protect
# the volume root before creating any migration state or repository storage.
chown 0:0 "$root"
chmod 0755 "$root"
if [ ! -e "$state" ] && [ ! -L "$state" ]; then mkdir -m 0700 "$state"; fi
[ -d "$state" ] && [ ! -L "$state" ] && [ "$(stat -c %u:%a "$state")" = 0:700 ] || exit 1
exec 9> "$state/lock"
flock -w 30 9
if [ -e "$state/ready" ]; then verify_ready; exit 0; fi
phase_file="$state/phase"
write_phase() {
  printf '%s\n' "$1" > "$state/phase.new"
  mv -f -- "$state/phase.new" "$phase_file"
  sync -f "$root"
}
if [ ! -e "$phase_file" ]; then
  if [ ! -e "$state/contents" ] && [ ! -L "$state/contents" ]; then mkdir -m 0700 "$state/contents"; fi
  [ -d "$state/contents" ] && [ ! -L "$state/contents" ] || exit 1
  write_phase collecting
fi
phase=$(cat "$phase_file")
if [ "$phase" = collecting ]; then
  [ -d "$state/contents" ] && [ ! -L "$state/contents" ] || exit 1
  for source in "$root"/.[!.]* "$root"/..?* "$root"/*; do
    [ -e "$source" ] || [ -L "$source" ] || continue
    [ "$source" != "$state" ] || continue
    name=${source##*/}
    [ ! -e "$state/contents/$name" ] && [ ! -L "$state/contents/$name" ] || exit 1
    mv -- "$source" "$state/contents/"
  done
  write_phase collected
  phase=collected
fi
if [ "$phase" = collected ]; then
  if [ -d "$state/contents" ] && [ ! -L "$state/contents" ]; then
    [ ! -e "$root/workspace" ] && [ ! -L "$root/workspace" ] || exit 1
    mv -- "$state/contents" "$root/workspace"
  fi
  [ -d "$root/workspace" ] && [ ! -L "$root/workspace" ] || exit 1
  if [ ! -e "$root/repositories" ] && [ ! -L "$root/repositories" ]; then mkdir -m 0700 "$root/repositories"; fi
  [ -d "$root/repositories" ] && [ ! -L "$root/repositories" ] || exit 1
  chmod 0755 "$root/workspace"
  chmod 0700 "$root/repositories"
  chown 1000:1000 "$root/workspace" "$root/repositories"
  write_phase installed
  phase=installed
fi
[ "$phase" = installed ]
printf '%s\n' "$nonce" > "$state/ready.new"
chmod 0600 "$state/ready.new"
mv -f -- "$state/ready.new" "$state/ready"
sync -f "$root"
verify_ready
`

func workspaceLayoutName(machine *domain.DevMachine) string {
	return "ku-" + machine.RoutingKey + "-workspace-layout"
}

func validateWorkspaceLayoutRecord(inspection container.InspectResponse, machine *domain.DevMachine, volumeName string) (string, error) {
	if inspection.Config == nil || inspection.HostConfig == nil {
		return "", errors.New("workspace layout record is incomplete")
	}
	labels := inspection.Config.Labels
	key, err := uuid.Parse(labels["com.ku.layout-key"])
	if err != nil || key == uuid.Nil || key.String() != labels["com.ku.layout-key"] || !ownedByMachine(labels, machine.ID) ||
		labels["com.ku.service"] != "workspace-layout" || labels["com.ku.layout-version"] != workspaceLayoutVersion || labels["com.ku.layout-volume"] != volumeName ||
		len(inspection.HostConfig.Mounts) != 0 || len(inspection.HostConfig.Binds) != 0 || len(inspection.Config.Volumes) != 0 {
		return "", errors.New("workspace layout record does not match this machine and volume")
	}
	return key.String(), nil
}

// Called only after a volume created by this Spawn invocation was removed.
// Existing migration records survive failed respawns and resume the same nonce.
func (r *DockerRuntime) removeWorkspaceLayoutRecord(ctx context.Context, machine *domain.DevMachine, volumeName string) error {
	inspection, err := r.client.ContainerInspect(ctx, workspaceLayoutName(machine), client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := validateWorkspaceLayoutRecord(inspection.Container, machine, volumeName); err != nil {
		return err
	}
	if inspection.Container.State == nil || inspection.Container.State.Running || inspection.Container.State.Paused {
		return errors.New("workspace layout record is not stopped")
	}
	_, err = r.client.ContainerRemove(ctx, inspection.Container.ID, client.ContainerRemoveOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	return err
}

// Git operations only verify existing authority. They never migrate, retire
// services, or infer readiness from agent-writable metadata.
func (r *DockerRuntime) verifyProtectedWorkspaceVolume(ctx context.Context, machine *domain.DevMachine, volumeName, imageID string) error {
	inspection, err := r.client.ContainerInspect(ctx, workspaceLayoutName(machine), client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return errors.New("workspace layout authority is unavailable; stop and prepare the machine first")
	}
	if err != nil {
		return err
	}
	key, err := validateWorkspaceLayoutRecord(inspection.Container, machine, volumeName)
	if err != nil {
		return err
	}
	return r.runWorkspaceLayoutInitializer(ctx, machine, volumeName, imageID, key, "verify")
}

// Inspect every legacy consumer before removing any. Running/paused or foreign
// consumers cause a read-only failure; a stopped machine can then be migrated
// and its services recreated without deleting volume data.
func (r *DockerRuntime) retireLegacyWorkspaceContainers(ctx context.Context, machine *domain.DevMachine, volumeName, imageID string) error {
	listed, err := r.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return err
	}
	var retired []string
	for _, item := range listed.Items {
		usesVolume := false
		for _, mounted := range item.Mounts {
			if mounted.Name == volumeName {
				usesVolume = true
				break
			}
		}
		if !usesVolume {
			continue
		}
		inspection, err := r.client.ContainerInspect(ctx, item.ID, client.ContainerInspectOptions{})
		if errdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		config := inspection.Container.Config
		trustedHelper := false
		if config != nil {
			operationID, operationErr := uuid.Parse(config.Labels["com.ku.repository-operation"])
			trustedHelper = ownedByMachine(config.Labels, machine.ID) && config.Labels["com.ku.service"] == "git-helper" &&
				operationErr == nil && operationID != uuid.Nil && config.Image == imageID && config.User == "1000:1000"
		}
		legacy := !workspaceConsumerHasSubpaths(inspection.Container.HostConfig, volumeName) &&
			!(trustedHelper && workspaceHelperMountsMatch(inspection.Container.HostConfig, volumeName))
		if !legacy {
			continue
		}
		if inspection.Container.Config == nil || !ownedByMachine(inspection.Container.Config.Labels, machine.ID) {
			return errors.New("legacy workspace container is not owned by this machine")
		}
		if inspection.Container.State == nil || inspection.Container.State.Running || inspection.Container.State.Paused {
			return errors.New("stop the machine before migrating its legacy workspace containers")
		}
		retired = append(retired, inspection.Container.ID)
	}
	for _, id := range retired {
		// Recheck immediately: never force-remove a container that was restarted
		// between the inspection pass and removal. Docker also refuses running
		// containers without Force. Model containers have no Docker access.
		inspection, err := r.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if errdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if inspection.Container.Config == nil || !ownedByMachine(inspection.Container.Config.Labels, machine.ID) || inspection.Container.State == nil || inspection.Container.State.Running || inspection.Container.State.Paused {
			return errors.New("legacy workspace container changed during migration preflight")
		}
		if _, err := r.client.ContainerRemove(ctx, id, client.ContainerRemoveOptions{RemoveVolumes: true}); err != nil && !errdefs.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func workspaceConsumerHasSubpaths(config *container.HostConfig, volumeName string) bool {
	if config == nil || len(config.Binds) != 0 {
		return false
	}
	found := false
	for _, mounted := range config.Mounts {
		if mounted.Type != mount.TypeVolume || mounted.Source != volumeName {
			continue
		}
		found = true
		if mounted.VolumeOptions == nil || (mounted.VolumeOptions.Subpath != "workspace" && mounted.VolumeOptions.Subpath != "repositories") {
			return false
		}
		if mounted.VolumeOptions.Subpath == "repositories" && !mounted.ReadOnly {
			return false
		}
	}
	return found
}

func workspaceHelperMountsMatch(config *container.HostConfig, volumeName string) bool {
	if config == nil || len(config.Binds) != 0 || len(config.Mounts) != 2 {
		return false
	}
	workspace, repositories := false, false
	for _, mounted := range config.Mounts {
		if mounted.Type != mount.TypeVolume || mounted.Source != volumeName || mounted.VolumeOptions == nil {
			return false
		}
		switch {
		case mounted.Target == "/workspace" && mounted.VolumeOptions.Subpath == "workspace" && mounted.ReadOnly:
			workspace = true
		case mounted.Target == "/repositories" && mounted.VolumeOptions.Subpath == "repositories" && !mounted.ReadOnly:
			repositories = true
		default:
			return false
		}
	}
	return workspace && repositories
}

func serviceWorkspaceMountsMatch(config *container.HostConfig, serviceType, volumeName string) bool {
	if config == nil {
		return false
	}
	if serviceType == "browser" || serviceType == "egress" {
		for _, mounted := range config.Mounts {
			if mounted.Type == mount.TypeVolume && mounted.Source == volumeName {
				return false
			}
		}
		return len(config.Binds) == 0
	}
	if !workspaceConsumerHasSubpaths(config, volumeName) {
		return false
	}
	workspace, repositories := false, false
	readOnly := serviceType == "collector"
	for _, mounted := range config.Mounts {
		if mounted.Type != mount.TypeVolume || mounted.Source != volumeName {
			continue
		}
		if mounted.Target == "/workspace" && mounted.VolumeOptions.Subpath == "workspace" && mounted.ReadOnly == readOnly {
			workspace = true
			continue
		}
		if mounted.Target == "/repositories" && mounted.VolumeOptions.Subpath == "repositories" && mounted.ReadOnly {
			repositories = true
			continue
		}
		return false
	}
	return workspace && repositories
}

func (r *DockerRuntime) requireQuiescentWorkspace(ctx context.Context, machine *domain.DevMachine, volumeName string) error {
	listed, err := r.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return err
	}
	for _, item := range listed.Items {
		for _, mounted := range item.Mounts {
			if mounted.Name != volumeName {
				continue
			}
			if !ownedByMachine(item.Labels, machine.ID) {
				return errors.New("workspace migration has a foreign volume consumer")
			}
			inspection, err := r.client.ContainerInspect(ctx, item.ID, client.ContainerInspectOptions{})
			if errdefs.IsNotFound(err) {
				break
			}
			if err != nil {
				return err
			}
			if inspection.Container.State == nil || inspection.Container.State.Running || inspection.Container.State.Paused {
				return errors.New("stop the machine before migrating its workspace layout")
			}
			break
		}
	}
	return nil
}

func (r *DockerRuntime) prepareProtectedWorkspaceVolume(ctx context.Context, machine *domain.DevMachine, volumeName, imageRef string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := r.ensureImage(ctx, imageRef); err != nil {
		return err
	}
	image, err := r.client.ImageInspect(ctx, imageRef)
	if err != nil {
		return err
	}
	if !isImmutableLocalImageID(image.ID) {
		return errors.New("workspace layout requires an immutable collector image")
	}
	exists, err := r.inspectWorkspaceVolume(ctx, volumeName, machineLabels(machine), machine.DiskGB)
	if err != nil || !exists {
		return errors.Join(err, errors.New("owned quota workspace volume is unavailable"))
	}
	name := workspaceLayoutName(machine)
	key := ""
	inspection, err := r.client.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err == nil {
		key, err = validateWorkspaceLayoutRecord(inspection.Container, machine, volumeName)
		if err != nil {
			return err
		}
	} else if !errdefs.IsNotFound(err) {
		return err
	}
	if err := r.retireLegacyWorkspaceContainers(ctx, machine, volumeName, image.ID); err != nil {
		return err
	}
	if key == "" {
		key = uuid.NewString()
		labels := machineLabels(machine)
		labels["com.ku.service"], labels["com.ku.layout-version"] = "workspace-layout", workspaceLayoutVersion
		labels["com.ku.layout-volume"], labels["com.ku.layout-key"] = volumeName, key
		_, err := r.client.ContainerCreate(ctx, client.ContainerCreateOptions{
			Name:   name,
			Config: &container.Config{Image: image.ID, User: "1000:1000", Entrypoint: []string{"/bin/true"}, Labels: labels},
			HostConfig: &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges=true"},
				Resources: container.Resources{Memory: 16 * 1024 * 1024, MemorySwap: 16 * 1024 * 1024, NanoCPUs: 100_000_000, PidsLimit: new(int64(8))}},
		})
		if err != nil {
			// A lost create response leaves only a mountless intent record. The
			// next attempt inspects its ownership and resumes the same nonce.
			return err
		}
	}
	// Prefer a read-only verification. It does not race repository helpers with
	// an unnecessary whole-volume writer on an already prepared machine.
	err = r.runWorkspaceLayoutInitializer(ctx, machine, volumeName, image.ID, key, "verify")
	if err == nil {
		return nil
	}
	if !errors.Is(err, errWorkspaceLayoutNotReady) || errors.Is(err, errWorkspaceLayoutCleanup) {
		return err
	}
	if err := r.requireQuiescentWorkspace(ctx, machine, volumeName); err != nil {
		return err
	}
	return r.runWorkspaceLayoutInitializer(ctx, machine, volumeName, image.ID, key, "migrate")
}

func (r *DockerRuntime) runWorkspaceLayoutInitializer(ctx context.Context, machine *domain.DevMachine, volumeName, imageID, key, mode string) (returnedErr error) {
	labels := machineLabels(machine)
	labels["com.ku.service"], labels["com.ku.layout-key"] = "volume-init", key
	capabilities := []string{}
	if mode == "migrate" {
		capabilities = []string{"CHOWN", "FOWNER", "DAC_OVERRIDE"}
	}
	operationID := uuid.NewString()
	name := "ku-" + machine.RoutingKey + "-layout-init-" + operationID
	labels["com.ku.layout-operation"] = operationID
	containerID := ""
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		cleanupFailed := func(err error) {
			returnedErr = fmt.Errorf("%w: %w", errWorkspaceLayoutCleanup, errors.Join(returnedErr, err))
		}
		if containerID == "" {
			inspection, err := r.client.ContainerInspect(cleanupCtx, name, client.ContainerInspectOptions{})
			if errdefs.IsNotFound(err) {
				return
			}
			if err != nil {
				cleanupFailed(err)
				return
			}
			if inspection.Container.Config == nil {
				cleanupFailed(errors.New("layout initializer cleanup record is incomplete"))
				return
			}
			labels := inspection.Container.Config.Labels
			if !ownedByMachine(labels, machine.ID) || labels["com.ku.layout-key"] != key || labels["com.ku.service"] != "volume-init" || labels["com.ku.layout-operation"] != operationID {
				cleanupFailed(errors.New("layout initializer cleanup ownership mismatch"))
				return
			}
			containerID = inspection.Container.ID
		}
		_, err := r.client.ContainerRemove(cleanupCtx, containerID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		if err != nil && !errdefs.IsNotFound(err) {
			// A cleanup failure must never be mistaken for an incomplete marker
			// and trigger a second, writable initializer.
			cleanupFailed(err)
		}
	}()
	created, err := r.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{Image: imageID, User: "0:0", WorkingDir: "/volume", Labels: labels,
			Entrypoint: isolatedGitCommand(workspaceLayoutScript, "ku-volume-layout", "/volume", key, mode)},
		HostConfig: &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, CapAdd: capabilities,
			SecurityOpt: []string{"no-new-privileges=true"}, LogConfig: boundedLogConfig(),
			Mounts:    []mount.Mount{{Type: mount.TypeVolume, Source: volumeName, Target: "/volume", ReadOnly: mode == "verify", VolumeOptions: &mount.VolumeOptions{NoCopy: true}}},
			Resources: container.Resources{Memory: 64 * 1024 * 1024, MemorySwap: 64 * 1024 * 1024, NanoCPUs: 100_000_000, PidsLimit: new(int64(32))}},
	})
	if err != nil {
		return err
	}
	containerID = created.ID
	if _, err := r.client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return err
	}
	waited := r.client.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case result, ok := <-waited.Result:
		if !ok {
			return errors.New("workspace layout wait ended without an exit result")
		}
		if result.Error != nil {
			return errors.New(result.Error.Message)
		}
		if result.StatusCode != 0 {
			if mode == "verify" && result.StatusCode == 76 {
				return errWorkspaceLayoutNotReady
			}
			_, stderr, _ := r.containerLogs(ctx, created.ID)
			return fmt.Errorf("workspace layout %s exited %d: %s", mode, result.StatusCode, strings.TrimSpace(stderr))
		}
		return nil
	case err, ok := <-waited.Error:
		if !ok || err == nil {
			return errors.New("workspace layout wait ended without an exit result")
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
