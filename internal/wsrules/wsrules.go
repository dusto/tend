// Package wsrules resolves a workspace's repository root to a named target via
// repos/under rules. It is the shared matching engine behind config-driven source
// resolution (task sources, memory sources): each caller keeps its own TOML config
// structs and target types, and delegates the path matching here so the symlink
// canonicalization and exact-over-prefix precedence live in one place.
package wsrules

import (
	"path/filepath"
	"strings"
)

// Rule maps a set of repositories to a named target. Repos are exact repository
// roots; Under are parent directories any repository beneath which applies. Use
// is the target name the matching repositories resolve to.
type Rule struct {
	Repos []string
	Under []string
	Use   string
}

// resolved is a Rule with its paths canonicalized, so they compare against the
// workspace root in the same symlink-resolved form.
type resolved struct {
	repos []string
	under []string
	use   string
}

// Resolver maps a repository root to a target name via canonicalized rules. Exact
// repo matches take precedence over under-prefix matches; within a tier the first
// rule wins. It is immutable after construction and safe for concurrent use.
type Resolver struct {
	rules []resolved
}

// NewResolver canonicalizes the rules once (WorkspaceID roots are symlink-resolved,
// so config paths must be too or a symlinked repo/under would silently miss).
func NewResolver(rules []Rule) *Resolver {
	out := make([]resolved, len(rules))
	for i, r := range rules {
		out[i] = resolved{
			repos: canonicalizeAll(r.Repos),
			under: canonicalizeAll(r.Under),
			use:   r.Use,
		}
	}
	return &Resolver{rules: out}
}

// Resolve returns the target name a repository root maps to, or ok=false when no
// rule matches. Exact repos matches beat under-prefix matches; within a tier the
// first rule wins.
func (r *Resolver) Resolve(root string) (string, bool) {
	for i := range r.rules {
		if matchesAny(root, r.rules[i].repos, pathEqual) {
			return r.rules[i].use, true
		}
	}
	for i := range r.rules {
		if matchesAny(root, r.rules[i].under, pathUnder) {
			return r.rules[i].use, true
		}
	}
	return "", false
}

// RepoRoot returns the worktree root for a WorkspaceID: it is the canonical path
// of the common git dir, so a trailing ".git" segment names the root's worktree.
func RepoRoot(ws string) string {
	if filepath.Base(ws) == ".git" {
		return filepath.Dir(ws)
	}
	return ws
}

// canonicalizeAll resolves each path to the symlink-free, absolute form used for
// workspace roots.
func canonicalizeAll(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = Canonicalize(p)
	}
	return out
}

// Canonicalize resolves p to its symlink-free absolute path, matching how a
// WorkspaceID is derived; it falls back to the absolute cleaned path when p
// cannot be resolved (e.g. does not exist yet).
func Canonicalize(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return filepath.Clean(p)
}

// matchesAny reports whether root satisfies pred against any candidate.
func matchesAny(root string, candidates []string, pred func(root, candidate string) bool) bool {
	for _, c := range candidates {
		if pred(root, c) {
			return true
		}
	}
	return false
}

// pathEqual reports whether root and candidate name the same path (segment-wise,
// not substring).
func pathEqual(root, candidate string) bool {
	return segEqual(segments(root), segments(candidate))
}

// pathUnder reports whether root is candidate or a descendant of it (segment-wise,
// not prefix-substring).
func pathUnder(root, candidate string) bool {
	rs, cs := segments(root), segments(candidate)
	if len(cs) == 0 || len(cs) > len(rs) {
		return false
	}
	return segEqual(rs[:len(cs)], cs)
}

func segEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// segments splits a path into its non-empty, slash-separated components.
func segments(p string) []string {
	var out []string
	for s := range strings.SplitSeq(filepath.ToSlash(filepath.Clean(p)), "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
