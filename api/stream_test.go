package api

import "testing"

func TestStreamConstructors(t *testing.T) {
	cases := []struct {
		got  StreamID
		want StreamID
	}{
		{SessionStream("s1"), "session:s1"},
		{PaneStream("p1"), "pane:p1"},
		{WorkspaceStream("/repo/.git"), "workspace:/repo/.git"},
		{WorktreeStream("/repo/.git", "abc123"), "worktree:/repo/.git:abc123"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("stream id = %q, want %q", c.got, c.want)
		}
	}
}

func TestStreamScope(t *testing.T) {
	cases := []struct {
		id    StreamID
		scope EventScope
		ok    bool
	}{
		{SessionStream("s1"), ScopeSession, true},
		{PaneStream("p1"), ScopePane, true},
		{WorkspaceStream("w"), ScopeWorkspace, true},
		{WorktreeStream("w", "t"), ScopeWorktree, true},
		{"bogus:x", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := c.id.Scope()
		if got != c.scope || ok != c.ok {
			t.Errorf("%q.Scope() = (%q, %v), want (%q, %v)", c.id, got, ok, c.scope, c.ok)
		}
	}
}

// TestWorktreeScopeNotConfusedWithWorkspace guards the shared "work" prefix.
func TestWorktreeScopeNotConfusedWithWorkspace(t *testing.T) {
	if s, _ := WorktreeStream("w", "t").Scope(); s != ScopeWorktree {
		t.Errorf("worktree stream scoped as %q", s)
	}
	if s, _ := WorkspaceStream("w").Scope(); s != ScopeWorkspace {
		t.Errorf("workspace stream scoped as %q", s)
	}
}
