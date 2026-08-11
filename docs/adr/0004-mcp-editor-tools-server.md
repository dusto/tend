# 4. Editor tools are exposed to agents as an MCP server

- Status: accepted
- Date: 2026-08-11
- Refs: tend-e7p.18

## Context

tend is **editor-fronted**: the daemon does not touch files directly for an
agent. Instead, when an agent wants to read or edit a file, that operation is
routed *through* the daemon to the Neovim plugin, so the edit lands in your live
buffer, shows up as a diff, and waits for your approval. That routing is the
whole point — it is what keeps an agent supervised instead of scribbling on disk.

Today the routing depends on a feature of **ACP** (the Agent Client Protocol —
the JSON-RPC protocol tend speaks to each agent process). During the ACP
`initialize` handshake, tend advertises two *client capabilities*:
`fs.readTextFile` and `fs.writeTextFile`. An agent that respects them then calls
the ACP methods `fs/read_text_file` / `fs/write_text_file`, which tend implements
by talking to the editor (and running edits through the change-set → approval →
`editor.write_buffer` flow).

The problem: this only works for agents that *choose* to use the client `fs`
capability.

- **Claude and Codex** do — their file operations go through tend, so you get
  buffer edits, diffs, and approvals.
- **Kiro does not.** By design it performs all file I/O with its own built-in
  tools and never calls the client `fs/*` methods (documented on Zed's ACP agent
  listing and in Kiro's docs). So Kiro edits go straight to disk, bypassing the
  buffer, the diff, and the approval gate.

Crucially, **no `initialize` change fixes this.** tend already advertises `fs`
exactly as the spec prescribes (`protocolVersion: 1`, both `fs` flags true), and
Kiro ignores it regardless. Advertising capabilities tend has not built (e.g.
`terminal`) would be lying to the agent.

But there is a lever every one of these agents *does* support: **MCP** (the Model
Context Protocol — the standard way to hand an agent a set of extra tools). ACP
even has a built-in hook for it: the `session/new` request carries an
`mcpServers` field where the client lists MCP servers the agent should connect to
for additional tools. tend currently sends that list empty.

## Decision

Expose tend's editor operations to agents as **named MCP tools**, advertised via
`session/new.mcpServers`.

- **The tools.** tend serves an MCP server offering a small, explicit set of
  editor tools — `read_buffer`, `edit_buffer` (write), `open_buffer`, `diff`
  (and room to grow). Any MCP-capable agent — Kiro, Claude, Codex — sees these
  in its tool list and can call them for buffer operations, *independently of*
  whether it honors ACP client `fs`.

- **Still supervised.** Each tool call routes to the daemon's existing
  editor/file services. Mutating tools (`edit_buffer`) go through the **same**
  `file.patch` → change-set → approval → `editor.write_buffer` path, so edits
  keep their diff preview and user approval and land in the live buffer — *even
  for Kiro*. This fixes the supervision bypass, not just buffer access.

- **Transport: a stdio bridge.** tend advertises the server as a subprocess the
  agent spawns — `tend mcp --session <id>` — using the standard ACP stdio MCP
  server shape (`{ name, command, args }`). The bridge connects to the running
  daemon over its Unix socket and translates MCP `tools/list` / `tools/call` into
  daemon RPCs. This reuses the CLI's existing daemon connection; no new HTTP
  server, no extra listening port.

- **Session binding.** The daemon knows the id of the session it is creating, so
  it embeds that id in the bridge's args. Every tool call the bridge forwards is
  therefore scoped to the right session's editor.

- **Uniform, not per-agent** (the load-bearing choice). Long term, MCP
  editor-tools become the **single** way *all* agents touch buffers — one named
  tool set, uniformly supervised — and tend stops depending on each agent's ACP
  client-`fs` support. Short term, the MCP path may **coexist** with the client-
  `fs` path Claude/Codex already use, so nothing regresses while the MCP path is
  proven out. We decide the endgame now (uniform) so the design points that way.

## Consequences

- Kiro — and any MCP-capable agent that skips ACP client `fs` — gains supervised,
  in-buffer editing instead of silent disk writes.
- The MCP server becomes tend's **extensibility surface**: task, memory, and pane
  tools can be exposed to agents the same way later, with no new plumbing.
- **We can offer the tools but not force their use.** MCP puts `edit_buffer` in
  the agent's tool list; it cannot stop the agent from preferring its own native
  `write`. Clear tool names plus steering (the memory/steering surface can nudge
  "use the buffer tools for edits") bias the choice, and an explicit "edit the
  buffer" request always has the right tool available.
- New surface to own: an MCP protocol implementation, the bridge subprocess, and
  the tool schemas. If the wiring changes a plugin↔daemon or daemon↔agent
  contract, bump the affected version.
- During the coexist phase an agent could reach a buffer two ways (client `fs`
  and the MCP tool). Both terminate in the same file service, so behavior is
  consistent; the uniform endgame removes the duplication.

## Alternatives considered

- **Change the ACP `initialize` capabilities** (bump the protocol version, or
  advertise more): rejected. tend is already spec-correct, and Kiro ignores
  client `fs` by design — there is nothing to negotiate. Advertising `terminal` /
  `elicitation` that tend has not implemented would make the agent call methods
  tend cannot serve.
- **Accept disk-direct edits and lean on Neovim's file reload**: rejected as the
  primary path. The plugin's `FileChangedShell` reload does surface on-disk edits
  in your buffers, but you lose the diff and approval — which is the entire value
  of the editor-fronted model.
- **An HTTP/SSE MCP endpoint embedded in the daemon**: deferred. It needs an HTTP
  server and an auth story; the stdio bridge is less infrastructure. Revisit only
  if a transport the agent does *not* spawn is required.
- **Per-agent shims** (special-case Kiro's native tools): rejected. It does not
  scale — every new agent would need its own shim. The MCP path is one uniform
  surface for all of them.
