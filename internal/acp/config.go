package acp

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// CwdMode selects the working directory an ACP provider process runs in.
type CwdMode string

const (
	// CwdWorkspace runs the provider in the workspace's worktree root.
	CwdWorkspace CwdMode = "workspace"
	// CwdInherit runs the provider in the daemon's working directory.
	CwdInherit CwdMode = "inherit"
)

// Config is the parsed TEND configuration. It models the ACP provider section.
type Config struct {
	ACP Settings `toml:"acp"`
}

// Settings holds the ACP provider definitions.
type Settings struct {
	Providers []Provider `toml:"providers"`
}

// Provider is a generic ACP provider definition (one [[acp.providers]] entry):
// any ACP server is described by these fields without per-provider code.
type Provider struct {
	ID      string   `toml:"id"`
	Command string   `toml:"command"`
	Args    []string `toml:"args"`
	Env     []string `toml:"env"`
	CwdMode CwdMode  `toml:"cwd_mode"`
	Enabled bool     `toml:"enabled"`
}

// Parse decodes TEND configuration from TOML and validates the provider
// definitions. Unknown keys are rejected so typos surface early.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("config: unknown keys: %v", undecoded)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// ParseFile reads and parses a TEND configuration file.
func ParseFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return Parse(data)
}

// validate normalizes defaults and checks the provider definitions.
func (c *Config) validate() error {
	seen := make(map[string]struct{}, len(c.ACP.Providers))
	for i := range c.ACP.Providers {
		p := &c.ACP.Providers[i]
		if p.ID == "" {
			return fmt.Errorf("config: acp.providers[%d]: id is required", i)
		}
		if _, dup := seen[p.ID]; dup {
			return fmt.Errorf("config: duplicate provider id %q", p.ID)
		}
		seen[p.ID] = struct{}{}
		if p.Command == "" {
			return fmt.Errorf("config: provider %q: command is required", p.ID)
		}
		if p.CwdMode == "" {
			p.CwdMode = CwdWorkspace
		}
		if p.CwdMode != CwdWorkspace && p.CwdMode != CwdInherit {
			return fmt.Errorf("config: provider %q: invalid cwd_mode %q", p.ID, p.CwdMode)
		}
	}
	return nil
}

// EnabledProviders returns the providers with enabled = true, in definition
// order.
func (c *Config) EnabledProviders() []Provider {
	out := make([]Provider, 0, len(c.ACP.Providers))
	for _, p := range c.ACP.Providers {
		if p.Enabled {
			out = append(out, p)
		}
	}
	return out
}

// Provider returns the definition with the given id.
func (c *Config) Provider(id string) (Provider, bool) {
	for _, p := range c.ACP.Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// LaunchCommand builds the launch Command for this provider in the given
// workspace root. The process inherits the daemon's environment with the
// provider's Env entries appended (config adds to, rather than replaces, the
// ambient environment). The working directory follows CwdMode.
func (p Provider) LaunchCommand(workspaceRoot string) Command {
	cmd := Command{Path: p.Command, Args: p.Args}
	if len(p.Env) > 0 {
		cmd.Env = append(os.Environ(), p.Env...)
	}
	if p.CwdMode == CwdWorkspace {
		cmd.Dir = workspaceRoot
	}
	return cmd
}
