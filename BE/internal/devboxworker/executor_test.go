package devboxworker

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/agent"
	"github.com/stretchr/testify/require"
)

func fixtureRepository(t *testing.T) Config {
	t.Helper()
	config := testConfig(t)
	repository := config.Repositories["owner/repo"]
	for _, command := range [][]string{{"init", "-b", "main"}, {"config", "user.name", "Worker Test"}, {"config", "user.email", "worker@example.test"}, {"remote", "add", "origin", repository.URL}} {
		_, err := gitCommand(t.Context(), repository.Path, command...)
		require.NoError(t, err)
	}
	require.NoError(t, os.WriteFile(filepath.Join(repository.Path, "README.md"), []byte("base\n"), 0600))
	_, err := gitCommand(t.Context(), repository.Path, "add", "README.md")
	require.NoError(t, err)
	_, err = gitCommand(t.Context(), repository.Path, "commit", "-m", "initial")
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(config.WorkRoot, 0700))
	script := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf 'changed\\n' > README.md\nprintf 'new\\n' > new.txt\nprintf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"Edited files\"}'\n"), 0700))
	config.Providers["claude-code"] = script
	return config
}

func fixtureJob(config Config) Job {
	id := uuid.NewString()
	return Job{ID: id, Provider: "claude-code", Prompt: "edit a file", RepositoryURL: config.Repositories["owner/repo"].URL, RepositoryFullName: "owner/repo", BaseBranch: "main", WorkingBranch: "ku/task-" + id[:8], MaxRuntimeSeconds: 30, TestCommand: []string{"/bin/sh", "-c", "test -f new.txt && test \"$(cat README.md)\" = changed"}}
}

func TestLocalExecutorProducesReviewPatchAndRunsTestsSeparately(t *testing.T) {
	config := fixtureRepository(t)
	index := filepath.Join(config.Repositories["owner/repo"].Path, ".git", "index")
	before, err := os.ReadFile(index)
	require.NoError(t, err)
	result := (&LocalExecutor{Config: config}).Execute(t.Context(), fixtureJob(config), func(string, string) {})
	require.Equal(t, "succeeded", result.Status, result.Summary)
	require.Equal(t, "passed", result.TestStatus)
	require.Equal(t, 0, *result.TestExitCode)
	require.ElementsMatch(t, []string{"README.md", "new.txt"}, result.ChangedFiles)
	require.Contains(t, result.Patch, "+changed")
	require.Contains(t, result.Patch, "new.txt")
	require.True(t, strings.HasPrefix(result.Branch, "ku/task-"))
	after, err := os.ReadFile(index)
	require.NoError(t, err)
	require.Equal(t, before, after, "original checkout index must stay unchanged")
	readme, err := os.ReadFile(filepath.Join(config.Repositories["owner/repo"].Path, "README.md"))
	require.NoError(t, err)
	require.Equal(t, "base\n", string(readme))
}

func TestIndependentFailureOverridesSuccessfulProvider(t *testing.T) {
	config := fixtureRepository(t)
	job := fixtureJob(config)
	job.TestCommand = []string{"/bin/sh", "-c", "exit 23"}
	result := (&LocalExecutor{Config: config}).Execute(t.Context(), job, func(string, string) {})
	require.Equal(t, "failed", result.Status)
	require.Equal(t, "failed", result.TestStatus)
	require.Equal(t, 23, *result.TestExitCode)
	require.NotEmpty(t, result.Patch, "failed tests still leave a reviewable patch")
}

func TestIndependentBinaryOutputDoesNotPoisonPublishedLogs(t *testing.T) {
	config := fixtureRepository(t)
	job := fixtureJob(config)
	job.TestCommand = []string{"python3", "-c", "import sys;sys.stdout.buffer.write(b'one\\x00two\\xff')"}
	var logs []string
	result := (&LocalExecutor{Config: config}).Execute(t.Context(), job, func(_, content string) { logs = append(logs, content) })
	require.Equal(t, "succeeded", result.Status, result.Summary)
	require.Equal(t, "passed", result.TestStatus)
	for _, content := range logs {
		require.NotContains(t, content, "\x00")
	}
	require.Contains(t, strings.Join(logs, ""), "one\\0two�")
}

func TestOversizedPatchFailsSuccessfulProviderAndRetainsWorktree(t *testing.T) {
	config := fixtureRepository(t)
	provider := config.Providers["claude-code"]
	require.NoError(t, os.WriteFile(provider, []byte("#!/bin/sh\npython3 -c \"open('huge.txt','w').write('a'*1100000)\"\nprintf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"Added file\"}'\n"), 0700))
	job := fixtureJob(config)
	job.TestCommand = nil
	result := (&LocalExecutor{Config: config}).Execute(t.Context(), job, func(string, string) {})
	require.Equal(t, "failed", result.Status)
	require.Equal(t, -1, result.ExitCode)
	require.Contains(t, result.Summary, "exceeds")
	require.Empty(t, result.Patch)
	require.Equal(t, job.WorkingBranch, result.Branch)
	worktrees, err := os.ReadDir(config.WorkRoot)
	require.NoError(t, err)
	require.Len(t, worktrees, 1)
	_, err = os.Stat(filepath.Join(config.WorkRoot, worktrees[0].Name(), "huge.txt"))
	require.NoError(t, err, "oversized changes must remain available locally")
}

func TestProviderCannotMisreportAlternateOrDetachedBranch(t *testing.T) {
	for _, command := range []string{"git checkout -b alternate-provider", "git checkout --detach HEAD"} {
		t.Run(command, func(t *testing.T) {
			config := fixtureRepository(t)
			provider := config.Providers["claude-code"]
			script, err := os.ReadFile(provider)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(provider, []byte(strings.Replace(string(script), "#!/bin/sh\n", "#!/bin/sh\n"+command+"\n", 1)), 0700))
			job := fixtureJob(config)
			result := (&LocalExecutor{Config: config}).Execute(t.Context(), job, func(string, string) {})
			require.Equal(t, "failed", result.Status)
			require.Equal(t, "not_run", result.TestStatus)
			require.Equal(t, job.WorkingBranch, result.Branch)
			require.Contains(t, result.Summary, "retained")
			require.Empty(t, result.Patch)
		})
	}
}

func TestIndependentTestsCannotChangeAssignedBranch(t *testing.T) {
	config := fixtureRepository(t)
	job := fixtureJob(config)
	job.TestCommand = []string{"git", "checkout", "-b", "alternate-test"}
	result := (&LocalExecutor{Config: config}).Execute(t.Context(), job, func(string, string) {})
	require.Equal(t, "failed", result.Status)
	require.Equal(t, "passed", result.TestStatus)
	require.Equal(t, job.WorkingBranch, result.Branch)
	require.Contains(t, result.Summary, "alternate-test")
	require.Empty(t, result.Patch)
}

func TestLocalExecutorRejectsUnmappedOrigin(t *testing.T) {
	config := fixtureRepository(t)
	job := fixtureJob(config)
	job.RepositoryURL = "https://attacker.example/repo.git"
	result := (&LocalExecutor{Config: config}).Execute(t.Context(), job, func(string, string) {})
	require.Equal(t, "failed", result.Status)
	require.Contains(t, result.Summary, "exact URL")
}

func TestLocalCodexUsesWorkspaceSandbox(t *testing.T) {
	argv, err := localInvocation(agent.NewCodexProvider(""), "/usr/bin/codex", "/tmp/worktree", "work")
	require.NoError(t, err)
	require.Contains(t, argv, "workspace-write")
	require.NotContains(t, argv, "danger-full-access")
}

func TestCancellationKillsEntireProcessGroup(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := runProcess(ctx, dir, childEnvironment(""), []string{"/bin/sh", "-c", "trap '' TERM; sleep 60 & echo $! > child.pid; wait"}, io.Discard, io.Discard)
		done <- err
	}()
	path := filepath.Join(dir, "child.pid")
	require.Eventually(t, func() bool { _, err := os.Stat(path); return err == nil }, 5*time.Second, 10*time.Millisecond)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var pid int
	_, err = fmt.Sscanf(string(data), "%d", &pid)
	require.NoError(t, err)
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled process did not terminate")
	}
	// Zombies are already terminated; /proc state distinguishes them from running descendants.
	require.Eventually(t, func() bool {
		if err := syscall.Kill(pid, 0); err != nil {
			return true
		}
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		return err != nil || strings.Contains(string(stat), ") Z ")
	}, 2*time.Second, 10*time.Millisecond)
}
