package machine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	cryptoutil "github.com/ku/ku-backend/pkg/crypto"
	"github.com/stretchr/testify/require"
)

type credentialManagerStoreFake struct {
	managerStoreFake
	values []domain.DevMachineEnvVar
}

func (f *credentialManagerStoreFake) ListEnvVarsInternal(context.Context, uuid.UUID, *string, string) ([]domain.DevMachineEnvVar, error) {
	return f.values, nil
}

func TestServiceSecretsDoNotFetchOrInjectForgejoCredentials(t *testing.T) {
	machine := fixtureHelperMachine()
	key := cryptoutil.DeriveKey("disposable-service-credential-fixture")
	encrypted, err := cryptoutil.Encrypt("allowed-provider-key", key)
	require.NoError(t, err)
	store := &credentialManagerStoreFake{values: []domain.DevMachineEnvVar{
		{Name: "FORGEJO_TOKEN", EncryptedValue: "reserved values must not even be decrypted"},
		{Name: "GITHUB_TOKEN", EncryptedValue: "reserved values must not even be decrypted"},
		{Name: "ANTHROPIC_API_KEY", EncryptedValue: encrypted},
	}}
	manager := NewManager(store, &runtimeFake{}, nil, nil, &GlobalForgejoCredentials{URL: "https://git.example.test", Token: "backend-only-token"}, key, nil, "owner")
	values, err := manager.serviceSecrets(t.Context(), machine, []domain.DevMachineService{{ServiceKey: "ide", ServiceType: "ide"}})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"ANTHROPIC_API_KEY": "allowed-provider-key"}, values["ide"])
	require.Empty(t, store.runtimeCredentials, "only a protected repository operation should obtain the publishing credential")
}

func TestManagedGitCredentialsAreExcludedFromScopedRuntimeInputs(t *testing.T) {
	input := map[string]string{"FORGEJO_TOKEN": "private-forgejo", "GITHUB_TOKEN": "private-github", "KU_GIT_REPOSITORY": "selected/repository", "KU_BROWSER_CDP_TOKEN": "internal-cdp", "ANTHROPIC_API_KEY": "allowed-provider", "TEST_SECRET": "allowed-task"}
	agent := untrustedGitSecrets(input, true)
	require.Equal(t, map[string]string{"ANTHROPIC_API_KEY": "allowed-provider", "TEST_SECRET": "allowed-task"}, agent)
	service := untrustedGitSecrets(input, false)
	require.Equal(t, "internal-cdp", service["KU_BROWSER_CDP_TOKEN"])
	require.NotContains(t, service, "FORGEJO_TOKEN")
	require.NotContains(t, service, "GITHUB_TOKEN")
	require.Equal(t, "private-forgejo", input["FORGEJO_TOKEN"], "filtering must not mutate the caller's authority")
	environment := scrubManagedGitEnvironment([]string{"PATH=/opt/tools:/usr/bin", "FORGEJO_TOKEN=baked-private", "GITHUB_TOKEN=baked-github", "KU_GIT_CREDENTIAL_HELPER=/malicious/helper"})
	require.Contains(t, environment, "PATH=/opt/tools:/usr/bin")
	require.Contains(t, environment, "FORGEJO_TOKEN=")
	require.Contains(t, environment, "GITHUB_TOKEN=")
	require.Contains(t, environment, "KU_GIT_CREDENTIAL_HELPER=")
	require.False(t, containerHasManagedGitCredentials(environment))
	require.True(t, containerHasManagedGitCredentials([]string{"KU_SECRET_NAMES=TEST_SECRET,FORGEJO_TOKEN"}))
}

func TestAgentBootstrapsPreserveProviderSecretsWithoutInstallingGitHelpers(t *testing.T) {
	root := repoRootFromCaller(t)
	shipped, err := os.ReadFile(filepath.Join(root, "devmachine/common/ku-agent-runner"))
	require.NoError(t, err)
	for name, source := range map[string]string{"shipped": string(shipped), "custom": customAgentBootstrap} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			secrets := filepath.Join(dir, "secrets")
			require.NoError(t, os.Mkdir(secrets, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(secrets, "ANTHROPIC_API_KEY"), []byte("provider-fixture-key\n\n"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(secrets, "FORGEJO_TOKEN"), []byte("blocked-forgejo"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(secrets, ".ready"), nil, 0o600))
			// Only the disposable secret path is substituted. Execute the shipped
			// bootstrap logic and inspect the child environment and filesystem.
			program := strings.ReplaceAll(source, "/run/ku-secrets", secrets)
			cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", program, "bootstrap-fixture", "/bin/sh", "-c", `set -eu; test -z "${FORGEJO_TOKEN:-}"; test -z "${GITHUB_TOKEN:-}"; printf '%s' "$ANTHROPIC_API_KEY"`)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "KU_SECRET_NAMES=ANTHROPIC_API_KEY,FORGEJO_TOKEN", "FORGEJO_TOKEN=inherited-forgejo", "GITHUB_TOKEN=inherited-github"}
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, string(output))
			require.Equal(t, "provider-fixture-key\n\n", string(output))
			require.NoFileExists(t, filepath.Join(dir, ".ku-git-credential"))
			require.NoFileExists(t, filepath.Join(dir, ".gitconfig"))
			require.NoFileExists(t, filepath.Join(secrets, "ANTHROPIC_API_KEY"))
			require.NoFileExists(t, filepath.Join(secrets, "FORGEJO_TOKEN"))
			require.NoFileExists(t, filepath.Join(secrets, ".ready"))
		})
	}
}
