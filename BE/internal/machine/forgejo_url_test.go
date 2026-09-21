package machine

import "testing"

func TestSafeForgejoPullRequestURL(t *testing.T) {
	const instance = "https://git.terrascale.tech"
	tests := []struct {
		name       string
		value      string
		repository string
		instance   string
		want       bool
	}{
		{name: "valid", value: "https://git.terrascale.tech/TerraScale/ku/pull/46", repository: "TerraScale/ku", instance: instance, want: true},
		{name: "gitea pulls path alias", value: "https://git.terrascale.tech/TerraScale/ku/pulls/46", repository: "TerraScale/ku", instance: instance, want: true},
		{name: "case insensitive repository", value: "https://git.terrascale.tech/terrascale/KU/pull/46", repository: "TerraScale/ku", instance: instance, want: true},
		{name: "scheme mismatch", value: "http://git.terrascale.tech/TerraScale/ku/pull/46", repository: "TerraScale/ku", instance: instance},
		{name: "lookalike host", value: "https://git.terrascale.tech.example.com/TerraScale/ku/pull/46", repository: "TerraScale/ku", instance: instance},
		{name: "github host not forgejo instance", value: "https://github.com/TerraScale/ku/pull/46", repository: "TerraScale/ku", instance: instance},
		{name: "userinfo", value: "https://git.terrascale.tech@evil.example/TerraScale/ku/pull/46", repository: "TerraScale/ku", instance: instance},
		{name: "other repository", value: "https://git.terrascale.tech/attacker/ku/pull/46", repository: "TerraScale/ku", instance: instance},
		{name: "repository prefix", value: "https://git.terrascale.tech/TerraScale/ku-malicious/pull/46", repository: "TerraScale/ku", instance: instance},
		{name: "non pull request", value: "https://git.terrascale.tech/TerraScale/ku/issues/46", repository: "TerraScale/ku", instance: instance},
		{name: "invalid pull request number", value: "https://git.terrascale.tech/TerraScale/ku/pull/zero", repository: "TerraScale/ku", instance: instance},
		{name: "pull request subpage", value: "https://git.terrascale.tech/TerraScale/ku/pull/46/files", repository: "TerraScale/ku", instance: instance},
		{name: "query", value: "https://git.terrascale.tech/TerraScale/ku/pull/46?redirect=https://evil.example", repository: "TerraScale/ku", instance: instance},
		{name: "malformed repository", value: "https://git.terrascale.tech/TerraScale/ku/pull/46", repository: "TerraScale/ku/extra", instance: instance},
		{name: "missing instance", value: "https://git.terrascale.tech/TerraScale/ku/pull/46", repository: "TerraScale/ku", instance: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := safeForgejoPullRequestURL(tt.value, tt.repository, tt.instance)
			if ok != tt.want {
				t.Fatalf("safeForgejoPullRequestURL() ok = %v, want %v", ok, tt.want)
			}
			if ok && got != tt.value {
				t.Fatalf("safeForgejoPullRequestURL() = %q, want %q", got, tt.value)
			}
		})
	}
}
