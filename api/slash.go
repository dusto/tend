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

// SlashCompleteParams asks the daemon to complete a command's argument against
// live state — e.g. task ids for the task-tracking commands. SessionID supplies
// the workspace the completion is scoped to. Prefix is the partial argument
// token typed so far ("" lists all candidates). The daemon completes only its
// own commands' arguments; a provider or unknown command yields no candidates.
type SlashCompleteParams struct {
	SessionID SessionID `json:"session_id"`
	Command   string    `json:"command"`
	Prefix    string    `json:"prefix,omitempty"`
}

// SlashCompleteResult is the argument candidates matching the prefix.
type SlashCompleteResult struct {
	Candidates []SlashCandidate `json:"candidates"`
}

// SlashCandidate is one completion: Value is inserted; Detail is an optional
// short description for display (e.g. a task's title).
type SlashCandidate struct {
	Value  string `json:"value"`
	Detail string `json:"detail,omitempty"`
}

// SlashInvokeParams invokes a slash command for a session. Command is the bare
// name (no leading slash); Args is the raw argument text after it. The daemon
// runs a command it owns as a daemon action and forwards anything else to the
// agent as a prompt turn.
type SlashInvokeParams struct {
	SessionID SessionID `json:"session_id"`
	Command   string    `json:"command"`
	Args      string    `json:"args,omitempty"`
}

// SlashInvokeResult reports how a command was handled. Origin is "daemon" (the
// daemon ran a task action) or "provider" (the command was forwarded to the
// agent). Message is a human-readable outcome; Task/Tasks carry a daemon
// command's result; StopReason is the turn's stop reason for a forwarded command.
type SlashInvokeResult struct {
	Origin     string `json:"origin"`
	Message    string `json:"message,omitempty"`
	Task       *Task  `json:"task,omitempty"`
	Tasks      []Task `json:"tasks,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
}
