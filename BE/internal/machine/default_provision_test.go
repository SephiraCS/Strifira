package machine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/stretchr/testify/require"
)

func defaultMigrationFixture(t *testing.T) (*protectedRepositoryFixture, string, string) {
	t.Helper()
	f := newProtectedRepositoryFixture(t)
	checkoutFixtureTools(t, f)
	for _, name := range []string{"stat", "cmp"} {
		tool, err := exec.LookPath(name)
		require.NoError(t, err)
		tool, err = filepath.EvalSymlinks(tool)
		require.NoError(t, err)
		f.toolPath += filepath.Dir(tool) + string(os.PathListSeparator)
	}
	root := f.workspace
	for _, name := range []string{"tasks", "repos", ".cache"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, name), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(root, name, "keep"), []byte("keep infrastructure"), 0o600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "source.txt"), []byte("staged content\n"), 0o600))
	f.git(t, root, "add", "source.txt")
	require.NoError(t, os.WriteFile(filepath.Join(root, "source.txt"), []byte("later unstaged content\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "untracked\nlast\n"), []byte("untracked data\n"), 0o600))
	require.NoError(t, os.Symlink("source.txt", filepath.Join(root, "source-link")))
	return f, uuid.NewString(), strings.Repeat("b", 64)
}

func TestDefaultRepositoryMigrationPreservesDirtyFilesIndexAndLinkedIssuePointers(t *testing.T) {
	for _, boundary := range []string{"none", "before-stage-move", "during-entry-moves", "before-publish", "after-publish", "before-pointer-rename", "after-pointer-rename"} {
		t.Run(boundary, func(t *testing.T) {
			f, checkoutID, key := defaultMigrationFixture(t)
			root := f.workspace
			destination := filepath.Join(root, "repository")
			for _, name := range []string{"linked-a", "linked-b"} {
				f.git(t, root, "worktree", "add", "--quiet", "-b", "ku/"+name, filepath.Join(root, "tasks", name))
			}
			head := f.git(t, root, "rev-parse", "HEAD")
			index, err := os.ReadFile(filepath.Join(root, ".git", "index"))
			require.NoError(t, err)
			inode, err := os.Stat(filepath.Join(root, "source.txt"))
			require.NoError(t, err)
			script := migrateLegacyRootCheckoutScript
			if boundary != "none" {
				needle := map[string]string{"before-stage-move": `atomic_volume_move -n "$journal/worktree" "$stage"`, "during-entry-moves": `mv -T -n -- "$root/$name" "$target/$name"`, "before-publish": `atomic_volume_move -n "$stage" "$destination"`, "after-publish": " sync -f \"$root\"\nfi\nfor record in \"$journal/pointers\"/*; do", "before-pointer-rename": `atomic_volume_move -f "$record/new-pointer" "$pointer"`, "after-pointer-rename": ` sync -f "$pointer"`}[boundary]
				require.Equal(t, 1, strings.Count(script, needle), needle)
				if boundary == "during-entry-moves" {
					script = strings.Replace(script, needle, needle+"\nkill -KILL $$", 1)
				} else {
					script = strings.Replace(script, needle, "kill -KILL $$\n"+needle, 1)
				}
				output, err := f.script(t, script, false, root, destination, f.repository, checkoutID, key)
				require.Error(t, err, output)
			}
			output, err := f.script(t, migrateLegacyRootCheckoutScript, false, root, destination, f.repository, checkoutID, key)
			require.NoError(t, err, output)
			require.Equal(t, head, f.git(t, destination, "rev-parse", "HEAD"))
			newIndex, err := os.ReadFile(filepath.Join(destination, ".git", "index"))
			require.NoError(t, err)
			require.Equal(t, index, newIndex)
			moved, err := os.Stat(filepath.Join(destination, "source.txt"))
			require.NoError(t, err)
			require.True(t, os.SameFile(inode, moved), "moves must preserve the source inode")
			require.Equal(t, "later unstaged content\n", readCheckoutFixtureFile(t, filepath.Join(destination, "source.txt")))
			require.Equal(t, "untracked data\n", readCheckoutFixtureFile(t, filepath.Join(destination, "untracked\nlast\n")))
			require.Equal(t, "source.txt", mustReadlink(t, filepath.Join(destination, "source-link")))
			for _, name := range []string{"tasks", "repos", ".cache"} {
				require.Equal(t, "keep infrastructure", readCheckoutFixtureFile(t, filepath.Join(root, name, "keep")))
			}
			for _, name := range []string{"linked-a", "linked-b"} {
				require.Equal(t, head, f.git(t, filepath.Join(root, "tasks", name), "rev-parse", "HEAD"))
				require.Equal(t, "ku/"+name, f.git(t, filepath.Join(root, "tasks", name), "branch", "--show-current"))
			}
			output, err = f.script(t, migrateLegacyRootCheckoutScript, false, root, destination, f.repository, checkoutID, key)
			require.NoError(t, err, output)
			require.NoDirExists(t, filepath.Join(root, ".ku-root-"+key))
		})
	}
}

func mustReadlink(t *testing.T, path string) string {
	t.Helper()
	target, err := os.Readlink(path)
	require.NoError(t, err)
	return target
}

func TestDefaultRepositoryMigrationRepairsLegacyRootGitPointer(t *testing.T) {
	for _, boundary := range []string{"none", "before-pointer-rename", "after-pointer-rename"} {
		t.Run(boundary, func(t *testing.T) {
			f, id, key := defaultMigrationFixture(t)
			root := f.workspace
			seed := filepath.Join(f.root, "root-seed")
			require.NoError(t, os.Rename(root, seed))
			cache := filepath.Join(f.root, "root-cache.git")
			f.git(t, f.root, "clone", "--quiet", "--bare", seed, cache)
			f.git(t, f.root, "--git-dir="+cache, "worktree", "add", "--quiet", "-b", "owner/root", root, f.branch)
			require.NoError(t, os.Mkdir(filepath.Join(root, "repos"), 0o700))
			movedCache := filepath.Join(root, "repos", "legacy.git")
			require.NoError(t, os.Rename(cache, movedCache))
			admin := filepath.Join(movedCache, "worktrees", filepath.Base(root))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+admin+"\n"), 0o600))
			f.git(t, f.root, "--git-dir="+movedCache, "worktree", "repair", root)
			require.NoError(t, os.WriteFile(filepath.Join(root, "source.txt"), []byte("root pointer staged\n"), 0o600))
			f.git(t, root, "add", "source.txt")
			require.NoError(t, os.WriteFile(filepath.Join(root, "source.txt"), []byte("root pointer later edit\n"), 0o600))
			head := f.git(t, root, "rev-parse", "HEAD")
			index := readCheckoutFixtureFile(t, filepath.Join(admin, "index"))
			destination := filepath.Join(root, "repository")
			if boundary != "none" {
				needle := `atomic_volume_move -f "$record/new-pointer" "$pointer"`
				if boundary == "after-pointer-rename" {
					needle = ` sync -f "$pointer"`
				}
				require.Equal(t, 1, strings.Count(migrateLegacyRootCheckoutScript, needle))
				interrupted := strings.Replace(migrateLegacyRootCheckoutScript, needle, "kill -KILL $$\n"+needle, 1)
				output, err := f.script(t, interrupted, false, root, destination, f.repository, id, key)
				require.Error(t, err, output)
			}
			for range 2 {
				output, err := f.script(t, migrateLegacyRootCheckoutScript, false, root, destination, f.repository, id, key)
				require.NoError(t, err, output)
				require.FileExists(t, filepath.Join(destination, ".git"))
				require.Equal(t, destination+"/.git", strings.TrimSpace(readCheckoutFixtureFile(t, filepath.Join(admin, "gitdir"))))
				require.Equal(t, "owner/root", f.git(t, destination, "branch", "--show-current"))
				require.Equal(t, head, f.git(t, destination, "rev-parse", "HEAD"))
				require.Equal(t, index, readCheckoutFixtureFile(t, filepath.Join(admin, "index")))
				require.Equal(t, "root pointer staged", f.git(t, destination, "show", ":source.txt"))
				require.Equal(t, "root pointer later edit\n", readCheckoutFixtureFile(t, filepath.Join(destination, "source.txt")))
			}
		})
	}
}

func TestDefaultRepositoryMigrationRefusesCollisionsWithoutMovingOwnerData(t *testing.T) {
	for _, mode := range []string{"destination", "stage", "tracked-reserved", "foreign-linked-pointer"} {
		t.Run(mode, func(t *testing.T) {
			f, id, key := defaultMigrationFixture(t)
			root := f.workspace
			destination := filepath.Join(root, "repository")
			switch mode {
			case "destination":
				require.NoError(t, os.Mkdir(destination, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(destination, "owner"), []byte("other project"), 0o600))
			case "stage":
				require.NoError(t, os.Mkdir(filepath.Join(root, ".ku-root-"+key), 0o700))
			case "tracked-reserved":
				f.git(t, root, "add", "tasks/keep")
			case "foreign-linked-pointer":
				f.git(t, root, "worktree", "add", "--quiet", "-b", "foreign", filepath.Join(f.root, "foreign"))
			}
			index, err := os.ReadFile(filepath.Join(root, ".git", "index"))
			require.NoError(t, err)
			_, err = f.script(t, migrateLegacyRootCheckoutScript, false, root, destination, f.repository, id, key)
			require.Error(t, err)
			after, err := os.ReadFile(filepath.Join(root, ".git", "index"))
			require.NoError(t, err)
			require.Equal(t, index, after)
			require.Equal(t, "later unstaged content\n", readCheckoutFixtureFile(t, filepath.Join(root, "source.txt")))
		})
	}
}

func TestDefaultRepositoryMigrationRecoversNewRootDataAfterJournalCreation(t *testing.T) {
	for _, mode := range []string{"before-stage-move", "after-publish", "collision-after-publish"} {
		t.Run(mode, func(t *testing.T) {
			f, id, key := defaultMigrationFixture(t)
			root, destination := f.workspace, filepath.Join(f.workspace, "repository")
			f.git(t, root, "worktree", "add", "--quiet", "-b", "ku/linked", filepath.Join(root, "tasks", "linked"))
			head := f.git(t, root, "rev-parse", "HEAD")
			index := readCheckoutFixtureFile(t, filepath.Join(root, ".git", "index"))
			needle := `atomic_volume_move -n "$journal/worktree" "$stage"`
			if mode != "before-stage-move" {
				needle = `atomic_volume_move -f "$record/new-pointer" "$pointer"`
			}
			require.Equal(t, 1, strings.Count(migrateLegacyRootCheckoutScript, needle))
			interrupted := strings.Replace(migrateLegacyRootCheckoutScript, needle, "kill -KILL $$\n"+needle, 1)
			output, err := f.script(t, interrupted, false, root, destination, f.repository, id, key)
			require.Error(t, err, output)
			name := "new root\nlast\n"
			if mode == "collision-after-publish" {
				name = "source.txt"
			}
			require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte("new owner data\n"), 0o640))
			before, err := os.Stat(filepath.Join(root, name))
			require.NoError(t, err)
			for range 2 {
				output, err = f.script(t, migrateLegacyRootCheckoutScript, false, root, destination, f.repository, id, key)
				if mode == "collision-after-publish" {
					require.Error(t, err)
					require.Equal(t, "new owner data\n", readCheckoutFixtureFile(t, filepath.Join(root, name)))
				} else {
					require.NoError(t, err, output)
					require.Equal(t, "new owner data\n", readCheckoutFixtureFile(t, filepath.Join(destination, name)))
					moved, err := os.Stat(filepath.Join(destination, name))
					require.NoError(t, err)
					require.True(t, os.SameFile(before, moved))
					require.NoFileExists(t, filepath.Join(root, name))
					require.Equal(t, head, f.git(t, filepath.Join(root, "tasks", "linked"), "rev-parse", "HEAD"))
				}
				require.Equal(t, head, f.git(t, destination, "rev-parse", "HEAD"))
				require.Equal(t, index, readCheckoutFixtureFile(t, filepath.Join(destination, ".git", "index")))
				require.Equal(t, "later unstaged content\n", readCheckoutFixtureFile(t, filepath.Join(destination, "source.txt")))
			}
		})
	}
}

func TestDefaultProvisionUsesCanonicalBackendCredentialOutsideServiceSecrets(t *testing.T) {
	machine := fixtureHelperMachine()
	machine.RepoOwner, machine.RepoName = "ku", "api"
	machine.RepoURL = "https://git.example.com/ku/api"
	machine.BaseBranch, machine.WorkingBranch = "main", "ku/task"
	store := &managerStoreFake{machine: machine, linkedRepository: &domain.ForgejoRepoModel{InstanceURL: "https://git.example.com"}}
	manager := NewManager(store, &runtimeFake{}, nil, nil, &GlobalForgejoCredentials{URL: "https://git.example.com", Token: "backend-only-token"}, make([]byte, 32), nil, "test")
	provision, err := manager.defaultRepositoryProvision(t.Context(), machine)
	require.NoError(t, err)
	require.Equal(t, "https://git.example.com/ku/api", provision.Checkout.RepositoryURL)
	require.Equal(t, machine.ID, provision.Checkout.ID)
	require.NoError(t, validateRepositoryProvision(machine, provision))
	require.Error(t, validateRepositoryProvision(machine, nil))
	secrets, err := manager.serviceSecrets(t.Context(), machine, []domain.DevMachineService{{ServiceType: "ide", ServiceKey: "ide"}})
	require.NoError(t, err)
	require.NotContains(t, secrets["ide"], "FORGEJO_TOKEN")
}

func TestDefaultRepositoryRefusesForgejoOriginReplacement(t *testing.T) {
	machine := fixtureHelperMachine()
	machine.RepoOwner, machine.RepoName = "ku", "api"
	machine.RepoURL = "https://original.example/ku/api"
	machine.BaseBranch, machine.WorkingBranch = "main", "owner/default"
	store := &managerStoreFake{machine: machine, linkedRepository: &domain.ForgejoRepoModel{InstanceURL: "https://replacement.example"}}
	manager := NewManager(store, &runtimeFake{}, nil, nil, &GlobalForgejoCredentials{URL: "https://replacement.example", Token: "backend-only-token"}, make([]byte, 32), nil, "test")
	provision, err := manager.defaultRepositoryProvision(t.Context(), machine)
	require.ErrorContains(t, err, "original Forgejo URL")
	require.Nil(t, provision)
	checkout := &domain.DevMachineCheckout{ID: machine.ID, MachineID: machine.ID, WorkspaceID: machine.WorkspaceID,
		RepositoryFullName: "ku/api", RepositoryURL: "https://replacement.example/ku/api", BaseBranch: "main",
		WorkingBranch: "owner/default", WorkspacePath: "/workspace/repository"}
	require.Error(t, validateRepositoryProvision(machine, &RepositoryProvision{Checkout: checkout, Token: "backend-only-token"}))
	store.linkedRepository.InstanceURL = "https://original.example"
	manager.globalForgejo.URL = "https://original.example"
	provision, err = manager.defaultRepositoryProvision(t.Context(), machine)
	require.NoError(t, err)
	require.Equal(t, machine.RepoURL, provision.Checkout.RepositoryURL)
	require.NoError(t, validateRepositoryProvision(machine, provision))
}
