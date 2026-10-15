package machine

import (
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type protectedRepositoryFixture struct {
	scriptDir                           string
	root, remote, repository, workspace string
	branch, base, run                   string
	certificate, repositoryURL, host    string
	gitPath, toolPath                   string
	authenticated                       atomic.Int32
	redirect                            atomic.Pointer[string]
}

const protectedFixtureToken = "disposable-protected-repository-token"

func (f *protectedRepositoryFixture) git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), f.gitPath, append([]string{"-C", dir}, args...)...)
	command.Env = []string{"PATH=" + f.toolPath, "HOME=" + f.root, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	return strings.TrimSpace(string(output))
}

func (f *protectedRepositoryFixture) script(t *testing.T, source string, token bool, args ...string) (string, error) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "/bin/sh", append([]string{"-c", source, "strifira-protected-fixture"}, args...)...)
	command.Dir = f.scriptDir
	command.Env = []string{"PATH=" + f.toolPath, "HOME=" + f.root,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/bin/false", "GIT_SSL_CAINFO=" + f.certificate}
	if token {
		command.Stdin = strings.NewReader(protectedFixtureToken + "\n")
	}
	output, err := command.CombinedOutput()
	if strings.Contains(string(output), protectedFixtureToken) {
		return "", errors.New("repository fixture exposed its disposable credential")
	}
	return string(output), err
}

func newProtectedRepositoryFixture(t *testing.T) *protectedRepositoryFixture {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	require.NoError(t, err)
	gitPath, err = filepath.EvalSymlinks(gitPath)
	require.NoError(t, err)
	f := &protectedRepositoryFixture{root: t.TempDir(), branch: "strifira/task", run: uuid.NewString(), gitPath: gitPath}
	// Resolve fixture tools explicitly on NixOS too. Product helpers use the
	// image's immutable /usr/bin:/bin instead of a host or IDE tool directory.
	for _, name := range []string{"git", "mkdir", "realpath", "sha256sum", "mktemp", "cat", "awk", "rm", "mv", "flock", "sync"} {
		path, err := exec.LookPath(name)
		require.NoError(t, err)
		path, err = filepath.EvalSymlinks(path)
		require.NoError(t, err)
		f.toolPath += filepath.Dir(path) + string(os.PathListSeparator)
	}
	f.remote = filepath.Join(f.root, "remote", "selected.git")
	f.repository = filepath.Join(f.root, "protected", "selected.git")
	f.workspace = filepath.Join(f.root, "workspace")
	seed := filepath.Join(f.root, "seed")
	require.NoError(t, os.MkdirAll(seed, 0o700))
	f.git(t, seed, "init", "--quiet", "--initial-branch=main")
	f.git(t, seed, "config", "user.name", "fixture")
	f.git(t, seed, "config", "user.email", "fixture@localhost")
	require.NoError(t, os.WriteFile(filepath.Join(seed, "source.txt"), []byte("base\n"), 0o600))
	f.git(t, seed, "add", ".")
	f.git(t, seed, "commit", "--quiet", "-m", "base")
	f.base = f.git(t, seed, "rev-parse", "HEAD")
	require.NoError(t, os.MkdirAll(filepath.Dir(f.remote), 0o700))
	f.git(t, seed, "clone", "--quiet", "--bare", "--", seed, f.remote)
	f.git(t, f.remote, "config", "http.receivepack", "true")
	backend := filepath.Join(f.git(t, seed, "--exec-path"), "git-http-backend")
	_, err = os.Stat(backend)
	require.NoError(t, err)
	cgiHandler := &cgi.Handler{Path: backend, Dir: filepath.Dir(f.remote), Env: []string{"GIT_PROJECT_ROOT=" + filepath.Dir(f.remote), "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=fixture"}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/selected.git/") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "x-access-token" || password != protectedFixtureToken {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.authenticated.Add(1)
		if redirect := f.redirect.Load(); redirect != nil {
			w.Header().Set("Location", *redirect)
			w.WriteHeader(http.StatusFound)
			return
		}
		cgiHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)
	f.host, f.repositoryURL = parsed.Host, server.URL+"/selected.git"
	f.certificate = filepath.Join(f.root, "fixture-ca.pem")
	require.NoError(t, os.WriteFile(f.certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600))
	f.prepare(t)
	f.git(t, seed, "-c", "protocol.file.allow=always", "clone", "--quiet", "--local", "--shared", "--no-checkout", "--", f.repository, f.workspace)
	f.git(t, f.workspace, "switch", "--quiet", "-c", f.branch, f.base)
	f.git(t, f.workspace, "remote", "set-url", "origin", f.repositoryURL)
	return f
}

func (f *protectedRepositoryFixture) prepare(t *testing.T) {
	t.Helper()
	output, err := f.script(t, prepareProtectedRepositoryScript, true, f.repository, f.repositoryURL, "main", f.branch, f.host, "selected", "", f.run)
	require.NoError(t, err, output)
}

func (f *protectedRepositoryFixture) snapshot(t *testing.T) RepositoryCandidate {
	t.Helper()
	output, err := f.script(t, retainRepositoryCandidateScript, false, f.workspace, f.repository, f.run)
	require.NoError(t, err, output)
	candidate, err := parseRepositoryCandidate(output)
	require.NoError(t, err, output)
	return candidate
}

func (f *protectedRepositoryFixture) publish(t *testing.T, candidate RepositoryCandidate) {
	t.Helper()
	output, err := f.script(t, publishRetainedRepositoryCandidateScript, true, f.repository, f.repositoryURL, f.branch, f.host, "selected", "", f.run, candidate.Commit)
	require.NoError(t, err, output)
}

func TestRetainedRepositoryCandidateCannotHideChangesWithWritableBaseRefs(t *testing.T) {
	f := newProtectedRepositoryFixture(t)
	marker := filepath.Join(f.root, "writable-hook-executed")
	f.git(t, f.workspace, "config", "user.name", "fixture")
	f.git(t, f.workspace, "config", "user.email", "fixture@localhost")
	require.NoError(t, os.WriteFile(filepath.Join(f.workspace, ".env"), []byte("forbidden=new\n"), 0o600))
	f.git(t, f.workspace, "add", ".")
	f.git(t, f.workspace, "commit", "--quiet", "-m", "forbidden")
	f.git(t, f.workspace, "update-ref", "refs/remotes/origin/main", "HEAD")
	f.git(t, f.workspace, "config", "core.fsmonitor", "touch "+marker)
	f.git(t, f.workspace, "config", "core.sshCommand", "touch "+marker)
	f.git(t, f.workspace, "config", "credential.helper", "!touch "+marker)
	f.git(t, f.workspace, "config", "url.ssh://attacker.invalid/.insteadOf", "https://")
	require.NoError(t, os.WriteFile(filepath.Join(f.workspace, ".git", "hooks", "post-commit"), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o700))
	candidate := f.snapshot(t)
	require.Equal(t, f.base, candidate.BaseCommit)
	require.Equal(t, []string{".env"}, candidate.ChangedFiles)
	require.NoFileExists(t, marker)
	violations, err := forbiddenChangedPaths([]byte(`[".env"]`), candidate.ChangedFiles)
	require.NoError(t, err)
	require.Equal(t, []string{".env"}, violations)
}

func TestRetainedRepositoryPublishesExactlyVerifiedCandidateAndKeepsItsObjects(t *testing.T) {
	f := newProtectedRepositoryFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.workspace, "source.txt"), []byte("verified\n"), 0o600))
	candidate := f.snapshot(t)
	require.True(t, candidate.Dirty)
	require.Equal(t, []string{"source.txt"}, candidate.ChangedFiles)
	require.Equal(t, []string{candidate.Commit}, candidate.Commits)
	require.NoError(t, os.WriteFile(filepath.Join(f.workspace, "source.txt"), []byte("later user edit\n"), 0o600))
	replayed := f.snapshot(t)
	require.Equal(t, candidate, replayed)
	marker := filepath.Join(f.root, "writable-config-executed")
	f.git(t, f.workspace, "config", "core.sshCommand", "touch "+marker)
	f.git(t, f.workspace, "config", "url.ssh://attacker.invalid/.insteadOf", "https://")
	f.publish(t, candidate)
	require.Equal(t, candidate.Commit, f.git(t, f.remote, "rev-parse", "refs/heads/"+f.branch))
	require.Equal(t, "verified", f.git(t, f.remote, "show", candidate.Commit+":source.txt"))
	require.Equal(t, candidate.Commit, f.git(t, f.repository, "rev-parse", "refs/strifira/runs/"+f.run+"/candidate"))
	require.NoFileExists(t, filepath.Join(f.repository, "objects", "info", "alternates"))
	require.NoFileExists(t, marker)
	require.Positive(t, f.authenticated.Load())
	contents, err := os.ReadFile(filepath.Join(f.workspace, "source.txt"))
	require.NoError(t, err)
	require.Equal(t, "later user edit\n", string(contents))
	output, err := f.script(t, publishRetainedRepositoryCandidateScript, true, f.repository, f.repositoryURL, f.branch, f.host, "selected", "", f.run, f.base)
	require.Error(t, err, output)
	// A normal next run uses the retained published commit, avoiding sibling
	// automatic commits and a second push's non-fast-forward rejection.
	f.git(t, f.workspace, "-c", "core.hooksPath=/dev/null", "update-ref", "refs/heads/"+f.branch, candidate.Commit, candidate.SourceCommit)
	f.git(t, f.workspace, "-c", "core.fsmonitor=false", "read-tree", candidate.Commit)
	f.run = uuid.NewString()
	f.prepare(t)
	next := f.snapshot(t)
	require.Equal(t, candidate.Commit, next.SourceCommit)
	f.publish(t, next)
	require.Equal(t, next.Commit, f.git(t, f.remote, "rev-parse", "refs/heads/"+f.branch))
	require.Equal(t, candidate.Commit, f.git(t, f.remote, "rev-parse", next.Commit+"^"))
}

func TestRetainedRepositoryPinsTheBaseBeforeUpstreamMoves(t *testing.T) {
	f := newProtectedRepositoryFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "seed", "upstream.txt"), []byte("upstream only\n"), 0o600))
	f.git(t, filepath.Join(f.root, "seed"), "add", ".")
	f.git(t, filepath.Join(f.root, "seed"), "commit", "--quiet", "-m", "upstream moves")
	f.git(t, filepath.Join(f.root, "seed"), "push", "--quiet", f.remote, "HEAD:main")
	before := f.authenticated.Load()
	f.prepare(t)
	require.Equal(t, before, f.authenticated.Load(), "a pinned run must not be rebased by a retry")
	require.NoError(t, os.WriteFile(filepath.Join(f.workspace, "source.txt"), []byte("task\n"), 0o600))
	candidate := f.snapshot(t)
	require.Equal(t, f.base, candidate.BaseCommit)
	require.Equal(t, []string{"source.txt"}, candidate.ChangedFiles)
}

func TestRetainedRepositoryRejectsRedirectBeforeConnectingToForeignOrigin(t *testing.T) {
	f := newProtectedRepositoryFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.workspace, "source.txt"), []byte("verified\n"), 0o600))
	candidate := f.snapshot(t)
	var foreignConnections atomic.Int32
	foreign := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	foreign.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			foreignConnections.Add(1)
		}
	}
	foreign.StartTLS()
	t.Cleanup(foreign.Close)
	f.redirect.Store(new(foreign.URL + "/selected.git/info/refs?service=git-receive-pack"))
	output, err := f.script(t, publishRetainedRepositoryCandidateScript, true, f.repository, f.repositoryURL, f.branch, f.host, "selected", "", f.run, candidate.Commit)
	require.Error(t, err, output)
	require.Zero(t, foreignConnections.Load())
	require.Equal(t, candidate.Commit, f.git(t, f.repository, "rev-parse", "refs/strifira/runs/"+f.run+"/candidate"))
	lookup := exec.CommandContext(t.Context(), f.gitPath, "--git-dir="+f.remote, "show-ref", "--verify", "refs/heads/"+f.branch)
	require.Error(t, lookup.Run(), "failed publication must leave upstream unchanged")
}

func TestRetainedRepositoryConcurrentSnapshotsReturnOneCandidate(t *testing.T) {
	f := newProtectedRepositoryFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.workspace, "source.txt"), []byte("verified\n"), 0o600))
	type result struct {
		output string
		err    error
	}
	results := make(chan result, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			output, err := f.script(t, retainRepositoryCandidateScript, false, f.workspace, f.repository, f.run)
			results <- result{output: output, err: err}
		})
	}
	wg.Wait()
	close(results)
	var first *RepositoryCandidate
	for result := range results {
		require.NoError(t, result.err, result.output)
		candidate, err := parseRepositoryCandidate(result.output)
		require.NoError(t, err)
		if first == nil {
			first = new(candidate)
		} else {
			require.Equal(t, *first, candidate)
		}
	}
}

func TestRetainedRepositoryUsesProtectedWorkingDirectoryDespiteHostileCaller(t *testing.T) {
	f := newProtectedRepositoryFixture(t)
	f.scriptDir = f.workspace
	marker := filepath.Join(f.root, "caller-configuration-executed")
	f.git(t, f.workspace, "config", "core.sshCommand", "touch "+marker)
	f.git(t, f.workspace, "config", "url.ssh://attacker.invalid/.insteadOf", f.workspace)
	require.NoError(t, os.WriteFile(filepath.Join(f.workspace, "source.txt"), []byte("verified\n"), 0o600))
	output, err := f.script(t, retainRepositoryCandidateScript+`[ "$(pwd -P)" = "$repository_dir" ]`, false, f.workspace, f.repository, f.run)
	require.NoError(t, err, output)
	candidate, err := parseRepositoryCandidate(output)
	require.NoError(t, err)
	f.publish(t, candidate)
	require.NoFileExists(t, marker)
}

func TestRetainedRepositoryRejectsGitMetadataOutsideStandaloneCheckout(t *testing.T) {
	for _, kind := range []string{"Git directory", "common directory", "index"} {
		t.Run(kind, func(t *testing.T) {
			f := newProtectedRepositoryFixture(t)
			gitDir := filepath.Join(f.workspace, ".git")
			switch kind {
			case "Git directory":
				outside := filepath.Join(f.root, "outside.git")
				require.NoError(t, os.Rename(gitDir, outside))
				require.NoError(t, os.Symlink(outside, gitDir))
			case "common directory":
				outside := filepath.Join(f.root, "outside.git")
				f.git(t, f.root, "clone", "--quiet", "--bare", f.workspace, outside)
				require.NoError(t, os.WriteFile(filepath.Join(gitDir, "commondir"), []byte(outside+"\n"), 0o600))
			case "index":
				outside := filepath.Join(f.root, "outside.index")
				require.NoError(t, os.Rename(filepath.Join(gitDir, "index"), outside))
				require.NoError(t, os.Symlink(outside, filepath.Join(gitDir, "index")))
			}
			output, err := f.script(t, retainRepositoryCandidateScript, false, f.workspace, f.repository, f.run)
			require.Error(t, err, output)
			require.Contains(t, output, "outside")
			require.NoFileExists(t, filepath.Join(f.repository, "strifira-runs", f.run, "candidate.info"))
		})
	}
}

func TestRetainedRepositoryLargeHistoryUsesDiskAndImportsOnlyNewObjects(t *testing.T) {
	if os.Getenv("STRIFIRA_GIT_LARGE_FIXTURE") != "1" {
		t.Skip("opt-in disk/resource qualification for a repository larger than helper tmpfs")
	}
	f := newProtectedRepositoryFixture(t)
	seed := filepath.Join(f.root, "seed")
	large, err := os.Create(filepath.Join(seed, "large.bin"))
	require.NoError(t, err)
	_, err = io.CopyN(large, rand.Reader, 145*1024*1024)
	require.NoError(t, err)
	require.NoError(t, large.Close())
	f.git(t, seed, "add", ".")
	f.git(t, seed, "commit", "--quiet", "-m", "large history")
	f.git(t, seed, "push", "--quiet", f.remote, "HEAD:main")
	f.run = uuid.NewString()
	f.prepare(t)
	base := f.git(t, f.repository, "rev-parse", "refs/strifira/runs/"+f.run+"/base")
	f.git(t, f.workspace, "reset", "--hard", base)
	require.NoError(t, os.WriteFile(filepath.Join(f.workspace, "source.txt"), []byte("small task in large repo\n"), 0o600))
	objectBytes := func() int64 {
		t.Helper()
		var total int64
		err := filepath.WalkDir(filepath.Join(f.repository, "objects"), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type().IsRegular() {
				info, err := entry.Info()
				if err != nil {
					return err
				}
				total += info.Size()
			}
			return nil
		})
		require.NoError(t, err)
		return total
	}
	before := objectBytes()
	require.Greater(t, before, int64(128*1024*1024))
	candidate := f.snapshot(t)
	added := objectBytes() - before
	require.Greater(t, added, int64(0))
	require.Less(t, added, int64(1024*1024), "the candidate must not duplicate the large trusted history")
	t.Logf("retained base objects=%d bytes; candidate adds=%d bytes", before, added)
	require.Equal(t, base, candidate.BaseCommit)
	require.Equal(t, []string{"source.txt"}, candidate.ChangedFiles)
	entries, err := os.ReadDir(filepath.Join(f.repository, "strifira-runs", f.run))
	require.NoError(t, err)
	require.Len(t, entries, 1, "staging pack and alternate snapshot must be removed")
	require.Equal(t, "candidate.info", entries[0].Name())
	f.publish(t, candidate)
}
