# 6. Approval, not a task, gates a mutation

- Status: accepted
- Date: 2026-08-29
- Refs: tend-5oc.13, tend-du1.14

## Context

tend is **supervised**: an agent's mutating actions — writing a file, applying a
change set, running a command in a pane — are routed through the daemon and held
at an **approval gate** until you review and approve them. That gate is the point
of the whole system: it is what keeps an agent from scribbling on disk or running
commands unwatched.

Until now the daemon layered a second precondition *before* that gate: a mutation
was refused outright unless the session had a **task** bound to it. A task-less
"conversation" session could read and converse but could not mutate anything —
`file.write`/`file.patch`/`file.apply_change_set` returned *"a task is required to
modify files"* and `pane.run` returned *"a task is required to run commands"*, in
both cases at the top of the handler, before any approval was raised.

The stated intent was traceability: every agent mutation should map to a tracked
work item. In practice the rule over-gates real work and conflates two separate
questions:

- **Is this action authorized?** — a supervision question, already answered by the
  approval gate (you review the diff / the command and approve it).
- **Is this work tracked?** — a bookkeeping question about task association.

Binding the first to the second breaks ordinary flows:

- **One-off asks.** "Fix this typo", "write a quick script", "drop a note in the
  README" should not require first creating a tracked task. The approval already
  supervises the edit.
- **Plan-document generation (a chicken-and-egg).** An agent is asked to write a
  planning document that is then broken down *into* tasks (this is exactly
  tend-5oc.5, "generate + maintain backlog from planning"). Under the old rule it
  could not write the plan, because the tasks the plan would define do not exist
  yet. The feature contradicted itself.

The failure was also **opaque**: because the refusal happened before the approval
gate, no `approval_requested` was ever emitted, so nothing appeared in Neovim or
tend-ui — a task-less write just failed, repeatedly and invisibly (observed in
tend-du1.14).

## Decision

**Remove task-gating from every mutating action. The approval gate is the sole
supervision boundary. Task association becomes optional metadata, not a
precondition.**

- `file.write`, `file.patch`, and `file.apply_change_set` no longer require a
  bound task; they run their normal change-set → approval → apply flow whether or
  not the session has a task.
- `pane.run` no longer requires a bound task; an agent-initiated run is still
  approval-gated exactly as before.
- **Uniformly, with no per-action distinction.** A file edit and a command run are
  both mutations; both are supervised by approval; neither needs a task. We do not
  special-case "writes are safe but commands are not" — the approval gate is where
  that judgement is made, per action, by the human.
- A session's `Task` is unchanged as a field: when a task is bound (by delegation)
  the mutation is attributable to it; when it is not, the work is untasked. Tasks
  can be created and linked afterward (e.g. when a plan is broken into a backlog).

Nothing about the approval flow, the worktree boundary, or the outside-worktree
consent decision (ADR 0003) changes. Those are the real guards and they remain.

## Consequences

- Task-less sessions can now write files and run commands — **each still gated by
  an approval you must grant.** The change removes ceremony, not supervision.
- One-off edits and the plan-document flow work without inventing a task first;
  tend-5oc.5 becomes coherent.
- The refusal that was invisible (no approval emitted) is gone: a task-less
  mutation now surfaces a real approval prompt like any other, so a client (Neovim
  today; tend-ui once tend-du1.12 lands) can act on it.
- **Traceability becomes opt-in.** An untasked mutation is not tied to a work item
  at creation time. Mitigations: the approval itself is a supervised, logged
  decision (who approved what diff/command); a task can be linked to the resulting
  change afterward; and a workspace that wants strict tracking can adopt a policy
  layer later (see Alternatives). We judged forced up-front tracking to cost more
  than it bought.
- The `files.ErrNoTask` sentinel and the two "a task is required" messages are
  removed; no wire contract changes (no method or schema is added or altered), so
  no contract-version bump.

## Alternatives considered

- **Keep task-gating as-is:** rejected. It conflates authorization with tracking,
  breaks one-off asks, and makes the plan→backlog feature self-contradictory. The
  invisible-failure UX is a direct symptom.
- **Auto-create an ad-hoc "untracked" task on the first task-less mutation:**
  deferred, not rejected. It would preserve attribution without ceremony and could
  be layered on top of this decision later. We start with plain optional
  association to keep the change small and the model simple; revisit if untracked
  mutations prove hard to audit in practice.
- **Path-scoped allowance** (writes to a planning/scratch dir allowed untasked,
  code writes still gated): rejected. It draws an arbitrary, leaky boundary and
  still fails the "quick edit to a real file" case; the approval gate is a cleaner
  place to make per-action judgements.
- **A config/mode flag** (`allow_untasked_mutations`): rejected as the default
  mechanism. It punts the policy to configuration rather than deciding the model.
  A workspace that genuinely needs mandatory task tracking can add such a policy
  later as an explicit opt-in; the daemon's default is that approval supervises.
- **Keep the gate only for `pane.run`** (commands are riskier than edits):
  rejected per the decision above — the human makes that risk call at the approval,
  per action; a blanket task precondition is the wrong tool for it.
