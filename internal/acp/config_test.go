package acp

import (
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/BurntSushi/toml"
)

// planExamples is the provider config from tend-plan.md § ACP Provider Model.
const planExamples = `
[[acp.providers]]
id = "codex"
command = "codex"
args = ["acp"]
env = []
cwd_mode = "workspace"
enabled = true

[[acp.providers]]
id = "claude"
command = "claude-agent-acp"
args = []
env = []
cwd_mode = "workspace"
enabled = true

[[acp.providers]]
id = "kiro"
command = "kiro-cli"
args = ["acp", "-a"]
env = []
cwd_mode = "workspace"
enabled = true
`

func TestParsePlanExamples(t *testing.T) {
	cfg, err := Parse([]byte(planExamples))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cfg.ACP.Providers) != 3 {
		t.Fatalf("got %d providers, want 3", len(cfg.ACP.Providers))
	}
	kiro, ok := cfg.Provider("kiro")
	if !ok {
		t.Fatal("kiro provider not found")
	}
	if kiro.Command != "kiro-cli" || !slices.Equal(kiro.Args, []string{"acp", "-a"}) {
		t.Errorf("kiro = %+v", kiro)
	}
	if kiro.CwdMode != CwdWorkspace || !kiro.Enabled {
		t.Errorf("kiro cwd_mode/enabled = %q/%v", kiro.CwdMode, kiro.Enabled)
	}
}

func TestParseRoundTrips(t *testing.T) {
	cfg, err := Parse([]byte(planExamples))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := Parse(out)
	if err != nil {
		t.Fatalf("re-Parse: %v", err)
	}
	if !reflect.DeepEqual(cfg, got) {
		t.Errorf("round-trip mismatch:\nfirst:  %+v\nsecond: %+v", cfg, got)
	}
}

func TestCwdModeDefaultsToWorkspace(t *testing.T) {
	cfg, err := Parse([]byte(`[[acp.providers]]
id = "x"
command = "x-cli"`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.ACP.Providers[0].CwdMode != CwdWorkspace {
		t.Errorf("default cwd_mode = %q, want workspace", cfg.ACP.Providers[0].CwdMode)
	}
}

func TestParseValidationErrors(t *testing.T) {
	cases := map[string]string{
		"missing id": `[[acp.providers]]
command = "x"`,
		"missing command": `[[acp.providers]]
id = "x"`,
		"duplicate id": `[[acp.providers]]
id = "x"
command = "a"
[[acp.providers]]
id = "x"
command = "b"`,
		"bad cwd_mode": `[[acp.providers]]
id = "x"
command = "a"
cwd_mode = "moon"`,
		"unknown key": `[[acp.providers]]
id = "x"
command = "a"
bogus = 1`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(src)); err == nil {
				t.Errorf("expected error for %s", name)
			}
		})
	}
}

func TestEnabledProviders(t *testing.T) {
	cfg, err := Parse([]byte(`[[acp.providers]]
id = "on"
command = "a"
enabled = true
[[acp.providers]]
id = "off"
command = "b"
enabled = false`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	enabled := cfg.EnabledProviders()
	if len(enabled) != 1 || enabled[0].ID != "on" {
		t.Errorf("EnabledProviders = %+v, want [on]", enabled)
	}
}

func TestProviderCommand(t *testing.T) {
	p := Provider{
		ID:      "codex",
		Command: "codex",
		Args:    []string{"acp"},
		Env:     []string{"TEND_TEST=1"},
		CwdMode: CwdWorkspace,
	}
	cmd := p.LaunchCommand("/repo")
	if cmd.Path != "codex" || !slices.Equal(cmd.Args, []string{"acp"}) {
		t.Errorf("cmd = %+v", cmd)
	}
	if cmd.Dir != "/repo" {
		t.Errorf("Dir = %q, want /repo", cmd.Dir)
	}
	if !slices.Contains(cmd.Env, "TEND_TEST=1") {
		t.Error("Env missing the provider's entry")
	}
	if len(cmd.Env) != len(os.Environ())+1 {
		t.Errorf("Env len = %d, want ambient+1 (%d)", len(cmd.Env), len(os.Environ())+1)
	}

	// inherit mode: no working directory override, no env override.
	inherit := Provider{ID: "x", Command: "x", CwdMode: CwdInherit}.LaunchCommand("/repo")
	if inherit.Dir != "" {
		t.Errorf("inherit Dir = %q, want empty", inherit.Dir)
	}
	if inherit.Env != nil {
		t.Errorf("inherit Env = %v, want nil (inherit ambient)", inherit.Env)
	}
}
