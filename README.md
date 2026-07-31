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

## Layout

- `cmd/tendd` — daemon entrypoint
- `cmd/tend` — CLI / debug client
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
