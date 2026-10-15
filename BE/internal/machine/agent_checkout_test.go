package machine

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

func TestProtectedCheckoutDescriptor(t *testing.T) {
	valid := domain.DevMachineCheckout{ID: uuid.New(), RepositoryFullName: "TerraScale/strifira",
		RepositoryURL: "https://git.terrascale.tech/TerraScale/strifira", BaseBranch: "main",
		WorkingBranch: "feat/TS-279", WorkspacePath: "/workspace/tasks/TS-279"}
	repository, parsed, err := protectedCheckoutRepository(&valid)
	require.NoError(t, err)
	require.Equal(t, "git.terrascale.tech", parsed.Host)
	require.Regexp(t, `^/repositories/[a-f0-9]{64}\.git$`, repository)
	withSuffix := valid
	withSuffix.RepositoryURL += ".git"
	suffixRepository, _, err := protectedCheckoutRepository(&withSuffix)
	require.NoError(t, err)
	require.Equal(t, repository, suffixRepository, "optional Git suffix must not create a second protected store")
	defaultPath := valid
	defaultPath.WorkspacePath = "/workspace/repository"
	_, _, err = protectedCheckoutRepository(&defaultPath)
	require.NoError(t, err)
	_, _, err = protectedCheckoutRepository(nil)
	require.Error(t, err)
	for _, test := range []struct {
		name   string
		mutate func(*domain.DevMachineCheckout)
	}{
		{"missing-identity", func(c *domain.DevMachineCheckout) { c.ID = uuid.Nil }},
		{"http", func(c *domain.DevMachineCheckout) { c.RepositoryURL = "http://git.terrascale.tech/TerraScale/strifira" }},
		{"credentials", func(c *domain.DevMachineCheckout) {
			c.RepositoryURL = "https://user:secret@git.terrascale.tech/TerraScale/strifira"
		}},
		{"other-repository", func(c *domain.DevMachineCheckout) { c.RepositoryURL = "https://git.terrascale.tech/TerraScale/other" }},
		{"encoded-path", func(c *domain.DevMachineCheckout) { c.RepositoryURL = "https://git.terrascale.tech/TerraScale/%6bu" }},
		{"query", func(c *domain.DevMachineCheckout) { c.RepositoryURL += "?token=secret" }},
		{"fragment", func(c *domain.DevMachineCheckout) { c.RepositoryURL += "#secret" }},
		{"traversal-repository", func(c *domain.DevMachineCheckout) { c.RepositoryFullName = "../strifira" }},
		{"workspace-root", func(c *domain.DevMachineCheckout) { c.WorkspacePath = "/workspace" }},
		{"workspace-traversal", func(c *domain.DevMachineCheckout) { c.WorkspacePath = "/workspace/tasks/../repository" }},
		{"nested-task", func(c *domain.DevMachineCheckout) { c.WorkspacePath = "/workspace/tasks/TS-279/nested" }},
		{"missing-base", func(c *domain.DevMachineCheckout) { c.BaseBranch = "" }},
		{"branch-option", func(c *domain.DevMachineCheckout) { c.WorkingBranch = "--upload-pack=evil" }},
		{"branch-traversal", func(c *domain.DevMachineCheckout) { c.WorkingBranch = "feat/../evil" }},
		{"branch-script", func(c *domain.DevMachineCheckout) { c.WorkingBranch = "feat/$(evil)" }},
		{"branch-leading-slash", func(c *domain.DevMachineCheckout) { c.WorkingBranch = "/feat" }},
		{"branch-trailing-slash", func(c *domain.DevMachineCheckout) { c.WorkingBranch = "feat/" }},
		{"branch-empty-component", func(c *domain.DevMachineCheckout) { c.WorkingBranch = "feat//task" }},
		{"branch-dot-component", func(c *domain.DevMachineCheckout) { c.WorkingBranch = "feat/.task" }},
		{"branch-lock-component", func(c *domain.DevMachineCheckout) { c.WorkingBranch = "feat.lock/task" }},
		{"branch-trailing-dot", func(c *domain.DevMachineCheckout) { c.WorkingBranch = "feat/task." }},
	} {
		t.Run(test.name, func(t *testing.T) {
			checkout := valid
			test.mutate(&checkout)
			_, _, err := protectedCheckoutRepository(&checkout)
			require.Error(t, err)
			// Invalid descriptors must fail before any Docker or network access.
			runtime := &DockerRuntime{}
			require.Error(t, runtime.PrepareCheckout(t.Context(), fixtureHelperMachine(), nil, &checkout, "unused"))
		})
	}
	otherHost := valid
	otherHost.RepositoryURL = "https://another.example/TerraScale/strifira"
	otherRepository, _, err := protectedCheckoutRepository(&otherHost)
	require.NoError(t, err)
	require.NotEqual(t, repository, otherRepository, "protected stores must be scoped to the authorized GitHub origin")
	runtime := &DockerRuntime{}
	require.Error(t, runtime.PrepareCheckout(t.Context(), nil, nil, &valid, "unused"))
	require.Error(t, runtime.PrepareCheckout(t.Context(), fixtureHelperMachine(), nil, &valid, ""))
}

func TestProtectedCheckoutStageUsesDockerAuthority(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "foreign-machine", "foreign-volume", "missing-config", "mounted-record"} {
		t.Run(mode, func(t *testing.T) {
			machine := fixtureHelperMachine()
			checkoutID := uuid.New()
			record := fixtureLayoutRecord(machine)
			switch mode {
			case "foreign-machine":
				record.Config.Labels["com.strifira.machine-id"] = uuid.NewString()
			case "foreign-volume":
				record.Config.Labels["com.strifira.layout-volume"] = "different-volume"
			case "missing-config":
				record.Config = nil
			case "mounted-record":
				record.HostConfig.Mounts = []mount.Mount{{Source: "writable-marker"}}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodGet || req.URL.Path != "/v1.47/containers/"+workspaceLayoutName(machine)+"/json" {
					t.Errorf("stage authority must only inspect the exact existing record: %s %s", req.Method, req.URL.Path)
					http.NotFound(w, req)
					return
				}
				if mode == "missing" {
					http.NotFound(w, req)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(record)
			}))
			t.Cleanup(server.Close)
			api, err := client.NewClientWithOpts(client.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")), client.WithAPIVersion("1.47"))
			require.NoError(t, err)
			t.Cleanup(func() { _ = api.Close() })
			runtime := &DockerRuntime{client: api}
			key, err := runtime.checkoutStageKey(t.Context(), machine, checkoutID)
			if mode != "valid" {
				require.Error(t, err)
				require.Empty(t, key, "workspace markers cannot substitute for missing or foreign Docker authority")
				return
			}
			require.NoError(t, err)
			require.Regexp(t, `^[a-f0-9]{64}$`, key)
			restarted := &DockerRuntime{client: api}
			retryKey, err := restarted.checkoutStageKey(t.Context(), machine, checkoutID)
			require.NoError(t, err)
			require.Equal(t, key, retryKey, "backend restart must retain the recoverable stage name")
			otherKey, err := runtime.checkoutStageKey(t.Context(), machine, uuid.New())
			require.NoError(t, err)
			require.NotEqual(t, key, otherKey, "another checkout must never authorize cleanup of this checkout's stage")
		})
	}
	for _, machine := range []*domain.DevMachine{nil, {}, {ID: uuid.New()}, {ID: uuid.New(), WorkspaceVolumeName: new("")}} {
		_, err := (&DockerRuntime{}).checkoutStageKey(t.Context(), machine, uuid.New())
		require.Error(t, err)
	}
	_, err := (&DockerRuntime{}).checkoutStageKey(t.Context(), fixtureHelperMachine(), uuid.Nil)
	require.Error(t, err)
}

func checkoutFixtureTools(t *testing.T, f *protectedRepositoryFixture) {
	t.Helper()
	for _, name := range []string{"cp", "sync"} {
		path, err := exec.LookPath(name)
		require.NoError(t, err)
		path, err = filepath.EvalSymlinks(path)
		require.NoError(t, err)
		f.toolPath += filepath.Dir(path) + string(os.PathListSeparator)
	}
}

func TestProtectedCheckoutInstallsWithoutCredentialsOrHistoryCopy(t *testing.T) {
	f := newProtectedRepositoryFixture(t)
	checkoutFixtureTools(t, f)
	root := filepath.Join(f.root, "checkouts")
	require.NoError(t, os.Mkdir(root, 0o700))
	workspace := filepath.Join(root, "tasks", "TS-279")
	before := f.authenticated.Load()
	output, err := runCheckoutFixture(t, f, installProtectedCheckoutScript, root, workspace, f.repository, f.repositoryURL, f.branch, f.run)
	require.NoError(t, err, output)
	require.Equal(t, before, f.authenticated.Load(), "installer must never authenticate or use the network")
	require.Equal(t, f.base, f.git(t, workspace, "rev-parse", "HEAD"))
	require.Equal(t, f.branch, f.git(t, workspace, "branch", "--show-current"))
	require.Equal(t, filepath.Join(f.repository, "objects"), strings.TrimSpace(readCheckoutFixtureFile(t, filepath.Join(workspace, ".git", "objects", "info", "alternates"))))
	packs, err := os.ReadDir(filepath.Join(workspace, ".git", "objects", "pack"))
	require.NoError(t, err)
	require.Empty(t, packs)
	require.Equal(t, "base\n", readCheckoutFixtureFile(t, filepath.Join(workspace, "source.txt")))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("later edit\n"), 0o640))
	index := readCheckoutFixtureFile(t, filepath.Join(workspace, ".git", "index"))
	output, err = runCheckoutFixture(t, f, installProtectedCheckoutScript, root, workspace, f.repository, f.repositoryURL, f.branch, f.run)
	require.NoError(t, err, output)
	require.Equal(t, "later edit\n", readCheckoutFixtureFile(t, filepath.Join(workspace, "source.txt")))
	require.Equal(t, index, readCheckoutFixtureFile(t, filepath.Join(workspace, ".git", "index")))
	assertCheckoutNoCredential(t, workspace)
}

func TestProtectedCheckoutDefaultPreservesOwnerBranchAfterMachineSelectionChanges(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(fmt.Sprintf("linked-%t", linked), func(t *testing.T) {
			f := newProtectedRepositoryFixture(t)
			checkoutFixtureTools(t, f)
			root := filepath.Join(f.root, "checkouts")
			workspace := filepath.Join(root, "repository")
			const ownerBranch = "owner/current"
			require.NoError(t, os.MkdirAll(filepath.Join(root, "repos"), 0o700))
			if linked {
				cache := filepath.Join(root, "repos", "legacy.git")
				f.git(t, f.root, "clone", "--quiet", "--bare", f.workspace, cache)
				f.git(t, f.root, "--git-dir="+cache, "worktree", "add", "--quiet", "-b", ownerBranch, workspace, f.branch)
			} else {
				f.git(t, f.root, "clone", "--quiet", f.workspace, workspace)
				f.git(t, workspace, "switch", "--quiet", "-c", ownerBranch)
			}
			require.NoError(t, os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("owner staged\n"), 0o600))
			f.git(t, workspace, "add", "source.txt")
			require.NoError(t, os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("owner unstaged\n"), 0o600))
			head := f.git(t, workspace, "rev-parse", "HEAD")
			index := readCheckoutFixtureFile(t, f.git(t, workspace, "rev-parse", "--path-format=absolute", "--git-path", "index"))
			for range 2 {
				output, err := runCheckoutFixture(t, f, installProtectedCheckoutScript, root, workspace, f.repository, f.repositoryURL, f.branch, f.run)
				require.NoError(t, err, output)
				require.Equal(t, ownerBranch, f.git(t, workspace, "branch", "--show-current"))
				require.Equal(t, head, f.git(t, workspace, "rev-parse", "HEAD"))
				require.Equal(t, index, readCheckoutFixtureFile(t, filepath.Join(workspace, ".git", "index")))
				require.Equal(t, "owner staged", f.git(t, workspace, "show", ":source.txt"))
				require.Equal(t, "owner unstaged\n", readCheckoutFixtureFile(t, filepath.Join(workspace, "source.txt")))
				require.Equal(t, "refs/heads/"+ownerBranch, f.git(t, workspace, "config", "branch."+ownerBranch+".merge"))
			}
		})
	}
}

func TestProtectedCheckoutConvertsDirtyLinkedWorktree(t *testing.T) {
	for _, interruption := range []string{"none", "before-pointer-rename", "after-pointer-rename", "after-directory-install", "after-directory-install-later-work"} {
		t.Run(interruption, func(t *testing.T) {
			f := newProtectedRepositoryFixture(t)
			checkoutFixtureTools(t, f)
			root := filepath.Join(f.root, "checkouts")
			cache := filepath.Join(root, "repos", "legacy.git")
			workspace := filepath.Join(root, "tasks", "TS-279")
			require.NoError(t, os.MkdirAll(filepath.Dir(cache), 0o700))
			f.git(t, f.root, "clone", "--quiet", "--bare", f.workspace, cache)
			f.git(t, f.root, "--git-dir="+cache, "worktree", "add", "--quiet", workspace, f.branch)
			f.git(t, workspace, "config", "user.name", "fixture")
			f.git(t, workspace, "config", "user.email", "fixture@localhost")
			require.NoError(t, os.WriteFile(filepath.Join(workspace, "committed.txt"), []byte("local commit\n"), 0o600))
			f.git(t, workspace, "add", ".")
			f.git(t, workspace, "commit", "--quiet", "-m", "local work")
			require.NoError(t, os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("staged\n"), 0o640))
			f.git(t, workspace, "add", "source.txt")
			require.NoError(t, os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("staged and later edit\n"), 0o640))
			require.NoError(t, os.WriteFile(filepath.Join(workspace, "untracked.txt"), []byte("untracked\n"), 0o600))
			f.git(t, workspace, "config", "credential.helper", "!echo "+protectedFixtureToken)
			head := f.git(t, workspace, "rev-parse", "HEAD")
			indexPath := f.git(t, workspace, "rev-parse", "--path-format=absolute", "--git-path", "index")
			index := readCheckoutFixtureFile(t, indexPath)
			beforeAuth := f.authenticated.Load()
			stagedContent, workingContent := "staged", "staged and later edit\n"
			if interruption != "none" {
				needle := map[string]string{
					"before-pointer-rename":              `    mv -T -n -- "$workspace_path/.git" "$backup"`,
					"after-pointer-rename":               `    [ ! -e "$workspace_path/.git" ]`,
					"after-directory-install":            `    [ ! -e "$stage/git" ]`,
					"after-directory-install-later-work": `    [ ! -e "$stage/git" ]`,
				}[interruption]
				require.Equal(t, 1, strings.Count(installProtectedCheckoutScript, needle))
				// kill -9 exercises missing EXIT cleanup and persisted Git locks.
				crashed := strings.Replace(installProtectedCheckoutScript, needle, needle+"\nkill -KILL $$", 1)
				output, err := runCheckoutFixture(t, f, crashed, root, workspace, f.repository, f.repositoryURL, f.branch, f.run)
				require.Error(t, err, output)
				require.DirExists(t, filepath.Join(root, ".strifira-checkout-"+checkoutFixtureStageKey(f)), "SIGKILL must exercise a persisted stage, not EXIT cleanup")
			}
			if interruption == "after-directory-install-later-work" {
				f.git(t, workspace, "config", "user.name", "fixture")
				f.git(t, workspace, "config", "user.email", "fixture@localhost")
				f.git(t, workspace, "commit", "--quiet", "-m", "later commit after installer interruption")
				head = f.git(t, workspace, "rev-parse", "HEAD")
				stagedContent, workingContent = "new staged content", "even later file edit\n"
				require.NoError(t, os.WriteFile(filepath.Join(workspace, "source.txt"), []byte(stagedContent+"\n"), 0o600))
				f.git(t, workspace, "add", "source.txt")
				require.NoError(t, os.WriteFile(filepath.Join(workspace, "source.txt"), []byte(workingContent), 0o600))
				index = readCheckoutFixtureFile(t, filepath.Join(workspace, ".git", "index"))
			}
			output, err := runCheckoutFixture(t, f, installProtectedCheckoutScript, root, workspace, f.repository, f.repositoryURL, f.branch, f.run)
			require.NoError(t, err, output)
			require.Equal(t, beforeAuth, f.authenticated.Load())
			require.DirExists(t, filepath.Join(workspace, ".git"))
			require.Equal(t, head, f.git(t, workspace, "rev-parse", "HEAD"))
			require.Equal(t, f.branch, f.git(t, workspace, "branch", "--show-current"))
			require.Equal(t, index, readCheckoutFixtureFile(t, filepath.Join(workspace, ".git", "index")))
			require.Equal(t, stagedContent, f.git(t, workspace, "show", ":source.txt"))
			f.git(t, workspace, "fsck", "--connectivity-only", "--no-dangling")
			assertCheckoutStageRemoved(t, f, root)
			require.NoFileExists(t, filepath.Join(workspace, ".git.strifira-linked-backup"))
			require.NoFileExists(t, indexPath+".lock")
			require.Equal(t, workingContent, readCheckoutFixtureFile(t, filepath.Join(workspace, "source.txt")))
			require.Equal(t, "local commit\n", readCheckoutFixtureFile(t, filepath.Join(workspace, "committed.txt")))
			require.Equal(t, "untracked\n", readCheckoutFixtureFile(t, filepath.Join(workspace, "untracked.txt")))
			assertCheckoutNoCredential(t, workspace)
		})
	}
}

func TestProtectedCheckoutRecoversConfigAndNewInstallInterruptions(t *testing.T) {
	for _, mode := range []string{"new-before-install", "new-after-install", "config-before-replace", "config-after-replace"} {
		t.Run(mode, func(t *testing.T) {
			f := newProtectedRepositoryFixture(t)
			checkoutFixtureTools(t, f)
			root := filepath.Join(f.root, "checkouts")
			workspace := filepath.Join(root, "repository")
			require.NoError(t, os.Mkdir(root, 0o700))
			unrelated := filepath.Join(root, ".strifira-checkout-user-notes")
			require.NoError(t, os.Mkdir(unrelated, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(unrelated, "keep.txt"), []byte("user work\n"), 0o600))
			var index string
			if strings.HasPrefix(mode, "config-") {
				output, err := runCheckoutFixture(t, f, installProtectedCheckoutScript, root, workspace, f.repository, f.repositoryURL, f.branch, f.run)
				require.NoError(t, err, output)
				f.git(t, workspace, "config", "credential.helper", "!echo "+protectedFixtureToken)
				require.NoError(t, os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("staged change\n"), 0o600))
				f.git(t, workspace, "add", "source.txt")
				index = readCheckoutFixtureFile(t, filepath.Join(workspace, ".git", "index"))
			}
			needle := map[string]string{
				"new-before-install":    `  mv -T -n -- "$stage/new" "$workspace_path"`,
				"new-after-install":     `  [ ! -e "$stage/new" ] ||`,
				"config-before-replace": `  mv -T -f -- "$stage/config.new" "$destination/config"`,
				"config-after-replace":  `  rm -f -- "$config_lock"`,
			}[mode]
			require.Equal(t, 1, strings.Count(installProtectedCheckoutScript, needle))
			crashed := strings.Replace(installProtectedCheckoutScript, needle, "kill -KILL $$\n"+needle, 1)
			output, err := runCheckoutFixture(t, f, crashed, root, workspace, f.repository, f.repositoryURL, f.branch, f.run)
			require.Error(t, err, output)
			require.DirExists(t, filepath.Join(root, ".strifira-checkout-"+checkoutFixtureStageKey(f)))
			if mode != "new-before-install" {
				require.NoError(t, os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("later edit after interruption\n"), 0o600))
				if index == "" {
					index = readCheckoutFixtureFile(t, filepath.Join(workspace, ".git", "index"))
				}
			}
			output, err = runCheckoutFixture(t, f, installProtectedCheckoutScript, root, workspace, f.repository, f.repositoryURL, f.branch, f.run)
			require.NoError(t, err, output)
			if mode == "new-before-install" {
				require.Equal(t, "base\n", readCheckoutFixtureFile(t, filepath.Join(workspace, "source.txt")))
			} else {
				require.Equal(t, "later edit after interruption\n", readCheckoutFixtureFile(t, filepath.Join(workspace, "source.txt")))
				require.Equal(t, index, readCheckoutFixtureFile(t, filepath.Join(workspace, ".git", "index")))
			}
			if strings.HasPrefix(mode, "config-") {
				require.Equal(t, "staged change", f.git(t, workspace, "show", ":source.txt"))
			}
			assertCheckoutStageRemoved(t, f, root)
			assertCheckoutNoCredential(t, workspace)
			require.NoFileExists(t, filepath.Join(workspace, ".git", "config.lock"))
			require.NoFileExists(t, filepath.Join(workspace, ".git", "config.new"))
			require.Equal(t, "user work\n", readCheckoutFixtureFile(t, filepath.Join(unrelated, "keep.txt")))
		})
	}
}

func TestProtectedCheckoutPreservesAllIndexStagesAndUnusualFilenames(t *testing.T) {
	f := newProtectedRepositoryFixture(t)
	checkoutFixtureTools(t, f)
	root := filepath.Join(f.root, "checkouts")
	cache := filepath.Join(root, "repos", "legacy.git")
	workspace := filepath.Join(root, "tasks", "TS-279")
	require.NoError(t, os.MkdirAll(filepath.Dir(cache), 0o700))
	f.git(t, f.root, "clone", "--quiet", "--bare", f.workspace, cache)
	f.git(t, f.root, "--git-dir="+cache, "worktree", "add", "--quiet", workspace, f.branch)
	filename := "conflicted space\nname.txt"
	var entries strings.Builder
	for stage, content := range []string{"conflict base", "conflict ours", "conflict theirs"} {
		path := filepath.Join(f.root, fmt.Sprintf("blob-%d", stage))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		object := f.git(t, workspace, "hash-object", "-w", path)
		fmt.Fprintf(&entries, "100644 %s %d\t%s\x00", object, stage+1, filename)
	}
	command := exec.CommandContext(t.Context(), f.gitPath, "-C", workspace, "update-index", "-z", "--index-info")
	command.Env = []string{"PATH=" + f.toolPath, "HOME=" + f.root, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
	command.Stdin = strings.NewReader(entries.String())
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	require.NoError(t, os.Symlink("untracked/target", filepath.Join(workspace, "staged-link")))
	f.git(t, workspace, "add", "staged-link")
	// A gitlink references a different repository; it must stay in the index
	// without requiring a submodule clone or importing its absent commit.
	f.git(t, workspace, "update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("1", 40)+",vendor/module")
	indexPath := f.git(t, workspace, "rev-parse", "--path-format=absolute", "--git-path", "index")
	index := readCheckoutFixtureFile(t, indexPath)
	beforeAuth := f.authenticated.Load()
	installed, err := runCheckoutFixture(t, f, installProtectedCheckoutScript, root, workspace, f.repository, f.repositoryURL, f.branch, f.run)
	require.NoError(t, err, installed)
	require.Equal(t, beforeAuth, f.authenticated.Load())
	require.Equal(t, index, readCheckoutFixtureFile(t, filepath.Join(workspace, ".git", "index")))
	for stage, content := range []string{"conflict base", "conflict ours", "conflict theirs"} {
		require.Equal(t, content, f.git(t, workspace, "show", fmt.Sprintf(":%d:%s", stage+1, filename)))
	}
	require.Equal(t, "untracked/target", f.git(t, workspace, "show", ":staged-link"))
	f.git(t, workspace, "fsck", "--connectivity-only", "--no-dangling")
	assertCheckoutStageRemoved(t, f, root)
}

func TestProtectedCheckoutRejectsConcurrentMetadataChanges(t *testing.T) {
	for _, mode := range []string{"index", "pointer"} {
		t.Run(mode, func(t *testing.T) {
			f := newProtectedRepositoryFixture(t)
			checkoutFixtureTools(t, f)
			root := filepath.Join(f.root, "checkouts")
			cache := filepath.Join(root, "repos", "legacy.git")
			workspace := filepath.Join(root, "tasks", "TS-279")
			require.NoError(t, os.MkdirAll(filepath.Dir(cache), 0o700))
			f.git(t, f.root, "clone", "--quiet", "--bare", f.workspace, cache)
			f.git(t, f.root, "--git-dir="+cache, "worktree", "add", "--quiet", workspace, f.branch)
			pointer := readCheckoutFixtureFile(t, filepath.Join(workspace, ".git"))
			indexPath := f.git(t, workspace, "rev-parse", "--path-format=absolute", "--git-path", "index")
			// Simulate an independent writer changing metadata despite a Git
			// lock. The installer must preserve that writer's bytes and refuse
			// the conversion instead of using its earlier index/pointer snapshot.
			needle := `    current_index_hash=$(sha256sum "$source_index")`
			change := `    git --git-dir="$stage/git" read-tree --empty
    cp -- "$stage/git/index" "$source_index"
`
			if mode == "pointer" {
				change = `    printf '\n' >> "$workspace_path/.git"
`
			}
			require.Equal(t, 1, strings.Count(installProtectedCheckoutScript, needle))
			changed := strings.Replace(installProtectedCheckoutScript, needle, change+needle, 1)
			output, err := runCheckoutFixture(t, f, changed, root, workspace, f.repository, f.repositoryURL, f.branch, f.run)
			require.Error(t, err, output)
			if mode == "pointer" {
				require.Equal(t, pointer+"\n", readCheckoutFixtureFile(t, filepath.Join(workspace, ".git")))
			} else {
				require.Equal(t, pointer, readCheckoutFixtureFile(t, filepath.Join(workspace, ".git")))
				require.Empty(t, f.git(t, workspace, "ls-files"), "the independent writer's empty index must not be rolled back")
			}
			require.NoFileExists(t, indexPath+".lock")
			require.NoFileExists(t, filepath.Join(workspace, ".git.strifira-linked-backup"))
			assertCheckoutStageRemoved(t, f, root)
		})
	}
}

func TestProtectedCheckoutRejectsHostilePathsAndForeignLocks(t *testing.T) {
	for _, mode := range []string{"workspace-symlink", "parent-symlink", "branch-changed", "foreign-index-lock", "outside-cache", "stage-symlink", "stage-file"} {
		t.Run(mode, func(t *testing.T) {
			f := newProtectedRepositoryFixture(t)
			checkoutFixtureTools(t, f)
			root := filepath.Join(f.root, "checkouts")
			cache := filepath.Join(root, "repos", "legacy.git")
			workspace := filepath.Join(root, "tasks", "TS-279")
			require.NoError(t, os.MkdirAll(filepath.Dir(cache), 0o700))
			if mode == "outside-cache" {
				cache = filepath.Join(f.root, "outside.git")
			}
			f.git(t, f.root, "clone", "--quiet", "--bare", f.workspace, cache)
			f.git(t, f.root, "--git-dir="+cache, "worktree", "add", "--quiet", workspace, f.branch)
			pointer := readCheckoutFixtureFile(t, filepath.Join(workspace, ".git"))
			stagePath := filepath.Join(root, ".strifira-checkout-"+checkoutFixtureStageKey(f))
			if mode == "stage-symlink" {
				require.NoError(t, os.Symlink(workspace, stagePath))
			}
			if mode == "stage-file" {
				require.NoError(t, os.WriteFile(stagePath, []byte("user-owned-file"), 0o600))
			}
			if mode == "foreign-index-lock" {
				index := f.git(t, workspace, "rev-parse", "--path-format=absolute", "--git-path", "index")
				require.NoError(t, os.WriteFile(index+".lock", []byte("user-owned-lock"), 0o600))
			}
			if mode == "branch-changed" {
				f.git(t, workspace, "checkout", "-b", "another")
			}
			if mode == "workspace-symlink" {
				require.NoError(t, os.Rename(workspace, workspace+"-original"))
				require.NoError(t, os.Symlink(workspace+"-original", workspace))
			}
			if mode == "parent-symlink" {
				parent := filepath.Dir(workspace)
				require.NoError(t, os.Rename(parent, parent+"-original"))
				require.NoError(t, os.Symlink(parent+"-original", parent))
			}
			output, err := runCheckoutFixture(t, f, installProtectedCheckoutScript, root, workspace, f.repository, f.repositoryURL, f.branch, f.run)
			require.Error(t, err, output)
			require.Equal(t, pointer, readCheckoutFixtureFile(t, filepath.Join(workspace, ".git")))
			if mode == "stage-file" {
				require.Equal(t, "user-owned-file", readCheckoutFixtureFile(t, stagePath))
			}
		})
	}
}

func checkoutFixtureStageKey(f *protectedRepositoryFixture) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(f.run)))
}

func runCheckoutFixture(t *testing.T, f *protectedRepositoryFixture, script string, args ...string) (string, error) {
	t.Helper()
	return f.script(t, script, false, append(args, checkoutFixtureStageKey(f))...)
}

func readCheckoutFixtureFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func assertCheckoutNoCredential(t *testing.T, workspace string) {
	t.Helper()
	config := readCheckoutFixtureFile(t, filepath.Join(workspace, ".git", "config"))
	require.Contains(t, config, "helper =\n")
	require.NotContains(t, config, protectedFixtureToken)
	require.NotContains(t, config, "include")
	require.NoError(t, filepath.WalkDir(filepath.Join(workspace, ".git"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() && entry.Name() == "config" {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(data), protectedFixtureToken) {
				return fmt.Errorf("credential leaked: %x", sha256.Sum256(data))
			}
		}
		return nil
	}))
}

func assertCheckoutStageRemoved(t *testing.T, f *protectedRepositoryFixture, root string) {
	t.Helper()
	_, err := os.Lstat(filepath.Join(root, ".strifira-checkout-"+checkoutFixtureStageKey(f)))
	require.ErrorIs(t, err, os.ErrNotExist)
}
