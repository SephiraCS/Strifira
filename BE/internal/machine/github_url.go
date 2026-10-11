package machine

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/ku/ku-backend/internal/domain"
)

// safeGitHubPullRequestURL validates an agent- or API-reported pull request
// URL: it must live on the linked repository's GitHub instance, match the
// repository owner/name, and use a /pull/<number> path.
func safeGitHubPullRequestURL(value, repositoryFullName, instanceURL string) (string, bool) {
	repository := strings.Split(repositoryFullName, "/")
	if len(repository) != 2 || repository[0] == "" || repository[1] == "" {
		return "", false
	}
	instance, err := url.Parse(strings.TrimSpace(instanceURL))
	if err != nil || instance.Scheme == "" || instance.Host == "" {
		return "", false
	}

	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != instance.Scheme || !strings.EqualFold(parsed.Hostname(), instance.Hostname()) ||
		parsed.User != nil || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}

	path := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(path) != 4 || !strings.EqualFold(path[0], repository[0]) || !strings.EqualFold(path[1], repository[1]) ||
		(path[2] != "pull" && path[2] != "pulls") {
		return "", false
	}
	number, err := strconv.Atoi(path[3])
	if err != nil || number < 1 {
		return "", false
	}

	return parsed.String(), true
}

// safePullRequestURL resolves the machine's GitHub instance and validates
// the URL against it.
func (m *Manager) safePullRequestURL(ctx context.Context, machine *domain.DevMachine, value, repositoryFullName string) (string, bool) {
	instanceURL, _, err := m.githubCredential(ctx, machine, repositoryFullName)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if instanceURL == "" {
		return "", false
	}
	return safeGitHubPullRequestURL(value, repositoryFullName, instanceURL)
}
