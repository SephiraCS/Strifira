package machine

import "testing"

func TestSafeGitHubPullRequestURL(t *testing.T) {
	const instance = "https://git.example.com"
	tests := []struct {
		name       string
		value      string
		repository string
		instance   string
		want       bool
	}{
		{name: "valid", value: "https://git.example.com/SephiraCS/Strifira/pull/46", repository: "SephiraCS/Strifira", instance: instance, want: true},
		{name: "gitea pulls path alias", value: "https://git.example.com/SephiraCS/Strifira/pulls/46", repository: "SephiraCS/Strifira", instance: instance, want: true},
		{name: "case insensitive repository", value: "https://git.example.com/sephiracs/STRIFIRA/pull/46", repository: "SephiraCS/Strifira", instance: instance, want: true},
		{name: "scheme mismatch", value: "http://git.example.com/SephiraCS/Strifira/pull/46", repository: "SephiraCS/Strifira", instance: instance},
		{name: "lookalike host", value: "https://git.example.com.evil.net/SephiraCS/Strifira/pull/46", repository: "SephiraCS/Strifira", instance: instance},
		{name: "github host not github instance", value: "https://github.com/SephiraCS/Strifira/pull/46", repository: "SephiraCS/Strifira", instance: instance},
		{name: "userinfo", value: "https://git.example.com@evil.example/SephiraCS/Strifira/pull/46", repository: "SephiraCS/Strifira", instance: instance},
		{name: "other repository", value: "https://git.example.com/attacker/strifira/pull/46", repository: "SephiraCS/Strifira", instance: instance},
		{name: "repository prefix", value: "https://git.example.com/SephiraCS/Strifira-malicious/pull/46", repository: "SephiraCS/Strifira", instance: instance},
		{name: "non pull request", value: "https://git.example.com/SephiraCS/Strifira/issues/46", repository: "SephiraCS/Strifira", instance: instance},
		{name: "invalid pull request number", value: "https://git.example.com/SephiraCS/Strifira/pull/zero", repository: "SephiraCS/Strifira", instance: instance},
		{name: "pull request subpage", value: "https://git.example.com/SephiraCS/Strifira/pull/46/files", repository: "SephiraCS/Strifira", instance: instance},
		{name: "query", value: "https://git.example.com/SephiraCS/Strifira/pull/46?redirect=https://evil.example", repository: "SephiraCS/Strifira", instance: instance},
		{name: "malformed repository", value: "https://git.example.com/SephiraCS/Strifira/pull/46", repository: "SephiraCS/Strifira/extra", instance: instance},
		{name: "missing instance", value: "https://git.example.com/SephiraCS/Strifira/pull/46", repository: "SephiraCS/Strifira", instance: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := safeGitHubPullRequestURL(tt.value, tt.repository, tt.instance)
			if ok != tt.want {
				t.Fatalf("safeGitHubPullRequestURL() ok = %v, want %v", ok, tt.want)
			}
			if ok && got != tt.value {
				t.Fatalf("safeGitHubPullRequestURL() = %q, want %q", got, tt.value)
			}
		})
	}
}
