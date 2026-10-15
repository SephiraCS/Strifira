package machine

import "strings"

func managedGitEnvironmentKey(key string) bool {
	switch key {
	case "GITHUB_TOKEN", "STRIFIRA_GIT_CREDENTIAL_HELPER", "STRIFIRA_GITHUB_HOST", "STRIFIRA_GIT_REPOSITORY", "STRIFIRA_REPO_URL", "STRIFIRA_BASE_BRANCH", "STRIFIRA_WORKING_BRANCH":
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
		if managedGitEnvironmentKey(name) || agent && strings.HasPrefix(name, "STRIFIRA_") {
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
		if key == "STRIFIRA_SECRET_NAMES" {
			for name := range strings.SplitSeq(value, ",") {
				if managedGitEnvironmentKey(name) {
					return true
				}
			}
		}
	}
	return false
}
