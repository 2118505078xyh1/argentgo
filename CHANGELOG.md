# Changelog

## v0.6.0 — 2026-11-20

Sixteen rounds of study-and-harden cycles against charmbracelet/crush,
block/goose, Aider, opencode, Cline, fzf, glamour and bubbletea, plus a full
independent code review. stdlib-only Go; MyGo remains the sole native-shell
dependency. Highlights:

**TUI & interaction**
- Session-panel fuzzy search (Ctrl+L): scored subsequence match, fzf query
  syntax (`^` prefix, `!` inverse, `|` OR, exact/word-boundary), relevance
  sort and character-level match highlighting.
- Markdown tables (two-pass layout), word-level diff highlighting (changed
  tokens only), and a streaming markdown boundary buffer (no mid-`**`/fence
  flicker, no dropped text at turn end).
- Grapheme-aware width (ZWJ families, flags, skin tones), an LRU-style entry
  cache, a binding-list help bar with ellipsis, one-line thinking indicator,
  activity summary, context-usage progress bar with threshold colors.

**Agent loop & correctness**
- Single-flight prompt with race-free Abort ownership; a watched-job marker
  fixes the long-standing exit-code `-1` race (1500 stress runs clean).
- Mistake tracker with injectable recovery guidance; tool-pairing normalization
  across resume; schema projection for gemini/moonshot models.
- Compaction now preserves non-first-line next steps / failures / changed
  files, and mid-turn compaction carries tool-call IDs so no orphan tool
  result survives. A prefix-hash summary cache avoids re-summarizing.

**Security (audit-driven)**
- Fixed workspace escape via symlink/Windows junction across read/write, MCP
  routing and approval display (real-path resolution via GetFinalPathNameByHandle).
- PowerShell `script` alias can no longer bypass `denyCommands` / command rules.
- Credential redaction covers mainstream key formats (`sk-ant-…`, `sk-proj-…`,
  `Authorization: Bearer/Basic …`, URL userinfo) in both LLM errors and audit logs.

**LLM & providers**
- Chat completions now retries transient failures (retry classification and
  hard-quota fast-fail apply), parses multi-line SSE `data:`, avoids generated
  tool-call ID collisions, and validates malformed tool arguments.
- `ActionableError` classification surfaced in CLI with a recovery next-step.

**Engineering**
- CI adds `-race` and `GOOS=linux` cross-compile gates; maintainer + API
  reference docs; benchmark baselines and golden snapshots for eval scenarios.

## v0.5.0 — 2026-11-20

Six rounds of study-and-harden cycles against opencode, DeepSeek Harness,
pi (rpi) and oh-my-pi (omp). All changes are stdlib-only Go; the desktop shell
remains MyGo. Highlights:

**Agent loop & correctness**
- Output-limit continuation: a length-truncated text reply now injects a
  "break the work into smaller pieces" message and stops after two consecutive
  truncations (was silently reported complete).
- Typed retry classification: provider fallback now uses `llm.ClassifyRetryError`
  and `IsRetryableStatus` instead of fragile string matching.
- Cancellation now records a terminal "Operation aborted" assistant message.
- The before-request/before-tool durability barrier is wired into the loop and
  registered to `Session.Sync()`.
- Tool-call intent (`i`) is extracted before execution and surfaced on events.
- Non-draining pending-message count exposed on turn-boundary events.

**Session durability**
- `Session.Sync()` flushes before model requests and tool dispatch.
- Torn-tail recovery: only a syntax error on the LAST JSONL line is healed
  (atomic prefix publish); schema errors or mid-file corruption fail closed.
- Tool-pairing-safe compaction cut: a call is never separated from its result.
- Byte-bounded transcript pagination (`ReadPage`) never truncates a record.
- Nested `AGENTS.md` / `CLAUDE.md` discovered for active subdirectories.

**Security & tools**
- Approval prompts are now context-cancellable and render rune-safe multiline args.
- Host-private environment variables are stripped from spawned children.
- ANSI/CR shell-output sanitizer; incremental UTF-8 decoding across read chunks.
- PowerShell tool registered for the Windows host.
- Central tool-result normalizer for malformed/oversized outputs.

**Prompt cache**
- Conversation-tail cache breakpoint with validated TTL tiers.

**Concurrency & CI**
- Fixed a cancelled-approval reader leak that could swallow the next prompt.
- Defensive event-slice snapshot before compaction-evidence collection.
- CI race gate moved to ubuntu-latest (reliable gcc); removed the stale web-UI job.

## v0.4.0 — 2026-11-19

- **Browser frontend removed; native MyGo is now the only desktop frontend.**
  Deleted `ui/` (SolidJS + Vite), `internal/gui/` (HTTP+SSE bridge),
  `internal/launcher/` (Edge/Chrome `--app`), `scripts/build-ui.ps1` and the web
  runner/approval adapters. `argent gui` now always launches `argent-native.exe`.
- Simplified `cmd/argent/gui.go` to engine resolution + native frontend launch;
  removed `--web`. Release zip and build script no longer build/embed a web UI.
- Docs (README, FRONTEND, ARCHITECTURE, AGENTS, MAINTAINERS, PARITY) updated to
  the native-only architecture. Core packages remain stdlib-only; MyGo is the
  sole third-party dependency, confined to `native/`.

## v0.3.1 — 2026-11-19

Correctness fixes derived from studying the rpi coding-agent runtime:

- Refuse a tool batch from a length-truncated reply: incomplete arguments are
  never executed; every call gets an ordered error result instead. All three
  wire clients now attach the provider's finish reason to `llm.Message`.
- Reject batches with duplicate or empty tool-call IDs wholesale — no call is
  executed when results could not be correlated.
- Suppress late streaming tool progress after a tool has returned, so a stray
  goroutine can no longer append stale rows to the transcript.
- Contain panics in hook callbacks: a panicking callback becomes a structured
  error carrying its call ID, and the run continues.

## v0.3.0 — 2026-11-19

- **Frontend fully moved to a MyGo native client.** `argent gui` now launches
  `argent-native.exe` by default: a pure-Go native UI (single process, ~76 MB
  working set, 0% idle CPU) with no WebView2. It drives the engine over the
  existing stdio `--rpc` transport, so core packages stay stdlib-only.
- The native client renders sidebar, message list, tool cards, streaming
  Markdown replies, approvals, keycaps and dark/light themes with MyGo's `ui`.
- Wails and the experimental Win32 syscall window were removed; the only
  third-party Go dependency is MyGo, confined to the `native/` desktop shell.
- Release zip now ships both `argent.exe` (engine/CLI) and `argent-native.exe`.
  `argent gui --web` keeps the browser UI as a fallback.

## v0.2.1 — 2026-11-19

- Desktop shell switched from the experimental self-painted Win32 window to
  **Wails v3 + WebView2** (`argent gui` default). It loads the existing local
  HTTP+SSE bridge, so the frontend and all IPC stay unchanged; WebView2 provides
  mature text selection, Markdown rendering and input handling.
- `--native` keeps the old syscall window for reference; `--web` keeps the
  Edge/Chrome `--app` path. The Wails entry is Windows-only with a stub elsewhere.
- Removed the intermittent startup hang and off-screen/minimized window issues:
  WebView2 window placement is handled by Wails and the exe stays a GUI-subsystem
  binary, so no black console appears.
- Core packages remain stdlib-only; the desktop shell intentionally adds Wails
  as the single third-party dependency (`go.mod` documents the exception).

## v0.2.0 — 2026-11-19

- **Native GUI** replaces the browser shell on Windows: pure Win32 syscall
  GDI painting, no WebView, no local HTTP server, no token. `argent gui --web`
  keeps the optional SolidJS interface.
- Ported portable OpenCode patterns: sandbox/deny-filtered tool catalog, a
  tool-free final answer at the turn limit, typed secret redaction, multi-env
  credential chains, retry classification, config diagnostics / `{env:}` /
  `{file:}` substitution / JSONC, permission wildcard rules, and a bounded
  context/session/TUI memory footprint.
- GUI visual polish: semantic palette, centered 720px reading column, tool-row
  states, blinking streaming caret, approval keycaps, double-buffered painting.
- Fixed a paint-loop hang from the 80ms animation timer and removed the black
  console window: release binaries are built as GUI-subsystem executables while
  CLI output from an existing terminal still works.
- Undo/retry wired into both frontends via session checkpoints (`Ctrl+Z` /
  `Ctrl+Y`).

## v0.1.2 — 2026-11-19

- Project renamed from `argent-go` to `argentgo`; Go module path and all
  documentation, scripts and wire identifiers follow the new name.
- Licensed under Apache-2.0 (see `LICENSE`).
- Checkpoint/undo/retry: file manifests with SHA-256, conflict-safe restore and
  fork-on-retry, wired through the agent loop and CLI (`--undo`, `--retry`,
  `--title`).
- Prompt caching for Anthropic / OpenAI chat / Responses with cached-token
  accounting and `--no-prompt-cache`.
- Failed, cancelled and stream-error attempts are recorded in JSONL without
  entering model history; session titles are derived locally with redaction.
- One-click Windows release (`scripts/publish-win.ps1`) publishes a real
  GitHub Release with the Windows zip and checksums, not just a tag.
- TUI/GUI: undo/retry actions, attempt badges, cached-token counts, command
  templates, SSE reconnect and accessibility polish.

## v0.1.1 — 2026-11-19

- Fix the GUI shell: the page now sets a GET-only asset cookie after the initial
  token URL, so embedded JS/CSS load instead of being rejected with 401.
- Frontend performance: coalesced stream deltas flush on hidden/pagehide, chat
  step counts are O(n), and tool updates preserve unrelated row references.
- Frontend polish: tighter dark/light theme, clearer focus states, lighter
  shadows, and reduced-motion support. No new frontend dependencies.

## v0.1.0 — 2026-11-19

First Go build: a stdlib-only, cgo-free rewrite of the frozen Rust `argent`
0.4.0; see `docs/PARITY.md` for the component-by-component status.

### Runtime

- Agent loop with streaming, tool batches (sequential by default, parallel for
  read-only calls), approval gate, repeat guard, context trimming, token/cost
  budget, steering and nested subagents.
- Providers: OpenAI chat completions, OpenAI Responses, Anthropic Messages,
  each with SSE streaming, tool calls, thinking deltas and usage accounting.
  Shared retry with `Retry-After` support.
- Provider templates for anthropic, openai, deepseek, moonshot, zhipu, qwen,
  ollama, lmstudio and openrouter.

### Tools

- `read`, `write`, `edit`, `multi_edit`, `bash`, `ls`, `glob`, `grep`, `find`,
  `todo`, `web_fetch`, `git_status`; registry-level guard hook and per-run
  output caps.

### Security

- Path/command/tool policy, interactive and `never` approvers, per-tool session
  caching, sandbox modes (`read-only`, `workspace-write`, `danger-full-access`)
  and a redacted daily JSONL audit log.

### Sessions and config

- JSONL session tree with resume, fork and compaction; layered configuration
  (home → project → `argent.json` → env → flags) with profiles.

### Surfaces

- CLI with prompt mode, `sessions`, `doctor`, `login`, `eval`, `config`.
- Headless transports: `--serve` (HTTP + SSE), `--rpc` (JSON-RPC over stdio),
  `acp`.
- Full-screen TUI: streaming transcript, markdown-lite rendering, input editor,
  approval modal.
- Desktop shell: the SolidJS UI served by the Go binary, opened in a WebView2
  app window (no Tauri, no Rust).

### Integrations

- MCP client over stdio and HTTP/SSE; hook documents with SHA-256 trust;
  plugin manifests (`.zcode-plugin`, `.claude-plugin`, `.codex-plugin`);
  LSP client; skills, slash commands and project memory injection.
- Automation scheduler (cron/interval, daemon, run logs) and scripted
  workflows.
- OAuth PKCE / device-code login with a local token store; GitHub self-update
  with checksum verification and atomic replace.
- Eval harness with the ported `evals/core.json` scenarios.

### Wave 6 additions

- `/review` slash command for read-only, structured code review.
- `sessions` full-text grep, branch tree and JSON/JSONL export.
- Background shell jobs with promotion to foreground and a read-only `jobs` board.
- Skills can be authored by the agent and discovered on subsequent turns.
- GUI shows a single approval per mutating tool call instead of duplicates.

### Not ported

- Native C-ABI plugin loader (Go uses process/MCP plugins).
- tree-sitter AST and `repo_map` (LSP symbols instead).
- Rhai script plugins.
- Desktop-control tool, telemetry, marketplace.
