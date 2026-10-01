package machine

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/require"
)

const fixtureVerificationOwner = "1000000000000000000:11111111-1111-1111-1111-111111111111"

func prepareVerificationFixture(t *testing.T, f *protectedRepositoryFixture, candidate RepositoryCandidate) string {
	t.Helper()
	output, err := f.script(t, claimCandidateVerificationScript, false, f.repository, f.run, candidate.Commit, fixtureVerificationOwner)
	require.NoError(t, err, output)
	output, err = f.script(t, prepareCandidateVerificationScript, false, f.repository, f.run, candidate.Commit, fixtureVerificationOwner, f.branch)
	require.NoError(t, err, output)
	return filepath.Join(f.repository, "ku-runs", f.run, "verification")
}

func TestCandidateVerificationMountPolicy(t *testing.T) {
	machine := fixtureHelperMachine()
	checkout := &domain.DevMachineCheckout{ID: uuid.New(), RepositoryURL: "https://git.example.test/owner/repo", RepositoryFullName: "owner/repo", BaseBranch: "main", WorkingBranch: "ku/task", WorkspacePath: "/workspace/repository"}
	candidate := RepositoryCandidate{GitState: GitState{Branch: checkout.WorkingBranch}, Commit: strings.Repeat("a", 40)}
	options, err := candidateVerificationOptions(machine, fixtureHelperImageID, uuid.NewString(), checkout, uuid.New(), candidate, []string{"true"})
	require.NoError(t, err)
	baseline := container.InspectResponse{Config: options.Config, HostConfig: options.HostConfig}
	require.True(t, protectedVerificationMounts(baseline, machine))
	encoded, err := json.Marshal(baseline)
	require.NoError(t, err)
	for name, mutate := range map[string]func(*container.InspectResponse){
		"foreign machine":     func(i *container.InspectResponse) { i.Config.Labels["com.ku.machine-id"] = uuid.NewString() },
		"foreign operation":   func(i *container.InspectResponse) { i.Config.Labels["com.ku.repository-operation"] = "unknown" },
		"foreign run":         func(i *container.InspectResponse) { i.Config.Labels["com.ku.agent-run-id"] = uuid.NewString() },
		"mutable image":       func(i *container.InspectResponse) { i.Config.Image = "latest" },
		"root user":           func(i *container.InspectResponse) { i.Config.User = "0:0" },
		"missing config":      func(i *container.InspectResponse) { i.Config = nil },
		"missing host config": func(i *container.InspectResponse) { i.HostConfig = nil },
		"writable objects":    func(i *container.InspectResponse) { i.HostConfig.Mounts[1].ReadOnly = false },
		"whole volume":        func(i *container.InspectResponse) { i.HostConfig.Mounts[0].VolumeOptions.Subpath = "" },
		"parent traversal":    func(i *container.InspectResponse) { i.HostConfig.Mounts[0].VolumeOptions.Subpath += "/../.." },
		"sibling run": func(i *container.InspectResponse) {
			i.HostConfig.Mounts[0].VolumeOptions.Subpath = strings.ReplaceAll(i.HostConfig.Mounts[0].VolumeOptions.Subpath, i.Config.Labels["com.ku.agent-run-id"], uuid.NewString())
		},
		"workspace alias": func(i *container.InspectResponse) { i.HostConfig.Mounts[0].Target = "/workspace" },
		"foreign volume":  func(i *container.InspectResponse) { i.HostConfig.Mounts[0].Source = "foreign" },
		"extra mount": func(i *container.InspectResponse) {
			i.HostConfig.Mounts = append(i.HostConfig.Mounts, i.HostConfig.Mounts[0])
		},
		"bind alias":              func(i *container.InspectResponse) { i.HostConfig.Binds = []string{"fixture-volume:/leak"} },
		"writable root":           func(i *container.InspectResponse) { i.HostConfig.ReadonlyRootfs = false },
		"added capabilities":      func(i *container.InspectResponse) { i.HostConfig.CapAdd = []string{"SYS_ADMIN"} },
		"privileged":              func(i *container.InspectResponse) { i.HostConfig.Privileged = true },
		"volumes from":            func(i *container.InspectResponse) { i.HostConfig.VolumesFrom = []string{"untrusted"} },
		"missing capability drop": func(i *container.InspectResponse) { i.HostConfig.CapDrop = nil },
		"privilege escalation":    func(i *container.InspectResponse) { i.HostConfig.SecurityOpt = nil },
		"traversal workdir":       func(i *container.InspectResponse) { i.Config.WorkingDir = "/workspace/tasks/../repositories" },
	} {
		t.Run(name, func(t *testing.T) {
			var inspection container.InspectResponse
			require.NoError(t, json.Unmarshal(encoded, &inspection))
			mutate(&inspection)
			require.False(t, protectedVerificationMounts(inspection, machine))
		})
	}
}

func TestCandidateVerificationToolingEnvironmentExcludesSecretsAndShellStartup(t *testing.T) {
	machine := fixtureHelperMachine()
	environment, err := candidateToolingEnvironment(machine, []string{"PATH=/custom/go/bin:/usr/bin:/bin", "GOROOT=/custom/go", "RUSTUP_HOME=/custom/rustup", "HOME=/live-ide", "FORGEJO_TOKEN=private-token", "ANTHROPIC_API_KEY=provider-token", "BASH_ENV=/live-ide/startup", "LD_PRELOAD=/live-ide/preload.so", "HTTP_PROXY=http://untrusted:3128"})
	require.NoError(t, err)
	require.Contains(t, environment, "PATH=/custom/go/bin:/usr/bin:/bin")
	require.Contains(t, environment, "GOROOT=/custom/go")
	require.Contains(t, environment, "RUSTUP_HOME=/custom/rustup")
	require.Contains(t, environment, "HOME=/tmp")
	require.Contains(t, environment, "FORGEJO_TOKEN=")
	require.Contains(t, environment, "ANTHROPIC_API_KEY=")
	require.Contains(t, environment, "BASH_ENV=")
	require.Contains(t, environment, "LD_PRELOAD=")
	require.Contains(t, environment, "HTTPS_PROXY=http://helper-fixture-egress:3128")
	for _, forbidden := range []string{"private-token", "provider-token", "live-ide", "untrusted"} {
		require.NotContains(t, strings.Join(environment, " "), forbidden)
	}
	_, err = candidateToolingEnvironment(machine, []string{"PATH=/usr/bin\nFORGEJO_TOKEN=private-token"})
	require.Error(t, err)
}

func TestCandidateVerificationUsesRetainedContentAndPreservesLaterSourceEdits(t *testing.T) {
	f := newProtectedRepositoryFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.workspace, "source.txt"), []byte("verified\n"), 0o600))
	candidate := f.snapshot(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.workspace, "source.txt"), []byte("later edit\n"), 0o600))
	indexBefore, err := os.ReadFile(filepath.Join(f.workspace, ".git", "index"))
	require.NoError(t, err)
	worktree := prepareVerificationFixture(t, f, candidate)
	require.Equal(t, candidate.Commit, f.git(t, worktree, "rev-parse", "HEAD"))
	require.Equal(t, f.branch, f.git(t, worktree, "branch", "--show-current"))
	command := exec.CommandContext(t.Context(), "/bin/sh", "-c", `set -eu; [ "$(cat source.txt)" = verified ]; [ -z "${FORGEJO_TOKEN-}" ]; printf 'build output\n' > generated.txt`)
	command.Dir = worktree
	command.Env = []string{"PATH=" + f.toolPath, "HOME=" + f.root}
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	attestation, err := f.script(t, attestCandidateVerificationScript, false, f.repository, f.run, candidate.Commit, fixtureVerificationOwner)
	require.NoError(t, err, attestation)
	require.Equal(t, candidate.Commit, strings.TrimSpace(attestation))
	source, err := os.ReadFile(filepath.Join(f.workspace, "source.txt"))
	require.NoError(t, err)
	require.Equal(t, "later edit\n", string(source))
	indexAfter, err := os.ReadFile(filepath.Join(f.workspace, ".git", "index"))
	require.NoError(t, err)
	require.Equal(t, indexBefore, indexAfter)
	outputText, err := f.script(t, cleanupCandidateVerificationScript, false, f.repository, f.run, candidate.Commit, fixtureVerificationOwner)
	require.NoError(t, err, outputText)
	require.NoDirExists(t, worktree)
	require.FileExists(t, filepath.Join(f.repository, "ku-runs", f.run, "candidate.info"))
	require.Equal(t, candidate.Commit, f.git(t, f.repository, "rev-parse", "refs/ku/runs/"+f.run+"/candidate"))
}

func TestCandidateVerificationRejectsTrackedChangesDespiteHostileGitMetadata(t *testing.T) {
	for _, mutation := range []string{"content", "deletion", "executable mode", "symlink", "newline filename", "attributes", "encoding attributes"} {
		t.Run(mutation, func(t *testing.T) {
			f := newProtectedRepositoryFixture(t)
			require.NoError(t, os.WriteFile(filepath.Join(f.workspace, "space\nand newline.txt"), []byte("original\n"), 0o600))
			candidate := f.snapshot(t)
			worktree := prepareVerificationFixture(t, f, candidate)
			file := filepath.Join(worktree, "source.txt")
			switch mutation {
			case "content":
				require.NoError(t, os.WriteFile(file, []byte("test mutation\n"), 0o600))
			case "deletion":
				require.NoError(t, os.Remove(file))
			case "executable mode":
				require.NoError(t, os.Chmod(file, 0o700))
			case "symlink":
				require.NoError(t, os.Remove(file))
				require.NoError(t, os.Symlink("untracked-target", file))
			case "newline filename":
				require.NoError(t, os.WriteFile(filepath.Join(worktree, "space\nand newline.txt"), []byte("changed\n"), 0o600))
			case "attributes":
				// An untracked attributes file can alter Git's view of existing
				// source. The fresh index must still catch content mutations.
				require.NoError(t, os.WriteFile(filepath.Join(worktree, ".gitattributes"), []byte("source.txt filter=hide\n"), 0o600))
				require.NoError(t, os.WriteFile(file, []byte("test mutation\n"), 0o600))
			case "encoding attributes":
				// A test-created attribute can otherwise normalize different
				// source bytes back into the original blob, hiding a mutation.
				require.NoError(t, os.WriteFile(filepath.Join(worktree, ".gitattributes"), []byte("source.txt working-tree-encoding=UTF-16LE\n"), 0o600))
				require.NoError(t, os.WriteFile(file, []byte{'b', 0, 'a', 0, 's', 0, 'e', 0, '\n', 0}, 0o600))
			}
			marker := filepath.Join(f.root, "verification-config-executed")
			// Replace all test-visible Git metadata. A trusted attestor must
			// neither run these commands nor use this bogus index/HEAD.
			require.NoError(t, os.RemoveAll(filepath.Join(worktree, ".git")))
			require.NoError(t, os.Mkdir(filepath.Join(worktree, ".git"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(worktree, ".git", "config"), []byte("[core]\n fsmonitor = touch "+marker+"\n[filter \"hide\"]\n clean = touch "+marker+"\n[credential]\n helper = !touch "+marker+"\n"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(worktree, ".git", "HEAD"), []byte(candidate.Commit+"\n"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(worktree, ".git", "index"), []byte("untrusted index"), 0o600))
			output, err := f.script(t, attestCandidateVerificationScript, false, f.repository, f.run, candidate.Commit, fixtureVerificationOwner)
			require.Error(t, err, output)
			require.Contains(t, output, "tests changed tracked candidate content")
			require.NoFileExists(t, marker)
			require.NoDirExists(t, filepath.Join(f.repository, "ku-runs", f.run, "verification-stage"))
		})
	}
}

func TestCandidateVerificationRetryRebuildsFromRetainedCommit(t *testing.T) {
	f := newProtectedRepositoryFixture(t)
	candidate := f.snapshot(t)
	worktree := prepareVerificationFixture(t, f, candidate)
	before, err := os.Stat(worktree)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(worktree, "source.txt"), []byte("mutation\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, "test-output"), []byte("old run\n"), 0o600))
	require.NoError(t, os.RemoveAll(filepath.Join(worktree, ".git")))
	stage := filepath.Join(f.repository, "ku-runs", f.run, "verification-stage")
	require.NoError(t, os.Mkdir(stage, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(stage, "interrupted-copy"), []byte("partial\n"), 0o600))
	require.Equal(t, worktree, prepareVerificationFixture(t, f, candidate))
	after, err := os.Stat(worktree)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after), "preparation must preserve the directory reserved by Docker")
	require.Equal(t, candidate.Commit, f.git(t, worktree, "rev-parse", "HEAD"))
	require.NoFileExists(t, filepath.Join(worktree, "test-output"))
	require.NoDirExists(t, stage)
	contents, err := os.ReadFile(filepath.Join(worktree, "source.txt"))
	require.NoError(t, err)
	require.Equal(t, "base\n", string(contents))
}

func TestCandidateVerificationUsesRetainedCheckoutAttributes(t *testing.T) {
	f := newProtectedRepositoryFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.workspace, ".gitattributes"), []byte("source.txt text eol=crlf\n"), 0o600))
	candidate := f.snapshot(t)
	worktree := prepareVerificationFixture(t, f, candidate)
	contents, err := os.ReadFile(filepath.Join(worktree, "source.txt"))
	require.NoError(t, err)
	require.Equal(t, "base\r\n", string(contents))
	output, err := f.script(t, attestCandidateVerificationScript, false, f.repository, f.run, candidate.Commit, fixtureVerificationOwner)
	require.NoError(t, err, output)
	require.Equal(t, candidate.Commit, strings.TrimSpace(output))
}

func TestCandidateVerificationRejectsForeignCommitAndUnsafeDirectories(t *testing.T) {
	for _, mode := range []string{"foreign commit", "verification symlink", "stage symlink", "scratch symlink", "verification file", "stage file", "scratch file"} {
		t.Run(mode, func(t *testing.T) {
			f := newProtectedRepositoryFixture(t)
			require.NoError(t, os.WriteFile(filepath.Join(f.workspace, "source.txt"), []byte("candidate\n"), 0o600))
			candidate := f.snapshot(t)
			commit := candidate.Commit
			prepareVerificationFixture(t, f, candidate)
			outside := filepath.Join(f.root, "user-notes")
			require.NoError(t, os.Mkdir(outside, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(outside, "keep"), []byte("preserve\n"), 0o600))
			name := "verification"
			if strings.HasPrefix(mode, "stage") {
				name = "verification-stage"
			} else if strings.HasPrefix(mode, "scratch") {
				name = "verification-scratch"
			}
			path := filepath.Join(f.repository, "ku-runs", f.run, name)
			require.NoError(t, os.RemoveAll(path))
			if mode == "foreign commit" {
				commit = f.base
			} else if strings.HasSuffix(mode, "symlink") {
				require.NoError(t, os.Symlink(outside, path))
			} else {
				require.NoError(t, os.WriteFile(path, []byte("user file\n"), 0o600))
			}
			for _, script := range []string{prepareCandidateVerificationScript, attestCandidateVerificationScript, cleanupCandidateVerificationScript} {
				output, err := f.script(t, script, false, f.repository, f.run, commit, fixtureVerificationOwner, f.branch)
				require.Error(t, err, output)
			}
			require.FileExists(t, filepath.Join(outside, "keep"))
			if mode != "foreign commit" {
				_, err := os.Lstat(path)
				require.NoError(t, err, "refusal must preserve the rejected entry")
			}
		})
	}
}

func TestCandidateVerificationMountsOnlyPrivateWorktreeAndReadOnlyObjects(t *testing.T) {
	machine := fixtureHelperMachine()
	checkout := &domain.DevMachineCheckout{ID: uuid.New(), RepositoryURL: "https://git.example.test/owner/repo", RepositoryFullName: "owner/repo", BaseBranch: "main", WorkingBranch: "ku/task", WorkspacePath: "/workspace/tasks/task-1"}
	run := uuid.New()
	candidate := RepositoryCandidate{GitState: GitState{Branch: checkout.WorkingBranch}, Commit: strings.Repeat("a", 40)}
	argv := []string{"/bin/sh", "-c", "printf '%s\\n' 'test argument'"}
	options, err := candidateVerificationOptions(machine, fixtureHelperImageID, uuid.NewString(), checkout, run, candidate, argv)
	require.NoError(t, err)
	repository, _, err := protectedCheckoutRepository(checkout)
	require.NoError(t, err)
	require.Equal(t, argv, options.Config.Entrypoint)
	require.Equal(t, checkout.WorkspacePath, options.Config.WorkingDir)
	require.Empty(t, options.Config.Cmd)
	require.False(t, options.Config.OpenStdin)
	require.Equal(t, "git-verification", options.Config.Labels["com.ku.service"])
	require.Equal(t, run.String(), options.Config.Labels["com.ku.agent-run-id"])
	require.Equal(t, candidate.Commit, options.Config.Labels["com.ku.verification-commit"])
	require.Len(t, options.HostConfig.Mounts, 3)
	require.False(t, options.HostConfig.Mounts[0].ReadOnly)
	require.Equal(t, checkout.WorkspacePath, options.HostConfig.Mounts[0].Target)
	require.Equal(t, strings.TrimPrefix(repository, "/")+"/ku-runs/"+run.String()+"/verification", options.HostConfig.Mounts[0].VolumeOptions.Subpath)
	require.True(t, options.HostConfig.Mounts[1].ReadOnly)
	require.Nil(t, options.HostConfig.Tmpfs)
	require.Equal(t, "/tmp", options.HostConfig.Mounts[2].Target)
	require.Equal(t, strings.TrimPrefix(repository, "/")+"/ku-runs/"+run.String()+"/verification-scratch", options.HostConfig.Mounts[2].VolumeOptions.Subpath)
	require.Equal(t, "repositories", options.HostConfig.Mounts[1].VolumeOptions.Subpath)
	require.True(t, options.HostConfig.ReadonlyRootfs)
	require.Equal(t, int64(machine.MemoryMB)*1024*1024, options.HostConfig.Resources.Memory)
	require.Equal(t, int64(machine.CPUMillis)*1_000_000, options.HostConfig.Resources.NanoCPUs)
	require.Equal(t, int64(machine.PidsLimit), *options.HostConfig.Resources.PidsLimit)
	for _, invalid := range []struct {
		run       uuid.UUID
		candidate RepositoryCandidate
		argv      []string
	}{
		{uuid.Nil, candidate, argv},
		{run, RepositoryCandidate{Commit: "HEAD"}, argv},
		{run, RepositoryCandidate{GitState: GitState{Branch: "foreign"}, Commit: candidate.Commit}, argv},
		{run, candidate, nil},
		{run, candidate, []string{""}},
		{run, candidate, []string{"sh", "bad\x00argument"}},
	} {
		_, err := candidateVerificationOptions(machine, fixtureHelperImageID, uuid.NewString(), checkout, invalid.run, invalid.candidate, invalid.argv)
		require.Error(t, err)
	}
}

func TestCandidateVerificationReservationGenerationFencesDelayedHelpers(t *testing.T) {
	f := newProtectedRepositoryFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.workspace, "source.txt"), []byte("candidate\n"), 0o600))
	candidate := f.snapshot(t)
	worktree := prepareVerificationFixture(t, f, candidate)
	newer := "1000000000000000001:22222222-2222-2222-2222-222222222222"
	output, err := f.script(t, claimCandidateVerificationScript, false, f.repository, f.run, candidate.Commit, newer)
	require.NoError(t, err, output)
	output, err = f.script(t, prepareCandidateVerificationScript, false, f.repository, f.run, candidate.Commit, newer, f.branch)
	require.NoError(t, err, output)
	marker := filepath.Join(worktree, "new-invocation-output")
	require.NoError(t, os.WriteFile(marker, []byte("preserve newer output\n"), 0o600))
	for _, script := range []string{claimCandidateVerificationScript, prepareCandidateVerificationScript, attestCandidateVerificationScript, cleanupCandidateVerificationScript} {
		output, err := f.script(t, script, false, f.repository, f.run, candidate.Commit, fixtureVerificationOwner, f.branch)
		require.Error(t, err, "delayed old helper must refuse before any private mutation: %s", output)
		content, err := os.ReadFile(marker)
		require.NoError(t, err)
		require.Equal(t, "preserve newer output\n", string(content))
	}
	for _, owner := range []string{
		"1000000000000000001:33333333-3333-3333-3333-333333333333",
		"999999999999999999:44444444-4444-4444-4444-444444444444",
	} {
		output, err := f.script(t, claimCandidateVerificationScript, false, f.repository, f.run, candidate.Commit, owner)
		require.Error(t, err, output)
		require.Contains(t, output, "generation did not advance")
	}
	output, err = f.script(t, claimCandidateVerificationScript, false, f.repository, f.run, candidate.Commit, newer)
	require.NoError(t, err, "same owner should be idempotent: %s", output)
	require.FileExists(t, marker)
	output, err = f.script(t, attestCandidateVerificationScript, false, f.repository, f.run, candidate.Commit, newer)
	require.NoError(t, err, output)
	require.Equal(t, candidate.Commit+"\n", output)
	output, err = f.script(t, cleanupCandidateVerificationScript, false, f.repository, f.run, candidate.Commit, newer)
	require.NoError(t, err, output)
	require.NoDirExists(t, worktree)
	ownerBytes, err := os.ReadFile(filepath.Join(f.repository, "ku-runs", f.run, "verification.owner"))
	require.NoError(t, err)
	require.Equal(t, newer+"\n", string(ownerBytes), "cleanup must preserve the fence against delayed requests")
	output, err = f.script(t, prepareCandidateVerificationScript, false, f.repository, f.run, candidate.Commit, fixtureVerificationOwner, f.branch)
	require.Error(t, err, output)
	require.NoDirExists(t, worktree)
}
