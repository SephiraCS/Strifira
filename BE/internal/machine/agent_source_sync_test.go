package machine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ku/ku-backend/internal/domain"
	"github.com/stretchr/testify/require"
)

func synchronizeFixtureCandidate(t *testing.T, f *protectedRepositoryFixture, candidate RepositoryCandidate, script string) (string, error) {
	t.Helper()
	return f.script(t, script, false, f.workspace, f.repository, f.run, strings.Repeat("a", 64), f.branch, candidate.Commit, candidate.SourceCommit, candidate.SourceIndexHash)
}

func dirtySynchronizationFixture(t *testing.T) (*protectedRepositoryFixture, RepositoryCandidate) {
	t.Helper()
	f := newProtectedRepositoryFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.workspace, "source.txt"), []byte("verified\n"), 0o600))
	return f, f.snapshot(t)
}

func TestSourceSynchronizationRecordsCandidateWithoutResettingLaterFiles(t *testing.T) {
	f, candidate := dirtySynchronizationFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.workspace, "source.txt"), []byte("later edit\n"), 0o600))
	output, err := synchronizeFixtureCandidate(t, f, candidate, synchronizeRepositoryCandidateScript)
	require.NoError(t, err, output)
	require.Equal(t, "updated\n", output)
	require.Equal(t, candidate.Commit, f.git(t, f.workspace, "rev-parse", "HEAD"))
	require.Equal(t, "verified", f.git(t, f.workspace, "show", "HEAD:source.txt"))
	require.Equal(t, "", f.git(t, f.workspace, "diff", "--cached", "--name-only"))
	contents, err := os.ReadFile(filepath.Join(f.workspace, "source.txt"))
	require.NoError(t, err)
	require.Equal(t, "later edit\n", string(contents))
	output, err = synchronizeFixtureCandidate(t, f, candidate, synchronizeRepositoryCandidateScript)
	require.NoError(t, err, output)
	require.Equal(t, "unchanged\n", output)
}

func TestSourceSynchronizationPreservesLaterGitMetadata(t *testing.T) {
	for _, mode := range []string{"index", "branch", "detached-head", "foreign-lock"} {
		t.Run(mode, func(t *testing.T) {
			f, candidate := dirtySynchronizationFixture(t)
			switch mode {
			case "index":
				f.git(t, f.workspace, "add", "source.txt")
			case "branch":
				f.git(t, f.workspace, "-c", "user.name=fixture", "-c", "user.email=fixture@localhost", "commit", "--allow-empty", "-m", "later commit")
			case "detached-head":
				f.git(t, f.workspace, "checkout", "--detach", "--quiet")
			case "foreign-lock":
				require.NoError(t, os.WriteFile(filepath.Join(f.workspace, ".git", "index.lock"), []byte("user Git operation\n"), 0o600))
			}
			head := f.git(t, f.workspace, "rev-parse", "HEAD")
			index, err := os.ReadFile(filepath.Join(f.workspace, ".git", "index"))
			require.NoError(t, err)
			output, err := synchronizeFixtureCandidate(t, f, candidate, synchronizeRepositoryCandidateScript)
			require.NoError(t, err, output)
			require.Contains(t, output, "conflict\n")
			require.Equal(t, head, f.git(t, f.workspace, "rev-parse", "HEAD"))
			after, err := os.ReadFile(filepath.Join(f.workspace, ".git", "index"))
			require.NoError(t, err)
			require.Equal(t, index, after)
			if mode == "foreign-lock" {
				lock, err := os.ReadFile(filepath.Join(f.workspace, ".git", "index.lock"))
				require.NoError(t, err)
				require.Equal(t, "user Git operation\n", string(lock))
			}
		})
	}
}

func TestSourceSynchronizationRecoversActualHardKills(t *testing.T) {
	for _, boundary := range []string{"before-index-lock", "before-index-rename", "after-index-rename"} {
		t.Run(boundary, func(t *testing.T) {
			f, candidate := dirtySynchronizationFixture(t)
			needle := map[string]string{"before-index-lock": `acquire_lock "$git_dir/index.lock"`, "before-index-rename": `atomic_volume_move -f "$stage/new-index" "$git_dir/index"`, "after-index-rename": "printf 'updated\\n'"}[boundary]
			require.Contains(t, synchronizeRepositoryCandidateScript, needle)
			interrupted := strings.Replace(synchronizeRepositoryCandidateScript, needle, "kill -KILL $$\n"+needle, 1)
			_, err := synchronizeFixtureCandidate(t, f, candidate, interrupted)
			require.Error(t, err, "the trusted helper was actually killed")
			require.NoError(t, os.WriteFile(filepath.Join(f.workspace, "source.txt"), []byte("later work after kill\n"), 0o600))
			output, err := synchronizeFixtureCandidate(t, f, candidate, synchronizeRepositoryCandidateScript)
			require.NoError(t, err, output)
			require.Contains(t, []string{"updated\n", "unchanged\n"}, output)
			require.Equal(t, candidate.Commit, f.git(t, f.workspace, "rev-parse", "HEAD"))
			require.Empty(t, f.git(t, f.workspace, "diff", "--cached", "--name-only"))
			contents, err := os.ReadFile(filepath.Join(f.workspace, "source.txt"))
			require.NoError(t, err)
			require.Equal(t, "later work after kill\n", string(contents))
			for _, file := range []string{"HEAD.lock", "index.lock", "refs/heads/" + f.branch + ".lock"} {
				require.NoFileExists(t, filepath.Join(f.workspace, ".git", file))
			}
			require.NoDirExists(t, filepath.Join(f.repository, "ku-runs", f.run, "source-sync-"+strings.Repeat("a", 64)))
		})
	}
}

func TestSourceSynchronizationRecoveryPreservesNewerUserCommit(t *testing.T) {
	f, candidate := dirtySynchronizationFixture(t)
	interrupted := strings.Replace(synchronizeRepositoryCandidateScript, `atomic_volume_move -f "$stage/new-index" "$git_dir/index"`, "kill -KILL $$", 1)
	_, err := synchronizeFixtureCandidate(t, f, candidate, interrupted)
	require.Error(t, err)
	for _, file := range []string{"HEAD.lock", "index.lock", "refs/heads/" + f.branch + ".lock"} {
		require.NoError(t, os.Remove(filepath.Join(f.workspace, ".git", file)))
	}
	require.NoError(t, os.WriteFile(filepath.Join(f.workspace, "source.txt"), []byte("newer user work\n"), 0o600))
	f.git(t, f.workspace, "add", "source.txt")
	f.git(t, f.workspace, "-c", "user.name=fixture", "-c", "user.email=fixture@localhost", "commit", "-m", "user resolved abandoned locks")
	head := f.git(t, f.workspace, "rev-parse", "HEAD")
	index, err := os.ReadFile(filepath.Join(f.workspace, ".git", "index"))
	require.NoError(t, err)
	output, err := synchronizeFixtureCandidate(t, f, candidate, synchronizeRepositoryCandidateScript)
	require.NoError(t, err, output)
	require.Contains(t, output, "conflict\nsource branch changed")
	require.Equal(t, head, f.git(t, f.workspace, "rev-parse", "HEAD"))
	after, err := os.ReadFile(filepath.Join(f.workspace, ".git", "index"))
	require.NoError(t, err)
	require.Equal(t, index, after)
}

func TestSourceSynchronizationRejectsSymlinkedRefParent(t *testing.T) {
	f, candidate := dirtySynchronizationFixture(t)
	parent := filepath.Join(f.workspace, ".git", "refs", "heads", "ku")
	require.NoError(t, os.Rename(parent, parent+".saved"))
	external := t.TempDir()
	require.NoError(t, os.Symlink(external, parent))
	_, err := synchronizeFixtureCandidate(t, f, candidate, synchronizeRepositoryCandidateScript)
	require.Error(t, err)
	entries, err := os.ReadDir(external)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestSourceSynchronizationIgnoresWritableGitExecutables(t *testing.T) {
	f, candidate := dirtySynchronizationFixture(t)
	marker := filepath.Join(t.TempDir(), "executed")
	f.git(t, f.workspace, "config", "core.fsmonitor", "touch "+marker)
	f.git(t, f.workspace, "config", "core.sshCommand", "touch "+marker)
	f.git(t, f.workspace, "config", "credential.helper", "!touch "+marker)
	f.git(t, f.workspace, "config", "uploadpack.packObjectsHook", "touch "+marker)
	require.NoError(t, os.WriteFile(filepath.Join(f.workspace, ".git", "hooks", "reference-transaction"), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o700))
	output, err := synchronizeFixtureCandidate(t, f, candidate, synchronizeRepositoryCandidateScript)
	require.NoError(t, err, output)
	require.Equal(t, "updated\n", output)
	require.NoFileExists(t, marker)
}

func TestManagerSourceConflictDoesNotMisreportPublishedCandidate(t *testing.T) {
	manager, store, runtime, run := verificationFixture(t)
	run.PushBranch = true
	runtime.sourceSyncResult = RepositorySourceSync{Status: "conflict", Reason: "source index changed after candidate retention"}
	require.NoError(t, manager.runAgent(t.Context(), store.machine, &domain.DevMachineOperation{AgentRunID: &run.ID, Generation: 1}))
	require.Equal(t, domain.DevMachineAgentRunStatusSucceeded, store.completedRun.Status)
	require.Equal(t, 1, runtime.publishedBranches)
	require.Equal(t, runtime.publishedCandidate, runtime.synchronizedCandidate)
	require.Contains(t, string(store.completedRun.RiskNotes), runtime.sourceSyncResult.Reason)
}
