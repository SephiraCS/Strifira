package machine

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

// This qualification creates disposable owned quota volumes and containers.
// It exercises the shipped script with real root/UID1000 permissions, rather
// than replacing chown/stat or treating a mocked daemon as preservation proof.
func TestWorkspaceLayoutActualDocker(t *testing.T) {
	image := os.Getenv("KU_LAYOUT_IMAGE")
	if image == "" {
		t.Skip("set KU_LAYOUT_IMAGE to the freshly built shipped collector image")
	}
	runtime, err := NewDockerRuntime(DockerConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = runtime.client.Close() })
	inspection, err := runtime.client.ImageInspect(t.Context(), image)
	require.NoError(t, err)
	image = inspection.ID
	t.Logf("immutable layout image: %s", image)
	newVolume := func(t *testing.T) *domain.DevMachine {
		t.Helper()
		machine := fixtureHelperMachine()
		machine.RoutingKey = "layout-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
		machine.WorkspaceVolumeName = new("ku-workspace-" + machine.RoutingKey)
		created, err := runtime.ensureVolume(t.Context(), *machine.WorkspaceVolumeName, machineLabels(machine), 1)
		require.NoError(t, err)
		require.True(t, created)
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			listed, err := runtime.client.ContainerList(ctx, client.ContainerListOptions{All: true})
			require.NoError(t, err)
			for _, item := range listed.Items {
				if ownedByMachine(item.Labels, machine.ID) {
					_, err := runtime.client.ContainerRemove(ctx, item.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
					require.NoError(t, err)
				}
			}
			require.NoError(t, runtime.removeManagedVolume(ctx, *machine.WorkspaceVolumeName, machine.ID))
		})
		return machine
	}
	run := func(t *testing.T, machine *domain.DevMachine, command []string) (int64, string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
		defer cancel()
		created, err := runtime.client.ContainerCreate(ctx, client.ContainerCreateOptions{
			Config: &container.Config{Image: image, User: "0:0", WorkingDir: "/volume", Entrypoint: command, Labels: machineLabels(machine)},
			HostConfig: &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, CapAdd: []string{"CHOWN", "FOWNER", "DAC_OVERRIDE"},
				SecurityOpt: []string{"no-new-privileges=true"}, LogConfig: boundedLogConfig(),
				Mounts:    []mount.Mount{{Type: mount.TypeVolume, Source: *machine.WorkspaceVolumeName, Target: "/volume", VolumeOptions: &mount.VolumeOptions{NoCopy: true}}},
				Resources: container.Resources{Memory: 256 * 1024 * 1024, MemorySwap: 256 * 1024 * 1024, PidsLimit: new(int64(32)), NanoCPUs: 400_000_000}},
		})
		require.NoError(t, err)
		defer func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cleanupCancel()
			_, err := runtime.client.ContainerRemove(cleanupCtx, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
			require.NoError(t, err)
		}()
		_, err = runtime.client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{})
		require.NoError(t, err)
		waited := runtime.client.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
		select {
		case result := <-waited.Result:
			require.Nil(t, result.Error)
			stdout, stderr, err := runtime.containerLogs(ctx, created.ID)
			require.NoError(t, err)
			if result.StatusCode != 0 {
				t.Logf("fixture exit %d: %s", result.StatusCode, stderr)
			}
			return result.StatusCode, stdout
		case err := <-waited.Error:
			t.Fatalf("fixture wait: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		return -1, ""
	}
	t.Run("large-dirty-checkout-and-collisions", func(t *testing.T) {
		machine := newVolume(t)
		seed := `set -eu
mkdir -p /volume/tasks/TS-279 /volume/workspace /volume/repositories
printf 'legacy-workspace\n' > /volume/workspace/existing
printf 'legacy-repositories\n' > /volume/repositories/existing
printf 'hidden\n' > /volume/.hidden
printf 'spaces\n' > '/volume/space name'
ln -s /etc/passwd /volume/hostile-link
cd /volume/tasks/TS-279
git init --quiet -b main
git config user.name fixture
git config user.email fixture@localhost
dd if=/dev/urandom of=history.bin bs=1M count=145 status=none
printf 'base\n' > source.txt
git add .
git commit --quiet -m base
printf 'staged\n' > source.txt
git add source.txt
printf 'later-edit\n' >> source.txt
printf 'untracked\n' > untracked.txt
chmod 0640 source.txt
ln source.txt source-hardlink
ln -s source.txt source-link
chown -R 1000:1000 /volume
`
		code, _ := run(t, machine, isolatedGitCommand(seed, "seed"))
		require.Zero(t, code)
		// Relative names, file modes/owners and inodes must survive the rename.
		manifest := `set -eu
cd "$1"
for name in .hidden 'space name' hostile-link workspace/existing repositories/existing tasks/TS-279/source.txt tasks/TS-279/source-hardlink tasks/TS-279/source-link tasks/TS-279/.git/index tasks/TS-279/history.bin; do
  printf '%s ' "$name"
  stat -c '%i:%u:%g:%a:%s' -- "$name"
  if [ -L "$name" ]; then readlink -- "$name"; else sha256sum -- "$name"; fi
done
git --no-optional-locks -c safe.directory="$PWD/tasks/TS-279" -C tasks/TS-279 status --porcelain=v1 -z
git --no-optional-locks -c safe.directory="$PWD/tasks/TS-279" -C tasks/TS-279 rev-parse HEAD
`
		code, before := run(t, machine, isolatedGitCommand(manifest, "manifest", "/volume"))
		require.Zero(t, code)
		require.NoError(t, runtime.prepareProtectedWorkspaceVolume(t.Context(), machine, *machine.WorkspaceVolumeName, image))
		code, after := run(t, machine, isolatedGitCommand(manifest, "manifest", "/volume/workspace"))
		require.Zero(t, code)
		require.Equal(t, before, after, "migration must preserve bytes, inodes, symlinks, metadata, HEAD and dirty index")
		require.NoError(t, runtime.prepareProtectedWorkspaceVolume(t.Context(), machine, *machine.WorkspaceVolumeName, image), "ready layout verification is idempotent")
		// Real service subpaths deny UID1000 repository/root writes while its
		// workspace remains writable. The helper uses the inverse write policy.
		created, err := runtime.client.ContainerCreate(t.Context(), client.ContainerCreateOptions{
			Config:     &container.Config{Image: image, User: "1000:1000", Entrypoint: []string{"/bin/sh", "-c", "exec sleep 120"}, Labels: machineLabels(machine)},
			HostConfig: runtime.secureHostConfig(machine, "ide", "none", *machine.WorkspaceVolumeName),
		})
		require.NoError(t, err)
		_, err = runtime.client.ContainerStart(t.Context(), created.ID, client.ContainerStartOptions{})
		require.NoError(t, err)
		_, err = runtime.execOutputAs(t.Context(), created.ID, []string{"/bin/sh", "-c", "set -eu; echo writable > /workspace/ide-proof; if echo forbidden > /repositories/ide-proof; then exit 91; fi; test ! -e /workspace/.ku-layout-*/ready"}, "1000:1000")
		require.NoError(t, err)
		require.NoError(t, runtime.prepareProtectedWorkspaceVolume(t.Context(), machine, *machine.WorkspaceVolumeName, image), "running modern IDE permits read-only verification")
		t.Log("145 MiB incompressible history, dirty index/worktree, colliding names, hardlink/symlink and metadata preserved; real IDE mount policy passed")
		_, err = runtime.client.ContainerRemove(t.Context(), created.ID, client.ContainerRemoveOptions{Force: true})
		require.NoError(t, err)
		code, _ = run(t, machine, isolatedGitCommand("set -eu; printf protected > /volume/repositories/authority-proof; chown 1000:1000 /volume/repositories/authority-proof", "protected-proof"))
		require.Zero(t, code)
		record, err := runtime.client.ContainerInspect(t.Context(), workspaceLayoutName(machine), client.ContainerInspectOptions{})
		require.NoError(t, err)
		_, err = runtime.client.ContainerRemove(t.Context(), record.Container.ID, client.ContainerRemoveOptions{})
		require.NoError(t, err)
		err = runtime.prepareProtectedWorkspaceVolume(t.Context(), machine, *machine.WorkspaceVolumeName, image)
		require.ErrorContains(t, err, "exited 77", "lost Docker authority must refuse a second migration")
		code, unchanged := run(t, machine, isolatedGitCommand(manifest, "manifest", "/volume/workspace"))
		require.Zero(t, code)
		require.Equal(t, before, unchanged)
		code, _ = run(t, machine, isolatedGitCommand("set -eu; test \"$(cat /volume/repositories/authority-proof)\" = protected; test ! -e /volume/workspace/repositories/authority-proof; test ! -e /volume/workspace/workspace/tasks", "authority-preserved"))
		require.Zero(t, code)
		t.Log("lost authority record refused without rewrapping workspace or exposing retained repositories")
	})
	for _, mode := range []string{"workspace-file", "repositories-file", "collect-collision"} {
		t.Run("reject-"+mode, func(t *testing.T) {
			machine := newVolume(t)
			key := uuid.NewString()
			if mode == "collect-collision" {
				code, _ := run(t, machine, isolatedGitCommand(`set -eu
state="/volume/.ku-layout-$1"
mkdir -m 0700 "$state" "$state/contents"
printf 'collecting\n' > "$state/phase"
printf original > /volume/keep
printf previously-collected > "$state/contents/keep"
`, "seed-collision", key))
				require.Zero(t, code)
				code, _ = run(t, machine, isolatedGitCommand(workspaceLayoutScript, "refuse-collision", "/volume", key, "migrate"))
				require.NotZero(t, code)
				code, _ = run(t, machine, isolatedGitCommand(`set -eu
test "$(cat /volume/keep)" = original
test "$(cat "/volume/.ku-layout-$1/contents/keep")" = previously-collected
`, "collision-preserved", key))
				require.Zero(t, code, "a collection collision must never overwrite either copy")
				return
			}
			code, _ := run(t, machine, isolatedGitCommand(workspaceLayoutScript, "prepare-empty", "/volume", key, "migrate"))
			require.Zero(t, code)
			path := strings.TrimSuffix(mode, "-file")
			code, _ = run(t, machine, isolatedGitCommand(`set -eu
rmdir "/volume/$1"
printf 'user-file\n' > "/volume/$1"
chown 1000:1000 "/volume/$1"
chmod "$2" "/volume/$1"
`, "invalid-subpath", path, "700"))
			require.Zero(t, code)
			code, _ = run(t, machine, isolatedGitCommand(workspaceLayoutScript, "refuse-file", "/volume", key, "verify"))
			require.NotZero(t, code, "a ready marker must never make a regular file a valid mount subdirectory")
			code, _ = run(t, machine, isolatedGitCommand(`set -eu; test "$(cat "/volume/$1")" = user-file`, "file-preserved", path))
			require.Zero(t, code)
		})
	}
	for _, point := range []string{"before-first-phase", "after-one-rename", "after-collected", "after-workspace-install", "after-installed", "before-ready-rename"} {
		t.Run("resume-"+point, func(t *testing.T) {
			machine := newVolume(t)
			key := uuid.NewString()
			code, _ := run(t, machine, isolatedGitCommand("set -eu; printf keep > /volume/.first; printf collision > /volume/workspace; ln -s /etc/passwd /volume/repositories; chown -R 1000:1000 /volume", "seed"))
			require.Zero(t, code)
			needle := map[string]string{
				"before-first-phase":      "  write_phase collecting",
				"after-one-rename":        "    mv -- \"$source\" \"$state/contents/\"",
				"after-collected":         "  write_phase collected",
				"after-workspace-install": "    mv -- \"$state/contents\" \"$root/workspace\"",
				"after-installed":         "  write_phase installed",
				"before-ready-rename":     "mv -f -- \"$state/ready.new\" \"$state/ready\"",
			}[point]
			replacement := needle + "\nexit 85"
			if point == "before-first-phase" || point == "before-ready-rename" {
				replacement = "exit 85\n" + needle
			}
			require.Equal(t, 1, strings.Count(workspaceLayoutScript, needle))
			interrupted := strings.Replace(workspaceLayoutScript, needle, replacement, 1)
			code, _ = run(t, machine, isolatedGitCommand(interrupted, "interrupt", "/volume", key, "migrate"))
			require.EqualValues(t, 85, code)
			code, _ = run(t, machine, isolatedGitCommand(workspaceLayoutScript, "resume", "/volume", key, "migrate"))
			require.Zero(t, code)
			code, _ = run(t, machine, isolatedGitCommand(`set -eu; test "$(cat /volume/workspace/.first)" = keep; test "$(cat /volume/workspace/workspace)" = collision; test "$(readlink /volume/workspace/repositories)" = /etc/passwd; test "$(stat -c %u:%a /volume/repositories)" = 1000:700`, "preserved"))
			require.Zero(t, code)
		})
	}
}
