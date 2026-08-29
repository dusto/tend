# tend

**TEND** — Tasked Editor-Native Delegation. A local-first Neovim AI work system:
a Go daemon (`tendd`) and CLI (`tend`) that own ACP provider processes, task-scoped
sessions, an event bus, approvals, and editor/pane/LSP tool services. Neovim is the
primary UX via the companion [`tend.nvim`](https://github.com/dusto/tend.nvim) plugin.

The daemon and plugin communicate over a bidirectional JSON-RPC 2.0 Unix socket.

> Status: early scaffolding.

## Install

`tend` ships two binaries: `tendd` (the daemon) and `tend` (the CLI / debug
client).

**Prebuilt binaries.** Download the archive for your OS/arch (Linux or macOS,
amd64 or arm64) from the [Releases](https://github.com/dusto/tend/releases) page
and put `tendd` and `tend` on your `PATH`.

**With Go (1.26+).** Install both from source:

```sh
go install github.com/dusto/tend/cmd/tendd@latest
go install github.com/dusto/tend/cmd/tend@latest
```

This drops `tendd` and `tend` in your `$(go env GOPATH)/bin` (put it on `PATH`).
Replace `@latest` with a tag (e.g. `@v0.1.0`) to pin a released version, or build
from a checkout with `go build -o bin/ ./cmd/tendd ./cmd/tend`.

Releases are cut with GoReleaser — see [RELEASING.md](RELEASING.md).

## Quickstart

1. **Install an ACP provider.** The default Claude provider is the cleanest:

   ```sh
   npm i -g @agentclientprotocol/claude-agent-acp   # or: pnpm add -g …
   command -v claude-agent-acp                       # confirm it's on PATH
   ```

   `claude-agent-acp` is a built-in default, so no config file is required to
   use it. (It uses your existing Claude auth.)

2. **Run the daemon:**

   ```sh
   tendd          # or, from a checkout: go run ./cmd/tendd
   ```

   The startup log reports the config in use and the socket it listens on
   (`$XDG_RUNTIME_DIR/tend/tend.sock`). The daemon is a singleton per user — a
   second `tendd` defers to the running one.

3. **Drive it from Neovim** with the [`tend.nvim`](https://github.com/dusto/tend.nvim)
   plugin: `:TendConnect`, then `:TendTaskNew` and `:TendSessionNew` → `:TendChat`.
   See the plugin's docs (`:help tend.txt`).

### Configuration

`tendd` reads a TOML config from the first of: `$TEND_CONFIG`,
`$XDG_CONFIG_HOME/tend/config.toml`, `~/.config/tend/config.toml`. With no file
it uses built-in defaults; a file **replaces** those defaults. A malformed or
invalid config makes the daemon exit with an error rather than silently falling
back. See [`config.example.toml`](config.example.toml).

> The task provider is currently an in-memory stand-in (tasks do not persist
> across restarts); the beads adapter is not yet wired into the daemon.

## Editor tools for agents

tend is **editor-fronted**: an agent's file reads and edits are meant to go
*through* the daemon to your Neovim buffers, so an edit shows up as a diff and
waits for your approval instead of scribbling on disk. For agents that speak
ACP's client filesystem calls (Claude, Codex) that happens automatically. But
some agents — **Kiro** most notably — do their own file I/O and never make those
calls, so their edits would bypass the buffer, the diff, and the approval gate.

To close that gap, tend also exposes its editor operations as **MCP tools** (the
Model Context Protocol — the standard way to hand an agent extra tools). Every
MCP-capable agent, including Kiro, sees these in its tool list and can call them,
independently of whether it honors ACP's `fs` calls:

- **`read_buffer`** — read a file as the editor sees it (the live buffer when
  it's open, otherwise disk), so the agent sees your unsaved edits.
- **`open_buffer`** — open a file in your editor so you can see it. Non-mutating.
- **`write_buffer`** — propose replacing a file's whole content.
- **`edit_buffer`** — propose targeted edits to part of a file.

`write_buffer` and `edit_buffer` are **supervised**: each proposal runs through
the same change-set → approval flow as any other tend edit, so you review a diff
and it lands in your live buffer only if you approve — even for Kiro.

**No setup.** The daemon declares this MCP server to the agent automatically, per
session (via ACP's `session/new.mcpServers`); there is nothing to configure and
nothing to run by hand. Under the hood the agent spawns `tend mcp`, which dials
the running daemon and routes each tool call to your session's editor. The one
requirement is that the **`tend` binary is reachable** — installed beside `tendd`
or on the daemon's `PATH` (see [Install](#install)) — so the agent can spawn it.

See [ADR 0004](docs/adr/0004-mcp-editor-tools-server.md) for the design, and
[`docs/api.md`](docs/api.md) for the underlying wire methods.

## Layout

- `cmd/tendd` — daemon entrypoint
- `cmd/tend` — CLI / debug client
- `client/` — shared daemon-socket client (dial + handshake + register + call, with typed error codes), used by the CLI, the MCP bridge, and external clients like tend-ui; `client/clienttest` is a fake daemon for external clients' tests
- `internal/rpc` — JSON-RPC socket server (bidirectional)
- `internal/acp` — generic ACP client, provider registry, process pool, session lifecycle
- `internal/tasks` — task provider interface + beads adapter
- `internal/events` — multiplexed per-stream event bus, durable log, replay, compaction
- `internal/approvals` — approval gate and pending state
- `internal/editor` — Neovim buffer/selection/diagnostics tools
- `internal/pty` — daemon-owned PTY/pane management
- `internal/lsp` — LSP code-intelligence tools
- `internal/memory` — memory search/write tools
- `api/` — single source of truth for the JSON-RPC wire contract (codegen input)
- `schemas/` — generated JSON Schemas
- `docs/` — generated API reference and docs

## License

MIT — see [LICENSE](LICENSE).
