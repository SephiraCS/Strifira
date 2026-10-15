package machine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func recoverTerminalFixture(t *testing.T, f *protectedRepositoryFixture, candidate RepositoryCandidate) (string, error) {
	t.Helper()
	return f.script(t, synchronizeRepositoryCandidateScript, false, f.workspace, f.repository, f.run, strings.Repeat("a", 64), f.branch, candidate.Commit, candidate.SourceCommit, candidate.SourceIndexHash, "true")
}

func TestTerminalAgentCleanupRecoversAbandonedSourceTransaction(t *testing.T) {
	for _, boundary := range []string{"before-index-lock", "before-index-rename", "after-index-rename"} {
		t.Run(boundary, func(t *testing.T) {
			f, candidate := dirtySynchronizationFixture(t)
			needle := map[string]string{"before-index-lock": `acquire_lock "$git_dir/index.lock"`, "before-index-rename": `atomic_volume_move -f "$stage/new-index" "$git_dir/index"`, "after-index-rename": "printf 'updated\\n'"}[boundary]
			interrupted := strings.Replace(synchronizeRepositoryCandidateScript, needle, "kill -KILL $$\n"+needle, 1)
			_, err := synchronizeFixtureCandidate(t, f, candidate, interrupted)
			require.Error(t, err)
			runDir := filepath.Join(f.repository, "strifira-runs", f.run)
			for _, directory := range []string{"verification", "verification-scratch", "verification-stage", "snapshot.abandoned"} {
				require.NoError(t, os.MkdirAll(filepath.Join(runDir, directory), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(runDir, directory, "private-file"), []byte("discarded output"), 0o400))
			}
			ownerFile := filepath.Join(runDir, "verification.owner")
			require.NoError(t, os.WriteFile(ownerFile, []byte("protected-owner-fence"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(f.workspace, "source.txt"), []byte("later work after backend death\n"), 0o600))
			output, err := f.script(t, cleanupTerminalAgentRunScript, false, f.repository, f.run)
			require.NoError(t, err, output)
			retained, err := parseRepositoryCandidate(output)
			require.NoError(t, err)
			require.Equal(t, candidate, retained)
			for _, directory := range []string{"verification", "verification-scratch", "verification-stage", "snapshot.abandoned"} {
				require.NoDirExists(t, filepath.Join(runDir, directory))
			}
			require.FileExists(t, ownerFile)
			require.Equal(t, candidate.Commit, f.git(t, f.repository, "rev-parse", "refs/strifira/runs/"+f.run+"/candidate"))
			require.Equal(t, candidate.BaseCommit, f.git(t, f.repository, "rev-parse", "refs/strifira/runs/"+f.run+"/base"))
			output, err = recoverTerminalFixture(t, f, candidate)
			require.NoError(t, err, output)
			require.Contains(t, []string{"updated\n", "unchanged\n"}, output)
			require.Equal(t, candidate.Commit, f.git(t, f.workspace, "rev-parse", "HEAD"))
			require.Empty(t, f.git(t, f.workspace, "diff", "--cached", "--name-only"))
			contents, err := os.ReadFile(filepath.Join(f.workspace, "source.txt"))
			require.NoError(t, err)
			require.Equal(t, "later work after backend death\n", string(contents))
			for _, file := range []string{"HEAD.lock", "index.lock", "refs/heads/" + f.branch + ".lock"} {
				require.NoFileExists(t, filepath.Join(f.workspace, ".git", file))
			}
			require.NoDirExists(t, filepath.Join(runDir, "source-sync-"+strings.Repeat("a", 64)))
			output, err = recoverTerminalFixture(t, f, candidate)
			require.NoError(t, err, output)
			require.Equal(t, "unchanged\n", output)
			_, err = synchronizeFixtureCandidate(t, f, candidate, synchronizeRepositoryCandidateScript)
			require.Error(t, err, "normal helpers cannot resurrect this run")
			_, err = f.script(t, retainRepositoryCandidateScript, false, f.workspace, f.repository, f.run)
			require.Error(t, err, "late retention cannot recreate private snapshots")
		})
	}
}

func TestTerminalAgentCleanupPreservesLaterMetadataAndForeignLocks(t *testing.T) {
	for _, mode := range []string{"newer-commit", "index", "detached-head", "foreign-lock"} {
		t.Run(mode, func(t *testing.T) {
			f, candidate := dirtySynchronizationFixture(t)
			interrupted := strings.Replace(synchronizeRepositoryCandidateScript, `atomic_volume_move -f "$stage/new-index" "$git_dir/index"`, "kill -KILL $$", 1)
			_, err := synchronizeFixtureCandidate(t, f, candidate, interrupted)
			require.Error(t, err)
			gitDir := filepath.Join(f.workspace, ".git")
			switch mode {
			case "newer-commit", "index":
				for _, file := range []string{"HEAD.lock", "index.lock", "refs/heads/" + f.branch + ".lock"} {
					require.NoError(t, os.Remove(filepath.Join(gitDir, file)))
				}
				require.NoError(t, os.WriteFile(filepath.Join(f.workspace, "source.txt"), []byte("new user staging\n"), 0o600))
				f.git(t, f.workspace, "add", "source.txt")
				if mode == "newer-commit" {
					f.git(t, f.workspace, "-c", "user.name=fixture", "-c", "user.email=fixture@localhost", "commit", "-m", "later owner work")
				}
			case "detached-head":
				require.NoError(t, os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte(candidate.Commit+"\n"), 0o600))
			case "foreign-lock":
				require.NoError(t, os.WriteFile(filepath.Join(gitDir, "index.lock"), []byte("owner Git operation\n"), 0o600))
			}
			head := f.git(t, f.workspace, "rev-parse", "HEAD")
			index, err := os.ReadFile(filepath.Join(gitDir, "index"))
			require.NoError(t, err)
			output, err := f.script(t, cleanupTerminalAgentRunScript, false, f.repository, f.run)
			require.NoError(t, err, output)
			output, err = recoverTerminalFixture(t, f, candidate)
			require.NoError(t, err, output)
			require.Contains(t, output, "conflict\n")
			require.Equal(t, head, f.git(t, f.workspace, "rev-parse", "HEAD"))
			after, err := os.ReadFile(filepath.Join(gitDir, "index"))
			require.NoError(t, err)
			require.Equal(t, index, after)
			require.NoFileExists(t, filepath.Join(gitDir, "HEAD.lock"))
			require.NoFileExists(t, filepath.Join(gitDir, "refs/heads/"+f.branch+".lock"))
			if mode == "foreign-lock" {
				contents, err := os.ReadFile(filepath.Join(gitDir, "index.lock"))
				require.NoError(t, err)
				require.Equal(t, "owner Git operation\n", string(contents))
			} else {
				require.NoFileExists(t, filepath.Join(gitDir, "index.lock"))
			}
			replayed, err := recoverTerminalFixture(t, f, candidate)
			require.NoError(t, err, replayed)
			require.Equal(t, output, replayed, "conflict survives a failed database acknowledgement")
		})
	}
}

func TestTerminalRecoveryNeverStartsANewSourceTransaction(t *testing.T) {
	f, candidate := dirtySynchronizationFixture(t)
	index, err := os.ReadFile(filepath.Join(f.workspace, ".git", "index"))
	require.NoError(t, err)
	output, err := f.script(t, cleanupTerminalAgentRunScript, false, f.repository, f.run)
	require.NoError(t, err, output)
	output, err = recoverTerminalFixture(t, f, candidate)
	require.NoError(t, err, output)
	require.Equal(t, "unchanged\n", output)
	require.Equal(t, candidate.SourceCommit, f.git(t, f.workspace, "rev-parse", "HEAD"))
	after, err := os.ReadFile(filepath.Join(f.workspace, ".git", "index"))
	require.NoError(t, err)
	require.Equal(t, index, after)
}

func TestTerminalAgentCleanupRecoversFailedMetadataInstall(t *testing.T) {
	f, candidate := dirtySynchronizationFixture(t)
	interrupted := strings.Replace(synchronizeRepositoryCandidateScript, `atomic_volume_move -f "$stage/new-index" "$git_dir/index"`, "exit 23", 1)
	_, err := synchronizeFixtureCandidate(t, f, candidate, interrupted)
	require.Error(t, err)
	require.Equal(t, candidate.Commit, f.git(t, f.workspace, "rev-parse", "HEAD"))
	output, err := f.script(t, cleanupTerminalAgentRunScript, false, f.repository, f.run)
	require.NoError(t, err, output)
	output, err = recoverTerminalFixture(t, f, candidate)
	require.NoError(t, err, output)
	require.Empty(t, f.git(t, f.workspace, "diff", "--cached", "--name-only"), "an ordinary exit/trap must retain the half-installed transaction for recovery")
}

func TestTerminalAgentCleanupRejectsSymlinkedPrivateDirectory(t *testing.T) {
	f, _ := dirtySynchronizationFixture(t)
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "keep"), []byte("owner data"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(f.repository, "strifira-runs", f.run, "verification")))
	_, err := f.script(t, cleanupTerminalAgentRunScript, false, f.repository, f.run)
	require.Error(t, err)
	require.FileExists(t, filepath.Join(outside, "keep"))
}

func TestTerminalAgentCleanupRequiresDatabaseAuthorityBeforeDocker(t *testing.T) {
	machine := fixtureHelperMachine()
	for _, status := range []domain.DevMachineAgentRunStatus{domain.DevMachineAgentRunStatusQueued, domain.DevMachineAgentRunStatusStarting, domain.DevMachineAgentRunStatusRunning, domain.DevMachineAgentRunStatusWaitingInput} {
		run := &domain.DevMachineAgentRun{ID: uuid.New(), MachineID: machine.ID, WorkspaceID: machine.WorkspaceID, Status: status}
		_, err := (&DockerRuntime{}).CleanupAgentRun(t.Context(), machine, nil, nil, run)
		require.ErrorContains(t, err, "terminal authority")
	}
}

func TestTerminalAgentCleanupRejectsForgedDefaultAuthorityBeforeDocker(t *testing.T) {
	for _, mode := range []string{"missing", "identity", "machine", "workspace", "path", "repository", "base", "branch", "credential-url"} {
		t.Run(mode, func(t *testing.T) {
			machine := fixtureHelperMachine()
			machine.RepoOwner, machine.RepoName = "strifira", "api"
			machine.RepoURL = "https://git.example.com/strifira/api"
			machine.BaseBranch, machine.WorkingBranch = "main", "strifira/task"
			checkout, err := defaultRepositoryCheckout(machine, "https://git.example.com")
			require.NoError(t, err)
			selected := *checkout
			switch mode {
			case "missing":
				checkout = nil
			case "identity":
				checkout.ID = uuid.New()
			case "machine":
				checkout.MachineID = uuid.New()
			case "workspace":
				checkout.WorkspaceID = uuid.New()
			case "path":
				checkout.WorkspacePath = "/workspace/tasks/foreign"
			case "repository":
				checkout.RepositoryFullName = "strifira/foreign"
			case "base":
				checkout.BaseBranch = "different"
			case "branch":
				checkout.WorkingBranch = "different"
			case "credential-url":
				checkout.RepositoryURL = "https://token@git.example.com/strifira/api"
			}
			run := &domain.DevMachineAgentRun{ID: uuid.New(), MachineID: machine.ID, WorkspaceID: machine.WorkspaceID, Status: domain.DevMachineAgentRunStatusTimeout, RepositorySelection: &selected}
			_, err = (&DockerRuntime{}).CleanupAgentRun(t.Context(), machine, nil, checkout, run)
			require.Error(t, err, "forged default selection must fail before touching the daemon")
		})
	}
}
