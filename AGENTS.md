# AGENTS.md

Guidance for AI agents working in this repository.

## What this is

`argentgo` is a personal coding-agent harness: stream a model reply, run tools,
append to a JSONL session, repeat. It is a Go rewrite of the frozen Rust project
`argent` (v0.4.0, archived on GitHub). Behaviour should match that build; the
feature checklist lives in `docs/TECHNICAL.md` (Feature parity section).

Interfaces to match: OpenAI chat completions, OpenAI Responses, Anthropic
Messages. UI strings are zh-CN and en-US, embedded in the binary.

## Toolchain

Go 1.27. Windows host; use PowerShell. The toolchain lives at

```powershell
$env:Path = "$env:USERPROFILE\.tools\go\go\bin;$env:Path"
```

```powershell
go build -o argent.exe ./cmd/argent     # binary
go test ./...                            # all packages
go test ./internal/agent -run TestName   # one test
go test -bench . -benchtime=10x ./...    # benchmark smoke: assertions must pass
go vet ./...                             # vet everything
gofmt -l .                               # must print nothing
```

Benchmarks are tests: their assertions run under `go test`, so a stale
assertion breaks the build even though CI never times them. Run the smoke pass
whenever you touch a session/agent/tools hot path.

Desktop frontend (`native/`, MyGo native UI):

```powershell
go build -o argent-native.exe ./native   # the native chat window
```

The browser frontend has been removed. `native/` drives the engine over `--rpc`.

## Layout

```text
cmd/argent          entry point; builds the App and calls cli.RunWith
internal/app        the only package that wires everything together
internal/cli        flags, subcommand dispatch, transports invocation
internal/server     --serve (HTTP/SSE), --rpc, acp
internal/agent      model/tool loop: budget, trimming, dedupe, steering, subagents
internal/llm        wire protocols (chat, anthropic, responses) + retry
internal/provider   provider templates
internal/tools      registry + built-in tools (fs, shell, search, todo, patch)
internal/security   policy, approvals, sandbox modes, audit log
internal/session    JSONL session tree, resume, fork, compaction
internal/config     layered config + profiles
internal/mcp        MCP client (stdio, HTTP/SSE)
internal/hooks      hook documents, SHA-256 trust, dispatch
internal/plugins    plugin manifest discovery/install
internal/lsp        LSP client (diagnostics, definition, hover)
internal/skills     SKILL.md discovery, progressive disclosure
internal/commands   markdown slash commands
internal/memory     AGENTS.md/CLAUDE.md injection
internal/i18n       zh-CN/en-US strings (go:embed)
internal/proc       process spawn/kill trees, timeouts
internal/pty        ConPTY / unix pty
internal/jobs       background job registry
internal/automation cron scheduler + daemon
internal/workflow   scripted multi-step tasks
internal/auth       PKCE / device-code login, token store
internal/update     GitHub release check + self-update
internal/eval       scenario runner (evals/core.json)
internal/tui        full-screen terminal UI
native/             MyGo native desktop chat window (the only GUI frontend)
```

## Dependency rules

- **Core is stdlib only.** The `internal/{agent,session,llm,tools,security,...}` packages
  must stay dependency-free. The native desktop shell (`native/`) is the single
  explicit exception: it requires `github.com/egoist/mygo` for GPU-rendered UI.
  It drives the engine over the stdlib `--rpc` transport. Do not add other modules.
- **No cgo.** Everything must cross-compile with `GOOS=linux go build ./...`.
- Go tools: `internal/app` is the only package allowed to import everything.
  Library packages may import `internal/llm` and stdlib; otherwise take
  interfaces (`ToolRunner`, `Approver`, `Recorder`, `Chatter`) as parameters.
- Provider wire details live only in `internal/llm`. Keep protocol quirks out
  of the agent loop.
- `internal/tools` never imports `internal/agent`.

## Organisation and paradigms

Three paradigms do different jobs here; do not force one to do another's work.

- **Modules are packages, not files.** The module boundary is `internal/<pkg>`
  and the dependency rules above — splitting a large file into several
  `foo_*.go` files is package-internal tidying and never changes the module
  graph. Choose file boundaries by responsibility ("how many files to change
  one behaviour?"), never by line count.
- **OOP in Go is composition + interfaces, not classes or inheritance.** Structs
  hold state, methods operate on it. Depend on small interfaces injected at the
  call site (`ToolRunner`, `Approver`, `Recorder`, `Chatter`); define the
  interface at the consumer, not the provider. Reuse by embedding/composing —
  there is no inheritance, and faking one produces rigid hierarchies.
- **Functional style is for streaming and strategy injection, not everywhere.**
  First-class functions and closures are the right tool for callbacks (`onText`,
  `Emit`, `Compactor`, `RecorderFunc`, lazy `sync.Once`), and for passing a
  policy in without coupling to its implementation. Keep plain loops and
  explicit state for ordinary transformations: Go has no rich higher-order
  collection stdlib, and forcing map/filter/reduce hurts readability. Do not add
  a dependency just to get functional helpers.

## Invariants worth breaking tests over

- Every filesystem tool resolves paths inside the workspace; `..` and absolute
  escapes are rejected.
- Mutating tools go through the security chain (policy → approval → audit). A
  policy deny ends the run; an approval denial becomes a tool error the model
  can read.
- `--sandbox-mode read-only` drops mutating tools; `workspace-write` (default)
  keeps the workspace boundary; `danger-full-access` removes it.
- Sessions are JSONL, one record per line, linked by `id`/`parent`. Compaction
  must preserve changed files, failures and next steps.
- Redaction happens before anything is written to the audit log.
- A cancelled context stops the run within one turn, and tool output is never
  truncated in the middle of a UTF-8 rune.

## Style

- `snake_case` files, `PascalCase` types.
- Split files by responsibility, not by line count. A cohesive 800-line file is
  better than four fragmented 200-line files you have to jump between. Use the
  real test: "how many files must I open to change one behaviour?" If the answer
  is already one, leave it alone — do not split just to hit a number. Go's own
  stdlib has multi-thousand-line files; length is not a smell, mixed concerns are.
- Tests beside source, table-driven, no network, no real model. Name tests after
  the behaviour: `TestEditRequiresUniqueMatch`.
- Errors: wrapped with `%w`, sentinel errors for callers to match with `errors.Is`.
- Comments explain *why*; identifiers are English.
- Commits: `<area>: <imperative summary>` (e.g. `tools: reject path escapes`).

## Do not

- Never commit built binaries (`argent.exe`, `argent-native.exe`) or `node_modules`.
  The native frontend is built from `native/` source at release time.
- Do not edit the Feature parity statuses in `docs/TECHNICAL.md` without running the corresponding test.
- Do not add a dependency to save 30 lines of code.
