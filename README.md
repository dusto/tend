# tend

**TEND** — Tasked Editor-Native Delegation. A local-first Neovim AI work system:
a Go daemon (`tendd`) and CLI (`tend`) that own ACP provider processes, task-scoped
sessions, an event bus, approvals, and editor/pane/LSP tool services. Neovim is the
primary UX via the companion [`tend.nvim`](https://github.com/dusto/tend.nvim) plugin.

The daemon and plugin communicate over a bidirectional JSON-RPC 2.0 Unix socket.

> Status: early scaffolding.

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
