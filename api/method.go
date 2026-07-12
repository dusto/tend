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
	{Name: "daemon.hello", Direction: PluginToDaemon, Params: HelloParams{}, Result: HelloResult{}, Summary: "Connect handshake: returns the daemon's contract versions and process epoch."},
	{Name: "workspace.open", Direction: PluginToDaemon, Params: WorkspaceOpenParams{}, Result: WorkspaceInfo{}, Summary: "Open/resolve a workspace from a directory: git-derived identity, or an ephemeral read-only workspace outside git."},
	{Name: "workspace.current", Direction: PluginToDaemon, Params: WorkspaceCurrentParams{}, Result: WorkspaceInfo{}, Summary: "Return the active workspace."},
	{Name: "events.subscribe", Direction: PluginToDaemon, Params: EventsSubscribeParams{}, Result: EventsSubscribeResult{}, Summary: "Subscribe to one logical stream from a cursor; replays then follows live. May return cursor_compacted."},
	{Name: "events.unsubscribe", Direction: PluginToDaemon, Params: EventsUnsubscribeParams{}, Result: nil, Summary: "Stop delivery for a stream."},
	{Name: "agent.start", Direction: PluginToDaemon, Params: AgentStartParams{}, Result: AgentStartResult{}, Summary: "Open a task-scoped agent session on a provider process; returns the session id and its event stream."},
	{Name: "agent.prompt", Direction: PluginToDaemon, Params: AgentPromptParams{}, Result: AgentPromptResult{}, Summary: "Run one prompt turn on a session (blocks until the turn ends); output streams as events on the session stream."},
	{Name: "agent.cancel", Direction: PluginToDaemon, Params: AgentCancelParams{}, Result: nil, Summary: "Cancel the in-flight turn on a session, returning it to idle."},
	{Name: "agent.stop", Direction: PluginToDaemon, Params: AgentStopParams{}, Result: nil, Summary: "End a session and release its hold on the provider process."},
	{Name: "session.list", Direction: PluginToDaemon, Params: SessionListParams{}, Result: SessionListResult{}, Summary: "List the daemon's sessions with status, task, stream, and editor-binding, optionally filtered by workspace."},
	{Name: "session.claim", Direction: PluginToDaemon, Params: SessionClaimParams{}, Result: SessionClaimResult{}, Summary: "Move a session's editor binding to the calling editor client, so editor-local calls for that session route to it."},
	{Name: "session.set_mode", Direction: PluginToDaemon, Params: SessionSetModeParams{}, Result: SessionSetModeResult{}, Summary: "Set a session's active mode (behavior/permission mode) from its available modes; emits agent_mode_updated. Errors when the provider offers no modes."},
	{Name: "session.set_model", Direction: PluginToDaemon, Params: SessionSetModelParams{}, Result: SessionSetModelResult{}, Summary: "Set a session's active model from its available models; emits agent_model_updated. Errors when the provider offers no models."},
	{Name: "session.set_thought_level", Direction: PluginToDaemon, Params: SessionSetThoughtLevelParams{}, Result: SessionSetThoughtLevelResult{}, Summary: "Set a session's active reasoning/thought level from its available thought levels; emits agent_thought_level_updated. Errors when the provider offers no thought levels."},
	{Name: "session.resume_seed", Direction: PluginToDaemon, Params: SessionResumeSeedParams{}, Result: SessionResumeSeedResult{}, Summary: "Reconstruct a resume seed from a prior session's durable history (summary records + recent transcript) plus workspace memory, condensed to a budget; the opening-prompt content for a fresh session (works across a daemon restart)."},
	{Name: "session.rename", Direction: PluginToDaemon, Params: SessionRenameParams{}, Result: SessionRenameResult{}, Summary: "Set or clear a session's user-facing label (independent of its task); an empty label clears it, an over-long one errors. Emits session_renamed so attached clients see the change."},
	{Name: "session.attach", Direction: PluginToDaemon, Params: SessionAttachParams{}, Result: SessionAttachResult{}, Summary: "Make the calling client follow a session: its prompts (approval/clarification) and waiting_* status are delivered to attached clients. Does not claim the editor binding. Until any client attaches, a session's prompts broadcast to all clients."},
	{Name: "session.detach", Direction: PluginToDaemon, Params: SessionDetachParams{}, Result: SessionDetachResult{}, Summary: "Stop the calling client following a session (does not end the session). Detaching an already-detached client is a no-op."},
	{Name: "provider.list", Direction: PluginToDaemon, Params: ProviderListParams{}, Result: ProviderListResult{}, Summary: "List the configured ACP providers with their enabled state and running-process count for a workspace."},
	{Name: "provider.start", Direction: PluginToDaemon, Params: ProviderStartParams{}, Result: ProviderStartResult{}, Summary: "Warm a provider for a workspace: ensure the pool holds at least one process for it; emits provider_started on a spawn."},
	{Name: "provider.stop", Direction: PluginToDaemon, Params: ProviderStopParams{}, Result: ProviderStopResult{}, Summary: "Stop a provider for a workspace: terminate its pooled processes; emits provider_stopped per process."},
	{Name: "provider.health", Direction: PluginToDaemon, Params: ProviderHealthParams{}, Result: ProviderHealthResult{}, Summary: "Report per-provider health for a workspace: command availability on the daemon's PATH and process state (disabled/never_started/running/stopped)."},
	{Name: "memory.search", Direction: PluginToDaemon, Params: MemorySearchParams{}, Result: MemorySearchResult{}, Summary: "Search a workspace's memories; returns ids + snippets (concise by design). Fetch full bodies with memory.get."},
	{Name: "memory.get", Direction: PluginToDaemon, Params: MemoryGetParams{}, Result: MemoryGetResult{}, Summary: "Fetch one memory entry's full text by id within a workspace."},
	{Name: "memory.write", Direction: PluginToDaemon, Params: MemoryWriteParams{}, Result: MemoryWriteResult{}, Summary: "Create or update a memory entry (upsert by id); task-bound and not approval-gated. Emits memory_written."},
	{Name: "memory.steering", Direction: PluginToDaemon, Params: MemorySteeringParams{}, Result: MemorySteeringResult{}, Summary: "Resolve the steering memories that apply to a context (always, or glob-matched to an optional worktree-relative path); returns full entries to inject into agent context."},
	{Name: "memory.context", Direction: PluginToDaemon, Params: MemoryContextParams{}, Result: MemoryContextResult{}, Summary: "Assemble the applicable steering (plus optional query-matched notes) and condense it to a character budget via the configured summarizer; returns a bounded context digest to inject."},
	{Name: "slash.list", Direction: PluginToDaemon, Params: SlashListParams{}, Result: SlashListResult{}, Summary: "List a session's merged slash-command set: the agent's advertised commands plus the daemon/harness commands."},
	{Name: "slash.complete", Direction: PluginToDaemon, Params: SlashCompleteParams{}, Result: SlashCompleteResult{}, Summary: "Complete a daemon command's argument against live state (e.g. task ids); provider/unknown commands yield no candidates."},
	{Name: "slash.invoke", Direction: PluginToDaemon, Params: SlashInvokeParams{}, Result: SlashInvokeResult{}, Summary: "Invoke a slash command: a daemon command runs its task action; any other command is forwarded to the agent as a prompt turn."},
	{Name: "client.register", Direction: PluginToDaemon, Params: ClientRegisterParams{}, Result: ClientRegisterResult{}, Summary: "Register the connection's stable client id, role (editor/observer), and prompt capability."},
	{Name: "file.read", Direction: PluginToDaemon, Params: FileReadParams{}, Result: FileReadResult{}, Summary: "Read a repo file editor-aware (non-mutating): live buffer content + changedtick when open, else disk content + content hash."},
	{Name: "file.patch", Direction: PluginToDaemon, Params: FilePatchParams{}, Result: FileMutationResult{}, Summary: "Apply non-overlapping text edits to a repo file (task-bound, approval-gated, base-checked); single-target change set."},
	{Name: "file.write", Direction: PluginToDaemon, Params: FileWriteParams{}, Result: FileMutationResult{}, Summary: "Replace a repo file's whole content (task-bound, approval-gated, base-checked); single-target change set."},
	{Name: "file.apply_change_set", Direction: PluginToDaemon, Params: FileApplyChangeSetParams{}, Result: FileApplyChangeSetResult{}, Summary: "Apply a multi-file change set as one approved unit (best-effort atomic: preflight, disk writes, editor buffers last, with partial-failure reporting)."},
	{Name: "file.diff", Direction: PluginToDaemon, Params: FileDiffParams{}, Result: FileDiffResult{}, Summary: "Fetch a change set's captured before/after snapshots for review (read-only, not task-gated)."},
	{Name: "lsp.diagnostics", Direction: PluginToDaemon, Params: LSPDiagnosticsParams{}, Result: LSPDiagnosticsResult{}, Summary: "Editor-fresh LSP diagnostics for a file via the session's bound editor (session-scoped, not approval-gated); empty uri means the editor's current buffer."},
	{Name: "lsp.symbols", Direction: PluginToDaemon, Params: LSPSymbolsParams{}, Result: LSPSymbolsResult{}, Summary: "Editor-fresh document symbols (outline) for a file via the session's bound editor; empty uri means the current buffer. Input uri is worktree-bounded."},
	{Name: "lsp.definition", Direction: PluginToDaemon, Params: LSPDefinitionParams{}, Result: LSPDefinitionResult{}, Summary: "Editor-fresh definition location(s) of the symbol at a position via the bound editor; empty uri means the current buffer. Result locations may point outside the worktree (dependency/stdlib)."},
	{Name: "lsp.references", Direction: PluginToDaemon, Params: LSPReferencesParams{}, Result: LSPReferencesResult{}, Summary: "Editor-fresh reference locations of the symbol at a position via the bound editor; include_declaration adds the declaration. Input uri is worktree-bounded."},
	{Name: "lsp.hover", Direction: PluginToDaemon, Params: LSPHoverParams{}, Result: LSPHoverResult{}, Summary: "Editor-fresh hover info (markdown) for the symbol at a position via the bound editor; empty uri means the current buffer. Input uri is worktree-bounded."},
	{Name: "lsp.code_actions", Direction: PluginToDaemon, Params: LSPCodeActionsParams{}, Result: LSPCodeActionsResult{}, Summary: "List the editor-fresh code actions available for a range (list-only, not approval-gated); each edit-carrying action includes change-set-ready edits. Apply a chosen action by submitting its changes to file.apply_change_set (reviewed as a file edit)."},
	{Name: "approval.list", Direction: PluginToDaemon, Params: ApprovalListParams{}, Result: ApprovalListResult{}, Summary: "List pending approvals (self-contained payloads), optionally filtered by session."},
	{Name: "approval.respond", Direction: PluginToDaemon, Params: ApprovalRespondParams{}, Result: ApprovalRespondResult{}, Summary: "Resolve a pending approval (approve/deny); returns an ack. Only prompt-capable clients may call it."},
	{Name: "task.create", Direction: PluginToDaemon, Params: TaskCreateParams{}, Result: Task{}, Summary: "Create a task in a workspace's provider."},
	{Name: "task.show", Direction: PluginToDaemon, Params: TaskShowParams{}, Result: Task{}, Summary: "Fetch one task by ref."},
	{Name: "task.list", Direction: PluginToDaemon, Params: TaskListParams{}, Result: TaskListResult{}, Summary: "List a workspace's tasks, optionally filtered by status."},
	{Name: "task.claim", Direction: PluginToDaemon, Params: TaskClaimParams{}, Result: Task{}, Summary: "Assign a task and mark it in progress; returns the updated task."},
	{Name: "task.comment", Direction: PluginToDaemon, Params: TaskCommentParams{}, Result: Task{}, Summary: "Append a comment to a task; returns the updated task."},
	{Name: "task.close", Direction: PluginToDaemon, Params: TaskCloseParams{}, Result: Task{}, Summary: "Close a task; returns the updated task."},
	{Name: "pane.open", Direction: PluginToDaemon, Params: PaneOpenParams{}, Result: PaneInfo{}, Summary: "Open an idle shell pane (daemon-owned PTY). Approval-gated when agent-initiated (a session is in context); ungated for a user open."},
	{Name: "pane.list", Direction: PluginToDaemon, Params: PaneListParams{}, Result: PaneListResult{}, Summary: "List panes with running and view state, optionally filtered by workspace."},
	{Name: "pane.read", Direction: PluginToDaemon, Params: PaneReadParams{}, Result: PaneReadResult{}, Summary: "Read a pane's captured output (optionally the last Tail bytes)."},
	{Name: "pane.close", Direction: PluginToDaemon, Params: PaneCloseParams{}, Result: PaneCloseResult{}, Summary: "Close a pane: terminate its process group and release the PTY."},
	{Name: "pane.run", Direction: PluginToDaemon, Params: PaneRunParams{}, Result: PaneRunResult{}, Summary: "Run a command in a pane (task-bound, approval-gated); output arrives on the pane stream."},

	// daemon -> bound editor
	{Name: "editor.current_buffer", Direction: DaemonToEditor, Params: EditorCurrentBufferParams{}, Result: EditorCurrentBufferResult{}, Summary: "Return the editor's active buffer (its file URI, or empty when none)."},
	{Name: "editor.read_buffer", Direction: DaemonToEditor, Params: EditorReadBufferParams{}, Result: EditorReadBufferResult{}, Summary: "Read a file editor-aware; returns content and its base (changedtick for an open buffer, content hash for a closed file)."},
	{Name: "editor.write_buffer", Direction: DaemonToEditor, Params: EditorWriteBufferParams{}, Result: EditorWriteBufferResult{}, Summary: "Write whole-buffer content through the editor (respecting unsaved state); returns the new base."},
	{Name: "editor.selection", Direction: DaemonToEditor, Params: EditorSelectionParams{}, Result: EditorSelectionResult{}, Summary: "Return the editor's current selection range, or empty when there is only a cursor."},
	{Name: "editor.open", Direction: DaemonToEditor, Params: EditorOpenParams{}, Result: EditorOpenResult{}, Summary: "Open files in editor buffers for in-place review (read-only affordance)."},
	{Name: "editor.diff", Direction: DaemonToEditor, Params: EditorDiffParams{}, Result: EditorDiffResult{}, Summary: "Render a change set's captured before/after snapshots in the editor's diff view."},
	{Name: "editor.diagnostics", Direction: DaemonToEditor, Params: EditorDiagnosticsParams{}, Result: EditorDiagnosticsResult{}, Summary: "Return editor-fresh LSP diagnostics for a file (open buffers); the daemon filters by severity."},
	{Name: "editor.symbols", Direction: DaemonToEditor, Params: EditorSymbolsParams{}, Result: EditorSymbolsResult{}, Summary: "Return editor-fresh document symbols (outline) for a file from its open buffer's LSP."},
	{Name: "editor.definition", Direction: DaemonToEditor, Params: EditorDefinitionParams{}, Result: EditorDefinitionResult{}, Summary: "Return editor-fresh definition location(s) of the symbol at a position from the open buffer's LSP."},
	{Name: "editor.references", Direction: DaemonToEditor, Params: EditorReferencesParams{}, Result: EditorReferencesResult{}, Summary: "Return editor-fresh reference locations of the symbol at a position from the open buffer's LSP."},
	{Name: "editor.hover", Direction: DaemonToEditor, Params: EditorHoverParams{}, Result: EditorHoverResult{}, Summary: "Return editor-fresh hover info (markdown) for the symbol at a position from the open buffer's LSP."},
	{Name: "editor.code_actions", Direction: DaemonToEditor, Params: EditorCodeActionsParams{}, Result: EditorCodeActionsResult{}, Summary: "Return the code actions for a range from the open buffer's LSP, each edit-carrying action resolved into change-set targets (with base filled)."},

	// daemon -> attached client
	{Name: "event.push", Direction: DaemonToClient, Params: EventPushParams{}, Result: nil, Summary: "Deliver an event on a subscribed stream (at-least-once; clients dedup by (stream_id, seq, kind))."},
	{Name: "prompt.raise", Direction: DaemonToClient, Params: PromptRaiseParams{}, Result: nil, Summary: "Raise an approval/clarification prompt and waiting_* status to attached clients."},
	{Name: "event.subscription_closed", Direction: DaemonToClient, Params: SubscriptionClosedParams{}, Result: nil, Summary: "A single stream subscription was dropped (per-stream overflow or ended); the client resubscribes from its own last_seq."},
}
