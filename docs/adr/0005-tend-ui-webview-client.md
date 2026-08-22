# 5. The rich session UI is a standalone webview client that owns its HTTP surface

- Status: accepted
- Date: 2026-08-22
- Refs: tend-du1, tend-du1.1

## Context

tend is terminal-native. **tendd** is the durable runtime — it owns sessions,
the event bus, approvals, tools, and workspace state, and exposes a bidirectional
JSON-RPC protocol over a Unix socket. **Neovim** (via tend.nvim) is the edit and
control client; the **CLI** is a terminal client. All three are clients of the
same daemon.

Some of what a session produces is awkward inside a terminal: a live event
timeline, rendered markdown, diffs, plans, generated webpages and frontend
previews, images, graphs, and other agent artifacts. We want a **richer visual
surface** for the same durable sessions — one that can attach to a running
daemon, replay what it missed, follow live, and preview artifacts — **without**
turning tendd into a UI server.

The guiding principle is *multiple clients, one durable runtime*: none of the
clients owns the session lifecycle, and the daemon boundary stays narrow —
socket protocol, session runtime, event/message bus, tool/approval coordination.
UI code, rendering, frontend assets, and browser bridges are explicitly **not**
the daemon's job. If a local asset server or browser bridge is ever needed, it
belongs to the UI, not to tendd.

A toolkit spike compared Fyne, gogpu/ui, and `webview_go` with the same dark
IDE-style layout. `webview_go` won: the smallest binary (~3 MB vs ~32 MB Fyne,
~17 MB gogpu/ui), the closest match to the intended layout and interactions, and
— decisively — **HTML/CSS/JS is the format an agent can generate and patch most
reliably**, which matters because agent-authored rich content (dashboards,
graphs, previews) is a first-class goal. On Fedora-style WebKitGTK it needs a
one-line `webkit2gtk-4.1` pkg-config change over the upstream `4.0` default.

## Decision

Build **`tend-ui`** as a standalone process (its own repository): a `webview_go`
shell rendering HTML/CSS/JS, attached to tendd over the existing daemon socket as
a peer client. It is not a second editor and not served by tendd.

- **tend-ui owns an in-process loopback HTTP server; tendd stays non-HTTP.** The
  frontend approach is htmx, which swaps HTML fragments fetched over HTTP — but
  tendd must remain a pure protocol daemon. We resolve this by putting a small
  HTTP server **inside tend-ui**: it is an adapter that translates daemon
  JSON-RPC into HTML fragments and streams the daemon's event bus to the browser
  as Server-Sent Events. The webview loads `http://127.0.0.1:<random>` gated by a
  per-run unguessable token. The HTTP concern lives entirely in the UI process;
  the daemon contract is unchanged.

- **Frontend stack: htmx + SSE + Alpine.js + templ, no build step.** htmx drives
  server-rendered fragment swaps for navigation and actions; SSE (htmx's SSE
  extension) carries the live event stream so the timeline and session list
  update without polling; **Alpine.js** handles local UI state that should not
  round-trip (expand/collapse an artifact card, filter tabs, dropdowns, the font
  picker); **templ** renders type-safe Go HTML components/fragments, reusing the
  daemon's Go types. htmx and Alpine are vendored and embedded (`go:embed`) — no
  Node toolchain, no JS/TS compilation.

- **Reuse the daemon client, not the wire format twice.** The Go JSON-RPC client
  the CLI already uses (dial, handshake, register, typed calls) is extracted into
  a shared package that both `cmd/tend` and `tend-ui` import, over the same `api`
  contract. tend-ui gains no private wire surface; a contract change reaches it
  through the same version pins as any other client.

- **Artifacts live in the timeline, not a separate browser.** Artifacts render as
  inline, expandable cards in the session timeline **where the tool call produced
  them**; a filter scopes the timeline to tool calls and artifacts, and the
  artifact index is a jump-index *into* the timeline rather than its own surface.
  (The initial visual design and mockups are recorded outside this ADR.)

- **Privileged shell vs. sandboxed agent content.** The operational shell is
  owned tend-ui HTML/CSS/JS; it may talk to the loopback server and, through it,
  the daemon. **Agent-authored** artifact and preview content (markdown, HTML,
  generated frontends, webpages) runs as constrained preview content in a
  separate sandbox — its own origin/iframe, loopback-only serving from the
  intended artifact directory, an unguessable per-run URL/token, cleaned up
  afterward — and gets **no** daemon-socket access and no privileged UI API. An
  "open in external browser" escape hatch remains for debugging or full browser
  behavior.

## Consequences

- The daemon boundary stays narrow and unchanged: no HTTP server, no asset
  serving, no UI logic in tendd. Everything web lives in tend-ui.
- tend-ui rides the existing `api` contract and daemon client, so it stays in
  step with the wire protocol for free and its version compatibility is governed
  the same way the CLI's is.
- Keeping rendering in Go (templ) with HTML-over-the-wire means UI logic reuses
  daemon types and stays server-side; the client JS surface is deliberately
  small. HTML/CSS/JS is also the surface agents generate most reliably, which is
  the point for agent-authored visuals.
- New surface to own: a `webview_go` shell (cgo + GTK/WebKit on Linux; carry or
  upstream the `webkit2gtk-4.1` pkg-config fix), a loopback HTTP + SSE server, the
  templ component set, and the sandbox boundary. This is UI infrastructure in a
  new repo, not a daemon change.
- The loopback server is an attack surface if mishandled: it must bind loopback
  only, require the per-run token, and never expose daemon access or privileged
  controls to sandboxed preview content. The preview boundary must be enforced
  and its per-run servers cleaned up.
- Read-only observation comes first (list, attach, replay, follow); mutation
  controls (prompt, approve/deny, stop) are gated behind a proven detached-
  observation flow before they are added.

## Alternatives considered

- **Embed the UI in tendd / serve HTML from the daemon:** rejected. It violates
  the narrow-daemon principle, couples the durable runtime to UI churn, and forces
  an HTTP server and auth story into the daemon. Putting the HTTP surface in
  tend-ui keeps tendd a pure protocol/runtime daemon.
- **A native-widget toolkit (Fyne, gogpu/ui):** kept as a fallback, not the first
  cut. Native widget trees are markedly harder for an agent to generate and adjust
  than HTML/CSS/JS, Fyne's binary is ~10× larger, and gogpu/ui is young with no
  webview. `webview_go`'s HTML surface is the whole reason it fits — revisit the
  natives only if WebKitGTK proves unacceptable after a real prototype.
- **A Wails-style app framework:** rejected. It adds a backend-like layer inside
  the UI process that overlaps tendd's responsibilities; we already have the
  durable backend.
- **htmx + a TypeScript companion (Stimulus / Lit), or Datastar instead of
  htmx+Alpine:** considered and deferred. TypeScript introduces a frontend build
  step we do not want yet; Alpine drops in with none. Datastar's SSE-native model
  is an appealing fit for such a stream-heavy UI but is younger and less
  documented — a bigger bet for agent-maintained code. Start with htmx + Alpine;
  revisit if local state outgrows Alpine.
- **Bind Go functions to JS (webview `Bind`) with client-side rendering, no HTTP
  server:** rejected as the primary model. It pushes rendering into JS —
  duplicating the Go types and losing templ's type-safe server rendering — and
  does not fit htmx's HTTP semantics. The loopback server keeps rendering in Go.
- **Static assets served from tendd over the socket:** rejected. It does not fit
  htmx's fetch-fragments-over-HTTP model and would make the daemon serve UI
  assets, which is exactly the coupling this ADR avoids.
