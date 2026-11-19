# AGENTS.md

Guidance for AI agents working in this repository.

## What this is

`argent-go` is a personal coding-agent harness: stream a model reply, run tools,
append to a JSONL session, repeat. It is a Go rewrite of the frozen Rust project
`argent` (v0.4.0, archived on GitHub). Behaviour should match that build; the
feature checklist lives in `docs/PARITY.md`.

Interfaces to match: OpenAI chat completions, OpenAI Responses, Anthropic
Messages. UI strings are zh-CN and en-US, embedded in the binary.

## Toolchain

Go 1.27, Windows host, Git Bash for shell work. The toolchain is not

```powershell
$env:Path = "$env:USERPROFILE\.tools\go\go\bin;$env:Path"
```

```powershell
go build -o argent.exe ./cmd/argent     # binary
go test ./...                            # all packages
go test ./internal/agent -run TestName   # one test
go vet ./...                             # vet everything
gofmt -l .                               # must print nothing
```

Frontend (`ui/`, SolidJS + Vite, built with bun):

```powershell
cd ui; bun install; bun run typecheck; bun run test; bun run build
powershell -File scripts/build-ui.ps1    # builds and copies into internal/gui/dist
```

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
internal/gui        WebView2 shell backend: HTTP + SSE + embedded ui/dist
ui/                 SolidJS frontend
```

## Dependency rules

- **stdlib only.** No third-party modules, no `go get`. `go.mod` has no requires.
- **No cgo.** Everything must cross-compile with `GOOS=linux go build ./...`.
- Go tools: `internal/app` is the only package allowed to import everything.
  Library packages may import `internal/llm` and stdlib; otherwise take
  interfaces (`ToolRunner`, `Approver`, `Recorder`, `Chatter`) as parameters.
- Provider wire details live only in `internal/llm`. Keep protocol quirks out
  of the agent loop.
- `internal/tools` never imports `internal/agent`.

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

- `snake_case` files, `PascalCase` types, small files (<500 lines preferred).
- Tests beside source, table-driven, no network, no real model. Name tests after
  the behaviour: `TestEditRequiresUniqueMatch`.
- Errors: wrapped with `%w`, sentinel errors for callers to match with `errors.Is`.
- Comments explain *why*; identifiers are English.
- Commits: `<area>: <imperative summary>` (e.g. `tools: reject path escapes`).

## Do not

- Never commit `argent.exe`, `ui/node_modules`, `ui/dist` build output produced
  by a local build (the placeholder `internal/gui/dist/index.html` is the
  exception, it keeps `go:embed` compiling).
- Do not edit `docs/PARITY.md` statuses without running the corresponding test.
- Do not add a dependency to save 30 lines of code.
