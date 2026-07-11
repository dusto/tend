package acp

import (
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/dusto/tend/internal/summarize"
)

// planExamples is a three-provider config covering Codex, Claude, and Kiro.
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

func TestParseTasksSection(t *testing.T) {
	cfg, err := Parse([]byte(`[[acp.providers]]
id = "x"
command = "x-cli"

[[tasks.sources]]
name = "tend"
type = "beads"
dir = "/planning"

[[tasks.rules]]
repos = ["/w/tend", "/w/tend.nvim"]
use = "tend"`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cfg.Tasks.Sources) != 1 || cfg.Tasks.Sources[0].Name != "tend" || cfg.Tasks.Sources[0].Dir != "/planning" {
		t.Fatalf("parsed tasks sources = %+v, want one named tend dir=/planning", cfg.Tasks.Sources)
	}
	if len(cfg.Tasks.Rules) != 1 || cfg.Tasks.Rules[0].Use != "tend" || len(cfg.Tasks.Rules[0].Repos) != 2 {
		t.Fatalf("parsed tasks rule = %+v, want use=tend with two repos", cfg.Tasks.Rules)
	}
}

func TestParseMemorySection(t *testing.T) {
	cfg, err := Parse([]byte(`[[acp.providers]]
id = "x"
command = "x-cli"

[[memory.sources]]
name = "central"
type = "file"
dir = "/shared/mem"

[[memory.rules]]
under = ["/w"]
use = "central"`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cfg.Memory.Sources) != 1 || cfg.Memory.Sources[0].Name != "central" || cfg.Memory.Sources[0].Dir != "/shared/mem" {
		t.Fatalf("parsed memory sources = %+v, want one named central dir=/shared/mem", cfg.Memory.Sources)
	}
	if len(cfg.Memory.Rules) != 1 || cfg.Memory.Rules[0].Use != "central" {
		t.Fatalf("parsed memory rule = %+v, want use=central", cfg.Memory.Rules)
	}
}

func TestParseMemorySectionInvalid(t *testing.T) {
	// An invalid memory-source section fails the whole config load, loudly.
	_, err := Parse([]byte(`[[acp.providers]]
id = "x"
command = "x-cli"

[[memory.rules]]
under = ["/w"]
use = "ghost"`))
	if err == nil {
		t.Fatal("expected error for a memory rule referencing an undefined source")
	}
}

func TestParseTasksSectionInvalid(t *testing.T) {
	// An invalid task-source section fails the whole config load, loudly.
	_, err := Parse([]byte(`[[acp.providers]]
id = "x"
command = "x-cli"

[[tasks.rules]]
repos = ["/w/tend"]
use = "ghost"`))
	if err == nil {
		t.Fatal("expected error for a rule referencing an undefined source")
	}
}

func TestParseSummarizeSection(t *testing.T) {
	cfg, err := Parse([]byte(`[[acp.providers]]
id = "claude"
command = "claude-agent-acp"
enabled = true

[summarize]
backend = "acp"
target_chars = 1500

[summarize.acp]
provider = "claude"
model = "opus"`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Summarize.Backend != summarize.BackendACP || cfg.Summarize.TargetChars != 1500 {
		t.Fatalf("parsed summarize = %+v", cfg.Summarize)
	}
	if cfg.Summarize.ACP.Provider != "claude" || cfg.Summarize.ACP.Model != "opus" {
		t.Fatalf("parsed summarize.acp = %+v", cfg.Summarize.ACP)
	}
}

func TestSummarizeDefaultsToNone(t *testing.T) {
	// With no [summarize] section the backend defaults to none, so the fallback
	// summarizer is always available.
	cfg, err := Parse([]byte(`[[acp.providers]]
id = "x"
command = "x-cli"`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Summarize.Backend != summarize.BackendNone {
		t.Errorf("default backend = %q, want none", cfg.Summarize.Backend)
	}
}

func TestSummarizeACPProviderCrossCheck(t *testing.T) {
	// The acp summarizer backend must name an enabled, configured provider — a
	// cross-check the summarize package cannot do on its own.
	cases := map[string]string{
		"unknown provider": `[[acp.providers]]
id = "claude"
command = "claude-agent-acp"
enabled = true
[summarize]
backend = "acp"
[summarize.acp]
provider = "ghost"`,
		"disabled provider": `[[acp.providers]]
id = "codex"
command = "codex-acp"
enabled = false
[summarize]
backend = "acp"
[summarize.acp]
provider = "codex"`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(src)); err == nil {
				t.Errorf("expected error for %s", name)
			}
		})
	}
}

func TestParseCompactSection(t *testing.T) {
	cfg, err := Parse([]byte(`[[acp.providers]]
id = "claude"
command = "claude-agent-acp"
enabled = true

[compact]
enabled = true
threshold = 0.9
budget = 1200`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !cfg.Compact.Enabled || cfg.Compact.Threshold != 0.9 || cfg.Compact.Budget != 1200 {
		t.Fatalf("parsed compact = %+v", cfg.Compact)
	}
	if got := cfg.Compact.EffectiveThreshold(); got != 0.9 {
		t.Errorf("EffectiveThreshold = %v, want 0.9", got)
	}
}

func TestCompactDefaults(t *testing.T) {
	// With no [compact] section the trigger is off and an unset threshold resolves
	// to the default when the section is later enabled.
	cfg, err := Parse([]byte(`[[acp.providers]]
id = "x"
command = "x-cli"`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Compact.Enabled {
		t.Error("compact should be disabled by default")
	}
	if got := cfg.Compact.EffectiveThreshold(); got != DefaultCompactThreshold {
		t.Errorf("EffectiveThreshold = %v, want default %v", got, DefaultCompactThreshold)
	}
}

func TestParseCompactValidationErrors(t *testing.T) {
	cases := map[string]string{
		"threshold too high": `[[acp.providers]]
id = "x"
command = "a"
[compact]
threshold = 1.5`,
		"negative threshold": `[[acp.providers]]
id = "x"
command = "a"
[compact]
threshold = -0.2`,
		"negative budget": `[[acp.providers]]
id = "x"
command = "a"
[compact]
budget = -1`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(src)); err == nil {
				t.Errorf("expected error for %s", name)
			}
		})
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
