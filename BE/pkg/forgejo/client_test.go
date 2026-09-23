package forgejo

import "testing"

func TestAllChecksPassed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state string
		count int
		want  bool
	}{
		{"absent", "success", 0, false},
		{"pending", "pending", 2, false},
		{"failed", "failure", 2, false},
		{"passing", "success", 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := (&CombinedStatus{State: tc.state, TotalCount: tc.count}).AllChecksPassed()
			if got != tc.want {
				t.Fatalf("AllChecksPassed() = %v, want %v", got, tc.want)
			}
		})
	}
}
