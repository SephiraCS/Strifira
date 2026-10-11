package machine

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestDefaultRepositoryProvisionActualDocker(t *testing.T) {
	image := os.Getenv("KU_GIT_HELPER_IMAGE")
	if image == "" {
		t.Skip("set KU_GIT_HELPER_IMAGE to the freshly built shipped collector image")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()
	runtime, err := NewDockerRuntime(DockerConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = runtime.client.Close() })
	inspection, err := runtime.client.ImageInspect(ctx, image)
	require.NoError(t, err)
	services := []domain.DevMachineService{{ServiceType: "collector", ImageRef: image}}
	t.Logf("immutable default provisioning helper: %s", inspection.ID)
	for _, linked := range []bool{false, true} {
		for _, boundary := range []string{"none", "before-stage-move", "during-entry-moves", "before-publish", "after-publish", "before-pointer-rename", "after-pointer-rename", "late-before-stage", "late-after-publish", "collision-after-publish"} {
			t.Run(fmt.Sprintf("linked-%t-%s", linked, boundary), func(t *testing.T) {
				machine := fixtureHelperMachine()
				machine.ID = uuid.New()
				machine.RoutingKey = "helper-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
				machine.WorkspaceVolumeName = new("ku-workspace-" + machine.RoutingKey)
				machine.DockerNetworkName = new("ku-machine-" + machine.RoutingKey)
				created, err := runtime.ensureVolume(ctx, *machine.WorkspaceVolumeName, machineLabels(machine), 1)
				require.NoError(t, err)
				require.True(t, created)
				t.Cleanup(func() {
					require.NoError(t, runtime.removeManagedVolume(context.Background(), *machine.WorkspaceVolumeName, machine.ID))
					require.NoError(t, runtime.removeWorkspaceLayoutRecord(context.Background(), machine, *machine.WorkspaceVolumeName))
				})
				require.NoError(t, runtime.prepareProtectedWorkspaceVolume(ctx, machine, *machine.WorkspaceVolumeName, inspection.ID))
				stageKey, err := runtime.checkoutStageKey(ctx, machine, machine.ID)
				require.NoError(t, err)
				run := func(script string, args ...string) (string, error) {
					return runtime.runImmutableGitHelper(ctx, machine, services, isolatedGitCommand(script, "actual-default-provision", args...), "", repositoryHelperInvocation{Checkout: true, ProvisionID: machine.ID})
				}
				output, err := run(atomicVolumeRenameFunctions+`set -eu
test -z "${GITHUB_TOKEN:-}"
test ! -e /sys/class/net/eth0
test "$(stat -c %u:%a /volume)" = 0:755
if touch /volume/cannot-write; then exit 1; fi
for record in /volume/.ku-layout-*; do
  if cat "$record/ready"; then exit 1; fi
done
# Keeping the source inode open rules out copy/delete plus inode reuse. Both
# aliases must reach the exact same live inode through the common mount.
printf 'atomic payload\n' > /repositories/atomic-source
exec 7< /repositories/atomic-source
expected=$(stat -c '%d:%i' /repositories/atomic-source)
atomic_volume_move -f /repositories/atomic-source /workspace/atomic-target
test ! -e /repositories/atomic-source
test "$(stat -c '%d:%i' /workspace/atomic-target)" = "$expected"
test "$(stat -L -c '%d:%i' /proc/self/fd/7)" = "$expected"
printf 'same open inode\n' >> /workspace/atomic-target
test "$(cat <&7)" = 'atomic payload
same open inode'
exec 7<&-
rm /workspace/atomic-target
git -c init.defaultBranch=owner/root init --quiet --template= /tmp/root-seed
printf 'base\n' > /tmp/root-seed/source.txt
git -C /tmp/root-seed add source.txt
git -C /tmp/root-seed -c user.name=fixture -c user.email=fixture@localhost commit --quiet -m base
git clone --quiet --bare --template= /tmp/root-seed /repositories/default.git
mkdir /workspace/tasks /workspace/repos /workspace/.cache
printf 'infrastructure\n' > /workspace/.cache/keep
if [ "$1" = true ]; then
  git clone --quiet --bare --template= /tmp/root-seed /workspace/repos/legacy.git
  git --git-dir=/workspace/repos/legacy.git worktree add --quiet /workspace/staging owner/root
  mv /workspace/staging/.git /workspace/.git
  mv /workspace/staging/source.txt /workspace/source.txt
  rmdir /workspace/staging
  git --git-dir=/workspace/repos/legacy.git worktree repair /workspace
else
  git clone --quiet --template= /tmp/root-seed /workspace/staging
  mv /workspace/staging/.git /workspace/.git
  mv /workspace/staging/source.txt /workspace/source.txt
  rmdir /workspace/staging
fi
git -C /workspace worktree add --quiet -b ku/issue /workspace/tasks/legacy
printf 'staged\n' > /workspace/source.txt
git -C /workspace add source.txt
printf 'later worktree edit\n' > /workspace/source.txt
name='untracked
last
'
printf 'untracked data\n' > "/workspace/$name"
ln -s source.txt /workspace/source-link
git -C /workspace rev-parse HEAD
git -C /workspace symbolic-ref --short HEAD
index=$(git -C /workspace rev-parse --path-format=absolute --git-path index)
sha256sum "$index" | awk '{print $1}'
stat -c '%d:%i' /workspace/source.txt
stat -c '%d:%i' /workspace/source-link
`, fmt.Sprint(linked))
				require.NoError(t, err, output)
				before := output
				args := []string{"/workspace", "/workspace/repository", "/repositories/default.git", machine.ID.String(), stageKey}
				if boundary != "none" {
					needle := map[string]string{
						"before-stage-move":       `atomic_volume_move -n "$journal/worktree" "$stage"`,
						"during-entry-moves":      `mv -T -n -- "$root/$name" "$target/$name"`,
						"before-publish":          `atomic_volume_move -n "$stage" "$destination"`,
						"after-publish":           " sync -f \"$root\"\nfi\nfor record in \"$journal/pointers\"/*; do",
						"before-pointer-rename":   `atomic_volume_move -f "$record/new-pointer" "$pointer"`,
						"after-pointer-rename":    ` sync -f "$pointer"`,
						"late-before-stage":       `atomic_volume_move -n "$journal/worktree" "$stage"`,
						"late-after-publish":      `atomic_volume_move -f "$record/new-pointer" "$pointer"`,
						"collision-after-publish": `atomic_volume_move -f "$record/new-pointer" "$pointer"`,
					}[boundary]
					require.Equal(t, 1, strings.Count(migrateLegacyRootCheckoutScript, needle))
					interrupted := strings.Replace(migrateLegacyRootCheckoutScript, needle, "kill -KILL $$\n"+needle, 1)
					if boundary == "during-entry-moves" {
						interrupted = strings.Replace(migrateLegacyRootCheckoutScript, needle, needle+"\nkill -KILL $$", 1)
					}
					// Run below PID 1 so SIGKILL actually interrupts the transaction.
					driver := `set -eu; script=$1; shift; /bin/sh -c "$script" interrupted-root "$@" & child=$!; wait "$child"`
					output, err = run(driver, append([]string{interrupted}, args...)...)
					require.Error(t, err, output)
					require.ErrorContains(t, err, "exited 137")
				}
				lateName, lateInode := "", ""
				if strings.HasPrefix(boundary, "late-") || boundary == "collision-after-publish" {
					lateName = "late root\nlast\n"
					seedName := lateName
					if boundary == "collision-after-publish" {
						seedName = "source.txt"
					}
					output, err = run(`set -eu; printf 'new owner data\n' > "/workspace/$1"; stat -c '%d:%i' "/workspace/$1"`, seedName)
					require.NoError(t, err, output)
					lateInode = strings.TrimSpace(output)
					if boundary == "collision-after-publish" {
						output, err = run(migrateLegacyRootCheckoutScript, args...)
						require.Error(t, err, "collision must preserve both owner files")
						output, err = run(`set -eu
test "$(cat /workspace/source.txt)" = 'new owner data'
test "$(cat /workspace/repository/source.txt)" = 'later worktree edit'
test ! -e "/workspace/$1"
mv -T -n -- /workspace/source.txt "/workspace/$1"
`, lateName)
						require.NoError(t, err, output)
					}
				}
				for range 2 {
					output, err = run(migrateLegacyRootCheckoutScript, args...)
					require.NoError(t, err, output)
					output, err = run(`set -eu
test ! -e /workspace/.git
test ! -e "/workspace/.ku-root-$1"
test "$(cat /workspace/.cache/keep)" = infrastructure
test "$(cat /workspace/repository/source.txt)" = 'later worktree edit'
test "$(git -C /workspace/repository show :source.txt)" = staged
name='untracked
last
'
test "$(cat "/workspace/repository/$name")" = 'untracked data'
test "$(readlink /workspace/repository/source-link)" = source.txt
test "$(git -C /workspace/tasks/legacy symbolic-ref --short HEAD)" = ku/issue
git -C /workspace/tasks/legacy rev-parse --verify HEAD > /tmp/issue-head
git -C /workspace/repository rev-parse HEAD > /tmp/default-head
cmp /tmp/issue-head /tmp/default-head
git -C /workspace/repository rev-parse HEAD
git -C /workspace/repository symbolic-ref --short HEAD
index=$(git -C /workspace/repository rev-parse --path-format=absolute --git-path index)
sha256sum "$index" | awk '{print $1}'
stat -c '%d:%i' /workspace/repository/source.txt
stat -c '%d:%i' /workspace/repository/source-link
`, stageKey)
					require.NoError(t, err, output)
					require.Equal(t, before, output, "root migration must preserve HEAD, branch, exact index and file/symlink inodes")
					if lateName != "" {
						output, err = run(`set -eu
test ! -e "/workspace/$1"
test "$(cat "/workspace/repository/$1")" = 'new owner data'
test "$(stat -c '%d:%i' "/workspace/repository/$1")" = "$2"
`, lateName, lateInode)
						require.NoError(t, err, output)
					}
				}
			})
		}
	}
}
