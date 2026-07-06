package memory

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dusto/tend/api"
)

func TestSourcesConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     SourcesConfig
		wantErr bool
	}{
		{"empty is valid", SourcesConfig{}, false},
		{
			"valid source + rule",
			SourcesConfig{
				Sources: []SourceDef{{Name: "central", Type: SourceFile, Dir: "/mem"}},
				Rules:   []MappingRule{{Under: []string{"/work"}, Use: "central"}},
			},
			false,
		},
		{"source needs name", SourcesConfig{Sources: []SourceDef{{Type: SourceFile, Dir: "/m"}}}, true},
		{"unknown type", SourcesConfig{Sources: []SourceDef{{Name: "s", Type: "vector", Dir: "/m"}}}, true},
		{"source needs dir", SourcesConfig{Sources: []SourceDef{{Name: "s", Type: SourceFile}}}, true},
		{
			"duplicate name",
			SourcesConfig{Sources: []SourceDef{
				{Name: "s", Type: SourceFile, Dir: "/a"},
				{Name: "s", Type: SourceFile, Dir: "/b"},
			}},
			true,
		},
		{
			"rule needs repos or under",
			SourcesConfig{
				Sources: []SourceDef{{Name: "s", Type: SourceFile, Dir: "/m"}},
				Rules:   []MappingRule{{Use: "s"}},
			},
			true,
		},
		{
			"rule use must exist",
			SourcesConfig{
				Sources: []SourceDef{{Name: "s", Type: SourceFile, Dir: "/m"}},
				Rules:   []MappingRule{{Under: []string{"/w"}, Use: "nope"}},
			},
			true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.cfg.Validate(); (err != nil) != c.wantErr {
				t.Errorf("Validate() err = %v, wantErr = %v", err, c.wantErr)
			}
		})
	}
}

// dirOf returns the directory a resolved FileProvider reads.
func dirOf(t *testing.T, p Provider) string {
	t.Helper()
	fp, ok := p.(*FileProvider)
	if !ok {
		t.Fatalf("provider is %T, want *FileProvider", p)
	}
	return fp.dir
}

func TestFactoryResolvesNamedSourceByUnder(t *testing.T) {
	cfg := SourcesConfig{
		Sources: []SourceDef{{Name: "central", Type: SourceFile, Dir: "/shared/mem"}},
		Rules:   []MappingRule{{Under: []string{"/home/u/work"}, Use: "central"}},
	}
	p := cfg.Factory()(api.WorkspaceID("/home/u/work/repo/.git"))
	if got := dirOf(t, p); got != "/shared/mem" {
		t.Errorf("resolved dir = %q, want /shared/mem", got)
	}
}

func TestFactoryExactRepoBeatsUnder(t *testing.T) {
	cfg := SourcesConfig{
		Sources: []SourceDef{
			{Name: "shared", Type: SourceFile, Dir: "/shared/mem"},
			{Name: "special", Type: SourceFile, Dir: "/special/mem"},
		},
		Rules: []MappingRule{
			{Under: []string{"/home/u/work"}, Use: "shared"},
			{Repos: []string{"/home/u/work/special"}, Use: "special"},
		},
	}
	p := cfg.Factory()(api.WorkspaceID("/home/u/work/special/.git"))
	if got := dirOf(t, p); got != "/special/mem" {
		t.Errorf("resolved dir = %q, want /special/mem (exact repo wins)", got)
	}
}

func TestFactoryFallsBackToInRepoWhenNoRuleMatches(t *testing.T) {
	cfg := SourcesConfig{
		Sources: []SourceDef{{Name: "central", Type: SourceFile, Dir: "/shared/mem"}},
		Rules:   []MappingRule{{Under: []string{"/other"}, Use: "central"}},
	}
	root := "/home/u/work/repo"
	p := cfg.Factory()(api.WorkspaceID(filepath.Join(root, ".git")))
	// No rule matches: default to the workspace's own .tend/memory.
	if got, want := dirOf(t, p), filepath.Join(root, ".tend", "memory"); got != want {
		t.Errorf("resolved dir = %q, want in-repo default %q", got, want)
	}
}

func TestEmptyConfigIsInRepoDefault(t *testing.T) {
	root := "/home/u/work/repo"
	p := SourcesConfig{}.Factory()(api.WorkspaceID(filepath.Join(root, ".git")))
	if got, want := dirOf(t, p), filepath.Join(root, ".tend", "memory"); got != want {
		t.Errorf("empty-config dir = %q, want %q", got, want)
	}
}

func TestFactoryProviderIsUsable(t *testing.T) {
	// A resolved provider actually reads/writes its configured dir.
	dir := t.TempDir()
	cfg := SourcesConfig{
		Sources: []SourceDef{{Name: "c", Type: SourceFile, Dir: dir}},
		Rules:   []MappingRule{{Under: []string{"/home/u/work"}, Use: "c"}},
	}
	p := cfg.Factory()(api.WorkspaceID("/home/u/work/repo/.git"))
	if _, err := p.Write(context.Background(), api.MemoryWriteParams{WorkspaceID: "ws1", ID: "n", Text: "hi"}); err != nil {
		t.Fatalf("write through resolved provider: %v", err)
	}
	if _, err := p.Get(context.Background(), "n"); err != nil {
		t.Errorf("get after write: %v", err)
	}
}
