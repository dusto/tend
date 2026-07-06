package wsrules

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolverExactBeatsUnder(t *testing.T) {
	r := NewResolver([]Rule{
		{Under: []string{"/home/u/work"}, Use: "shared"},
		{Repos: []string{"/home/u/work/special"}, Use: "special"},
	})
	// An exact repo match wins over an under-prefix match, regardless of order.
	if use, ok := r.Resolve("/home/u/work/special"); !ok || use != "special" {
		t.Errorf("Resolve(special) = %q,%v; want special,true", use, ok)
	}
	// A different repo under the prefix falls to the under rule.
	if use, ok := r.Resolve("/home/u/work/other"); !ok || use != "shared" {
		t.Errorf("Resolve(other) = %q,%v; want shared,true", use, ok)
	}
}

func TestResolverFirstRuleWinsWithinTier(t *testing.T) {
	r := NewResolver([]Rule{
		{Under: []string{"/home/u/work"}, Use: "first"},
		{Under: []string{"/home/u/work"}, Use: "second"},
	})
	if use, _ := r.Resolve("/home/u/work/x"); use != "first" {
		t.Errorf("first matching under rule should win, got %q", use)
	}
}

func TestResolverNoMatch(t *testing.T) {
	r := NewResolver([]Rule{{Repos: []string{"/a/b"}, Use: "x"}})
	if _, ok := r.Resolve("/c/d"); ok {
		t.Error("unrelated root should not resolve")
	}
}

func TestResolverMatchingIsSegmentWise(t *testing.T) {
	// Exact repos: segment-wise, not substring.
	r := NewResolver([]Rule{{Repos: []string{"/a/b/tend"}, Use: "t"}})
	if _, ok := r.Resolve("/a/b/nettend"); ok {
		t.Error("exact match must be segment-wise, not substring")
	}
	// Under: /home/u/workshop is not under /home/u/work.
	r = NewResolver([]Rule{{Under: []string{"/home/u/work"}, Use: "w"}})
	if _, ok := r.Resolve("/home/u/workshop"); ok {
		t.Error("under must be segment-wise, not prefix-substring")
	}
	if _, ok := r.Resolve("/home/u/work"); !ok {
		t.Error("under should match the directory itself")
	}
}

func TestRepoRoot(t *testing.T) {
	if got := RepoRoot("/home/u/repo/.git"); got != filepath.FromSlash("/home/u/repo") {
		t.Errorf("RepoRoot trailing .git = %q, want /home/u/repo", got)
	}
	if got := RepoRoot("/home/u/repo"); got != "/home/u/repo" {
		t.Errorf("RepoRoot no .git = %q, want unchanged", got)
	}
}

func TestResolverCanonicalizesRulePaths(t *testing.T) {
	// A symlinked rule path resolves to the same canonical form as the workspace
	// root, so the rule still matches through the symlink.
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	r := NewResolver([]Rule{{Repos: []string{link}, Use: "s"}})
	if _, ok := r.Resolve(Canonicalize(real)); !ok {
		t.Error("symlinked rule path should resolve to the real root")
	}
}
