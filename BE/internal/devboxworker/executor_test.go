package devboxworker

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/SephiraCS/strifira-backend/internal/agent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBoundedBufferRedactsBeforeWriteAndLogChunkBoundaries(t *testing.T) {
	token := "sfw_" + strings.Repeat("a", 64)
	for split := 1; split < len(token); split++ {
		t.Run(fmt.Sprintf("token write split %d", split), func(t *testing.T) {
			var logs strings.Builder
			output := &boundedBuffer{limit: maxOutputBytes, token: token, stream: "stdout", log: func(stream, content string) {
				require.Equal(t, "stdout", stream)
				require.LessOrEqual(t, len(content), 16*1024)
				require.True(t, utf8.ValidString(content))
				logs.WriteString(content)
			}}
			_, err := output.Write([]byte("before " + token[:split]))
			require.NoError(t, err)
			require.Equal(t, "before ", logs.String(), "potential token prefixes stay private")
			_, err = output.Write([]byte(token[split:] + " after"))
			require.NoError(t, err)
			output.Flush()
			require.Equal(t, "before [REDACTED] after", output.String())
			require.Equal(t, output.String(), logs.String())
		})
	}
	t.Run("token at 16 KiB chunk boundary", func(t *testing.T) {
		var logs strings.Builder
		output := &boundedBuffer{limit: maxOutputBytes, token: token, log: func(_, content string) {
			require.LessOrEqual(t, len(content), 16*1024)
			logs.WriteString(content)
		}}
		prefix := strings.Repeat("x", 16*1024-len(token)/2)
		_, err := output.Write([]byte(prefix + token + strings.Repeat("y", 16*1024)))
		require.NoError(t, err)
		output.Flush()
		require.Equal(t, prefix+"[REDACTED]"+strings.Repeat("y", 16*1024), logs.String())
		require.Equal(t, output.String(), logs.String())
		require.NotContains(t, logs.String(), token)
	})
}

func TestBoundedBufferStreamingUTF8EOFAndLimits(t *testing.T) {
	for _, test := range []struct {
		name   string
		writes []string
		want   string
	}{
		{name: "split Unicode runes", writes: []string{"text \xe2", "\x82", "\xac and \xf0\x9f", "\x8c\x8d"}, want: "text € and 🌍"},
		{name: "incomplete UTF8 at EOF", writes: []string{"text \xf0\x9f"}, want: "text �"},
		{name: "partial token prefix at EOF", writes: []string{"ordinary strifira"}, want: "ordinary strifira"},
		{name: "binary log output", writes: []string{"\x00\xff\x01"}, want: "\\0�\x01"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var logs strings.Builder
			output := &boundedBuffer{limit: maxOutputBytes, token: "sfw_" + strings.Repeat("b", 64), log: func(_, content string) {
				require.LessOrEqual(t, len(content), 16*1024)
				require.True(t, utf8.ValidString(content))
				require.NotContains(t, content, "\x00")
				logs.WriteString(content)
			}}
			for _, write := range test.writes {
				_, err := output.Write([]byte(write))
				require.NoError(t, err)
			}
			output.Flush()
			require.Equal(t, test.want, logs.String())
			require.Equal(t, strings.Join(test.writes, ""), output.rawString(), "raw capture preserves binary and NUL-delimited tool output")
		})
	}
	t.Run("Unicode chunk boundary and capture cap", func(t *testing.T) {
		var logs strings.Builder
		output := &boundedBuffer{limit: 16*1024 + 3, log: func(_, content string) {
			require.LessOrEqual(t, len(content), 16*1024)
			require.True(t, utf8.ValidString(content))
			logs.WriteString(content)
		}}
		content := strings.Repeat("x", 16*1024-1) + "€🌍"
		_, err := output.Write([]byte(content))
		require.NoError(t, err)
		output.Flush()
		require.Equal(t, strings.Repeat("x", 16*1024-1)+"€", output.String())
		require.Equal(t, output.String(), logs.String())
		require.True(t, output.truncated)
	})
}

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
	return Job{ID: id, Provider: "claude-code", Prompt: "edit a file", RepositoryURL: config.Repositories["owner/repo"].URL, RepositoryFullName: "owner/repo", BaseBranch: "main", WorkingBranch: "strifira/task-" + id[:8], MaxRuntimeSeconds: 30, TestCommand: []string{"/bin/sh", "-c", "test -f new.txt && test \"$(cat README.md)\" = changed"}}
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
	require.True(t, strings.HasPrefix(result.Branch, "strifira/task-"))
	after, err := os.ReadFile(index)
	require.NoError(t, err)
	require.Equal(t, before, after, "original checkout index must stay unchanged")
	readme, err := os.ReadFile(filepath.Join(config.Repositories["owner/repo"].Path, "README.md"))
	require.NoError(t, err)
	require.Equal(t, "base\n", string(readme))
}

func TestLocalExecutorRedactsProviderTestsAndSystemLogsAndFlushesEOF(t *testing.T) {
	config := fixtureRepository(t)
	token := "sfw_" + strings.Repeat("c", 64)
	privateRoot := config.WorkRoot + "-" + token
	require.NoError(t, os.Rename(config.WorkRoot, privateRoot))
	config.WorkRoot = privateRoot
	provider := config.Providers["claude-code"]
	script := "#!/bin/sh\nprintf 'changed\\n' > README.md\nprintf '%s' '" + token[:34] + "' >&2\nprintf '%s' '" + token[34:] + "' >&2\nprintf '%s' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"" + token + "\"}'\n"
	require.NoError(t, os.WriteFile(provider, []byte(script), 0700))
	job := fixtureJob(config)
	job.TestCommand = []string{"/bin/sh", "-c", "printf '%s' '" + token + "'; printf '\\342\\202\\254' >&2"}
	var logs strings.Builder
	var logsMu sync.Mutex
	result := (&LocalExecutor{Config: config, redactionToken: token}).Execute(t.Context(), job, func(_, content string) {
		require.True(t, utf8.ValidString(content))
		require.LessOrEqual(t, len(content), 16*1024)
		logsMu.Lock()
		defer logsMu.Unlock()
		logs.WriteString(content)
	})
	require.Equal(t, "succeeded", result.Status, result.Summary)
	require.Equal(t, "[REDACTED]", result.Summary, "captured provider output is redacted before parsing")
	require.Equal(t, "passed", result.TestStatus)
	require.NotContains(t, logs.String(), token)
	require.Contains(t, logs.String(), "[REDACTED]", "system paths, stderr and independent stdout are sanitized")
	require.Contains(t, logs.String(), "€", "EOF flush preserves the final Unicode output")
}

func TestLocalExecutorServicePromptIncludesAcceptanceCriteriaOnce(t *testing.T) {
	for _, count := range []int{2, 50} {
		t.Run(fmt.Sprintf("%d criteria", count), func(t *testing.T) {
			config := fixtureRepository(t)
			job := fixtureJob(config)
			var prompt strings.Builder
			fmt.Fprintf(&prompt, "Implement the issue\n\nDescription\n\nRequested change\n\nKu issue execution contract:\nIssue: TS-279\nRepository: owner/repo\nBase branch: main\nWorking branch: %s\nWork only in the isolated job worktree. Do not push or open a pull request. Never print or persist credentials.\n", job.WorkingBranch)
			for i := range count {
				criterion := fmt.Sprintf("Criterion %02d: ", i)
				criterion += strings.Repeat("a", 1400-len(criterion))
				job.AcceptanceCriteria = append(job.AcceptanceCriteria, criterion)
				fmt.Fprintf(&prompt, "Acceptance criterion: %s\n", criterion)
			}
			prompt.WriteString("Finish with the implemented changes, test outcomes, and remaining risks.\n")
			job.Prompt = prompt.String()
			require.LessOrEqual(t, len(job.Prompt), 100000, "service-shaped prompt meets the composed context limit")
			capture := filepath.Join(t.TempDir(), "prompt.txt")
			provider := config.Providers["claude-code"]
			script, err := os.ReadFile(provider)
			require.NoError(t, err)
			captureCommand := "for argument do prompt=$argument; done\nprintf '%s' \"$prompt\" > '" + strings.ReplaceAll(capture, "'", "'\\''") + "'\n"
			require.NoError(t, os.WriteFile(provider, []byte(strings.Replace(string(script), "#!/bin/sh\n", "#!/bin/sh\n"+captureCommand, 1)), 0700))
			result := (&LocalExecutor{Config: config}).Execute(t.Context(), job, func(string, string) {})
			require.Equal(t, "succeeded", result.Status, result.Summary)
			require.Equal(t, 0, result.ExitCode, "large valid prompt must start the actual fake-provider process")
			actual, err := os.ReadFile(capture)
			require.NoError(t, err)
			for _, criterion := range job.AcceptanceCriteria {
				require.Equal(t, 1, strings.Count(string(actual), criterion))
			}
			require.True(t, strings.HasPrefix(string(actual), job.Prompt))
			require.Contains(t, string(actual), "Work only in this task worktree. Do not push, create pull requests, or publish changes.")
			require.Less(t, len(actual)+1, 128*1024, "prompt and terminating NUL stay below the Linux single-argument limit")
		})
	}
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
