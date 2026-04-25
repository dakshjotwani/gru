package tailer

import "testing"

func TestEncodeCwd(t *testing.T) {
	cases := []struct {
		cwd  string
		want string
	}{
		{"/Users/foo/bar", "-Users-foo-bar"},
		{"/Users/foo/.gru/journal", "-Users-foo--gru-journal"},
		{"/Users/foo/.claude/worktrees/abc123", "-Users-foo--claude-worktrees-abc123"},
		{"/Users/dakshjotwani/workspace/gru", "-Users-dakshjotwani-workspace-gru"},
		{"", ""},
	}
	for _, tc := range cases {
		got := encodeCwd(tc.cwd)
		if got != tc.want {
			t.Errorf("encodeCwd(%q) = %q, want %q", tc.cwd, got, tc.want)
		}
	}
}
