package api

// Slash command origins: who handles an invoked command. A provider command is
// forwarded to the agent (ACP has no invoke method — it is sent as prompt text);
// a daemon command runs a daemon/harness action (e.g. the task.* methods).
const (
	SlashOriginProvider = "provider"
	SlashOriginDaemon   = "daemon"
)

// SlashCommand is one entry in a session's merged slash-command set: a command
// the user can invoke from the prompt. The daemon aggregates the agent's own
// advertised commands (Origin provider) with the static daemon/harness commands
// (Origin daemon) so a client has a single list to complete and render.
type SlashCommand struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Origin      string `json:"origin"`
	// ArgHint describes the command's argument for display/completion (the ACP
	// command input hint, or a daemon command's argument spec). Empty when the
	// command takes no argument.
	ArgHint string `json:"arg_hint,omitempty"`
}

// SlashListParams asks for the merged slash-command set for a session: the
// session's provider commands plus the daemon commands.
type SlashListParams struct {
	SessionID SessionID `json:"session_id"`
}

// SlashListResult is the merged command set, daemon commands first then the
// session's provider commands.
type SlashListResult struct {
	Commands []SlashCommand `json:"commands"`
}
