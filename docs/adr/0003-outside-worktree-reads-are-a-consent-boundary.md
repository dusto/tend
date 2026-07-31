# 3. Outside-worktree reads are a consent boundary, not a hard boundary

- Status: accepted
- Date: 2026-07-30
- Refs: tend-3se.6, tend-tuq.6, tend-xdb.1, tend-3se.2 (ADR 0002)

## Context

The worktree is the daemon's filesystem boundary: every tool that turns an
agent-supplied `file://` uri into a path resolves it through
`worktree.ResolvePath`, which resolves symlinks and refuses anything outside the
worktree root (`ErrOutsideWorkspace`). Reads (`file.read`, `lsp.diagnostics` and
the other LSP navigation tools) and writes alike hard-denied outside paths
(landed in tend-tuq.6 and tend-xdb.1).

A hard deny is the right default — an agent should not roam the filesystem — but
it is too blunt for reads. An agent legitimately needs to read a file just
outside the worktree (a sibling package, a generated file, a dependency in the
module cache), and today it simply cannot, with no way for the user to allow it.
Meanwhile the same boundary must stay hard for writes: an outside write is never
something to reach for.

## Decision

Turn the worktree from a hard boundary into a **consent** boundary **for reads
only**.

- Add `worktree.ClassifyPath(uri, root, extraRoots...) -> (resolved, inside,
  err)`. Unlike `ResolvePath`, an outside path is a valid, non-error result
  (`inside=false`) — the caller decides whether to refuse or ask. Only a
  malformed uri errors. Symlinks are resolved first, so a link that escapes the
  worktree classifies as outside and the resolved target is what a consent
  decision is about. **`ResolvePath` stays** the hard-deny helper: writes and any
  never-escape caller keep calling it and are refused by construction.

- A new **`filesystem_access`** approval kind + `FilesystemAccessApproval`
  detail carries the requested uri, the resolved path (they differ under a
  symlink; the user consents to the resolved target), the mode
  (`read`|`diagnostics`), and the requesting tool.

- `file.read` and the LSP reads gate outside paths: in-worktree stays ungated
  (unchanged); an outside path raises a `filesystem_access` approval and **blocks
  like any gated action**. On approval the daemon **re-resolves and requires the
  same target** before reading (TOCTOU: a symlink can be repointed between prompt
  and read; the approval is for the resolved target). On denial it returns a
  not-authorized error without touching disk or editor. When no approver is wired
  (reads cannot be gated), the outside path stays hard-denied.

- Outside-worktree **writes stay hard-denied** — `file.write` /
  `file.apply_change_set` keep using `ResolvePath`.

- Configured **extra readable roots** (e.g. the module cache) classify as
  in-scope and are read **without prompting**. The default is conservative
  (empty); broad, home-dir-style roots are opt-in config, never a default.

- `DaemonToClientVersion` gets a minor bump (1.0.0 → 1.1.0): the new approval
  detail is additive, so a client that does not render `filesystem_access` still
  sees every other approval kind.

## Consequences

- An agent can read just outside the worktree with per-access user consent,
  instead of being blocked outright.
- A gated outside read **waits for approval** — there is no timeout and no
  headless auto-deny. This is consistent with ADR 0002 (approvals have no TTL):
  the read waits until answered, or evicts when its turn is cancelled. Waiting is
  the intended behavior for anything gated, not a hang.
- The LSP gate lives in the shared `resolveTarget`, so **all** LSP navigation
  reads (diagnostics, symbols, definition, references, hover, code_actions) gate
  uniformly — avoiding the inconsistency where one read prompts and another
  refuses for the same outside file. This is slightly broader than the bead's
  "lsp.diagnostics" exemplar, and deliberately so.

## Alternatives considered

- **Keep the hard deny** for outside reads: rejected — it blocks a legitimate,
  common need (a sibling file, the module cache) with no user recourse.
- **Liberal / ungated outside reads**: rejected — a secrets-exposure footgun. An
  agent could read `~/.ssh`, `.env`, credentials, anywhere, silently. Consent per
  access is the point.
- **Remembered grants / persisted trust first**: deferred, not rejected —
  per-access consent ships first; scoped/remembered trust is a later decision
  (avoid a broad standing grant before the interaction is proven).
- **Fold outside-read permission into `file_edit`**: rejected — a read is not an
  edit; reusing the edit approval would misrepresent the operation and its
  detail (a diff/base makes no sense for a read).
- **A no-responder/headless hard-deny fallback**: dropped — a gated read simply
  waits for approval like any gated action (see Consequences / ADR 0002), so no
  special-case client-presence check is needed.

## Deferrals (each its own later decision)

- Outside-worktree **writes** (gated).
- Empty-current-buffer prompting (stays empty — no surprise path disclosure).
- Remembered grants / persistence / scoped trust.
- Broad home-dir-style allowlists.
- **Plugin rendering** of the `filesystem_access` approval (a tend.nvim follow-up:
  a prompt that shows the requested/resolved path and read mode).
