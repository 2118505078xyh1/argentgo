# Roadmap — after the Rust checkout was retired

The Rust reference (`argent` v0.4.0) is archived upstream and its local checkout
has been deleted. `argentgo` is the only local source of truth. This roadmap
records what comes next and why; `docs/PARITY.md` remains the behaviour checklist.

## Source of the next-step list

Weekly star deltas for 2026-11-04 → 11-11 were supplied by the user as a snapshot:
DeepSeek Harness (dsh), Hermes Agent, Codex CLI, OpenCode, Pi. GitHub does not
expose weekly deltas, so the ordering is the snapshot's claim, not an
independently verified statistic. The features below were selected by reading
each project's primary docs, not by copying their implementation. Nothing here
adds a Go dependency, cgo, or a hosted service.

## Wave 5 — close the production-wiring gap (done)

All six items below are complete; see `docs/PARITY.md` for their done status.

- Configured security rules reach `security.Policy` instead of an empty policy.
- Hook documents dispatch PreToolUse/PostToolUse/SessionStart/SessionEnd.
- Auto-compaction is verified to be active by default.
- TUI gains tasks / workspace / plan panels.
- MCP honours per-server `enabled`, `readOnly`, `allowedTools`, `deniedTools`.
- `current_time`, `web_search`, `repo_map`, `submit_plan`, `tool_search` exist.

## Wave 6 — portable ideas from the five projects (in progress)

This wave is owned by another worker with in-progress changes in the working
tree; this round does not touch any of its code.

| # | Feature | Source idea | Acceptance |
|---|---|---|---|
| 1 | `/review` read-only diff review | Codex CLI | dirty tree → findings; tree byte-identical afterwards |
| 2 | `sessions --grep` full-text search | Hermes | matches in tool output found; snippets redacted |
| 3 | Shell timeout → background job | OpenCode | long command survives tool timeout; job id returned |
| 4 | `/tree` branch nav + offline HTML export | Pi | forked session renders; output has no network refs |
| 5 | Self-authored `SKILL.md` | Hermes + dsh | `save_skill` writes a discoverable skill; opt-in |

Explicitly rejected from those projects: Node/Cordis plugin graphs, SQLite/FTS5,
Honcho user modelling, chat-platform bots, OS sandbox primitives outside Go's
stdlib, remote sandboxes, JS code-mode VMs and hosted catalogs. They are either
heavy dependencies, services, or positioning rather than portable behaviour.

## Release gate

1. `go vet ./...`, `go test ./...`, `gofmt -l .` clean on Windows and Linux cross-build.
2. `argent eval evals/core.json` passes through the scripted provider and the
   real agent loop (no network).
3. `argent gui` end-to-end: stream → approval → tool → `turn_done`.
4. Privacy scan: no keys, tokens, real emails or personal paths in tracked files
   or git history. Local paths are replaced with `<local-checkout>` before push.
5. Push with the pen name from the writer workspace as the commit author; no
   other personal information is included.

## 发行范围

发行脚本 `scripts/build-release.ps1` 已收口为 Windows/amd64 单平台（本机实测
`GOOS=windows GOARCH=amd64`），只打包当前电脑能用的版本，其他平台不考虑。

自更新的 GitHub Release 路线不启用：本地自用，无需跨平台上架。
