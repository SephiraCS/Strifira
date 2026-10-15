package machine

import "testing"

func TestSafeGitHubPullRequestURL(t *testing.T) {
	const instance = "https://git.terrascale.tech"
	tests := []struct {
		name       string
		value      string
		repository string
		instance   string
		want       bool
	}{
		{name: "valid", value: "https://git.terrascale.tech/TerraScale/strifira/pull/46", repository: "TerraScale/strifira", instance: instance, want: true},
		{name: "gitea pulls path alias", value: "https://git.terrascale.tech/TerraScale/strifira/pulls/46", repository: "TerraScale/strifira", instance: instance, want: true},
		{name: "case insensitive repository", value: "https://git.terrascale.tech/terrascale/STRIFIRA/pull/46", repository: "TerraScale/strifira", instance: instance, want: true},
		{name: "scheme mismatch", value: "http://git.terrascale.tech/TerraScale/strifira/pull/46", repository: "TerraScale/strifira", instance: instance},
		{name: "lookalike host", value: "https://git.terrascale.tech.example.com/TerraScale/strifira/pull/46", repository: "TerraScale/strifira", instance: instance},
		{name: "github host not github instance", value: "https://github.com/TerraScale/strifira/pull/46", repository: "TerraScale/strifira", instance: instance},
		{name: "userinfo", value: "https://git.terrascale.tech@evil.example/TerraScale/strifira/pull/46", repository: "TerraScale/strifira", instance: instance},
		{name: "other repository", value: "https://git.terrascale.tech/attacker/strifira/pull/46", repository: "TerraScale/strifira", instance: instance},
		{name: "repository prefix", value: "https://git.terrascale.tech/TerraScale/strifira-malicious/pull/46", repository: "TerraScale/strifira", instance: instance},
		{name: "non pull request", value: "https://git.terrascale.tech/TerraScale/strifira/issues/46", repository: "TerraScale/strifira", instance: instance},
		{name: "invalid pull request number", value: "https://git.terrascale.tech/TerraScale/strifira/pull/zero", repository: "TerraScale/strifira", instance: instance},
		{name: "pull request subpage", value: "https://git.terrascale.tech/TerraScale/strifira/pull/46/files", repository: "TerraScale/strifira", instance: instance},
		{name: "query", value: "https://git.terrascale.tech/TerraScale/strifira/pull/46?redirect=https://evil.example", repository: "TerraScale/strifira", instance: instance},
		{name: "malformed repository", value: "https://git.terrascale.tech/TerraScale/strifira/pull/46", repository: "TerraScale/strifira/extra", instance: instance},
		{name: "missing instance", value: "https://git.terrascale.tech/TerraScale/strifira/pull/46", repository: "TerraScale/strifira", instance: ""},
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
