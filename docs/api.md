# TEND JSON-RPC API

Generated from `api/` by `cmd/gen`. Do not edit by hand — run `go generate ./...`.

Schemas live under `schemas/` (`methods/<name>.params.json` / `.result.json`, `events/<type>.json`, `event-envelope.json`).

## Methods

### plugin → daemon

- **`daemon.hello`** — Connect handshake: returns the daemon's contract versions and process epoch.
  - Params: `HelloParams` · Result: `HelloResult`
- **`workspace.open`** — Open/resolve a workspace from a directory: git-derived identity, or an ephemeral read-only workspace outside git.
  - Params: `WorkspaceOpenParams` · Result: `WorkspaceInfo`
- **`workspace.current`** — Return the active workspace.
  - Params: `WorkspaceCurrentParams` · Result: `WorkspaceInfo`
- **`events.subscribe`** — Subscribe to one logical stream from a cursor; replays then follows live. May return cursor_compacted.
  - Params: `EventsSubscribeParams` · Result: `EventsSubscribeResult`
- **`events.unsubscribe`** — Stop delivery for a stream.
  - Params: `EventsUnsubscribeParams` · Result: _(notification)_
- **`agent.start`** — Open a task-scoped agent session on a provider process; returns the session id and its event stream.
  - Params: `AgentStartParams` · Result: `AgentStartResult`
- **`agent.prompt`** — Run one prompt turn on a session (blocks until the turn ends); output streams as events on the session stream.
  - Params: `AgentPromptParams` · Result: `AgentPromptResult`
- **`agent.cancel`** — Cancel the in-flight turn on a session, returning it to idle.
  - Params: `AgentCancelParams` · Result: _(notification)_
- **`agent.stop`** — End a session and release its hold on the provider process.
  - Params: `AgentStopParams` · Result: _(notification)_
- **`session.list`** — List the daemon's sessions with status, task, stream, and editor-binding, optionally filtered by workspace.
  - Params: `SessionListParams` · Result: `SessionListResult`
- **`session.claim`** — Move a session's editor binding to the calling editor client, so editor-local calls for that session route to it.
  - Params: `SessionClaimParams` · Result: `SessionClaimResult`
- **`session.set_mode`** — Set a session's active mode (behavior/permission mode) from its available modes; emits agent_mode_updated. Errors when the provider offers no modes.
  - Params: `SessionSetModeParams` · Result: `SessionSetModeResult`
- **`session.set_model`** — Set a session's active model from its available models; emits agent_model_updated. Errors when the provider offers no models.
  - Params: `SessionSetModelParams` · Result: `SessionSetModelResult`
- **`session.set_thought_level`** — Set a session's active reasoning/thought level from its available thought levels; emits agent_thought_level_updated. Errors when the provider offers no thought levels.
  - Params: `SessionSetThoughtLevelParams` · Result: `SessionSetThoughtLevelResult`
- **`provider.list`** — List the configured ACP providers with their enabled state and running-process count for a workspace.
  - Params: `ProviderListParams` · Result: `ProviderListResult`
- **`provider.start`** — Warm a provider for a workspace: ensure the pool holds at least one process for it; emits provider_started on a spawn.
  - Params: `ProviderStartParams` · Result: `ProviderStartResult`
- **`provider.stop`** — Stop a provider for a workspace: terminate its pooled processes; emits provider_stopped per process.
  - Params: `ProviderStopParams` · Result: `ProviderStopResult`
- **`provider.health`** — Report per-provider health for a workspace: command availability on the daemon's PATH and process state (disabled/never_started/running/stopped).
  - Params: `ProviderHealthParams` · Result: `ProviderHealthResult`
- **`memory.search`** — Search a workspace's memories; returns ids + snippets (concise by design). Fetch full bodies with memory.get.
  - Params: `MemorySearchParams` · Result: `MemorySearchResult`
- **`memory.get`** — Fetch one memory entry's full text by id within a workspace.
  - Params: `MemoryGetParams` · Result: `MemoryGetResult`
- **`memory.write`** — Create or update a memory entry (upsert by id); task-bound and not approval-gated. Emits memory_written.
  - Params: `MemoryWriteParams` · Result: `MemoryWriteResult`
- **`slash.list`** — List a session's merged slash-command set: the agent's advertised commands plus the daemon/harness commands.
  - Params: `SlashListParams` · Result: `SlashListResult`
- **`slash.complete`** — Complete a daemon command's argument against live state (e.g. task ids); provider/unknown commands yield no candidates.
  - Params: `SlashCompleteParams` · Result: `SlashCompleteResult`
- **`slash.invoke`** — Invoke a slash command: a daemon command runs its task action; any other command is forwarded to the agent as a prompt turn.
  - Params: `SlashInvokeParams` · Result: `SlashInvokeResult`
- **`client.register`** — Register the connection's stable client id, role (editor/observer), and prompt capability.
  - Params: `ClientRegisterParams` · Result: `ClientRegisterResult`
- **`file.read`** — Read a repo file editor-aware (non-mutating): live buffer content + changedtick when open, else disk content + content hash.
  - Params: `FileReadParams` · Result: `FileReadResult`
- **`file.patch`** — Apply non-overlapping text edits to a repo file (task-bound, approval-gated, base-checked); single-target change set.
  - Params: `FilePatchParams` · Result: `FileMutationResult`
- **`file.write`** — Replace a repo file's whole content (task-bound, approval-gated, base-checked); single-target change set.
  - Params: `FileWriteParams` · Result: `FileMutationResult`
- **`file.apply_change_set`** — Apply a multi-file change set as one approved unit (best-effort atomic: preflight, disk writes, editor buffers last, with partial-failure reporting).
  - Params: `FileApplyChangeSetParams` · Result: `FileApplyChangeSetResult`
- **`file.diff`** — Fetch a change set's captured before/after snapshots for review (read-only, not task-gated).
  - Params: `FileDiffParams` · Result: `FileDiffResult`
- **`lsp.diagnostics`** — Editor-fresh LSP diagnostics for a file via the session's bound editor (session-scoped, not approval-gated); empty uri means the editor's current buffer.
  - Params: `LSPDiagnosticsParams` · Result: `LSPDiagnosticsResult`
- **`approval.list`** — List pending approvals (self-contained payloads), optionally filtered by session.
  - Params: `ApprovalListParams` · Result: `ApprovalListResult`
- **`approval.respond`** — Resolve a pending approval (approve/deny); returns an ack. Only prompt-capable clients may call it.
  - Params: `ApprovalRespondParams` · Result: `ApprovalRespondResult`
- **`task.create`** — Create a task in a workspace's provider.
  - Params: `TaskCreateParams` · Result: `Task`
- **`task.show`** — Fetch one task by ref.
  - Params: `TaskShowParams` · Result: `Task`
- **`task.list`** — List a workspace's tasks, optionally filtered by status.
  - Params: `TaskListParams` · Result: `TaskListResult`
- **`task.claim`** — Assign a task and mark it in progress; returns the updated task.
  - Params: `TaskClaimParams` · Result: `Task`
- **`task.comment`** — Append a comment to a task; returns the updated task.
  - Params: `TaskCommentParams` · Result: `Task`
- **`task.close`** — Close a task; returns the updated task.
  - Params: `TaskCloseParams` · Result: `Task`
- **`pane.open`** — Open an idle shell pane (daemon-owned PTY). Approval-gated when agent-initiated (a session is in context); ungated for a user open.
  - Params: `PaneOpenParams` · Result: `PaneInfo`
- **`pane.list`** — List panes with running and view state, optionally filtered by workspace.
  - Params: `PaneListParams` · Result: `PaneListResult`
- **`pane.read`** — Read a pane's captured output (optionally the last Tail bytes).
  - Params: `PaneReadParams` · Result: `PaneReadResult`
- **`pane.close`** — Close a pane: terminate its process group and release the PTY.
  - Params: `PaneCloseParams` · Result: `PaneCloseResult`
- **`pane.run`** — Run a command in a pane (task-bound, approval-gated); output arrives on the pane stream.
  - Params: `PaneRunParams` · Result: `PaneRunResult`

### daemon → bound editor

- **`editor.current_buffer`** — Return the editor's active buffer (its file URI, or empty when none).
  - Params: `EditorCurrentBufferParams` · Result: `EditorCurrentBufferResult`
- **`editor.read_buffer`** — Read a file editor-aware; returns content and its base (changedtick for an open buffer, content hash for a closed file).
  - Params: `EditorReadBufferParams` · Result: `EditorReadBufferResult`
- **`editor.write_buffer`** — Write whole-buffer content through the editor (respecting unsaved state); returns the new base.
  - Params: `EditorWriteBufferParams` · Result: `EditorWriteBufferResult`
- **`editor.selection`** — Return the editor's current selection range, or empty when there is only a cursor.
  - Params: `EditorSelectionParams` · Result: `EditorSelectionResult`
- **`editor.open`** — Open files in editor buffers for in-place review (read-only affordance).
  - Params: `EditorOpenParams` · Result: `EditorOpenResult`
- **`editor.diff`** — Render a change set's captured before/after snapshots in the editor's diff view.
  - Params: `EditorDiffParams` · Result: `EditorDiffResult`
- **`editor.diagnostics`** — Return editor-fresh LSP diagnostics for a file (open buffers); the daemon filters by severity.
  - Params: `EditorDiagnosticsParams` · Result: `EditorDiagnosticsResult`

### daemon → attached client

- **`event.push`** — Deliver an event on a subscribed stream (at-least-once; clients dedup by (stream_id, seq, kind)).
  - Params: `EventPushParams` · Result: _(notification)_
- **`prompt.raise`** — Raise an approval/clarification prompt and waiting_* status to attached clients.
  - Params: `PromptRaiseParams` · Result: _(notification)_
- **`event.subscription_closed`** — A single stream subscription was dropped (per-stream overflow or ended); the client resubscribes from its own last_seq.
  - Params: `SubscriptionClosedParams` · Result: _(notification)_

## Events

- **`agent_error`** (`session` stream) — A session's turn failed (e.g. its provider process exited mid-turn).
  - Payload: `AgentError`
- **`agent_message_chunk`** (`session` stream) — A streamed chunk of an agent message.
  - Payload: `AgentMessageChunk`
- **`agent_mode_updated`** (`session` stream) — A session's active mode (behavior/permission mode) changed, by a client set or the agent itself.
  - Payload: `AgentModeUpdated`
- **`agent_model_updated`** (`session` stream) — A session's active model changed.
  - Payload: `AgentModelUpdated`
- **`agent_plan`** (`session` stream) — The agent's tactical per-turn plan (its todo list): the full set of entries with their status, replacing any prior plan for the turn.
  - Payload: `AgentPlan`
- **`agent_prompt_usage`** (`session` stream) — The size of the prompt input the daemon composed for a turn: bytes and an approximate, model-agnostic token estimate. Measures only client-side prompt content, not the agent-owned system prompt/history.
  - Payload: `AgentPromptUsage`
- **`agent_thought_chunk`** (`session` stream) — A streamed chunk of the agent's reasoning (thinking), distinct from its message.
  - Payload: `AgentThoughtChunk`
- **`agent_thought_level_updated`** (`session` stream) — A session's active reasoning/thought level changed.
  - Payload: `AgentThoughtLevelUpdated`
- **`approval_requested`** (`session` stream) — A mutating action is awaiting approval.
  - Payload: `ApprovalRequested`
- **`approval_resolved`** (`session` stream) — A pending approval was resolved.
  - Payload: `ApprovalResolved`
- **`memory_searched`** (`workspace` stream) — A workspace's memories were searched (memory.search), so a supervisor can see what an agent recalled. Repo-wide: delivered on the workspace stream.
  - Payload: `MemorySearched`
- **`memory_written`** (`workspace` stream) — A memory entry was created or updated (memory.write). Repo-wide: delivered on the workspace stream.
  - Payload: `MemoryWritten`
- **`pane_exited`** (`pane` stream) — A pane's process exited.
  - Payload: `PaneExited`
- **`pane_output`** (`pane` stream) — A chunk of a pane's output, on the pane stream. Lossy under load; pane.read is the authoritative scrollback.
  - Payload: `PaneOutput`
- **`provider_notification`** (`session` stream) — A provider-private ACP notification preserved verbatim as a metadata event.
  - Payload: `ProviderNotification`
- **`provider_started`** (`workspace` stream) — A provider process joined the pool (spawned for a turn or an explicit start). Repo-wide: delivered on the workspace stream.
  - Payload: `ProviderStarted`
- **`provider_stopped`** (`workspace` stream) — A provider process left the pool (exit or crash). Repo-wide: delivered on the workspace stream.
  - Payload: `ProviderStopped`
- **`slash_commands_updated`** (`session` stream) — A session's merged slash-command set changed (the agent advertised new commands): the full set of provider + daemon commands, replacing the prior set.
  - Payload: `SlashCommandsUpdated`
- **`task_closed`** (`workspace` stream) — A task was closed. Repo-wide: delivered on the workspace stream.
  - Payload: `TaskChange`
- **`task_commented`** (`workspace` stream) — A comment was added to a task. Repo-wide: delivered on the workspace stream.
  - Payload: `TaskChange`
- **`task_created`** (`workspace` stream) — A task was created. Repo-wide: delivered on the workspace stream.
  - Payload: `TaskChange`
- **`task_updated`** (`workspace` stream) — A task changed (e.g. claimed or linked). Repo-wide: delivered on the workspace stream.
  - Payload: `TaskChange`
- **`tool_call`** (`session` stream) — An agent tool call started.
  - Payload: `ToolCall`
- **`tool_call_update`** (`session` stream) — Progress update for a tool call.
  - Payload: `ToolCallUpdate`
- **`turn_end`** (`session` stream) — The agent's turn ended.
  - Payload: `TurnEnd`

## Errors

TEND-specific JSON-RPC error codes (carried as the error `code`, with typed `data`).

- **`1001` `cursor_compacted`** — Stream cursor predates the exact-replay retention window; resume at the summary boundary.
  - Data: `CursorCompactedData`
- **`1002` `conflict`** — A file mutation's cited base no longer matches (disk hash or buffer changedtick changed).
  - Data: `ConflictData`
- **`1003` `editor_unavailable`** — An editor-local capability was requested but no editor holds the session's binding.
  - Data: _(none)_
- **`1004` `no_workspace_mutation`** — A mutating operation was attempted outside git (only an ephemeral read-only workspace is available).
  - Data: _(none)_
- **`1005` `no_active_workspace`** — workspace.current was called before any workspace was opened on the connection.
  - Data: _(none)_
- **`1006` `not_prompt_capable`** — A client that did not register as prompt-capable tried to resolve a prompt.
  - Data: _(none)_
- **`1007` `unknown_change_set`** — file.diff named a change set with no retained snapshots (never recorded or evicted).
  - Data: `UnknownChangeSetData`
