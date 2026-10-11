package machine

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

func TestRepositoryHelperActualDocker(t *testing.T) {
	image := os.Getenv("KU_GIT_HELPER_IMAGE")
	if image == "" {
		t.Skip("set KU_GIT_HELPER_IMAGE to the freshly built shipped collector image")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
	defer cancel()
	runtime, err := NewDockerRuntime(DockerConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = runtime.client.Close() })
	machine := fixtureHelperMachine()
	machine.RoutingKey = "helper-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	machine.WorkspaceVolumeName = new("ku-workspace-" + machine.RoutingKey)
	machine.DockerNetworkName = new("ku-machine-" + machine.RoutingKey)
	services := []domain.DevMachineService{{ServiceType: "collector", ImageRef: image}}
	inspection, err := runtime.client.ImageInspect(ctx, image)
	require.NoError(t, err)
	t.Logf("immutable helper image: %s", inspection.ID)
	serverImage := os.Getenv("KU_GIT_SERVER_FIXTURE_IMAGE")
	require.NotEmpty(t, serverImage, "a separate disposable server fixture image with git-daemon is required; the shipped helper needs only Git client tools")
	serverInspection, err := runtime.client.ImageInspect(ctx, serverImage)
	require.NoError(t, err)
	t.Logf("disposable Git server fixture image: %s", serverInspection.ID)
	created, err := runtime.ensureVolume(ctx, *machine.WorkspaceVolumeName, machineLabels(machine), 1)
	require.NoError(t, err)
	require.True(t, created)
	t.Cleanup(func() {
		require.NoError(t, runtime.removeManagedVolume(context.Background(), *machine.WorkspaceVolumeName, machine.ID))
		require.NoError(t, runtime.removeWorkspaceLayoutRecord(context.Background(), machine, *machine.WorkspaceVolumeName))
	})
	require.NoError(t, runtime.prepareProtectedWorkspaceVolume(ctx, machine, *machine.WorkspaceVolumeName, inspection.ID))
	created, err = runtime.ensureNetwork(ctx, *machine.DockerNetworkName, true, machineLabels(machine))
	require.NoError(t, err)
	require.True(t, created)
	t.Cleanup(func() {
		require.NoError(t, runtime.removeManagedNetwork(context.Background(), *machine.DockerNetworkName, machine.ID))
	})
	initContainer, err := runtime.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{Image: inspection.ID, User: "0:0", Entrypoint: []string{"/bin/sh", "-c"},
			Cmd: []string{"set -eu; mkdir -p /volume/workspace/tasks; chown 1000:1000 /volume/workspace/tasks; exec sleep 420"}, Labels: machineLabels(machine)},
		HostConfig: &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, CapAdd: []string{"CHOWN", "DAC_OVERRIDE"},
			Mounts:    []mount.Mount{{Type: mount.TypeVolume, Source: *machine.WorkspaceVolumeName, Target: "/volume", VolumeOptions: &mount.VolumeOptions{NoCopy: true}}},
			Resources: container.Resources{Memory: 64 * 1024 * 1024, MemorySwap: 64 * 1024 * 1024, NanoCPUs: 100_000_000, PidsLimit: new(int64(16))}},
	})
	require.NoError(t, err)
	initRemoved := false
	t.Cleanup(func() {
		if !initRemoved {
			_, _ = runtime.client.ContainerRemove(context.Background(), initContainer.ID, client.ContainerRemoveOptions{Force: true})
		}
	})
	_, err = runtime.client.ContainerStart(ctx, initContainer.ID, client.ContainerStartOptions{})
	require.NoError(t, err)
	_, err = runtime.execOutputAs(ctx, initContainer.ID, []string{"/bin/sh", "-c", "while [ ! -d /volume/workspace/tasks ]; do sleep 0.1; done"}, "0:0")
	require.NoError(t, err)
	f := newProtectedRepositoryFixture(t)
	// Import only a disposable source checkout. Keep its Git objects standalone;
	// the authenticated helper's protected store is fetched independently below.
	f.git(t, f.workspace, "repack", "-a", "-d")
	require.NoError(t, os.Remove(filepath.Join(f.workspace, ".git", "objects", "info", "alternates")))
	seedArchive := helperWorkspaceArchive(t, f.workspace)
	_, err = runtime.client.CopyToContainer(ctx, initContainer.ID, client.CopyToContainerOptions{DestinationPath: "/volume/workspace/tasks", Content: bytes.NewReader(seedArchive), CopyUIDGID: true})
	require.NoError(t, err)
	certificate, key := helperFixtureCertificate(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "helper-key.pem"), key, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "helper-cert.pem"), certificate, 0o600))
	testExecutable, err := os.Executable()
	require.NoError(t, err)
	gitServer, err := runtime.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{Image: serverInspection.ID, User: "1000:1000", WorkingDir: "/fixture", Labels: machineLabels(machine),
			Entrypoint: []string{"/fixture-test"}, Cmd: []string{"-test.run", "TestRepositoryHelperFixtureServer", "-test.timeout", "300s"},
			Env: []string{"KU_GIT_FIXTURE_SERVER=1", "PATH=/usr/bin:/bin"}},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(*machine.DockerNetworkName), ReadonlyRootfs: true,
			CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges=true"},
			Mounts:    []mount.Mount{{Type: mount.TypeBind, Source: f.root, Target: "/fixture"}, {Type: mount.TypeBind, Source: testExecutable, Target: "/fixture-test", ReadOnly: true}},
			Resources: container.Resources{Memory: 128 * 1024 * 1024, MemorySwap: 128 * 1024 * 1024, NanoCPUs: 100_000_000, PidsLimit: new(int64(32))}},
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{*machine.DockerNetworkName: {Aliases: []string{"git-fixture"}}}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, removeErr := runtime.client.ContainerRemove(context.Background(), gitServer.ID, client.ContainerRemoveOptions{Force: true})
		require.NoError(t, removeErr)
	})
	_, err = runtime.client.ContainerStart(ctx, gitServer.ID, client.ContainerStartOptions{})
	require.NoError(t, err)
	caArchive := new(bytes.Buffer)
	archive := tar.NewWriter(caArchive)
	require.NoError(t, archive.WriteHeader(&tar.Header{Name: "fixture-ca.pem", Mode: 0o600, Size: int64(len(certificate)), Uid: 1000, Gid: 1000}))
	_, err = archive.Write(certificate)
	require.NoError(t, err)
	require.NoError(t, archive.Close())
	_, err = runtime.client.CopyToContainer(ctx, initContainer.ID, client.CopyToContainerOptions{DestinationPath: "/volume/repositories", Content: bytes.NewReader(caArchive.Bytes()), CopyUIDGID: true})
	require.NoError(t, err)
	// Docker's copyUIDGID applies the destination container's root identity.
	// Restore the disposable fixture's intended UID before removing the init
	// writer, so both checkout and public CA are readable to the helper.
	_, err = runtime.execOutputAs(ctx, initContainer.ID, []string{"chown", "-R", "1000:1000", "/volume/workspace", "/volume/repositories"}, "0:0")
	require.NoError(t, err)
	_, err = runtime.client.ContainerRemove(ctx, initContainer.ID, client.ContainerRemoveOptions{Force: true})
	require.NoError(t, err)
	initRemoved = true
	run := func(ctx context.Context, script string, credential string, args ...string) (string, error) {
		command := isolatedGitCommand(script, "actual-helper", args...)
		// Public fixture CA only; production keeps the shipped system trust store.
		command = append(command[:9:9], append([]string{"GIT_SSL_CAINFO=/repositories/fixture-ca.pem"}, command[9:]...)...)
		return runtime.runRepositoryHelper(ctx, machine, services, command, credential, repositoryHelperInvocation{})
	}
	output, err := run(ctx, `set -eu
git --version
flock --version
setsid --version
printf 'memory='; cat /sys/fs/cgroup/memory.max
printf 'pids='; cat /sys/fs/cgroup/pids.max
printf 'cpu='; cat /sys/fs/cgroup/cpu.max
test "$(awk '/CapEff:/ {print $2}' /proc/self/status)" = 0000000000000000
if touch /workspace/cannot-write; then echo "unexpected successful forbidden operation" >&2; exit 1; fi
touch /repositories/helper-write
test -f /repositories/helper-write`, "")
	require.NoError(t, err, output)
	require.Contains(t, output, "memory=536870912")
	require.Contains(t, output, "pids=64")
	require.Contains(t, output, "cpu=40000 100000")
	t.Logf("actual shipped tools and cgroup limits: %s", strings.TrimSpace(output))
	host := "git-fixture:8443"
	repositoryURL := "https://" + host + "/selected.git"
	const repository = "/repositories/selected.git"
	const workspace = "/workspace/tasks/task"
	output, err = run(ctx, prepareProtectedRepositoryScript, protectedFixtureToken, repository, repositoryURL, "main", f.branch, host, "selected", "", f.run)
	require.NoError(t, err, output)
	t.Run("credential-free-checkout-role", func(t *testing.T) {
		stageKey, err := runtime.checkoutStageKey(ctx, machine, uuid.MustParse(f.run))
		require.NoError(t, err)
		command := isolatedGitCommand(installProtectedCheckoutScript, "actual-checkout", "/workspace", "/workspace/repository", repository, repositoryURL, f.branch, f.run, stageKey)
		output, err := runtime.runCheckoutHelper(ctx, machine, services, command)
		require.NoError(t, err, output)
		output, err = runtime.runCheckoutHelper(ctx, machine, services, isolatedGitCommand(`set -eu
test -z "${GITHUB_TOKEN:-}"
test "$(wc -l < /proc/net/route)" -eq 1
test ! -e /sys/class/net/eth0
if touch /repositories/checkout-cannot-write; then echo "unexpected successful forbidden operation" >&2; exit 1; fi
if touch /repositories/selected.git/checkout-cannot-write; then echo "unexpected successful forbidden operation" >&2; exit 1; fi
test "$(cat /workspace/repository/source.txt)" = base
test "$(cat /workspace/repository/.git/objects/info/alternates)" = /repositories/selected.git/objects
test -z "$(ls -A /workspace/repository/.git/objects/pack)"
printf 'later checkout edit\n' > /workspace/repository/source.txt
sha256sum /workspace/repository/.git/index`, "checkout-policy"))
		require.NoError(t, err, output)
		indexHash := output
		output, err = runtime.runCheckoutHelper(ctx, machine, services, command)
		require.NoError(t, err, output)
		output, err = runtime.runCheckoutHelper(ctx, machine, services, isolatedGitCommand(`set -eu
test "$(cat /workspace/repository/source.txt)" = 'later checkout edit'
test ! -e /workspace/repository/.git.ku-linked-backup
if grep -q 'GITHUB_TOKEN\|helper-disposable-secret' /workspace/repository/.git/config; then echo "unexpected successful forbidden operation" >&2; exit 1; fi
sha256sum /workspace/repository/.git/index`, "checkout-preservation"))
		require.NoError(t, err, output)
		require.Equal(t, indexHash, output, "reinstall must preserve exact index bytes and later worktree edits")
	})
	t.Run("linked-checkout-hard-kill-and-later-work", func(t *testing.T) {
		checkoutID := uuid.New()
		runKey := checkoutID.String()
		path := "/workspace/tasks/legacy-" + runKey
		stageKey, err := runtime.checkoutStageKey(ctx, machine, checkoutID)
		require.NoError(t, err)
		output, err := run(ctx, prepareProtectedRepositoryScript, protectedFixtureToken, repository, repositoryURL, "main", f.branch, host, "selected", "", runKey)
		require.NoError(t, err, output)
		output, err = runtime.runCheckoutHelper(ctx, machine, services, isolatedGitCommand(`set -eu
cache="/workspace/repos/legacy-$1.git"
path=$2
mkdir -p /workspace/repos
git -c protocol.file.allow=always clone --quiet --bare --local --shared --template= -- /workspace/tasks/task "$cache"
git --git-dir="$cache" worktree add --quiet "$path" "$3"
printf 'legacy local history\n' > "$path/committed.txt"
git -C "$path" add committed.txt
git -C "$path" -c user.name=fixture -c user.email=fixture@localhost commit --quiet -m 'legacy local commit'
printf 'legacy staged content\n' > "$path/source.txt"
name='staged
name.txt'
printf 'staged unusual filename\n' > "$path/$name"
ln -s untracked/target "$path/staged-link"
git -C "$path" add source.txt "$name" staged-link
printf 'legacy unstaged content\n' > "$path/source.txt"
`, "seed-linked-checkout", runKey, path, f.branch))
		require.NoError(t, err, output)
		needle := `    [ ! -e "$stage/git" ]`
		require.Equal(t, 1, strings.Count(installProtectedCheckoutScript, needle))
		crashed := strings.Replace(installProtectedCheckoutScript, needle, needle+"\nkill -KILL $$", 1)
		// PID 1 ignores namespace-internal signals without a handler. Run the
		// installer as a child so this fault really kills it before EXIT cleanup.
		killDriver := `set -eu
script=$1
shift
/bin/sh -c "$script" ku-interrupted-installer "$@" &
installer_pid=$!
wait "$installer_pid"`
		output, err = runtime.runCheckoutHelper(ctx, machine, services, isolatedGitCommand(killDriver, "interrupt-linked-checkout", crashed, "/workspace", path, repository, repositoryURL, f.branch, runKey, stageKey))
		require.Error(t, err, output)
		require.ErrorContains(t, err, "exited 137", "only reaching the intended SIGKILL qualifies crash recovery")
		output, err = runtime.runCheckoutHelper(ctx, machine, services, isolatedGitCommand(`set -eu
path=$1
test -d "/workspace/.ku-checkout-$2"
test -f "$path/.git.ku-linked-backup"
test "$(git -C "$path" show :source.txt)" = 'legacy staged content'
test "$(git -C "$path" show ':staged
name.txt')" = 'staged unusual filename'
test "$(git -C "$path" show :staged-link)" = untracked/target
git -C "$path" -c user.name=fixture -c user.email=fixture@localhost commit --quiet -m 'later commit after installer kill'
printf 'new staged after kill\n' > "$path/source.txt"
git -C "$path" add source.txt
printf 'even later worktree edit\n' > "$path/source.txt"
git -C "$path" rev-parse HEAD
sha256sum "$path/.git/index" | awk '{print $1}'
`, "later-linked-work", path, stageKey))
		require.NoError(t, err, output)
		laterMetadata := output
		command := isolatedGitCommand(installProtectedCheckoutScript, "recover-linked-checkout", "/workspace", path, repository, repositoryURL, f.branch, runKey, stageKey)
		output, err = runtime.runCheckoutHelper(ctx, machine, services, command)
		require.NoError(t, err, output)
		output, err = runtime.runCheckoutHelper(ctx, machine, services, isolatedGitCommand(`set -eu
path=$1
test ! -e "/workspace/.ku-checkout-$2"
test ! -e "$path/.git.ku-linked-backup"
test ! -e "/workspace/repos/legacy-$3.git/worktrees/${path##*/}/index.lock"
test "$(git -C "$path" show :source.txt)" = 'new staged after kill'
test "$(cat "$path/source.txt")" = 'even later worktree edit'
test "$(cat "$path/committed.txt")" = 'legacy local history'
git -C "$path" fsck --connectivity-only --no-dangling > /dev/null
git -C "$path" rev-parse HEAD
sha256sum "$path/.git/index" | awk '{print $1}'
`, "linked-recovery-preserved", path, stageKey, runKey))
		require.NoError(t, err, output)
		require.Equal(t, laterMetadata, output, "recovery must retain newer HEAD and exact index bytes")
	})
	// An IDE-equivalent mount can modify the checkout and read objects but cannot
	// write the protected store, even when a custom environment is being used.
	ideOptions, err := repositoryHelperOptions(machine, inspection.ID, uuid.NewString(), []string{"/bin/sh", "-c", `set -eu
printf 'verified\n' > /workspace/tasks/task/source.txt
if touch /repositories/cannot-write; then echo "unexpected successful forbidden operation" >&2; exit 1; fi
if touch /repositories/selected.git/cannot-write; then echo "unexpected successful forbidden operation" >&2; exit 1; fi
exec sleep 420`})
	require.NoError(t, err)
	ideOptions.Config.Labels["com.ku.service"] = "ide"
	ideOptions.Config.WorkingDir = workspace
	ideOptions.HostConfig.Mounts[0].ReadOnly = false
	ideOptions.HostConfig.Mounts[1].ReadOnly = true
	ide, err := runtime.client.ContainerCreate(ctx, ideOptions)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, removeErr := runtime.client.ContainerRemove(context.Background(), ide.ID, client.ContainerRemoveOptions{Force: true})
		require.NoError(t, removeErr)
	})
	_, err = runtime.client.ContainerStart(ctx, ide.ID, client.ContainerStartOptions{})
	require.NoError(t, err)
	_, err = runtime.execOutput(ctx, ide.ID, []string{"/bin/sh", "-c", "while [ \"$(cat source.txt)\" != verified ]; do sleep 0.1; done"})
	require.NoError(t, err)
	output, err = run(ctx, retainRepositoryCandidateScript, "", workspace, repository, f.run)
	require.NoError(t, err, output)
	candidate, err := parseRepositoryCandidate(output)
	require.NoError(t, err)
	require.Equal(t, []string{"source.txt"}, candidate.ChangedFiles)
	_, err = runtime.execOutput(ctx, ide.ID, []string{"/bin/sh", "-c", "printf 'later edit\n' > source.txt"})
	require.NoError(t, err)
	t.Run("private-retained-candidate-verification", func(t *testing.T) {
		checkout := &domain.DevMachineCheckout{ID: uuid.New(), RepositoryURL: "https://git.example.test/owner/selected", RepositoryFullName: "owner/selected", BaseBranch: "main", WorkingBranch: f.branch, WorkspacePath: workspace}
		privateRepository, _, err := protectedCheckoutRepository(checkout)
		require.NoError(t, err)
		// The HTTPS fixture uses a single-segment server path. Copy its small,
		// standalone protected store to the URL-keyed production mount path.
		// This is fixture setup only, not the production preparation algorithm.
		output, err := run(ctx, `set -eu; cp -a -- "$1" "$2"`, "", repository, privateRepository)
		require.NoError(t, err, output)
		replayed, err := runtime.RetainAgentCandidate(ctx, machine, services, checkout, uuid.MustParse(f.run))
		require.NoError(t, err)
		require.Equal(t, candidate, replayed, "the Runtime API must replay the retained candidate despite a later live edit")
		verificationServices := append(slices.Clone(services), domain.DevMachineService{ServiceType: "ide", ImageRef: serverImage})
		testCredentialFreeAgentBootstrapsActualDocker(t, ctx, runtime, machine, checkout, serverImage)
		for _, mode := range []string{"unchanged", "tracked-mutation", "attribute-masking", "nonzero"} {
			argv := []string{"/bin/sh", "-c", `set -eu
test "$(cat source.txt)" = verified
test -z "${GITHUB_TOKEN:-}"
test -z "${GITHUB_TOKEN:-}"
test -z "${ANTHROPIC_API_KEY:-}"
test -z "${BASH_ENV:-}"
test "$(ku-fixture-tool)" = fixture-tool-ok
test "$(stat -f -c %T /tmp)" = xfs
if ln "$2/HEAD" /tmp/protected-hardlink; then echo "unexpected successful forbidden operation" >&2; exit 1; fi
dd if=/dev/zero of=/tmp/compiler-cache bs=1M count=2 status=none
test ! -e /workspace/repository
test ! -e /workspace/tasks/legacy
if touch /repositories/cannot-write-verification; then echo "unexpected successful forbidden operation" >&2; exit 1; fi
if touch /repositories/selected.git/cannot-write-verification; then echo "unexpected successful forbidden operation" >&2; exit 1; fi
test -x "$(git --exec-path)/git-daemon"
test "$(git rev-parse HEAD)" = "$1"
printf 'test output\n' > generated.txt
rm -rf .git
mkdir .git
printf '[core]\n fsmonitor = touch /tmp/untrusted-hook\n' > .git/config
printf 'candidate-tests-ok\n'
printf 'candidate-test-diagnostic\n' >&2
`, "ku-test", candidate.Commit, privateRepository}
			if mode == "tracked-mutation" {
				argv[2] += "printf 'mutated by test\\n' > source.txt\n"
			} else if mode == "attribute-masking" {
				argv[2] += `printf 'source.txt working-tree-encoding=UTF-16LE\n' > .gitattributes
printf 'v\000e\000r\000i\000f\000i\000e\000d\000\n\000' > source.txt
`
			} else if mode == "nonzero" {
				argv[2] += "exit 23\n"
			}
			execution, err := runtime.RunCandidateCommand(ctx, machine, verificationServices, checkout, uuid.MustParse(f.run), candidate, argv)
			if mode == "tracked-mutation" || mode == "attribute-masking" {
				require.Error(t, err)
				require.Contains(t, err.Error(), "tests changed tracked candidate content")
			} else {
				require.NoError(t, err)
			}
			require.NotNil(t, execution, "completed test output should survive an attestation refusal")
			require.Contains(t, execution.Stdout, "candidate-tests-ok")
			require.Contains(t, execution.Stderr, "candidate-test-diagnostic")
			if mode == "nonzero" {
				require.Equal(t, 23, execution.ExitCode)
			} else {
				require.Zero(t, execution.ExitCode)
			}
			output, err := run(ctx, `set -eu; test ! -e "$1/ku-runs/$2/verification"; test ! -e "$1/ku-runs/$2/verification-scratch"; test ! -e "$1/ku-runs/$2/verification-stage"`, "", privateRepository, f.run)
			require.NoError(t, err, output)
			t.Logf("runtime candidate mode=%s exit=%d; output retained and private directories removed", mode, execution.ExitCode)
		}

		for _, mode := range []string{"created", "exited", "running"} {
			t.Run("restart-verification-"+mode, func(t *testing.T) {
				runID := uuid.MustParse(f.run)
				output, err := run(ctx, ensureCandidateVerificationDirectoryScript, "", privateRepository, f.run, candidate.Commit)
				require.NoError(t, err, output)
				oldOptions, err := candidateVerificationOptions(machine, serverInspection.ID, uuid.NewString(), checkout, runID, candidate, []string{"/bin/sh", "-c", "sleep 60"})
				require.NoError(t, err)
				oldOptions.Config.Labels[repositoryReservationVersionLabel] = "1"
				oldOptions.Config.Labels[repositoryReservationDeadlineLabel] = strconv.FormatInt(time.Now().Add(-time.Minute).UnixNano(), 10)
				if mode == "exited" {
					oldOptions.Config.Entrypoint = []string{"true"}
				}
				abandoned, err := runtime.client.ContainerCreate(ctx, oldOptions)
				require.NoError(t, err)
				defer func() {
					_, removeErr := runtime.client.ContainerRemove(context.Background(), abandoned.ID, client.ContainerRemoveOptions{Force: true})
					require.True(t, removeErr == nil || errdefs.IsNotFound(removeErr))
				}()
				oldInspection, err := runtime.client.ContainerInspect(ctx, abandoned.ID, client.ContainerInspectOptions{})
				require.NoError(t, err)
				oldOwner, err := verificationReservationOwner(oldInspection.Container, oldOptions.Config.Labels["com.ku.repository-operation"])
				require.NoError(t, err)
				output, err = run(ctx, claimCandidateVerificationScript, "", privateRepository, f.run, candidate.Commit, oldOwner)
				require.NoError(t, err, output)
				if mode != "created" {
					_, err = runtime.client.ContainerStart(ctx, abandoned.ID, client.ContainerStartOptions{})
					require.NoError(t, err)
					require.Eventually(t, func() bool {
						inspected, err := runtime.client.ContainerInspect(ctx, abandoned.ID, client.ContainerInspectOptions{})
						if err != nil || inspected.Container.State == nil {
							return false
						}
						if mode == "running" {
							return inspected.Container.State.Running
						}
						return inspected.Container.State.Status == container.StateExited
					}, 10*time.Second, 50*time.Millisecond)
				}
				// A new Runtime has no process-local invocation claims. Recovery
				// must therefore use persisted ownership and terminal state.
				restarted, err := NewDockerRuntime(DockerConfig{})
				require.NoError(t, err)
				defer restarted.client.Close()
				execution, err := restarted.RunCandidateCommand(ctx, machine, verificationServices, checkout, runID, candidate, []string{"/bin/sh", "-c", "set -eu; test \"$(cat source.txt)\" = verified; printf restart-verification-ok"})
				if mode == "running" {
					require.Error(t, err)
					require.Nil(t, execution)
					current, err := runtime.client.ContainerInspect(ctx, abandoned.ID, client.ContainerInspectOptions{})
					require.NoError(t, err)
					require.True(t, current.Container.State.Running, "restart must preserve the live writer even when its recorded deadline expired")
					return
				}
				require.NoError(t, err)
				require.Equal(t, "restart-verification-ok", execution.Stdout)
				_, err = runtime.client.ContainerInspect(ctx, abandoned.ID, client.ContainerInspectOptions{})
				require.True(t, errdefs.IsNotFound(err), "reclaim must remove the old immutable ID")
				for _, script := range []string{claimCandidateVerificationScript, prepareCandidateVerificationScript, attestCandidateVerificationScript, cleanupCandidateVerificationScript} {
					output, err := run(ctx, script, "", privateRepository, f.run, candidate.Commit, oldOwner, checkout.WorkingBranch)
					require.Error(t, err, "old helper must remain fenced after new cleanup: %s", output)
				}
				output, err = run(ctx, `set -eu; test ! -e "$1/ku-runs/$2/verification"; test ! -e "$1/ku-runs/$2/verification-scratch"`, "", privateRepository, f.run)
				require.NoError(t, err, output)
				t.Logf("fresh runtime recovered expired %s verification and fenced the abandoned owner", mode)
			})
		}
		for _, mode := range []string{"created", "exited"} {
			t.Run("restart-source-sync-"+mode, func(t *testing.T) {
				options, err := repositoryHelperOptions(machine, inspection.ID, uuid.NewString(), []string{"true"})
				require.NoError(t, err)
				options.Name = "ku-" + machine.RoutingKey + "-source-sync-" + f.run
				options.Config.Labels["com.ku.source-sync-run"] = f.run
				options.Config.Labels[repositoryReservationVersionLabel] = "1"
				options.Config.Labels[repositoryReservationDeadlineLabel] = strconv.FormatInt(time.Now().Add(-time.Minute).UnixNano(), 10)
				options.HostConfig.Mounts[0].ReadOnly = false
				options.HostConfig.Mounts = append(options.HostConfig.Mounts, mount.Mount{Type: mount.TypeVolume, Source: *machine.WorkspaceVolumeName, Target: "/volume", VolumeOptions: &mount.VolumeOptions{NoCopy: true}})
				options.HostConfig.NetworkMode = "none"
				options.NetworkingConfig = nil
				abandoned, err := runtime.client.ContainerCreate(ctx, options)
				require.NoError(t, err)
				defer func() {
					_, removeErr := runtime.client.ContainerRemove(context.Background(), abandoned.ID, client.ContainerRemoveOptions{Force: true})
					require.True(t, removeErr == nil || errdefs.IsNotFound(removeErr))
				}()
				if mode == "exited" {
					_, err = runtime.client.ContainerStart(ctx, abandoned.ID, client.ContainerStartOptions{})
					require.NoError(t, err)
					require.Eventually(t, func() bool {
						inspected, err := runtime.client.ContainerInspect(ctx, abandoned.ID, client.ContainerInspectOptions{})
						return err == nil && inspected.Container.State != nil && inspected.Container.State.Status == container.StateExited
					}, 10*time.Second, 50*time.Millisecond)
				}
				restarted, err := NewDockerRuntime(DockerConfig{})
				require.NoError(t, err)
				defer restarted.client.Close()
				output, err := restarted.runImmutableGitHelper(ctx, machine, services, isolatedGitCommand(`set -eu; test -f "$1/source.txt"; printf restart-source-sync-ok`, "restart-source-sync", checkout.WorkspacePath), "", repositoryHelperInvocation{RunID: uuid.MustParse(f.run), Checkout: true, SourceSync: true})
				require.NoError(t, err)
				require.Equal(t, "restart-source-sync-ok", output)
				_, err = runtime.client.ContainerInspect(ctx, abandoned.ID, client.ContainerInspectOptions{})
				require.True(t, errdefs.IsNotFound(err))
				t.Logf("fresh runtime recovered expired %s source-sync helper", mode)
			})
		}
		for _, mode := range []string{"concurrent", "cancel"} {
			func() {
				heldCtx, cancel := context.WithTimeout(ctx, time.Minute)
				defer cancel()
				type outcome struct {
					execution *CommandExecution
					err       error
				}
				results := make(chan outcome, 1)
				joined := false
				defer func() {
					cancel()
					if !joined {
						select {
						case <-results:
						case <-time.After(30 * time.Second):
							t.Error("held candidate did not terminate")
						}
					}
				}()
				go func() {
					execution, err := runtime.RunCandidateCommand(heldCtx, machine, verificationServices, checkout, uuid.MustParse(f.run), candidate, []string{"/bin/sh", "-c", `set -eu
test "$(cat source.txt)" = verified
if [ "$1" = cancel ]; then
  (printf 'child started\n' > /tmp/child-started; sleep 45; printf escaped > /tmp/child-escaped) &
  printf '%s\n' "$!" > /tmp/child-pid
  while [ ! -f /tmp/child-started ]; do sleep 0.1; done
fi
printf 'started\n' > /tmp/started
while [ ! -f /tmp/release ]; do sleep 0.1; done
printf 'held-candidate-ok\n'
`, "ku-held-test", mode})
					results <- outcome{execution, err}
				}()
				name := "ku-" + machine.RoutingKey + "-verification-" + f.run
				marker := privateRepository + "/ku-runs/" + f.run + "/verification-scratch/started"
				require.Eventually(t, func() bool {
					_, err := runtime.execOutput(ctx, ide.ID, []string{"test", "-f", marker})
					return err == nil
				}, 30*time.Second, 100*time.Millisecond)
				if mode == "concurrent" {
					second, err := runtime.RunCandidateCommand(ctx, machine, verificationServices, checkout, uuid.MustParse(f.run), candidate, []string{"false"})
					require.Error(t, err)
					require.Nil(t, second)
					inspection, err := runtime.client.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
					require.NoError(t, err)
					require.True(t, inspection.Container.State.Running, "the conflicting invocation must not remove the first reservation")
					_, err = runtime.execOutput(ctx, name, []string{"touch", "/tmp/release"})
					require.NoError(t, err)
				} else {
					_, err := runtime.execOutput(ctx, name, []string{"/bin/sh", "-c", `kill -0 "$(cat /tmp/child-pid)"`})
					require.NoError(t, err, "a live child must exist before cancellation")
					cancel()
				}
				select {
				case result := <-results:
					joined = true
					if mode == "cancel" {
						require.ErrorIs(t, result.err, context.Canceled)
						require.Nil(t, result.execution)
					} else {
						require.NoError(t, result.err)
						require.Contains(t, result.execution.Stdout, "held-candidate-ok")
					}
				case <-time.After(30 * time.Second):
					t.Fatal("held verification did not finish")
				}
				_, err := runtime.client.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
				require.True(t, errdefs.IsNotFound(err), "test container must be removed")
				output, err := run(ctx, `set -eu; test ! -e "$1/ku-runs/$2/verification"; test ! -e "$1/ku-runs/$2/verification-scratch"`, "", privateRepository, f.run)
				require.NoError(t, err, output)
				t.Logf("runtime candidate %s: reservation preserved, terminal container and private files absent", mode)
			}()
		}
		output, err = runtime.execOutput(ctx, ide.ID, []string{"cat", "source.txt"})
		require.NoError(t, err)
		require.Equal(t, "later edit\n", output)
		stageKey, err := runtime.checkoutStageKey(ctx, machine, uuid.MustParse(f.run))
		require.NoError(t, err)
		testAgentRunCancellationActualDocker(t, ctx, runtime, machine, services, verificationServices, checkout, candidate, privateRepository, inspection.ID, ide.ID)
		interrupted := strings.Replace(synchronizeRepositoryCandidateScript, `atomic_volume_move -f "$stage/new-index" "$git_dir/index"`, "kill -KILL $$", 1)
		// Namespace init ignores self-SIGKILL. Run the transaction in a child
		// and propagate its actual wait status through the helper's PID 1.
		killDriver := `set -eu
program=$1
shift
/bin/sh -c "$program" ku-source-sync-child "$@" &
child=$!
wait "$child"
`
		beforeSync, err := runtime.execOutput(ctx, ide.ID, []string{"/bin/sh", "-c", `set -eu; cat .git/HEAD; git rev-parse HEAD; sha256sum .git/index; find .git -name '*.lock'`})
		require.NoError(t, err)
		t.Logf("source synchronization before kill: candidate=%s source=%s index=%s dirty=%t; actual=%s", candidate.Commit, candidate.SourceCommit, candidate.SourceIndexHash, candidate.Dirty, beforeSync)
		killOutput, err := runtime.runImmutableGitHelper(ctx, machine, services, isolatedGitCommand(killDriver, "interrupt-source-sync",
			interrupted, workspace, privateRepository, f.run, stageKey, candidate.Branch, candidate.Commit, candidate.SourceCommit, candidate.SourceIndexHash), "", repositoryHelperInvocation{RunID: uuid.MustParse(f.run), Checkout: true, SourceSync: true})
		require.Error(t, err, "the source synchronization helper must actually die between its branch and index renames; returned output=%q", killOutput)
		head, err := runtime.execOutput(ctx, ide.ID, []string{"git", "rev-parse", "HEAD"})
		require.NoError(t, err)
		require.Equal(t, candidate.Commit, strings.TrimSpace(head))
		indexHash, err := runtime.execOutput(ctx, ide.ID, []string{"sha256sum", ".git/index"})
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(indexHash, candidate.SourceIndexHash+" "), "the original index remains until recovery")
		synchronized, err := runtime.SynchronizeAgentCandidate(ctx, machine, services, checkout, uuid.MustParse(f.run), candidate)
		require.NoError(t, err)
		require.Equal(t, "updated", synchronized.Status)
		synchronized, err = runtime.SynchronizeAgentCandidate(ctx, machine, services, checkout, uuid.MustParse(f.run), candidate)
		require.NoError(t, err)
		require.Equal(t, "unchanged", synchronized.Status)
		staged, err := runtime.execOutput(ctx, ide.ID, []string{"git", "diff", "--cached", "--name-only"})
		require.NoError(t, err)
		require.Empty(t, staged)
		output, err = runtime.execOutput(ctx, ide.ID, []string{"cat", "source.txt"})
		require.NoError(t, err)
		require.Equal(t, "later edit\n", output)
		testTerminalRunCleanupActualDocker(t, ctx, runtime, machine, services, checkout, candidate, privateRepository, ide.ID)
		t.Log("source sync recovered an actual Docker helper SIGKILL between branch/index renames, preserved the later file edit, and replayed without changes")
		t.Log("private worktree tests executed the retained SHA, had read-only objects, and preserved the live edit; trusted attestation rejected tracked mutation despite replaced .git")
	})
	output, err = run(ctx, publishRetainedRepositoryCandidateScript, protectedFixtureToken, repository, repositoryURL, f.branch, host, "selected", "", f.run, candidate.Commit)
	require.NoError(t, err, output)
	require.Equal(t, candidate.Commit, f.git(t, f.remote, "rev-parse", "refs/heads/"+f.branch))
	require.Equal(t, "verified", f.git(t, f.remote, "show", candidate.Commit+":source.txt"))
	output, err = runtime.execOutput(ctx, ide.ID, []string{"cat", "source.txt"})
	require.NoError(t, err)
	require.Equal(t, "later edit\n", output)
	output, err = run(ctx, `set -eu
backing_before=$(df -Pk / | awk 'NR==2 {print $4}')
[ "$backing_before" -gt 3145728 ]
set +e
dd if=/dev/zero of=/repositories/quota-probe bs=1M count=1100 status=none 2>/tmp/quota-error
status=$?
written=$(stat -c %s /repositories/quota-probe)
backing_after=$(df -Pk / | awk 'NR==2 {print $4}')
cat /tmp/quota-error
[ "$status" -ne 0 ] && [ "$written" -gt 943718400 ] && [ "$written" -le 1073741824 ] && [ "$backing_after" -gt 2097152 ] && grep -E 'Disk quota exceeded|No space left on device' /tmp/quota-error
accepted=$?
if [ "$accepted" -ne 0 ]; then printf 'quota probe rejected: status=%s bytes=%s\n' "$status" "$written" >&2; cat /tmp/quota-error >&2; fi
rm -f /repositories/quota-probe /tmp/quota-error
[ "$accepted" -eq 0 ] || exit 1
printf 'hard-quota-enforced bytes=%s backing-kib-before=%s backing-kib-after=%s\n' "$written" "$backing_before" "$backing_after"`, "")
	require.NoError(t, err, output)
	require.Contains(t, output, "hard-quota-enforced")
	t.Logf("bounded quota rejection with independent backing filesystem headroom: %s", strings.TrimSpace(output))
	cancelCtx, cancelHelper := context.WithCancel(ctx)
	defer cancelHelper()
	helperFinished := make(chan error, 1)
	go func() {
		_, helperErr := run(cancelCtx, "trap '' TERM; sleep 120 & child=$!; kill -0 \"$child\"; touch /repositories/cancel-started; wait \"$child\"", "")
		helperFinished <- helperErr
	}()
	_, err = runtime.execOutput(ctx, ide.ID, []string{"/bin/sh", "-c", "while [ ! -f /repositories/cancel-started ]; do sleep 0.1; done"})
	require.NoError(t, err, "the helper and child must start before cancellation")
	cancelHelper()
	err = <-helperFinished
	require.True(t, errors.Is(err, context.Canceled), err)
	listed, err := runtime.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	require.NoError(t, err)
	for _, item := range listed.Items {
		if ownedByMachine(item.Labels, machine.ID) {
			require.NotEqual(t, "git-helper", item.Labels["com.ku.service"], "one-shot helper and all descendants must be removed")
		}
	}
	ideInspection, err := runtime.client.ContainerInspect(ctx, ide.ID, client.ContainerInspectOptions{})
	require.NoError(t, err)
	require.True(t, ideInspection.Container.State.Running, "helper cancellation must preserve the IDE")
	t.Log("authenticated retained-SHA publication, later edit preservation, read-only isolation, real XFS hard quota, cancellation and owned cleanup passed")
}

func testCredentialFreeAgentBootstrapsActualDocker(t *testing.T, ctx context.Context, runtime *DockerRuntime, machine *domain.DevMachine, checkout *domain.DevMachineCheckout, image string) {
	t.Helper()
	for _, custom := range []bool{false, true} {
		t.Run(fmt.Sprintf("credential-free-agent-custom-%t", custom), func(t *testing.T) {
			argv, err := json.Marshal([]string{"/bin/sh", "-c", `set -eu
test -z "${GITHUB_TOKEN:-}"
test -z "${GITHUB_TOKEN:-}"
test ! -e /run/ku-secrets/GITHUB_TOKEN
test ! -e /run/ku-secrets/GITHUB_TOKEN
test ! -e "$HOME/.ku-git-credential"
if git config --global --get credential.helper; then exit 1; fi
test "$ANTHROPIC_API_KEY" = scoped-provider-fixture
printf credential-free-provider-ok
`})
			require.NoError(t, err)
			run := &domain.DevMachineAgentRun{ID: uuid.New(), MachineID: machine.ID, WorkspaceID: machine.WorkspaceID, Mode: "task", CommandArgv: argv}
			provider := &domain.DevMachineAgentProvider{ImageRef: image, IsCustom: custom}
			name := "ku-" + machine.RoutingKey + "-agent-" + run.ID.String()
			t.Cleanup(func() {
				inspection, err := runtime.client.ContainerInspect(context.Background(), name, client.ContainerInspectOptions{})
				if errdefs.IsNotFound(err) {
					return
				}
				require.NoError(t, err)
				require.Equal(t, run.ID.String(), inspection.Container.Config.Labels["com.ku.agent-run-id"])
				_, err = runtime.client.ContainerRemove(context.Background(), inspection.Container.ID, client.ContainerRemoveOptions{Force: true})
				require.NoError(t, err)
			})
			execution, err := runtime.RunAgent(ctx, machine, run, provider, checkout, map[string]string{"GITHUB_TOKEN": "blocked-github-fixture", "ANTHROPIC_API_KEY": "scoped-provider-fixture"})
			require.NoError(t, err)
			require.Zero(t, execution.ExitCode, execution.Stderr)
			require.Contains(t, execution.Stdout, "credential-free-provider-ok")
			replayed, err := runtime.RunAgent(ctx, machine, run, provider, checkout, nil)
			require.NoError(t, err)
			require.Equal(t, execution.ContainerID, replayed.ContainerID)
			require.Equal(t, execution.Stdout, replayed.Stdout)
		})
	}
}

func helperWorkspaceArchive(t *testing.T, dir string) []byte {
	t.Helper()
	buffer := new(bytes.Buffer)
	archive := tar.NewWriter(buffer)
	require.NoError(t, filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name, header.Uid, header.Gid = filepath.Join("task", relative), 1000, 1000
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(archive, file)
			return errors.Join(copyErr, file.Close())
		}
		return nil
	}))
	require.NoError(t, archive.Close())
	return buffer.Bytes()
}

func helperFixtureCertificate(t *testing.T) ([]byte, []byte) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "helper fixture"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"git-fixture"}}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, public, private)
	require.NoError(t, err)
	key, err := x509.MarshalPKCS8PrivateKey(private)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})
}

// This subprocess fixture serves only its disposable bare repository over the
// isolated test network. It is never invoked by a production helper.
func TestRepositoryHelperFixtureServer(t *testing.T) {
	if os.Getenv("KU_GIT_FIXTURE_SERVER") != "1" {
		return
	}
	output, err := exec.CommandContext(t.Context(), "/usr/bin/git", "--exec-path").Output()
	require.NoError(t, err)
	backend := &cgi.Handler{Path: filepath.Join(strings.TrimSpace(string(output)), "git-http-backend"), Dir: "/fixture/remote",
		Env: []string{"GIT_PROJECT_ROOT=/fixture/remote", "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=fixture"}}
	server := &http.Server{Addr: ":8443", ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "oauth2" || password != protectedFixtureToken {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/selected.git/") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		backend.ServeHTTP(w, r)
	})}
	require.NoError(t, server.ListenAndServeTLS("/fixture/helper-cert.pem", "/fixture/helper-key.pem"))
}

// Cancellation is called through a fresh Runtime while the original callers
// and their process trees are still live. This proves daemon/persistent state,
// rather than relying on one backend's in-memory cancellation functions.
func testAgentRunCancellationActualDocker(t *testing.T, ctx context.Context, runtime *DockerRuntime, machine *domain.DevMachine, services, verificationServices []domain.DevMachineService, checkout *domain.DevMachineCheckout, candidate RepositoryCandidate, repository, imageID, ideID string) {
	t.Helper()
	t.Run("fresh-runtime-run-cancellation", func(t *testing.T) {
		runID := uuid.New()
		setup := isolatedGitCommand(`set -eu; git --git-dir="$1" update-ref "refs/ku/runs/$2/candidate" "$3"; mkdir -m 0700 "$1/ku-runs/$2"`, "cancel-run-setup", repository, runID.String(), candidate.Commit)
		output, err := runtime.runRepositoryHelper(ctx, machine, services, setup, "", repositoryHelperInvocation{})
		require.NoError(t, err, output)
		heldCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		type outcome struct {
			execution *CommandExecution
			err       error
		}
		results := make(chan outcome, 1)
		helperResults := make(chan error, 2)
		// On any failed assertion, cancel and join launched callers before the
		// outer fixture removes its machine volume/network.
		verificationJoined, helpersJoined := false, 0
		defer func() {
			cancel()
			if !verificationJoined {
				select {
				case <-results:
				case <-time.After(20 * time.Second):
					t.Error("verification caller did not terminate during fixture cleanup")
				}
			}
			for helpersJoined < 2 {
				select {
				case <-helperResults:
					helpersJoined++
				case <-time.After(20 * time.Second):
					t.Error("helper caller did not terminate during fixture cleanup")
					return
				}
			}
		}()
		go func() {
			execution, err := runtime.RunCandidateCommand(heldCtx, machine, verificationServices, checkout, runID, candidate, []string{"/bin/sh", "-c", `set -eu
(sleep 45; printf escaped > /tmp/child-escaped) &
printf '%s\n' "$!" > /tmp/child-pid
touch /tmp/cancellation-verification-started
wait`})
			results <- outcome{execution, err}
		}()
		// Launch both helpers before any assertion that can terminate this subtest.
		for _, sourceSync := range []bool{false, true} {
			go func() {
				marker := "/repositories/cancel-helper-" + runID.String()
				if sourceSync {
					marker += "-source"
				}
				_, err := runtime.runImmutableGitHelper(heldCtx, machine, services, isolatedGitCommand(`set -eu; touch "$1"; sleep 45 & wait`, "held-cancel-helper", marker), "", repositoryHelperInvocation{RunID: runID, Checkout: sourceSync, SourceSync: sourceSync})
				helperResults <- err
			}()
		}
		verificationName := "ku-" + machine.RoutingKey + "-verification-" + runID.String()
		require.Eventually(t, func() bool {
			_, err := runtime.execOutput(heldCtx, verificationName, []string{"test", "-f", "/tmp/cancellation-verification-started"})
			return err == nil
		}, 15*time.Second, 100*time.Millisecond)
		_, err = runtime.execOutput(heldCtx, verificationName, []string{"/bin/sh", "-c", `kill -0 "$(cat /tmp/child-pid)"`})
		require.NoError(t, err, "verification child must be alive before cancellation")
		require.Eventually(t, func() bool {
			_, err := runtime.execOutput(ctx, ideID, []string{"/bin/sh", "-c", `test -f "$1" && test -f "$1-source"`, "wait-helper-markers", "/repositories/cancel-helper-" + runID.String()})
			return err == nil
		}, 15*time.Second, 100*time.Millisecond)
		createAgent := func(agentRun uuid.UUID) string {
			options, err := repositoryHelperOptions(machine, imageID, uuid.NewString(), []string{"/bin/sh", "-c", "sleep 45"})
			require.NoError(t, err)
			options.Name = "ku-" + machine.RoutingKey + "-agent-" + agentRun.String()
			options.Config.Labels["com.ku.service"], options.Config.Labels["com.ku.agent-run-id"] = "agent", agentRun.String()
			options.HostConfig.Mounts[0].ReadOnly = false
			options.HostConfig.Mounts[1].ReadOnly = true
			created, err := runtime.client.ContainerCreate(ctx, options)
			require.NoError(t, err)
			t.Cleanup(func() {
				_, err := runtime.client.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
				require.True(t, err == nil || errdefs.IsNotFound(err))
			})
			_, err = runtime.client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{})
			require.NoError(t, err)
			return created.ID
		}
		ownedAgent := createAgent(runID)
		otherAgent := createAgent(uuid.New())
		restarted, err := NewDockerRuntime(DockerConfig{})
		require.NoError(t, err)
		defer restarted.client.Close()
		// Register marker cleanup before exercising cancellation, including a lost
		// response or a failed assertion. Removal always revalidates its exact ID.
		t.Cleanup(func() {
			marker, err := runtime.client.ContainerInspect(context.Background(), agentCancellationName(machine, runID), client.ContainerInspectOptions{})
			if errdefs.IsNotFound(err) {
				return
			}
			require.NoError(t, err)
			require.NoError(t, validateAgentCancellation(marker.Container, machine, runID))
			_, err = runtime.client.ContainerRemove(context.Background(), marker.Container.ID, client.ContainerRemoveOptions{})
			require.NoError(t, err)
		})
		runRecord := &domain.DevMachineAgentRun{ID: runID, MachineID: machine.ID, WorkspaceID: machine.WorkspaceID, Status: domain.DevMachineAgentRunStatusTimeout}
		require.NoError(t, restarted.CancelAgent(ctx, machine, runRecord))
		select {
		case result := <-results:
			verificationJoined = true
			t.Logf("cancelled verification returned: %v", result.err)
			require.Error(t, result.err)
			require.Nil(t, result.execution)
		case <-time.After(15 * time.Second):
			t.Fatal("cancelled verification caller did not terminate")
		}
		for helpersJoined < 2 {
			select {
			case err := <-helperResults:
				helpersJoined++
				require.Error(t, err)
			case <-time.After(15 * time.Second):
				t.Fatal("cancelled Git helper caller did not terminate")
			}
		}
		for _, id := range []string{ownedAgent, verificationName, "ku-" + machine.RoutingKey + "-source-sync-" + runID.String()} {
			_, err := runtime.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
			require.True(t, errdefs.IsNotFound(err), "cancelled container and descendants must be gone: %s", id)
		}
		other, err := runtime.client.ContainerInspect(ctx, otherAgent, client.ContainerInspectOptions{})
		require.NoError(t, err)
		require.True(t, other.Container.State.Running, "other run must remain alive")
		require.NoError(t, restarted.CancelAgent(ctx, machine, runRecord), "repeated cancellation must preserve its durable record")
		late, err := restarted.RunCandidateCommand(ctx, machine, verificationServices, checkout, runID, candidate, []string{"true"})
		require.ErrorIs(t, err, context.Canceled)
		require.Nil(t, late)
		output, err = runtime.runRepositoryHelper(ctx, machine, services, isolatedGitCommand(`set -eu; find "$1/ku-runs/$2" -maxdepth 2 -type d; test ! -e "$1/ku-runs/$2/verification"; test ! -e "$1/ku-runs/$2/verification-scratch"`, "check-cancel-cleanup", repository, runID.String()), "", repositoryHelperInvocation{})
		require.NoError(t, err, output)
		remaining, err := runtime.client.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", "com.ku.agent-run-id="+runID.String())})
		require.NoError(t, err)
		require.Len(t, remaining.Items, 1)
		require.Equal(t, "agent-cancellation", remaining.Items[0].Labels["com.ku.service"])
		t.Log("fresh runtime cancelled agent, verification child, repository helper and source-sync helper; another run survived, private test directories were cleaned, repeated/late calls stayed fenced")
	})
}

func testTerminalRunCleanupActualDocker(t *testing.T, ctx context.Context, runtime *DockerRuntime, machine *domain.DevMachine, services []domain.DevMachineService, original *domain.DevMachineCheckout, previous RepositoryCandidate, repository, ideID string) {
	t.Helper()
	for _, defaultCheckout := range []bool{false, true} {
		t.Run(fmt.Sprintf("abandoned-terminal-run-recovery-default-%t", defaultCheckout), func(t *testing.T) {
			machineCopy := *machine
			machine = &machineCopy
			runID := uuid.New()
			checkout := *original
			checkout.MachineID, checkout.WorkspaceID = machine.ID, machine.WorkspaceID
			if defaultCheckout {
				owner, name, valid := splitRepositoryFullName(checkout.RepositoryFullName)
				require.True(t, valid)
				machine.RepoOwner, machine.RepoName = owner, name
				machine.BaseBranch, machine.WorkingBranch = checkout.BaseBranch, checkout.WorkingBranch
				checkout.ID = machine.ID
				checkout.WorkspacePath = "/workspace/repository"
				// The earlier shipped installer fixture already provisioned this
				// default worktree. Exercise it directly, retaining its history.
				_, err := runtime.execOutput(ctx, ideID, []string{"/bin/sh", "-c", `set -eu; test -d /workspace/repository/.git; test "$(git -C /workspace/repository symbolic-ref --short HEAD)" = "$1"`, "default-checkout-fixture", checkout.WorkingBranch})
				require.NoError(t, err)
			}
			setup := isolatedGitCommand(`set -eu
git --git-dir="$1" update-ref "refs/ku/runs/$2/base" "$3"
git --git-dir="$1" update-ref "refs/ku/runs/$2/parent" "$4"
`, "terminal-run-setup", repository, runID.String(), previous.BaseCommit, previous.Commit)
			output, err := runtime.runRepositoryHelper(ctx, machine, services, setup, "", repositoryHelperInvocation{})
			require.NoError(t, err, output)
			verifiedContents := "terminal verified"
			if defaultCheckout {
				verifiedContents = "terminal verified default"
			}
			_, err = runtime.execOutput(ctx, ideID, []string{"/bin/sh", "-c", `printf '%s\n' "$2" > "$1/source.txt"`, "terminal-fixture", checkout.WorkspacePath, verifiedContents})
			require.NoError(t, err)
			candidate, err := runtime.RetainAgentCandidate(ctx, machine, services, &checkout, runID)
			require.NoError(t, err)
			_, err = runtime.execOutput(ctx, ideID, []string{"/bin/sh", "-c", `printf 'later edit\n' > "$1/source.txt"`, "terminal-fixture", checkout.WorkspacePath})
			require.NoError(t, err)
			stageKey, err := runtime.checkoutStageKey(ctx, machine, runID)
			require.NoError(t, err)
			interrupted := strings.Replace(synchronizeRepositoryCandidateScript, `atomic_volume_move -f "$stage/new-index" "$git_dir/index"`, "kill -KILL $$", 1)
			killDriver := `set -eu; program=$1; shift; /bin/sh -c "$program" terminal-sync-child "$@" & child=$!; wait "$child"`
			_, err = runtime.runImmutableGitHelper(ctx, machine, services, isolatedGitCommand(killDriver, "terminal-sync-kill", interrupted, checkout.WorkspacePath, repository, runID.String(), stageKey, candidate.Branch, candidate.Commit, candidate.SourceCommit, candidate.SourceIndexHash), "", repositoryHelperInvocation{RunID: runID, Checkout: true, SourceSync: true})
			require.Error(t, err, "a real SIGKILL must interrupt branch/index installation")
			// Private artifacts have no live caller or test container to reclaim them.
			output, err = runtime.runRepositoryHelper(ctx, machine, services, isolatedGitCommand(`set -eu
for name in verification verification-scratch verification-stage snapshot.abandoned; do
  mkdir -m 0700 "$1/ku-runs/$2/$name"
  printf abandoned > "$1/ku-runs/$2/$name/private-file"
done
`, "abandoned-run-artifacts", repository, runID.String()), "", repositoryHelperInvocation{})
			require.NoError(t, err, output)
			run := &domain.DevMachineAgentRun{ID: runID, MachineID: machine.ID, WorkspaceID: machine.WorkspaceID, CheckoutID: &checkout.ID, Status: domain.DevMachineAgentRunStatusTimeout, RepositorySelection: &checkout}
			if defaultCheckout {
				run.CheckoutID = nil
				machine.RepoOwner, machine.RepoName = "changed", "repository"
				machine.BaseBranch, machine.WorkingBranch = "new-base", "new-issue-branch"
			}
			restarted := &DockerRuntime{client: runtime.client, config: runtime.config}
			result, err := restarted.CleanupAgentRun(ctx, machine, services, &checkout, run)
			require.NoError(t, err)
			require.Equal(t, "updated", result.Status)
			result, err = restarted.CleanupAgentRun(ctx, machine, services, &checkout, run)
			require.NoError(t, err)
			require.Equal(t, "unchanged", result.Status)
			_, err = restarted.SynchronizeAgentCandidate(ctx, machine, services, &checkout, runID, candidate)
			require.ErrorIs(t, err, context.Canceled, "ordinary synchronization cannot resurrect a terminal run")
			output, err = runtime.runRepositoryHelper(ctx, machine, services, isolatedGitCommand(`set -eu
for name in verification verification-scratch verification-stage snapshot.abandoned; do test ! -e "$1/ku-runs/$2/$name"; done
test ! -e "$1/ku-runs/$2/source-sync-$3"
test -f "$1/ku-runs/$2/candidate.info"
test -f "$1/ku-runs/$2/terminal-cleanup"
test "$(git --git-dir="$1" rev-parse "refs/ku/runs/$2/candidate")" = "$4"
`, "terminal-run-artifacts-reclaimed", repository, runID.String(), stageKey, candidate.Commit), "", repositoryHelperInvocation{})
			require.NoError(t, err, output)
			head, err := runtime.execOutput(ctx, ideID, []string{"git", "-C", checkout.WorkspacePath, "rev-parse", "HEAD"})
			require.NoError(t, err)
			require.Equal(t, candidate.Commit, strings.TrimSpace(head))
			staged, err := runtime.execOutput(ctx, ideID, []string{"git", "-C", checkout.WorkspacePath, "diff", "--cached", "--name-only"})
			require.NoError(t, err)
			require.Empty(t, staged)
			contents, err := runtime.execOutput(ctx, ideID, []string{"cat", checkout.WorkspacePath + "/source.txt"})
			require.NoError(t, err)
			require.Equal(t, "later edit\n", contents)
			marker, err := runtime.client.ContainerInspect(ctx, agentCancellationName(machine, runID), client.ContainerInspectOptions{})
			require.NoError(t, err)
			require.NoError(t, validateAgentCancellation(marker.Container, machine, runID))
			_, err = runtime.client.ContainerRemove(ctx, marker.Container.ID, client.ContainerRemoveOptions{})
			require.NoError(t, err, "only this disposable test fixture tears down its persistent marker")
			t.Log("fresh runtime recovered a genuinely killed terminal source transaction, reclaimed abandoned private files, retained objects/fences and preserved the later owner edit")
		})
	}
}
