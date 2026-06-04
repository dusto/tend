package api

// ClientRole distinguishes a client that can serve editor-local operations (an
// editor) from one that can only observe. Editor-local operations are buffer
// reads/writes, selection, and editor-fresh LSP; only an editor can serve them,
// so a session's editor binding must name an editor client.
type ClientRole string

// ClientRole values.
const (
	// RoleEditor: the client can serve editor-local operations.
	RoleEditor ClientRole = "editor"
	// RoleObserver: the client observes only (e.g. the CLI or a read-only UI).
	RoleObserver ClientRole = "observer"
)

// Valid reports whether r is a known role.
func (r ClientRole) Valid() bool {
	return r == RoleEditor || r == RoleObserver
}

// ClientRegisterParams registers a connection's stable identity and declared
// capabilities. A client sends it once after the handshake, before issuing other
// calls that depend on identity (editor binding, prompt responses).
type ClientRegisterParams struct {
	// ClientID is a stable id the client chooses; a reconnecting client reuses it
	// to keep its identity.
	ClientID ClientID `json:"client_id"`
	// Role is whether the client can serve editor-local operations.
	Role ClientRole `json:"role"`
	// PromptCapable declares the client may respond to approval/clarification
	// prompts. Only prompt-capable clients can resolve a prompt; observers may see
	// it but not answer.
	PromptCapable bool `json:"prompt_capable"`
}

// ClientRegisterResult confirms the registered identity.
type ClientRegisterResult struct {
	ClientID ClientID `json:"client_id"`
}
