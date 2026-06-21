package acp

// DefaultProviders returns the built-in ACP provider definitions the daemon
// ships with. Codex is the milestone-0 provider; Claude and Kiro are included so
// a fresh config has working examples. A user's config can add to or override
// these.
func DefaultProviders() []Provider {
	return []Provider{
		{ID: "codex", Command: "codex-acp", CwdMode: CwdWorkspace, Enabled: true},
		{ID: "claude", Command: "claude-agent-acp", CwdMode: CwdWorkspace, Enabled: true},
		{ID: "kiro", Command: "kiro-cli", Args: []string{"acp", "-a"}, CwdMode: CwdWorkspace, Enabled: true},
	}
}

// DefaultConfig returns a Config populated with the default providers.
func DefaultConfig() *Config {
	return &Config{ACP: Settings{Providers: DefaultProviders()}}
}
