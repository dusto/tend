# 2. Approvals have no TTL; a dead turn evicts its approval

- Status: accepted
- Date: 2026-07-30
- Refs: tend-3se.2, tend-3se.7 (ADR 0001)

## Context

A mutating tool (file edit, pane run, code action) blocks on the approval gate
until a prompt-capable client resolves it. The gate previously carried TTL
scaffolding: `Options.TTL` (wired to a 5-minute `approvalTTL` in the daemon)
stamped each pending approval's `ExpiresAt`, surfaced in `approval.list` and the
prompt envelope. Nothing ever enforced it — no path expired an approval — so it
advertised a deadline that never arrived.

That scaffolding modelled nothing real. An approval in this system is an ACP
`session/request_permission` call: a plain JSON-RPC request the agent issues and
then `await`s. ACP defines no timeout on it, and the harnesses we drive over ACP
(Codex, Claude Code, Kiro) block the turn on a permission prompt **indefinitely**.
Crucially the wait is free: the model call already returned a tool-use, and the
agent is idle-blocked on our reply — not re-hitting the model API on a clock. An
approval can sit for days at zero cost. A client-invented TTL would only
auto-deny an approval a human meant to answer later (stepped away overnight) —
strictly worse than letting it wait.

The real gap was different. When the turn holding a pending approval dies —
provider crash, power loss, `agent.cancel`, session end — `Gate.Request` removed
the pending on ctx cancellation and returned `ctx.Err()` **silently**. It had
already broadcast `approval_requested` on the workspace stream (ADR 0001) but
emitted no `approval_resolved`, so a subscribed client's UI showed an approval it
could never answer until the next reconnect-driven `approval.list` sync.

## Decision

**No TTL.** Remove the TTL concept from the gate: `Options.TTL`, `Gate.ttl`, the
`ExpiresAt` stamping, and the `approvalTTL` constant. An approval persists until
it is answered or its turn is cancelled. The `expires_at` fields on the wire
types (`api.ApprovalSummary`, `api.PromptRaiseParams`) are **kept declared** but
never populated (`omitzero` omits them), so this is not a wire-contract change —
"the deadline, if any" now reads as "no deadline," always.

**Evict + signal on turn death.** When `Gate.Request` observes ctx cancellation
and still owns the pending, it emits `approval_resolved` on the workspace stream
with `Approved=false` and the cause in `Reason` before returning `ctx.Err()`.
This is a client-facing eviction signal only — there is no live turn to unblock,
and the session's status is owned by whoever cancelled the turn. All
turn-death causes reach the gate identically, as ctx cancellation, so one path
covers provider crash, `agent.cancel`, and session end. It is not a user denial;
consumers distinguish it by the cause carried in `Reason`, not a new event type.
The plugin already treats `approval_resolved` as "remove from the model," so the
stale approval clears live with no plugin change. `approval.list` remains the
durable snapshot and simply no longer lists the evicted approval.

**Resume re-gates.** A pending permission is in-agent turn state that cannot
survive a provider process death, so "resume where we left off" means the
*session* resumes (via `session.resume_seed`) and re-issues a **fresh**
permission request when it re-reaches the gated action — a recurrence, not
restoration of the same approval object. The gate does not get stuck after an
eviction: a subsequent `Request` raises a new approval with a new id.

## Consequences

- An approval never auto-expires; a human can answer it minutes or days later.
- A UI subscribed to the workspace stream clears a stale approval the instant its
  turn dies, rather than only on the next `approval.list` sync.
- Exactly one of {eviction signal, `Resolve`} fires per approval: both go through
  `claim(id)`, so a decision and an eviction cannot both emit.
- Keeping a provider process alive while its turn is parked on a pending approval
  is at most a soft optimization (do not reap eagerly), not a correctness
  requirement — resume covers the unpreventable-death case.

## Alternatives considered

- **A TTL / auto-expiry** (auto-deny after N minutes): rejected — ACP has no
  permission timeout, the harnesses block indefinitely at zero cost, and
  auto-denying risks discarding an approval a human meant to answer later. It
  models nothing real and contradicts the durable-queue decision in ADR 0001.
- **Keep an unenforced `ExpiresAt`**: rejected — advertising a deadline that
  never arrives is misleading; a client could render "expires in 5 min" that
  never expires.
- **Pin the provider alive while an approval is pending** (so the same approval
  can be answered after any outage): rejected as a correctness mechanism —
  crashes and power loss are outside our control, so resume must handle them
  anyway; pinning is at best an optimization on top of resume.
- **A new `approval_evicted` event**: rejected — `approval_resolved(Approved=false)`
  with the cause in `Reason` already means "this approval is gone," and clients
  already handle it; a new event would be contract churn for no new information.
