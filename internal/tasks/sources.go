package tasks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dusto/tend/api"
)

// Source type identifiers for a SourceDef.
const (
	// SourceBeads is a beads-backed source (bd subprocess over a .beads dir).
	SourceBeads = "beads"
)

// SourcesConfig is the [tasks] section of the TEND config: named backlog sources
// plus rules mapping workspaces to them. A workspace resolves to the source of
// the first rule whose repos/under matches its repo root; with no match it uses
// its own in-tree source (e.g. a .beads directory), else no source at all.
type SourcesConfig struct {
	// Sources are the named backlogs ([[tasks.sources]]).
	Sources []SourceDef `toml:"sources"`
	// Rules map workspaces to a source by name ([[tasks.rules]]).
	Rules []MappingRule `toml:"rules"`
}

// SourceDef defines one named backlog source.
type SourceDef struct {
	// Name is the source identity, carried in TaskRef.Provider so several
	// backlogs are distinguishable.
	Name string `toml:"name"`
	// Type selects the backend (currently "beads").
	Type string `toml:"type"`
	// Dir is the beads working directory (a planning repo whose .beads holds the
	// backlog).
	Dir string `toml:"dir"`
	// Labels optionally scopes the source to a subset of the backlog: only tasks
	// carrying all of them are listed, and new tasks are tagged with them. This
	// lets one planning repo back several code repos that each see their slice
	// (e.g. labels = ["repo:tend"]).
	Labels []string `toml:"labels"`
}

// MappingRule maps a set of repositories to a named source.
type MappingRule struct {
	// Repos are exact repository roots this rule applies to.
	Repos []string `toml:"repos"`
	// Under are parent directories: any repository beneath one applies.
	Under []string `toml:"under"`
	// Use is the name of the source these repositories resolve to.
	Use string `toml:"use"`
}

// Validate checks the sources and rules and reports the first problem, so a
// config typo fails loudly at load rather than at first task request.
func (c SourcesConfig) Validate() error {
	names := make(map[string]struct{}, len(c.Sources))
	for i := range c.Sources {
		s := &c.Sources[i]
		if s.Name == "" {
			return fmt.Errorf("config: tasks.sources[%d]: name is required", i)
		}
		if _, dup := names[s.Name]; dup {
			return fmt.Errorf("config: duplicate task source name %q", s.Name)
		}
		names[s.Name] = struct{}{}
		if s.Type != SourceBeads {
			return fmt.Errorf("config: task source %q: unknown type %q", s.Name, s.Type)
		}
		if s.Dir == "" {
			return fmt.Errorf("config: task source %q: dir is required", s.Name)
		}
	}
	for i := range c.Rules {
		r := &c.Rules[i]
		if len(r.Repos) == 0 && len(r.Under) == 0 {
			return fmt.Errorf("config: tasks.rules[%d]: needs at least one of repos or under", i)
		}
		if r.Use == "" {
			return fmt.Errorf("config: tasks.rules[%d]: use is required", i)
		}
		if _, ok := names[r.Use]; !ok {
			return fmt.Errorf("config: tasks.rules[%d]: use references unknown source %q", i, r.Use)
		}
	}
	return nil
}

// resolvedRule is a MappingRule with its paths canonicalized, so they compare
// against the workspace root in the same symlink-resolved form.
type resolvedRule struct {
	repos []string
	under []string
	use   string
}

// Factory returns the per-workspace task Factory these rules describe: a
// workspace resolves to a rule's named source, else its own in-tree source, else
// an empty provider (an empty queue whose writes report that none is configured).
func (c SourcesConfig) Factory() Factory {
	byName := make(map[string]SourceDef, len(c.Sources))
	for _, s := range c.Sources {
		byName[s.Name] = s
	}
	// Canonicalize rule paths once: WorkspaceID is symlink-resolved, so config
	// paths must be too or a symlinked repo/under would silently miss its rule.
	rules := make([]resolvedRule, len(c.Rules))
	for i := range c.Rules {
		rules[i] = resolvedRule{
			repos: canonicalizeAll(c.Rules[i].Repos),
			under: canonicalizeAll(c.Rules[i].Under),
			use:   c.Rules[i].Use,
		}
	}
	return func(ws api.WorkspaceID) Provider {
		root := repoRoot(ws)
		if def, ok := resolve(root, rules, byName); ok {
			return newSource(ws, def)
		}
		if def, ok := detectInRepoSource(root); ok {
			return newSource(ws, def)
		}
		return emptyProvider{ws: ws}
	}
}

// resolve returns the source a repo root maps to via the rules. Exact repos
// matches take precedence over under-prefix matches; within a tier the first
// rule wins.
func resolve(root string, rules []resolvedRule, byName map[string]SourceDef) (SourceDef, bool) {
	for i := range rules {
		if matchesAny(root, rules[i].repos, pathEqual) {
			return byName[rules[i].use], true
		}
	}
	for i := range rules {
		if matchesAny(root, rules[i].under, pathUnder) {
			return byName[rules[i].use], true
		}
	}
	return SourceDef{}, false
}

// canonicalizeAll resolves each path to the symlink-free, absolute form used for
// workspace roots. A path that cannot be resolved (e.g. does not exist yet)
// falls back to its absolute, cleaned form.
func canonicalizeAll(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = canonicalize(p)
	}
	return out
}

// canonicalize resolves p to its symlink-free absolute path, matching how a
// WorkspaceID is derived; it falls back to the absolute cleaned path when p
// cannot be resolved.
func canonicalize(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return filepath.Clean(p)
}

// matchesAny reports whether root satisfies pred against any of candidates.
func matchesAny(root string, candidates []string, pred func(root, candidate string) bool) bool {
	for _, c := range candidates {
		if pred(root, c) {
			return true
		}
	}
	return false
}

// pathEqual reports whether root and candidate name the same path.
func pathEqual(root, candidate string) bool {
	return segEqual(segments(root), segments(candidate))
}

// pathUnder reports whether root is candidate or a descendant of it.
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

// detectInRepoSource reports the repository's own task source: a beads source
// when the repo root holds a .beads directory. It is the resolver's in-repo
// fallback, kept separate so each backend's native-source detection lives in one
// place.
func detectInRepoSource(root string) (SourceDef, bool) {
	if info, err := os.Stat(filepath.Join(root, ".beads")); err == nil && info.IsDir() {
		return SourceDef{Name: SourceBeads, Type: SourceBeads, Dir: root}, true
	}
	return SourceDef{}, false
}

// newSource builds the provider for one resolved source.
func newSource(ws api.WorkspaceID, def SourceDef) Provider {
	switch def.Type {
	case SourceBeads:
		return NewBeads(def.Name, ws, def.Dir, def.Labels...)
	default:
		// Validate rejects unknown types; guard defensively.
		return emptyProvider{ws: ws}
	}
}

// repoRoot is the workspace's repository root: the WorkspaceID is the canonical
// path of the common git dir, so a trailing ".git" segment names the root's
// worktree.
func repoRoot(ws api.WorkspaceID) string {
	p := string(ws)
	if filepath.Base(p) == ".git" {
		return filepath.Dir(p)
	}
	return p
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

// emptyProvider is the resolved provider for a workspace with no task source:
// listing yields an empty queue and every write reports that no source is
// configured, so task.* degrades cleanly instead of forcing a backlog on every
// workspace.
type emptyProvider struct{ ws api.WorkspaceID }

func (emptyProvider) Name() string { return "none" }

func (p emptyProvider) err() error {
	return fmt.Errorf("tasks: no source configured for workspace %s", p.ws)
}

func (p emptyProvider) Create(context.Context, CreateParams) (Task, error) {
	return Task{}, p.err()
}

func (p emptyProvider) Show(context.Context, api.TaskRef) (Task, error) {
	return Task{}, p.err()
}

func (emptyProvider) List(context.Context, Filter) ([]Task, error) { return nil, nil }

func (p emptyProvider) Claim(context.Context, api.TaskRef, string) error { return p.err() }

func (p emptyProvider) Comment(context.Context, api.TaskRef, Comment) error { return p.err() }

func (p emptyProvider) Close(context.Context, api.TaskRef) error { return p.err() }

func (p emptyProvider) Link(context.Context, api.TaskRef, api.TaskRef, LinkType) error {
	return p.err()
}

func (emptyProvider) Events(ctx context.Context) (<-chan Event, error) {
	ch := make(chan Event)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}
