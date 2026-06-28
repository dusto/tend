package api

// ProviderInfo is one configured ACP provider as seen for a workspace: its
// definition plus how many of its processes the pool currently holds for that
// workspace. Running is 0 for a provider with no live process (including a
// disabled one, which cannot be started).
type ProviderInfo struct {
	ProviderID ProviderID `json:"provider_id"`
	Command    string     `json:"command"`
	Enabled    bool       `json:"enabled"`
	Running    int        `json:"running"`
}

// ProviderListParams lists the configured providers and their running state for
// a workspace. WorkspaceID is required: running counts are per workspace.
type ProviderListParams struct {
	WorkspaceID WorkspaceID `json:"workspace_id"`
}

// ProviderListResult is the configured providers in definition order, each with
// its running-process count for the requested workspace.
type ProviderListResult struct {
	Providers []ProviderInfo `json:"providers"`
}

// ProviderStartParams warms a provider for a workspace: it ensures the pool holds
// at least one process for {workspace, provider}, spawning one if none is live.
// WorktreeRoot is the directory a CwdWorkspace provider process starts in; it is
// required so a warmed process gets a real worktree cwd rather than the repo's
// common git dir (a pool process is shared per {workspace, provider} across
// worktrees, so this is the root of whichever caller first spawns it — the same
// rule as agent.start).
type ProviderStartParams struct {
	WorkspaceID  WorkspaceID `json:"workspace_id"`
	ProviderID   ProviderID  `json:"provider_id"`
	WorktreeRoot string      `json:"worktree_root"`
}

// ProviderStartResult reports the provider's running-process count after the
// start.
type ProviderStartResult struct {
	ProviderID ProviderID `json:"provider_id"`
	Running    int        `json:"running"`
}

// ProviderStopParams stops a provider for a workspace: it terminates every
// process the pool holds for {workspace, provider}. In-flight turns on those
// processes fail (provider_stopped/agent_error are emitted as on a crash).
type ProviderStopParams struct {
	WorkspaceID WorkspaceID `json:"workspace_id"`
	ProviderID  ProviderID  `json:"provider_id"`
}

// ProviderStopResult reports how many processes were terminated.
type ProviderStopResult struct {
	ProviderID ProviderID `json:"provider_id"`
	Stopped    int        `json:"stopped"`
}
