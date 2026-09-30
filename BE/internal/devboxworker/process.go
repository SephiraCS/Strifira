package devboxworker

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// childEnvironment intentionally excludes worker/server/Forgejo tokens and unrelated secrets.
// Provider login files remain local to this explicitly trusted execution user.
func childEnvironment(provider string) []string {
	allowed := map[string]bool{"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "LANG": true, "TERM": true,
		"TMPDIR": true, "NIX_PATH": true, "NIX_SSL_CERT_FILE": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
		"NODE_EXTRA_CA_CERTS": true, "XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_CACHE_HOME": true,
		"XDG_STATE_HOME": true, "XDG_RUNTIME_DIR": true}
	switch provider {
	case "claude-code":
		allowed["ANTHROPIC_API_KEY"] = true
	case "codex":
		allowed["CODEX_API_KEY"] = true
		allowed["OPENAI_API_KEY"] = true
	case "opencode":
		allowed["ANTHROPIC_API_KEY"] = true
		allowed["OPENAI_API_KEY"] = true
	}
	var environment []string
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if allowed[key] || strings.HasPrefix(key, "LC_") {
			environment = append(environment, value)
		}
	}
	return append(environment, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=push.default", "GIT_CONFIG_VALUE_0=nothing",
		"GIT_CONFIG_KEY_1=remote.origin.pushurl", "GIT_CONFIG_VALUE_1=disabled://publishing-disabled")
}

func runProcess(ctx context.Context, dir string, environment, argv []string, stdout, stderr io.Writer) (int, error) {
	if len(argv) == 0 || argv[0] == "" {
		return -1, errors.New("empty command")
	}
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Dir, command.Env, command.Stdout, command.Stderr = dir, environment, stdout, stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = 2 * time.Second
	var killTimer atomic.Pointer[time.Timer]
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		pid := command.Process.Pid
		err := syscall.Kill(-pid, syscall.SIGTERM)
		killTimer.Store(time.AfterFunc(500*time.Millisecond, func() { _ = syscall.Kill(-pid, syscall.SIGKILL) }))
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	if err := command.Start(); err != nil {
		return -1, err
	}
	err := command.Wait()
	if timer := killTimer.Load(); timer != nil {
		timer.Stop()
	}
	// Shells and provider tools may exit while descendants still hold pipes or run.
	// No process in this invocation's group may outlive the invocation.
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if err == nil {
		return 0, nil
	}
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return exit.ExitCode(), err
	}
	return -1, err
}
