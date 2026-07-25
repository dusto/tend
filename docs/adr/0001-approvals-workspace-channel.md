# 1. Approvals are a workspace-stream channel

- Status: accepted
- Date: 2026-07-25
- Refs: tend-3se.7

## Context

A mutating tool (file edit, pane run, code action) blocks on the approval gate
until a prompt-capable client resolves it. Historically the gate published
`approval_requested` / `approval_resolved` on the **owning session's** stream
(`ScopeSession`), and additionally raised a `prompt.raise` notification to the
session's *attached* clients (falling back to all clients when none had
attached).

Two consequences of session-scoped delivery:

- A client only saw an approval live if it followed that session's stream. An
  approval for a session nobody follows was never pushed live; it surfaced only
  on the next reconnect-driven `approval.list` sync.
- Once any client attached to a session, `prompt.raise` scoped that session's
  approvals to the attached set, so a separate approval console could be excluded
  from approvals for sessions another client had claimed.

We want any client — the editor today, a TUI/GUI approval console tomorrow — to
receive **every** pending approval live by subscribing to **one** channel,
without enumerating and following each session.

## Decision

Publish `approval_requested` / `approval_resolved` on the **repo-wide workspace
stream** (`ScopeWorkspace`, `api.WorkspaceStream(workspaceID)`) instead of the
session stream. A client subscribes to the workspace stream once and receives
all approvals for the repo, independent of which sessions it follows.

- The live event stays a **lightweight signal** (session id, approval id, kind).
  `approval.list` remains the durable, self-contained snapshot and source of
  truth — a connecting or reconnecting client syncs from it, so an event missed
  while disconnected is never lost.
- `prompt.raise` no longer carries approvals. It is retained as the (currently
  sender-less) transport for **clarification** prompts, which stay attach-scoped.
- `session.attach` is redefined: it scopes **clarification prompts + `waiting_*`
  status** to attached clients only. It no longer gates approvals. `waiting_*`
  session status is unchanged and still delivered on the session stream.
- `DaemonToClientVersion` is bumped **breaking** (0.13.0 → **1.0.0**), not minor.
  Removing approvals from the session stream / `prompt.raise` silently strips live
  approval delivery from a pre-1.0 client — it would still connect (a minor bump
  satisfies its `>= 0.13.0` pin) but a gated tool would only resurface on the next
  reconnect-driven `approval.list`. A major bump makes such a client reject this
  daemon at the handshake (`versionAtLeast` requires a shared major), honoring the
  reject-at-handshake house rule instead of degrading in place. A client is
  compatible again once it subscribes to the workspace stream and pins
  `daemon_to_client >= 1.0.0` (the plugin counterpart, tend-48d.31).

## Consequences

- One subscription (the workspace stream) delivers every approval; the approval
  console does not track sessions.
- Approval events share the workspace stream with other repo-wide events
  (`task_*`, `provider_*`, `memory_*`); consumers filter by event type.
- The rich prompt envelope (`prompt.raise`: full detail + text + expiry) is no
  longer pushed for approvals; a client fetches detail from `approval.list`. This
  is a deliberate trade for a single, uniform delivery path.

## Alternatives considered

- **Mirror / dual-emit** (session *and* workspace stream): rejected — two live
  copies of every approval, ambiguous source of truth, and consumers must dedup
  across streams.
- **Worktree scope** (`api.WorktreeStream`): rejected — approvals are a
  repo-wide concern; a console should see the repo's approvals without knowing
  its worktrees. Workspace scope matches how the pool and task/provider events
  are already delivered.
- **Keep attach-gated `prompt.raise` for approvals**: rejected — it is exactly
  the mechanism that hid approvals from non-attached clients; keeping it
  alongside the workspace broadcast is the rejected mirror.
