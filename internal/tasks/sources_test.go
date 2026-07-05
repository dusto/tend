package tasks

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dusto/tend/api"
)

func TestSourcesConfigValidate(t *testing.T) {
	beadsSrc := SourceDef{Name: "central", Type: SourceBeads, Dir: "/plan"}
	tests := []struct {
		name    string
		cfg     SourcesConfig
		wantErr string // substring; "" means no error
	}{
		{
			name: "named source with a rule ok",
			cfg: SourcesConfig{
				Sources: []SourceDef{beadsSrc},
				Rules:   []MappingRule{{Repos: []string{"/x/tend"}, Use: "central"}},
			},
		},
		{
			name:    "source missing name",
			cfg:     SourcesConfig{Sources: []SourceDef{{Type: SourceBeads, Dir: "/p"}}},
			wantErr: "name is required",
		},
		{
			name: "duplicate source name",
			cfg: SourcesConfig{Sources: []SourceDef{
				{Name: "a", Type: SourceBeads, Dir: "/p"},
				{Name: "a", Type: SourceBeads, Dir: "/q"},
			}},
			wantErr: "duplicate task source name",
		},
		{
			name:    "unknown source type",
			cfg:     SourcesConfig{Sources: []SourceDef{{Name: "a", Type: "github", Dir: "/p"}}},
			wantErr: `unknown type "github"`,
		},
		{
			name:    "beads source missing dir",
			cfg:     SourcesConfig{Sources: []SourceDef{{Name: "a", Type: SourceBeads}}},
			wantErr: "dir is required",
		},
		{
			name: "rule without repos or under",
			cfg: SourcesConfig{
				Sources: []SourceDef{beadsSrc},
				Rules:   []MappingRule{{Use: "central"}},
			},
			wantErr: "at least one of repos or under",
		},
		{
			name: "rule use references unknown source",
			cfg: SourcesConfig{
				Sources: []SourceDef{beadsSrc},
				Rules:   []MappingRule{{Repos: []string{"/x/tend"}, Use: "nope"}},
			},
			wantErr: `unknown source "nope"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestFactoryResolvesNamedSourceByExactRepo(t *testing.T) {
	cfg := SourcesConfig{
		Sources: []SourceDef{
			{Name: "tend", Type: SourceBeads, Dir: "/planning/tend"},
			{Name: "acme", Type: SourceBeads, Dir: "/planning/acme"},
		},
		Rules: []MappingRule{
			{Repos: []string{"/w/tend", "/w/tend.nvim"}, Use: "tend"},
			{Under: []string{"/home/u/acme"}, Use: "acme"},
		},
	}
	factory := cfg.Factory()

	// Exact repo -> its named source; provider name is the source name so the
	// two backlogs are distinguishable.
	p := factory(api.WorkspaceID("/w/tend.nvim/.git"))
	beadsSource(t, p, "tend", "/planning/tend")

	// under-prefix: any repo beneath /home/u/acme.
	p = factory(api.WorkspaceID("/home/u/acme/widgets/.git"))
	beadsSource(t, p, "acme", "/planning/acme")
}

func TestFactoryExactRepoBeatsUnderPrefix(t *testing.T) {
	cfg := SourcesConfig{
		Sources: []SourceDef{
			{Name: "grouped", Type: SourceBeads, Dir: "/planning/group"},
			{Name: "special", Type: SourceBeads, Dir: "/planning/special"},
		},
		Rules: []MappingRule{
			{Under: []string{"/home/u/work"}, Use: "grouped"},
			{Repos: []string{"/home/u/work/special"}, Use: "special"},
		},
	}
	// The repo is both under /home/u/work and an exact repos entry; exact wins.
	p := cfg.Factory()(api.WorkspaceID("/home/u/work/special/.git"))
	beadsSource(t, p, "special", "/planning/special")
}

func TestFactoryCanonicalizesRulePaths(t *testing.T) {
	// The workspace root is symlink-resolved (as WorkspaceID is), but the rule
	// is configured through a symlink to the same repo; it must still match.
	realRoot, err := filepath.EvalSymlinks(t.TempDir()) // as a WorkspaceID is
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cfg := SourcesConfig{
		Sources: []SourceDef{{Name: "central", Type: SourceBeads, Dir: "/planning"}},
		Rules:   []MappingRule{{Repos: []string{link}, Use: "central"}},
	}
	// WorkspaceID carries the resolved (real) path.
	p := cfg.Factory()(api.WorkspaceID(filepath.Join(realRoot, ".git")))
	beadsSource(t, p, "central", "/planning")
}

func TestFactoryMultipleSourcesShareDirDistinctLabels(t *testing.T) {
	// One planning repo backs two code repos: two named sources over the same dir,
	// each scoped to its own label. They must resolve independently and stay
	// distinguishable by provider name.
	cfg := SourcesConfig{
		Sources: []SourceDef{
			{Name: "tend", Type: SourceBeads, Dir: "/planning", Labels: []string{"repo:tend"}},
			{Name: "nvim", Type: SourceBeads, Dir: "/planning", Labels: []string{"repo:nvim"}},
		},
		Rules: []MappingRule{
			{Repos: []string{"/w/tend"}, Use: "tend"},
			{Repos: []string{"/w/tend.nvim"}, Use: "nvim"},
		},
	}
	factory := cfg.Factory()

	tend := factory(api.WorkspaceID("/w/tend/.git")).(*Beads)
	if tend.name != "tend" || tend.dir != "/planning" || !slices.Equal(tend.labels, []string{"repo:tend"}) {
		t.Fatalf("tend source = %+v", tend)
	}
	nvim := factory(api.WorkspaceID("/w/tend.nvim/.git")).(*Beads)
	if nvim.name != "nvim" || nvim.dir != "/planning" || !slices.Equal(nvim.labels, []string{"repo:nvim"}) {
		t.Fatalf("nvim source = %+v", nvim)
	}
}

func TestFactoryInRepoDefaultWhenDotBeadsPresent(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	// No rule matches -> the repo's own .beads is used, running bd in the root.
	p := SourcesConfig{}.Factory()(api.WorkspaceID(filepath.Join(root, ".git")))
	beadsSource(t, p, "beads", root)
}

func TestFactoryNoSourceYieldsEmptyProvider(t *testing.T) {
	// A repo with no matching rule and no in-tree .beads.
	root := t.TempDir()
	p := SourcesConfig{}.Factory()(api.WorkspaceID(filepath.Join(root, ".git")))

	if got := p.Name(); got != "none" {
		t.Fatalf("Name() = %q, want none", got)
	}
	got, err := p.List(context.Background(), Filter{})
	if err != nil || len(got) != 0 {
		t.Fatalf("List() = %v, %v; want empty, nil", got, err)
	}
	if _, err := p.Create(context.Background(), CreateParams{Title: "x"}); err == nil {
		t.Fatalf("Create() error = nil, want a no-source error")
	}
}

func TestPathMatchers(t *testing.T) {
	if !pathEqual("/a/b/tend", "/a/b/tend") {
		t.Error("pathEqual identical should be true")
	}
	if pathEqual("/a/b/tend", "/a/b/nettend") {
		t.Error("pathEqual must be segment-wise, not substring")
	}
	if !pathUnder("/home/u/work/x", "/home/u/work") {
		t.Error("pathUnder descendant should be true")
	}
	if !pathUnder("/home/u/work", "/home/u/work") {
		t.Error("pathUnder self should be true")
	}
	if pathUnder("/home/u/workshop", "/home/u/work") {
		t.Error("pathUnder must be segment-wise, not prefix-substring")
	}
}

// beadsSource asserts p is a beads provider with the given name and dir.
func beadsSource(t *testing.T, p Provider, name, dir string) {
	t.Helper()
	b, ok := p.(*Beads)
	if !ok {
		t.Fatalf("provider is %T, want *Beads", p)
	}
	if b.name != name {
		t.Fatalf("beads name = %q, want %q", b.name, name)
	}
	if b.dir != dir {
		t.Fatalf("beads dir = %q, want %q", b.dir, dir)
	}
}
