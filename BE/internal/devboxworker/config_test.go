package devboxworker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	directory := t.TempDir()
	return Config{ServerURL: "https://strifira.example", TokenFile: filepath.Join(directory, "token"), WorkRoot: filepath.Join(directory, "worktrees"), ExecutionMode: "trusted-local", Repositories: map[string]Repository{"owner/repo": {Path: directory, URL: "https://github.example/owner/repo.git"}}, Providers: map[string]string{"claude-code": "/bin/true"}}
}

func TestConfigRequiresExplicitLocalTrustAndHTTPS(t *testing.T) {
	config := testConfig(t)
	require.NoError(t, config.Validate())
	config.ExecutionMode = ""
	require.ErrorContains(t, config.Validate(), "trusted-local")
	config.ExecutionMode = "trusted-local"
	config.ServerURL = "http://strifira.example"
	config.LocalhostHTTP = true
	require.ErrorContains(t, config.Validate(), "HTTPS")
	config.ServerURL = "http://127.0.0.1:8080"
	require.NoError(t, config.Validate())
	config.ServerURL = "https://user:secret@strifira.example"
	require.Error(t, config.Validate())
	config.ServerURL = "https://strifira.example"
	config.Repositories["owner/repo"] = Repository{Path: t.TempDir(), URL: "https://user:secret@github.example/owner/repo.git"}
	require.Error(t, config.Validate())
}

func TestReadTokenRejectsUnsafePermissionsSymlinksAndPersonalKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.token")
	token := "strifira_" + strings.Repeat("a", 64)
	require.NoError(t, os.WriteFile(path, []byte(token+"\n"), 0600))
	value, err := ReadToken(path)
	require.NoError(t, err)
	require.Equal(t, token, value)
	require.NoError(t, os.Chmod(path, 0644))
	_, err = ReadToken(path)
	require.ErrorContains(t, err, "0600")
	require.NoError(t, os.Chmod(path, 0600))
	link := path + ".link"
	require.NoError(t, os.Symlink(path, link))
	_, err = ReadToken(link)
	require.Error(t, err)
	require.NoError(t, os.WriteFile(path, []byte("strifira_personal_key"), 0600))
	_, err = ReadToken(path)
	require.ErrorContains(t, err, "format")
}

func TestChildEnvironmentDoesNotPassWorkerOrGitHubCredentials(t *testing.T) {
	t.Setenv("STRIFIRA_WORKER_TOKEN", "worker-secret")
	t.Setenv("GITHUB_TOKEN", "github-secret")
	t.Setenv("GITHUB_TOKEN", "github-secret")
	t.Setenv("ANTHROPIC_API_KEY", "provider-secret")
	provider := strings.Join(childEnvironment("claude-code"), "\n")
	require.NotContains(t, provider, "worker-secret")
	require.NotContains(t, provider, "github-secret")
	require.NotContains(t, provider, "github-secret")
	require.Contains(t, provider, "ANTHROPIC_API_KEY=provider-secret")
	require.NotContains(t, strings.Join(childEnvironment(""), "\n"), "provider-secret")
}
