# Migration plan: Rust `argent` → Go `argent-go`

Source of truth for behaviour: `<local-checkout>` (Rust, v0.4.0, read-only reference).
Target: this repository. Go 1.27, **stdlib only** (no `go get`, no third-party modules).

## Hard rules for every slice

1. **Stay inside your directories.** Never edit `go.mod`, `README.md`, `docs/`, or another
   slice's package.
2. **No git commands.** The integrator commits. Do not run `git add/commit/checkout`.
3. **No new module dependencies.** stdlib + already-present packages only.
4. **Cross-package imports are limited to `internal/llm`.** If you need config, session,
   tools, security, mcp or the agent loop, declare a small local interface in your own
   package instead of importing it. This is what lets six slices land at once; the
   integrator wires the real types afterwards.
5. **Verify only your own packages**: `go vet ./internal/<yourpkg>/...` and
   `go test ./internal/<yourpkg>/...`. Never run repo-wide builds or tests.
6. Add unit tests inside your package (`*_test.go`, table-driven, no network).
7. Files must be `gofmt`ed (`go fmt ./internal/<yourpkg>/...`).

Shell setup (Go is not on PATH in child sessions):

```powershell
$env:Path = "$env:USERPROFILE\.tools\go\go\bin;$env:Path"
cd <local-checkout>
go vet ./internal/mypkg/...
```

## Package layout

```text
cmd/argent            CLI entry, wiring                       (slice: cli)
internal/llm          messages, tool calls, protocol clients   (slice: llm)  [exists]
internal/provider     provider templates + resolution          (slice: llm)
internal/agent        model/tool loop                          (integrator) [exists]
internal/tools        tool registry + built-in tools           (slice: tools) [exists]
internal/security     policy, approvals, audit                 (slice: security)
internal/session      JSONL session tree, resume, compaction   (slice: session)
internal/config       layered config + profiles                (slice: session)
internal/mcp          MCP client (stdio / HTTP / SSE)          (slice: mcp)
internal/hooks        hook document trust + dispatch           (slice: mcp)
internal/tui          full-screen terminal UI                  (slice: tui)
internal/server       --serve / --rpc / acp transports         (slice: cli)
```

## Shared contracts

These are the only types a slice may rely on. `internal/llm` is the single shared
package; **additive changes only** (no renames, no signature changes to `Chat`).

```go
package llm

type Message struct {
    Role       string     // system | user | assistant | tool
    Content    string
    ToolCalls  []ToolCall
    ToolCallID string
    Reasoning  string     // thinking / reasoning text (optional, additive)
    Usage      *Usage     // optional, additive
}

type ToolCall struct {
    ID       string
    Type     string           // "function"
    Function ToolCallFunction
}

type ToolCallFunction struct {
    Name      string
    Arguments string          // raw JSON
}

type ToolSpec struct {
    Type     string
    Function struct {
        Name        string
        Description string
        Parameters  json.RawMessage
    }
}

type Usage struct {
    PromptTokens     int
    CompletionTokens int
}

// Implemented by every protocol client (openai, anthropic, responses).
type Chatter interface {
    Chat(ctx context.Context, model string, messages []Message, tools []ToolSpec, onText func(string)) (Message, error)
}
```

Existing wire types (`Client`, `New`, `Chat`) stay as they are; new protocols are new
types in the same package exposing `Chat` with the same signature.

## Interface sketches each slice must honour

Declare these in your own package; the integrator adapts them.

```go
// tools: guard hook consulted before every tool runs
type Guard interface {
    Check(ctx context.Context, name string, args json.RawMessage) error
}
// func (r *Registry) SetGuard(g Guard)

// session
type Record struct { ID, Parent, Kind string; TS time.Time; Data json.RawMessage }
type Meta struct { ID, Title string; Updated time.Time }
// Open(dir) (*Store, error)
// (*Store) Append(kind string, payload any) error
// (*Store) Load(id string) ([]Record, error)
// (*Store) List() ([]Meta, error)
// (*Store) Compact(keep int) error

// config
type Config struct {
    Model, BaseURL, APIKey string
    Protocol, Locale       string
    SandboxMode            string // read-only | workspace-write | danger-full-access
    MaxTurns               int
    Yolo                   bool
    StorageDir             string
}
// Load(cwd string, env func(string) string) (Config, error)

// security
type Decision int // Allow, Ask, Deny
type Policy struct{ /* denyPaths, allowPaths, denyCommands, denyTools */ }
// (*Policy) Check(name string, args json.RawMessage) Decision
// Approver interface { Ask(ctx, prompt string) (bool, error) }

// mcp
type Server struct { Name, Command, URL, Transport string; Args, Env []string }
type Tool struct { Name, Description string; Schema json.RawMessage }
// Connect(ctx, Server) (Client, error)
// Client.ListTools(ctx) ([]Tool, error); Client.Call(ctx, name string, args json.RawMessage) (string, error)

// tui
type Options struct {
    Root, Model, Locale string
    Prompt              func(ctx context.Context, text string, onText func(string)) error
    Approval            func(ctx context.Context, name, args string) (bool, error)
    Sessions            []string
}
// Run(ctx, Options) error
```

## Slices for this wave

| Slice | Directories | Deliverable |
|---|---|---|
| llm | `internal/llm`, `internal/provider` | Anthropic Messages + OpenAI Responses streaming clients (tool_use / tool_result / thinking / usage), provider template table, retry on 429/5xx with backoff |
| security | `internal/security` | path/command/tool policy, approval gate (interactive + `never`), sandbox modes, redacted JSONL audit log |
| session | `internal/session`, `internal/config` | JSONL session tree (id/parent), resume, list, compaction, layered config (defaults → home → project → env → flags) + profiles |
| tools | `internal/tools` | more built-ins: `glob`, `todo`, `multi_edit`, `web_fetch` (no browser), `git_status`; registry guard hook; per-tool output caps |
| mcp | `internal/mcp`, `internal/hooks` | MCP client over stdio + HTTP/SSE, tool discovery, hook document trust check and dispatch |
| tui | `internal/tui` | full-screen renderer (alt screen, ANSI), input editing, streaming transcript, approval modal, scrollback |
| cli | `cmd/argent`, `internal/server`, `internal/cli` | full flag surface, subcommands (`sessions`, `doctor`, `login`, `eval`), `--serve` HTTP/SSE, `--rpc`, `acp` |

Not in this wave (later): plugins/script-plugins, LSP/AST, pty/jobs/device, workflow,
automation, telemetry, update, desktop bridge for the Tauri shell.

## Wave 2 (running in parallel with wave 1; same hard rules)

Wave-1 directories are off limits: `internal/{llm,provider,security,session,config,tools,mcp,hooks,tui,cli,server}`
and `cmd/argent`.

| Slice | Directories | Deliverable |
|---|---|---|
| agent | `internal/agent` | full loop: tool batching, approval gate, dedupe guard, context trimming, token/cost budget, turn limit, steering, subagents, cancellation, usage accounting |
| context | `internal/skills`, `internal/commands`, `internal/memory`, `internal/i18n` | SKILL.md discovery + progressive disclosure, markdown slash commands, project memory files (AGENTS.md/CLAUDE.md) injection, zh-CN/en-US strings |
| plugins | `internal/plugins`, `internal/lsp` | `.zcode-plugin`/`.claude-plugin`/`.codex-plugin` manifest discovery, install/enable, bundled MCP+skills+hooks wiring; LSP client (stdio JSON-RPC) with diagnostics after edits |
| proc | `internal/proc`, `internal/pty`, `internal/jobs` | process spawn/kill trees with timeouts (Windows job objects via pure syscall), pseudo-terminal (ConPTY / unix pty behind build tags), background job registry with logs |
| eval | `internal/eval`, `evals/` | scripted scenario runner (JSON) + the ported `evals/core.json` scenarios, fake provider, parity report output |
| automation | `internal/automation`, `internal/workflow` | cron-style scheduler + daemon mode with run logs, scripted workflow steps with retries/conditions |
| auth | `internal/auth`, `internal/update` | OAuth PKCE / device-code login with token store (DPAPI-free: file with 0600 + optional env), self-update from GitHub releases with checksum verify and atomic replace |

Still deferred: native C-ABI plugin loader (Go uses process/MCP plugins instead),
tree-sitter AST, desktop control, and the Tauri bridge (wave 3, needs `internal/server` settled).

## Retirement of the Rust checkout`github.com/<owner>/argent` is frozen at v0.4.0 and archived; the local checkout
`<local-checkout>` is the behavioural reference **only until this wave lands**.
After the last slice is merged and the Go tests are green, delete the local Rust
directory (history stays on GitHub). Do not delete it earlier: the slices read it.

## Reporting

When done, report: files added, exported API, what is stubbed, verification output
(`go vet`, `go test`), and any contract mismatch you hit. Keep it under 15 lines.
