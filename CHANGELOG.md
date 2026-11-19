# Changelog

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
