package summarize

import "fmt"

// Backend selects how summarization is performed.
type Backend string

const (
	// BackendNone uses the deterministic Fallback (no model). It is the default,
	// so summarization works with no configuration.
	BackendNone Backend = "none"
	// BackendLocal runs a local command as the model (see LocalConfig).
	BackendLocal Backend = "local"
	// BackendACP summarizes on a dedicated ACP session/model (see ACPConfig). The
	// concrete completer is injected by the daemon; this package never imports acp.
	BackendACP Backend = "acp"
)

// DefaultTargetChars is the fallback output budget when none is configured.
const DefaultTargetChars = 2000

// Config is the [summarize] configuration section: which backend condenses
// TEND-owned context and the default output budget.
type Config struct {
	// Backend is none|local|acp; empty defaults to none.
	Backend Backend `toml:"backend"`
	// TargetChars is the default soft output budget; 0 uses DefaultTargetChars.
	TargetChars int `toml:"target_chars"`
	// Local configures the local-command backend (backend = "local").
	Local LocalConfig `toml:"local"`
	// ACP configures the dedicated-ACP-session backend (backend = "acp").
	ACP ACPConfig `toml:"acp"`
}

// LocalConfig configures the local-command Completer: Command is run with Args,
// the prompt is written to its stdin, and its stdout is the completion.
type LocalConfig struct {
	Command string   `toml:"command"`
	Args    []string `toml:"args"`
}

// ACPConfig configures the dedicated-ACP-session Completer: Provider names an
// [[acp.providers]] id to summarize on (a separate session from the working
// one), and Model optionally pins a model on it.
type ACPConfig struct {
	Provider string `toml:"provider"`
	Model    string `toml:"model"`
}

// Validate normalizes defaults and checks the section. It does not cross-check
// that ACP.Provider names a configured provider — that lives with the acp config
// which can see the provider list.
func (c *Config) Validate() error {
	if c.Backend == "" {
		c.Backend = BackendNone
	}
	switch c.Backend {
	case BackendNone, BackendLocal, BackendACP:
	default:
		return fmt.Errorf("config: summarize.backend %q is invalid (want none|local|acp)", c.Backend)
	}
	if c.TargetChars < 0 {
		return fmt.Errorf("config: summarize.target_chars must be >= 0")
	}
	if c.Backend == BackendLocal && c.Local.Command == "" {
		return fmt.Errorf("config: summarize.local.command is required for backend \"local\"")
	}
	if c.Backend == BackendACP && c.ACP.Provider == "" {
		return fmt.Errorf("config: summarize.acp.provider is required for backend \"acp\"")
	}
	return nil
}

// effectiveTargetChars returns the configured budget or the default.
func (c Config) effectiveTargetChars() int {
	if c.TargetChars > 0 {
		return c.TargetChars
	}
	return DefaultTargetChars
}
