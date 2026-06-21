package acp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigPathPrecedence(t *testing.T) {
	t.Setenv("TEND_CONFIG", "/explicit/tend.toml")
	if got := ConfigPath(); got != "/explicit/tend.toml" {
		t.Errorf("ConfigPath with TEND_CONFIG = %q, want the explicit path", got)
	}

	t.Setenv("TEND_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if got, want := ConfigPath(), filepath.Join("/xdg", "tend", "config.toml"); got != want {
		t.Errorf("ConfigPath with XDG_CONFIG_HOME = %q, want %q", got, want)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/u")
	if got, want := ConfigPath(), filepath.Join("/home/u", ".config", "tend", "config.toml"); got != want {
		t.Errorf("ConfigPath fallback = %q, want %q", got, want)
	}
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	cfg, loaded, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded {
		t.Error("loaded = true for a missing file, want false")
	}
	// Falls back to the built-in providers.
	if _, ok := cfg.Provider("codex"); !ok {
		t.Errorf("default config missing the codex provider: %+v", cfg.ACP.Providers)
	}
}

func TestLoadExistingFileIsParsed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	toml := `
[[acp.providers]]
id = "myagent"
command = "my-acp"
enabled = true
`
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded {
		t.Error("loaded = false for an existing file, want true")
	}
	p, ok := cfg.Provider("myagent")
	if !ok || p.Command != "my-acp" {
		t.Errorf("loaded provider = %+v (ok=%v), want the file's definition", p, ok)
	}
	// The file replaces the defaults, it is not merged with them.
	if _, ok := cfg.Provider("codex"); ok {
		t.Error("default codex provider leaked into a file-loaded config")
	}
}

func TestLoadInvalidFileErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	// A provider with no command fails validation.
	if err := os.WriteFile(path, []byte("[[acp.providers]]\nid = \"x\"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := Load(path); err == nil {
		t.Fatal("Load of an invalid config should error, not fall back to defaults")
	}
}

func TestDefaultCodexProviderCommand(t *testing.T) {
	// The codex ACP adapter is the standalone codex-acp binary; the codex CLI
	// has no acp subcommand.
	p, ok := DefaultConfig().Provider("codex")
	if !ok {
		t.Fatal("default config missing codex")
	}
	if p.Command != "codex-acp" || len(p.Args) != 0 {
		t.Errorf("codex default = {command:%q args:%v}, want codex-acp with no args", p.Command, p.Args)
	}
}
