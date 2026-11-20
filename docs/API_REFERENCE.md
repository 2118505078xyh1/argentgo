# API reference

This document describes the programmatic surfaces of `argentgo` as implemented
in this repository (version `0.5.0`). It is descriptive, not aspirational: every
command, method, tool and field below was read from the code. The in-process
architecture is covered by [ARCHITECTURE.md](ARCHITECTURE.md); build and release
details by [DISTRIBUTION.md](DISTRIBUTION.md).

There are two surfaces:

1. the **CLI** (`cmd/argent` → `internal/cli`), including the headless transports
   `--serve`, `--rpc` and `acp`;
2. the **tool / integration API** exposed to the model and to external systems
   (`internal/tools`, `internal/mcp`), plus the layered configuration that wires
   them.

## 1. CLI surface

`internal/cli/cli.go` builds a dispatch map (`subcommands`) and falls back to a
single one-shot prompt. The accepted invocation forms are:

```text
argent [flags] <prompt>
argent [flags] -              # prompt read from stdin
argent <command> [args]
```

Flags are registered in `internal/cli/flags_register.go`; the authoritative help
text is `writeUsage` in `internal/cli/flags_usage.go`. `-h/--help` and
`-V/--version` print help and the version. The version constant is
`cli.Version = "0.6.0"`.

### 1.1 Subcommands

All 30 dispatched names from `subcommands` / `subcommandOrder`. `--json` selects
machine-readable output where the handler supports it; `-C <dir>` selects the
workspace root where noted.

| Command | Purpose | Key arguments / flags |
|---|---|---|
| `sessions` | Inspect stored sessions. | `list`, `show <id>`, `rm <id>`, `find <keyword>`, `--grep <text>`, `--limit N`, `tree <id>`, `undo <id> [-C <workspace>]`, `retry <id> [checkpoint-id]`, `name <id> <title>`, `export <id> [-o file.html]` |
| `replay` | Print a stored transcript (most recent if no id). | `[<id>] [--json]` |
| `usage` | Summarise turns, tokens and tool calls per session. | `[--json]` |
| `doctor` | Check config, storage, provider reachability, git and Go. | `[--base-url <url>]` |
| `login` | Store an API key in the config file (never echoed). | `<provider> [--api-key <key>] [--base-url <url>]`; also reads `<PROVIDER>_API_KEY`, `ARGENT_API_KEY` or stdin |
| `logout` | Remove stored credentials. | `[provider]` (no provider removes all) |
| `auth` | Show credential status; secrets are never printed. | `[--json]` |
| `models` | List built-in provider templates. | `[--json]` |
| `mcp` | List MCP servers declared in the merged config; no server is contacted. | `[--json] [-C <dir>]` |
| `skills` | List discovered `SKILL.md` documents. | `[--json] [-C <dir>]` |
| `commands` | List discovered markdown slash commands. | `[--json] [-C <dir>]` |
| `hooks` | List hook documents and SHA-256 trust state. | `[-C <dir>]` |
| `policy` | Print effective sandbox, approval, provider and policy. | `[--json] [-C <dir>]` |
| `audit` | Tail the redacted audit log. | `[--tail <n>] [--json]` |
| `update` | Check GitHub releases; optionally verify and install. | `[--check] [--apply] [--repo <owner/name>] [--current <version>] [--asset <name>] [--sha256 <hex>] [--target <path>]` |
| `completion` | Print a shell completion script. | `bash\|zsh\|powershell` |
| `tui` | Run the full-screen terminal UI. | `[flags]` (same flags as one-shot mode) |
| `bug` | Write a secret-free diagnostics dump. | `[--out <path>]` |
| `eval` | Run scripted JSON eval scenarios. | `<file.json>` |
| `config` | Read or write dotted config keys. | `get <dotted.key>`, `set <dotted.key> <value>`, `path` |
| `workflow` | Inspect and run workflow definitions. | `list [-C dir] [--json]`, `show <name\|path> [-C dir]`, `run <file.json> [--input s]` |
| `telemetry` | Report the personal-build telemetry preference (no-op). | `status\|off` |
| `attach` | Print the tail of a background job log. | `<id> [-n N]` |
| `logs` | List background jobs and their logs. | `[<id>]` (`jobs` is an alias) |
| `jobs` | Alias for `logs`. | — |
| `follow` | Tail a job log until interrupted. | `<id> [-n N]` |
| `stop` | Kill a background job. | `<id>` |
| `board` | Read-only view of jobs and scheduled automations. | — |
| `help` | Print the command/flag help. | — |
| `version` | Print `argentgo <version>`. | — |

There is **no `plugins` / `plugin` subcommand** in this rewrite. Plugins are
enabled per run with the repeatable `--plugins <name>` flag (`all` loads every
one) and contribute skills, commands and MCP servers; discovery itself lives in
`internal/plugins`.

### 1.2 One-shot flags (selected)

| Flag | Meaning |
|---|---|
| `-m`, `--model <id>` | Model id (default `gpt-4o-mini`). |
| `--provider <id>` | Provider template id (see `argent models`). |
| `--profile <name>`, `--list-profiles` | Select / list config profiles. |
| `--base-url <url>`, `--api-key <key>`, `--protocol <name>` | Provider endpoint, key and wire protocol (`anthropic`, `openai`, `responses`). |
| `-C`, `--cwd <dir>` | Workspace root (default current directory). |
| `--sandbox-mode <mode>` | `read-only`, `workspace-write` (default) or `danger-full-access`. |
| `--approval <mode>` | `ask`, `never` or `auto`. |
| `--yolo` | Auto-approve every tool call. |
| `--max-turns`, `--max-parallel` | Turn budget; read-only tool workers per batch. |
| `--max-tokens`, `--max-cost`, `--max-seconds` | Budget stop conditions. |
| `--session <id>`, `--resume`, `--undo`, `--retry` | Session resume and checkpoint operations. |
| `--mode text\|json`, `--json-lines` | Streaming output shape. |
| `-c`, `--config <key=value>` | Config override, dotted keys nest (repeatable). |
| `--serve`, `--rpc`, `--acp` | Start a headless transport (see §2). |
| `--serve-address`, `--serve-port`, `--serve-token` | HTTP bind settings; a serve token is mandatory. |

Aliases share storage with their canonical flag (for example `--token` =
`--serve-token`, `--session-id` = `--session`, `--continue` = `--resume`,
`-V` = `--version`). Flags marked "not migrated" in `flags_register.go` are
parsed but inert.

## 2. Headless transports (`internal/server`)

All three transports drive the same injected interface:

```go
type Runner interface {
    Prompt(ctx context.Context, text string, onText func(string)) (string, error)
    Abort()
}
```

`Config` carries `Addr`, `Token`, `Version`, `Runner` and an optional `Sessions`
store (`List() ([]SessionInfo, error)` and `Load(id) ([]SessionEvent, error)`).

### 2.1 `--serve` — HTTP + SSE

Default address `127.0.0.1:7777` (`server.DefaultAddr`), overridable with
`--serve-address` / `--serve-port`. `Listen` refuses to start without a token
(`--serve-token` or `ARGENT_SERVE_TOKEN`). Every endpoint except health requires
the shared secret in the `Authorization` header (HTTP bearer scheme) or the
`token` query parameter; comparison is constant-time.

| Method & path | Behaviour |
|---|---|
| `GET /v1/health` | `{"status":"ok","version":"..."}`; no auth. |
| `POST /v1/prompt` | Runs one turn, responds `Content-Type: text/event-stream`. |
| `POST /v1/abort` | Aborts the current turn, `{"ok":true}`. |

SSE frames are one JSON object per `data:` line:

```text
data: {"type":"text","text":"..."}     # streamed chunk (0..n)
data: {"type":"error","error":"..."}   # on failure, then the stream ends
data: {"type":"done"}                  # on success
```

Prompt bodies are accepted as raw text, `{"text":"..."}`, `{"prompt":"..."}` or
`{"prompt":[{"type":"text","text":"..."}]}` by `promptText`, which is the single
parser shared by all three transports. Request bodies are capped at 1 MiB.

### 2.2 `--rpc` — newline-delimited JSON-RPC 2.0 (stdio)

`RunRPC` reads one JSON object per line from stdin and writes one per line to
stdout; diagnostics go to stderr. `native/` drives the engine exclusively
through this transport (`native/bridge.go` spawns `argent --rpc`).

| Method | Params | Result |
|---|---|---|
| `prompt` | `{"text":"..."}` (any `promptText` carrier) | `{"text":"<final answer>"}`; streamed chunks arrive first as `prompt/delta` notifications `{"text":"..."}` |
| `abort` | none | `{"ok":true}` |
| `session.list` | none | `{"sessions":[{"id","title","updated"}, ...]}` |
| `session.load` | `{"id":"..."}` or `{"sessionId":"..."}` | `{"id":"...","events":[{"id","parent","kind","ts","data"}, ...]}` |

Responses are `{"jsonrpc":"2.0","id":<id>,"result":...}`; errors are
`{"jsonrpc":"2.0","id":<id>,"error":{"code":...,"message":...}}` with standard
codes (`-32700` parse, `-32600` invalid request, `-32601` method not found,
`-32602` invalid params, `-32603` internal, `-32000` server). Notifications omit
`id`. `session.load` returns records shaped like the JSONL session contract
(`id` / `parent` / `kind` / `ts` / `data`).

### 2.3 `acp` — Agent Client Protocol (stdio)

`RunACP` is JSON-RPC 2.0 over stdio, one message per line. Methods:
`initialize`, `authenticate`, `session/new`, `session/prompt`, `session/cancel`,
`session/set_model`, `session/set_mode`. Streamed assistant text is delivered as
`session/update` notifications; `session/load` is explicitly not supported.

### 2.4 In-process event vocabulary (`internal/agent/events.go`)

The agent loop emits `agent.Event` values with a `Kind` plus the fields that
make sense for that kind. Frontends switch on `Kind`; the transports above
currently forward streamed text through their own simpler frames rather than
serialising this struct.

| Kind | Populated fields |
|---|---|
| `turn_start`, `turn_end` | `Turn`, `PendingMessages` |
| `checkpoint_ready` | `Turn`, `Checkpoint` (emitted before a mutating batch) |
| `text_delta` | `Turn`, `Text` |
| `reasoning_delta` | `Turn`, `Text` (whole reasoning block) |
| `tool_start` | `Turn`, `ToolCallID`, `ToolName`, `Args`, `Intent` |
| `tool_end` | `Turn`, `ToolCallID`, `ToolName`, `Output`, `IsError`, `DurationMS`, `Intent` |
| `usage` | `Turn`, `Usage` (this request), `TotalUsage` (run total) |
| `error` | `Err` |
| `notice` | `Text` |
| `stop` | `Reason`, `PendingMessages` |
| `subagent_start` | `Text`, `ToolName`, `Depth` |
| `subagent_end` | `Text`, `Output`, `IsError`, `Err`, `Depth` |

`Depth` is 0 for the root run and increases for delegated subagents.

## 3. Tool API (`internal/tools`)

A tool implements `Name() string`, `Description() string`, `Schema() string`,
`Mutating() bool` and `Run(ctx, root, args json.RawMessage)`. The `Registry`
owns the tools, enforces the workspace boundary (`resolve` rejects `..` and
absolute escapes), runs the guard before every call, and clips output to 40 000
characters by default. `Specs()` exposes each tool as an `llm.ToolSpec` function
schema. Tool descriptions are clipped to 1024 runes before crossing the wire.

### 3.1 Registered built-ins

Registration order is from `tools_registry.go:NewWithLimit`. **M** = `Mutating()
== true`, so the call goes through the security chain (policy → approval →
audit) and is hidden in `read-only` sandbox mode; tools marked **R** are
read-only (`Mutating() == false`). Calls to M tools without `--yolo` are
rejected by the registry as well. Key arguments are taken from each `Schema()`.

| Tool | M/R | Purpose | Key arguments |
|---|---|---|---|
| `read` | R | Read a UTF-8 text file; paged with `offset`/`limit`. | `path` (required), `offset`, `limit` |
| `write` | **M** | Create or overwrite a file. | `path`, `content` (required) |
| `edit` | **M** | Replace one unique occurrence of old text. | `path`, `old`/`new` (or `old_text`/`new_text`), `repair`, `anchor`, `anchor_hash`, `hashline` |
| `bash` | **M** | Run a shell command in the workspace root. | `command` (required), `timeout_seconds`, `background_after_ms` |
| `ls` | R | List a directory. | `path` |
| `grep` | R | Search file contents with a regular expression. | `pattern` (required), `path` |
| `find` | R | Find files by name glob. | `pattern` (required), `path` |
| `powershell` | **M** | Run a PowerShell script on Windows. | `command` or `script`, `timeout_seconds` |
| `glob` | R | Find files by path glob, e.g. `**/*.go`. | `pattern` (required), `path` |
| `todo` | R | Maintain the session task list. | `op` (`write`/`read`), `todos[]` (`id`, `text`, `status`) |
| `multi_edit` | **M** | Apply several ordered replacements to one file; all must match. | `path`, `edits[]` (`old`/`new`), `repair` |
| `web_fetch` | R | Fetch an http(s) URL as plain text. | `url` (required), `allowLocal` |
| `git_status` | R | `git status --short --branch`. | none |
| `current_time` | R | Current UTC/zone time. | `timezone`, `format` (`rfc3339`/`date`/`time`/`unix`) |
| `web_search` | R | Search via the optional `ARGENT_WEB_SEARCH_URL` endpoint. | `query` (required), `limit` |
| `repo_map` | R | Map workspace directories, files and ranked Go symbols. | `path`, `mention`, `mentions[]`, `symbols` |
| `submit_plan` | R | Store plan steps as `plan.json`; never executes them. | `steps[]`, `status` |
| `lsp_symbols` | R | Document symbols (functions, types, methods, variables). | `path` (required) |
| `lsp_references` | R | References to the symbol at a position. | `path`, `line`, `character`, `includeDeclaration` |
| `lsp_code_actions` | R | Quick fixes / refactorings for a range. | `path`, `startLine`, `startChar`, `endLine`, `endChar` |
| `lsp_rename_preview` | R | **Read-only preview** of rename workspace edits; returns text, never applies it. | `path`, `line`, `character`, `newName` |
| `lsp_format` | R | **Read-only preview** of formatting text edits; never applies them. | `path`, `tabSize`, `insertSpaces` |
| `lsp_workspace_symbols` | R | Workspace-wide symbol search. | `query` (required) |
| `memory_retain` | **M** | Persist a durable fact into the cross-session memory bank (redacted first). | `text` (required), `tags[]`, `source` |
| `memory_recall` | R | Search the memory bank by word overlap and tags. | `query`, `tags[]`, `limit` |
| `memory_forget` | **M** | Remove one memory by id. | `id` (required) |
| `tool_search` | R | Find up to 20 tools by name/description relevance. | `query` (required), `limit` |

Notes:

- Every schema also accepts an `i` string (a concise per-call intent) injected by
  `internal/agent/tool_intent.go`; it is surfaced on tool events.
- `lsp_code_actions`, `lsp_rename_preview` and `lsp_format` are previews: they
  propose edits but never write files. Use `edit`/`write` to apply them. All six
  `lsp_*` tools report "not available" until the app installs an LSP adapter.
- `internal/app` adds one more advertised tool, `use_skill`
  (`internal/skills/tool.go`, argument `name`), which loads a `SKILL.md` body on
  demand. MCP tools are appended as `server__tool` (see §4).

Three further tools exist as opt-in constructors but are **not** in the default
registry and are not advertised by a normal run: `checkpoint_diff`
(`SetCheckpointSource`), `memory_reflect` and `memory_learn`
(`MemoryAdvancedTools`).

## 4. MCP integration (`internal/mcp`)

Servers are declared under the `mcpServers` key of the merged config (§5). The
key accepts either an object keyed by server name or an array of objects with a
`name` field; both camelCase and snake_case spellings are read for permission
fields. Malformed entries are skipped with a warning rather than failing the
whole config (`DecodeConfig`).

| Field | Meaning |
|---|---|
| `name` | Server id; namespaces its tools as `server__tool`. Required for object form. |
| `command`, `args`, `env` | stdio program, arguments and extra `KEY=VALUE` environment entries. |
| `url`, `headers` | HTTP endpoint and extra headers. |
| `transport` | `stdio` or `http` (`sse` accepted); inferred from `command` vs `url` when empty. |
| `timeout` | Per-request seconds (default 30s). |
| `enabled` | Defaults to true; explicit false disables the server. |
| `readOnly` | Blocks calls to tools that look mutating before any request is sent. |
| `allowedTools`, `deniedTools` | Tool allowlist / denylist; deny wins over allow. |

Additional sources merged at run time (`internal/app/mcp.go`): MCP servers from
enabled plugin bundles, and `<workspace>/.argent/mcp.json`, which is read **only**
when `--trust-project` is set. Handshakes are bounded at 5 seconds and failures
are fail-soft: a broken server is reported once and skipped.

`argent mcp [--json] [-C <dir>]` lists the configured servers (name, transport,
target) from the merged config. It does not contact any server.

## 5. Configuration (`internal/config`)

### 5.1 Precedence and locations

Lowest to highest precedence:

1. compiled defaults (`model=gpt-4o-mini`, `protocol=openai-chat-completions`,
   `sandboxMode=workspace-write`, `maxTurns=12`, `locale=en-US`, storage under
   the home directory);
2. `~/.argentgo/config.json` (home resolved from `HOME` or `USERPROFILE`);
3. `<cwd>/.argent/config.json`;
4. `<cwd>/argent.json`;
5. the selected profile;
6. `ARGENT_*` environment variables;
7. explicit flag overrides (`-c` / `--config`).

Objects merge recursively; arrays and scalars replace. Files may use JSONC
(comments and trailing commas) and `{env:VAR}` / `{file:path}` references;
relative `{file:}` paths resolve against the config file's directory.

### 5.2 Profiles and environment

A profile is selected with `ARGENT_PROFILE` or the `profile` / `activeProfile`
key, and defined inline under `profiles` in any file layer; an unknown name is an
error when profiles exist. Recognised environment overrides include
`ARGENT_MODEL`, `ARGENT_BASE_URL`, `ARGENT_API_KEY`, `ARGENT_PROTOCOL`,
`ARGENT_LOCALE`, `ARGENT_SANDBOX_MODE`, `ARGENT_STORAGE_DIR`, `ARGENT_PROVIDER`,
`ARGENT_MAX_TURNS` and `ARGENT_YOLO`. `-c key=value` overrides nest on dotted
keys (for example `-c models.gpt-4o.contextWindow=128000`) and sit above all
file and environment layers.

Storage defaults to `<home>/.argentgo`; sessions live in its `sessions/`
subdirectory. Override it with `ARGENT_STORAGE_DIR` or `ARGENT_HOME`.

### 5.3 Version literals

The version string `0.6.0` appears in three source literals and must be bumped
together:

| File | Symbol / use |
|---|---|
| `internal/cli/cli.go` | `const Version` — `--version`, `/v1/health`, ACP `initialize`; the packaging scripts read this one. |
| `internal/mcp/mcp.go` | `clientVersion` — MCP `clientInfo.version`. |
| `internal/lsp/client.go` | `version` in the LSP `initialize` `clientInfo`. |

`scripts/build-release.ps1` validates `-Version` / `-Tag` only against
`cli.Version`; it does not check the other two. See
[DISTRIBUTION.md](DISTRIBUTION.md#version-locations) for release-gate details.
