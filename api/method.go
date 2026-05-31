package api

// Direction is which side implements a method. The three sets travel over the
// same bidirectional socket but are distinct contracts that version
// independently. See docs: Transport, JSON-RPC API.
type Direction string

// Direction values for the three method sets.
const (
	PluginToDaemon Direction = "plugin->daemon"
	DaemonToEditor Direction = "daemon->editor" // routed only to the session's editor-binding owner
	DaemonToClient Direction = "daemon->client" // delivered to subscribed/attached clients
)

// Method declares one JSON-RPC method in the contract. Params/Result hold a
// zero value of the request/response struct; Result is nil for notifications.
// Codegen reflects Params/Result into schemas/ and docs/api.md.
type Method struct {
	Name      string
	Direction Direction
	Params    any
	Result    any
	Summary   string
}

// Methods is the method catalog. This is the initial slice covering all three
// directions plus the event transport; remaining methods are added as their
// features land.
var Methods = []Method{
	// plugin -> daemon
	{Name: "workspace.open", Direction: PluginToDaemon, Params: WorkspaceOpenParams{}, Result: WorkspaceInfo{}, Summary: "Open/resolve a workspace from a directory: git-derived identity, or an ephemeral read-only workspace outside git."},
	{Name: "workspace.current", Direction: PluginToDaemon, Params: WorkspaceCurrentParams{}, Result: WorkspaceInfo{}, Summary: "Return the active workspace."},
	{Name: "events.subscribe", Direction: PluginToDaemon, Params: EventsSubscribeParams{}, Result: EventsSubscribeResult{}, Summary: "Subscribe to one logical stream from a cursor; replays then follows live. May return cursor_compacted."},
	{Name: "events.unsubscribe", Direction: PluginToDaemon, Params: EventsUnsubscribeParams{}, Result: nil, Summary: "Stop delivery for a stream."},

	// daemon -> bound editor
	{Name: "editor.read_buffer", Direction: DaemonToEditor, Params: EditorReadBufferParams{}, Result: EditorReadBufferResult{}, Summary: "Read a file editor-aware; returns content and its base (changedtick for an open buffer, content hash for a closed file)."},

	// daemon -> attached client
	{Name: "event.push", Direction: DaemonToClient, Params: EventPushParams{}, Result: nil, Summary: "Deliver an event on a subscribed stream (at-least-once; clients dedup by (stream_id, seq, kind))."},
	{Name: "prompt.raise", Direction: DaemonToClient, Params: PromptRaiseParams{}, Result: nil, Summary: "Raise an approval/clarification prompt and waiting_* status to attached clients."},
	{Name: "event.subscription_closed", Direction: DaemonToClient, Params: SubscriptionClosedParams{}, Result: nil, Summary: "A single stream subscription was dropped (per-stream overflow or ended); the client resubscribes from its own last_seq."},
}
