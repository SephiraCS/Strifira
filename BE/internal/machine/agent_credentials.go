package machine

import "strings"

func managedGitEnvironmentKey(key string) bool {
	switch key {
	case "GITHUB_TOKEN", "KU_GIT_CREDENTIAL_HELPER", "KU_GITHUB_HOST", "KU_GIT_REPOSITORY", "KU_REPO_URL", "KU_BASE_BRANCH", "KU_WORKING_BRANCH":
		return true
	default:
		return false
	}
}

// Runtime boundaries enforce the same rule as the manager, even for direct
// callers. Provider API credentials still follow the explicit secret scope.
func untrustedGitSecrets(secrets map[string]string, agent bool) map[string]string {
	result := make(map[string]string, len(secrets))
	for name, value := range secrets {
		if managedGitEnvironmentKey(name) || agent && strings.HasPrefix(name, "KU_") {
			continue
		}
		result[name] = value
	}
	return result
}

// Docker merges image defaults into Config.Env. Empty overrides are necessary
// to prevent baked credentials from returning when a key is simply omitted.
func scrubManagedGitEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if managedGitEnvironmentKey(key) {
			result = append(result, key+"=")
		} else {
			result = append(result, entry)
		}
	}
	return result
}

func containerHasManagedGitCredentials(environment []string) bool {
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		if managedGitEnvironmentKey(key) && value != "" {
			return true
		}
		if key == "KU_SECRET_NAMES" {
			for name := range strings.SplitSeq(value, ",") {
				if managedGitEnvironmentKey(name) {
					return true
				}
			}
		}
	}
	return false
}
