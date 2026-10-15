package devboxworker

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/SephiraCS/strifira-backend/internal/agent"
	"github.com/google/uuid"
)

type Executor interface {
	Execute(context.Context, Job, func(string, string)) Result
}

type LocalExecutor struct {
	Config         Config
	redactionToken string
}

type boundedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	limit     int
	truncated bool
	stream    string
	log       func(string, string)
	token     string
	pending   []byte
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.writeLocked(data, false)
	return len(data), nil
}

// Flush marks EOF. Until then potential token prefixes and incomplete UTF-8
// runes remain private, including across arbitrary subprocess writes.
func (b *boundedBuffer) Flush() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.writeLocked(nil, true)
}

func (b *boundedBuffer) writeLocked(data []byte, final bool) {
	data = append(b.pending, data...)
	if b.token != "" {
		data = bytes.ReplaceAll(data, []byte(b.token), []byte("[REDACTED]"))
	}
	end := len(data)
	if !final {
		for held := min(len(b.token)-1, end); held > 0; held-- {
			if bytes.Equal(data[end-held:], []byte(b.token[:held])) {
				end -= held
				break
			}
		}
		for held := min(utf8.UTFMax-1, end); held > 0; held-- {
			if !utf8.FullRune(data[end-held : end]) {
				end -= held
				break
			}
		}
	}
	b.pending = bytes.Clone(data[end:])
	complete := data[:end]
	available := max(0, b.limit-b.buffer.Len())
	part := complete[:min(available, len(complete))]
	if utf8.Valid(complete) {
		for !utf8.Valid(part) {
			part = part[:len(part)-1]
		}
	}
	_, _ = b.buffer.Write(part)
	b.truncated = b.truncated || len(part) < len(complete)
	if b.log != nil {
		clean := []byte(cleanText(string(part)))
		for len(clean) > 0 {
			end := min(len(clean), 16*1024)
			for !utf8.Valid(clean[:end]) {
				end--
			}
			b.log(b.stream, string(clean[:end]))
			clean = clean[end:]
		}
	}
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.ToValidUTF8(b.buffer.String(), "�")
}

func (b *boundedBuffer) rawString() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func cleanText(value string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(value, "�"), "\x00", "\\0")
}

func boundedText(value string, limit int) string {
	value = cleanText(value)
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func providerFor(id string) (agent.Provider, error) {
	switch id {
	case "claude-code":
		return agent.NewClaudeCodeProvider(""), nil
	case "codex":
		return agent.NewCodexProvider(""), nil
	case "opencode":
		return agent.NewOpenCodeProvider(""), nil
	default:
		return nil, fmt.Errorf("unsupported provider %q", id)
	}
}

func localInvocation(provider agent.Provider, path, dir, prompt string) ([]string, error) {
	invocation, err := provider.BuildInvocation(agent.RunInput{Mode: agent.ModeAutonomous, Prompt: prompt, WorkspacePath: dir})
	if err != nil {
		return nil, err
	}
	argv := invocation.Argv
	argv[0] = path
	if provider.Metadata().ID == "codex" {
		for i, value := range argv {
			if value == "--sandbox" && i+1 < len(argv) {
				argv[i+1] = "workspace-write"
			}
		}
	}
	return argv, nil
}

func gitCommand(ctx context.Context, dir string, args ...string) (string, error) {
	output := &boundedBuffer{limit: maxPatchBytes + 1}
	errorsOutput := &boundedBuffer{limit: 4000}
	argv := append([]string{"git", "-c", "core.hooksPath=/dev/null", "-c", "credential.helper=", "-c", "protocol.file.allow=never"}, args...)
	_, err := runProcess(ctx, dir, childEnvironment(""), argv, output, errorsOutput)
	if err != nil {
		return "", fmt.Errorf("git %s failed: %s", args[0], boundedText(strings.TrimSpace(errorsOutput.String()), 1000))
	}
	if output.truncated {
		return "", errors.New("git output exceeds artifact limit")
	}
	return output.String(), nil
}

func requireTaskBranch(ctx context.Context, dir, assigned string) error {
	current, err := gitCommand(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return errors.New("task HEAD is detached or its branch cannot be read; worktree is retained")
	}
	if current = strings.TrimSpace(current); current != assigned {
		return fmt.Errorf("task branch changed from %s to %s; worktree is retained", assigned, current)
	}
	return nil
}

func (e *LocalExecutor) Execute(ctx context.Context, job Job, log func(string, string)) Result {
	publishLog := log
	log = func(stream, content string) {
		if e.redactionToken != "" {
			content = strings.ReplaceAll(content, e.redactionToken, "[REDACTED]")
		}
		publishLog(stream, cleanText(content))
	}
	result := Result{Status: "failed", ExitCode: -1, TestStatus: "not_run", ChangedFiles: []string{}, TestsRun: []string{}, RiskNotes: []string{}}
	fail := func(err error) Result {
		result.Status = "failed"
		result.ExitCode = -1
		result.Summary = boundedText(err.Error(), maxSummaryBytes)
		return result
	}
	if _, err := uuid.Parse(job.ID); err != nil {
		return fail(errors.New("job ID is invalid"))
	}
	repository, ok := e.Config.Repositories[job.RepositoryFullName]
	if !ok || repository.URL != job.RepositoryURL {
		return fail(errors.New("job repository is not configured with this exact URL"))
	}
	providerPath, ok := e.Config.Providers[job.Provider]
	if !ok {
		return fail(errors.New("job provider is not configured locally"))
	}
	if job.BaseBranch == "" || strings.HasPrefix(job.BaseBranch, "-") || strings.ContainsAny(job.BaseBranch, "\x00\r\n") {
		return fail(errors.New("job base branch is invalid"))
	}
	origin, err := gitCommand(ctx, repository.Path, "remote", "get-url", "origin")
	if err != nil {
		return fail(err)
	}
	if strings.TrimSpace(origin) != cmp.Or(repository.OriginURL, repository.URL) {
		return fail(errors.New("configured checkout origin does not match allowed repository URL"))
	}
	base, err := gitCommand(ctx, repository.Path, "rev-parse", "--verify", "--end-of-options", job.BaseBranch+"^{commit}")
	if err != nil {
		return fail(err)
	}
	base = strings.TrimSpace(base)
	branch := job.WorkingBranch
	if branch == "" || strings.HasPrefix(branch, "-") {
		return fail(errors.New("job working branch is invalid"))
	}
	if _, err := gitCommand(ctx, repository.Path, "check-ref-format", "--branch", branch); err != nil {
		return fail(err)
	}
	worktree := filepath.Join(e.Config.WorkRoot, job.ID+"-"+uuid.NewString()[:8])
	if _, err := gitCommand(ctx, repository.Path, "worktree", "add", "-b", branch, "--", worktree, base); err != nil {
		return fail(err)
	}
	result.Branch = branch
	// Worktrees and the task branch remain available locally for review/recovery.
	log("system", "Created isolated worktree at "+worktree+" from "+base+"\n")
	provider, err := providerFor(job.Provider)
	if err != nil {
		return fail(err)
	}
	prompt := job.Prompt
	prompt += "\n\nWork only in this task worktree. Do not push, create pull requests, or publish changes. Leave changes available for review; the worker will run configured tests independently."
	argv, err := localInvocation(provider, providerPath, worktree, prompt)
	if err != nil {
		return fail(err)
	}
	stdout := &boundedBuffer{limit: maxOutputBytes, stream: "stdout", log: log, token: e.redactionToken}
	stderr := &boundedBuffer{limit: maxOutputBytes, stream: "stderr", log: log, token: e.redactionToken}
	code, processErr := runProcess(ctx, worktree, childEnvironment(job.Provider), argv, stdout, stderr)
	parsed := provider.ParseResult(stdout.String(), stderr.String(), code)
	result.Status, result.ExitCode, result.Summary = parsed.Status, code, boundedText(parsed.Summary, maxSummaryBytes)
	for _, note := range parsed.RiskNotes[:min(len(parsed.RiskNotes), 97)] {
		result.RiskNotes = append(result.RiskNotes, boundedText(strings.ToValidUTF8(note, "�"), 4096))
	}
	if processErr != nil && code == -1 {
		result.Summary = boundedText(processErr.Error(), maxSummaryBytes)
		result.Status = "failed"
	}
	if stdout.truncated || stderr.truncated {
		result.Status = "failed"
		result.RiskNotes = append(result.RiskNotes, "Provider output exceeded the local limit; terminal result may be incomplete.")
	}
	if ctx.Err() != nil {
		return fail(context.Cause(ctx))
	}
	if err := requireTaskBranch(ctx, worktree, branch); err != nil {
		return fail(err)
	}
	if len(job.TestCommand) > 0 {
		log("system", "Running configured tests independently of the provider.\n")
		result.TestsRun = []string{boundedText(strings.Join(job.TestCommand, " "), 4096)}
		testOutput := &boundedBuffer{limit: maxOutputBytes, stream: "stdout", log: log, token: e.redactionToken}
		testError := &boundedBuffer{limit: maxOutputBytes, stream: "stderr", log: log, token: e.redactionToken}
		testCode, testErr := runProcess(ctx, worktree, childEnvironment(""), job.TestCommand, testOutput, testError)
		result.TestExitCode = new(testCode)
		result.TestStatus = "passed"
		if testErr != nil || testCode != 0 {
			result.TestStatus = "failed"
			result.Status = "failed"
			result.RiskNotes = append(result.RiskNotes, "The worker's independent test command failed.")
		}
	}
	if ctx.Err() != nil {
		return fail(context.Cause(ctx))
	}
	if err := requireTaskBranch(ctx, worktree, branch); err != nil {
		return fail(err)
	}
	// Include committed, staged, unstaged, and new files without changing the task index.
	patch, files, err := collectPatch(ctx, worktree, base)
	if err != nil {
		return fail(err)
	}
	result.Patch, result.ChangedFiles = patch, files
	if result.Status != "succeeded" {
		result.Status = "failed"
	}
	return result
}

func collectPatch(ctx context.Context, worktree, base string) (string, []string, error) {
	index, err := os.CreateTemp("", "strifira-dev-box-index-*")
	if err != nil {
		return "", nil, err
	}
	indexPath := index.Name()
	_ = index.Close()
	_ = os.Remove(indexPath)
	defer os.Remove(indexPath)
	environment := append(childEnvironment(""), "GIT_INDEX_FILE="+indexPath)
	run := func(args ...string) (string, error) {
		stdout := &boundedBuffer{limit: maxPatchBytes + 1}
		stderr := &boundedBuffer{limit: 4000}
		argv := append([]string{"git", "-c", "core.hooksPath=/dev/null", "-c", "credential.helper="}, args...)
		_, err := runProcess(ctx, worktree, environment, argv, stdout, stderr)
		if err != nil {
			return "", fmt.Errorf("collect review artifact: %s", boundedText(stderr.String(), 1000))
		}
		if stdout.truncated || stdout.buffer.Len() > maxPatchBytes {
			return "", errors.New("review patch exceeds 1 MiB; changes remain in the local worktree")
		}
		return stdout.rawString(), nil
	}
	if _, err := run("read-tree", "HEAD"); err != nil {
		return "", nil, err
	}
	if _, err := run("add", "--all", "--", "."); err != nil {
		return "", nil, err
	}
	patch, err := run("diff", "--cached", "--binary", "--no-ext-diff", "--no-textconv", base, "--")
	if err != nil {
		return "", nil, err
	}
	if !utf8.ValidString(patch) || strings.ContainsRune(patch, '\x00') {
		return "", nil, errors.New("review patch cannot be represented as valid UTF-8; changes remain in the local worktree")
	}
	names, err := run("diff", "--cached", "--name-only", "-z", base, "--")
	if err != nil {
		return "", nil, err
	}
	files := []string{}
	for name := range strings.SplitSeq(names, "\x00") {
		if name != "" {
			if len(files) == 500 || len(name) > 4096 || !utf8.ValidString(name) {
				return "", nil, errors.New("review changed-file list exceeds the server limit; worktree is retained")
			}
			files = append(files, name)
		}
	}
	return patch, files, nil
}

var _ io.Writer = (*boundedBuffer)(nil)
