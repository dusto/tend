package memory

import (
	"fmt"

	"github.com/dusto/tend/api"
	"github.com/dusto/tend/internal/wsrules"
)

// Source type identifiers for a SourceDef.
const (
	// SourceFile is a directory of markdown memory files (the default backend).
	SourceFile = "file"
)

// SourcesConfig is the [memory] section of the TEND config: named memory sources
// plus rules mapping workspaces to them, mirroring task sources. A workspace
// resolves to the source of the first rule whose repos/under matches its repo
// root; with no match it uses its own in-tree memory directory
// (<repo>/.tend/memory). This only changes where a workspace's memory is stored;
// the pluggable-backend contract behind memory.* is unchanged.
type SourcesConfig struct {
	// Sources are the named memory stores ([[memory.sources]]).
	Sources []SourceDef `toml:"sources"`
	// Rules map workspaces to a source by name ([[memory.rules]]).
	Rules []MappingRule `toml:"rules"`
}

// SourceDef defines one named memory source.
type SourceDef struct {
	// Name is the source identity referenced by a rule's use.
	Name string `toml:"name"`
	// Type selects the backend (currently "file").
	Type string `toml:"type"`
	// Dir is the directory holding the markdown memory files. Several workspaces
	// can share one dir (a central memory repo) or map to distinct dirs.
	Dir string `toml:"dir"`
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
// config typo fails loudly at load rather than at first memory request.
func (c SourcesConfig) Validate() error {
	names := make(map[string]struct{}, len(c.Sources))
	for i := range c.Sources {
		s := &c.Sources[i]
		if s.Name == "" {
			return fmt.Errorf("config: memory.sources[%d]: name is required", i)
		}
		if _, dup := names[s.Name]; dup {
			return fmt.Errorf("config: duplicate memory source name %q", s.Name)
		}
		names[s.Name] = struct{}{}
		if s.Type != SourceFile {
			return fmt.Errorf("config: memory source %q: unknown type %q", s.Name, s.Type)
		}
		if s.Dir == "" {
			return fmt.Errorf("config: memory source %q: dir is required", s.Name)
		}
	}
	for i := range c.Rules {
		r := &c.Rules[i]
		if len(r.Repos) == 0 && len(r.Under) == 0 {
			return fmt.Errorf("config: memory.rules[%d]: needs at least one of repos or under", i)
		}
		if r.Use == "" {
			return fmt.Errorf("config: memory.rules[%d]: use is required", i)
		}
		if _, ok := names[r.Use]; !ok {
			return fmt.Errorf("config: memory.rules[%d]: use references unknown source %q", i, r.Use)
		}
	}
	return nil
}

// Factory returns the per-workspace memory Factory these rules describe: a
// workspace resolves to a rule's named source directory, else its own in-tree
// memory directory (the InRepoFactory default). The result is always a working
// provider, so memory.* never fails for lack of config.
func (c SourcesConfig) Factory() Factory {
	byName := make(map[string]SourceDef, len(c.Sources))
	for _, s := range c.Sources {
		byName[s.Name] = s
	}
	resolver := wsrules.NewResolver(mappingRules(c.Rules))
	return func(ws api.WorkspaceID) Provider {
		root := wsrules.RepoRoot(string(ws))
		if use, ok := resolver.Resolve(root); ok {
			return NewFileProvider(ws, byName[use].Dir)
		}
		return InRepoFactory(ws)
	}
}

// mappingRules converts the config rules to the shared resolver's rule shape.
func mappingRules(rules []MappingRule) []wsrules.Rule {
	out := make([]wsrules.Rule, len(rules))
	for i, r := range rules {
		out[i] = wsrules.Rule{Repos: r.Repos, Under: r.Under, Use: r.Use}
	}
	return out
}
