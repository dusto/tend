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

### daemon → bound editor

- **`editor.read_buffer`** — Read a file editor-aware; returns content and its base (changedtick for an open buffer, content hash for a closed file).
  - Params: `EditorReadBufferParams` · Result: `EditorReadBufferResult`

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
- **`approval_requested`** (`session` stream) — A mutating action is awaiting approval.
  - Payload: `ApprovalRequested`
- **`approval_resolved`** (`session` stream) — A pending approval was resolved.
  - Payload: `ApprovalResolved`
- **`provider_notification`** (`session` stream) — A provider-private ACP notification preserved verbatim as a metadata event.
  - Payload: `ProviderNotification`
- **`provider_stopped`** (`workspace` stream) — A provider process left the pool (exit or crash). Repo-wide: delivered on the workspace stream.
  - Payload: `ProviderStopped`
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
