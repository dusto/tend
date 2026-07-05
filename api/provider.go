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

// Provider process states (ProviderHealth.State).
const (
	// ProviderStateDisabled is a configured provider that cannot be started.
	ProviderStateDisabled = "disabled"
	// ProviderStateNeverStarted is an enabled provider the daemon has not tried
	// to run for the workspace.
	ProviderStateNeverStarted = "never_started"
	// ProviderStateRunning is a provider with at least one live process.
	ProviderStateRunning = "running"
	// ProviderStateStopped is an enabled provider run before but with no live
	// process now (exited, crashed, or was stopped).
	ProviderStateStopped = "stopped"
)

// ProviderHealth is the daemon-reported health of one configured provider for a
// workspace: whether its command is executable and the state of its pooled
// processes. The daemon owns provider processes, so health is reported here
// rather than probed by a client.
type ProviderHealth struct {
	ProviderID ProviderID `json:"provider_id"`
	Command    string     `json:"command"`
	Enabled    bool       `json:"enabled"`
	// CommandFound reports whether Command resolves to an executable on the
	// daemon's PATH.
	CommandFound bool `json:"command_found"`
	// CommandPath is the resolved path of Command, or "" when it is not found.
	CommandPath string `json:"command_path,omitempty"`
	// State is the process state for the workspace: one of the ProviderState*
	// values (disabled, never_started, running, stopped).
	State string `json:"state"`
	// Running is the number of live pooled processes for the workspace.
	Running int `json:"running"`
}

// ProviderHealthParams requests provider health for a workspace. WorkspaceID is
// required: process state is per workspace.
type ProviderHealthParams struct {
	WorkspaceID WorkspaceID `json:"workspace_id"`
}

// ProviderHealthResult is the health of each configured provider in definition
// order.
type ProviderHealthResult struct {
	Providers []ProviderHealth `json:"providers"`
}
