# AGENTS.md

Guardrails and conventions for working in **tend** (the TEND daemon `tendd` + CLI `tend`).
Harness-agnostic: this file is the source of truth so any agent or contributor — regardless
of tooling — knows the standards, tech choices, and available guidance.

## Project

TEND (Tasked Editor-Native Delegation): a local-first, supervised Neovim AI work system.
`tendd` owns ACP provider processes, task-scoped sessions, a multiplexed event bus,
approvals, and editor/pane/LSP tool services. The Neovim plugin (`tend.nvim`) and the CLI
talk to the daemon over a **bidirectional JSON-RPC 2.0 Unix socket**.

## Dev commands

```sh
go build ./...                 # build
go test ./...                  # tests (stdlib testing, table-driven)
go vet ./...                   # vet
gofmt -l .                     # format check (must be empty)
golangci-lint run              # lint (config: .golangci.yml)
go generate ./...              # regenerate schemas/ + docs/api.md from api/
```

Toolchain is pinned in `.mise.toml` (Go 1.26.3). CI runs build + test + vet + gofmt +
golangci-lint on every PR, plus a codegen-drift check.

### Before pushing a PR (required)

Run **all** of these locally and ensure they pass — do not push a PR until they are green.
CI enforces the same checks; running them locally first is mandatory, not optional.

```sh
go build ./...                 # must succeed
go test ./...                  # all tests pass
go vet ./...                   # no findings
gofmt -l .                     # output must be empty
golangci-lint run              # 0 issues
go generate ./... && git diff --exit-code schemas/ docs/api.md   # no codegen drift
```

## Tech choices (ADR)

These are deliberate. Do not introduce the rejected alternatives without an explicit decision.

| Area | Choice | Not |
|---|---|---|
| Transport | bidirectional JSON-RPC 2.0 over Unix socket | gRPC / protobuf |
| Wire contract | Go types in `api/` are the single source of truth; codegen → `schemas/`, `docs/api.md` | hand-maintained schemas |
| CLI | `urfave/cli` v3 (`github.com/urfave/cli/v3`) | cobra |
| Config | struct-based decoding (TOML for provider defs) | viper |
| Testing | stdlib `testing`, table-driven | testify |
| Logging | `log/slog` (+ `samber/slog-*` handlers as needed) | zap / logrus / zerolog |
| Code indexing | SQLite — prefer pure-Go `modernc.org/sqlite` (no cgo) | cgo drivers |
| Dependency injection | manual constructor injection | wire / dig / fx / samber/do |

## Conventions

- **Commits & branches: conventional style.** Commit subjects `feat: …`, `fix: …`,
  `refactor: …`, `chore: …`, `docs: …`. Branch names `type/short-desc`
  (e.g. `refactor/rename-namespace-to-tend`). Never prefix with task keys.
- **Branch + PR per task.** Reference the task in a `Refs: <id>` footer, not the subject.
- **Layout:** `cmd/tendd`, `cmd/tend`, `internal/{rpc,acp,tasks,events,approvals,editor,pty,lsp,memory}`,
  `api/` (wire-contract source of truth), `schemas/` + `docs/` (generated).

## Coding guidance (skill index)

The following Go practice areas are the standards for this repo. They are encoded as
Claude Code skills (from `samber/cc-skills-golang`), but the expectations apply under any
harness — follow them even if the skill itself isn't loaded.

**Core**

- `golang-project-layout`, `golang-naming`, `golang-code-style`
- `golang-error-handling` — wrap with `%w`, sentinel errors, `errors.Is/As`, slog
- `golang-concurrency` — goroutines, channels, pools, no leaks (process pool, event bus)
- `golang-context` — propagation, cancellation, deadlines across the daemon
- `golang-structs-interfaces` — accept interfaces, return structs; small interfaces
- `golang-design-patterns` — functional options, graceful shutdown, resilience
- `golang-cli` — CLI structure (with `urfave/cli` v3)
- `golang-testing` — table-driven, fakes, goleak
- `golang-lint`, `golang-continuous-integration`
- `golang-safety` — nil/panic/concurrent-map/numeric safety
- `golang-documentation`, `golang-modernize`, `golang-dependency-management`

**Also in scope**

- `golang-performance`, `golang-benchmark` — for the event bus / process pool hot paths
- `golang-observability`, `golang-samber-slog` — structured logging
- `golang-database` — SQLite code-indexing store

**Deliberately excluded** (do not reach for these): cobra/viper, testify, gRPC, GraphQL,
Swagger, DI frameworks (wire/dig/fx/samber-do), and the other `samber-*` libraries
(lo/mo/ro/hot/oops).

## License

MIT.
