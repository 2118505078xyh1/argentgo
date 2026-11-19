# Changelog

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
