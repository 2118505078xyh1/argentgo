# Next steps — candidate slices after Wave 6

Selection rule: only behaviour that is local, offline-first, stdlib-only, cgo-free and
shippable in one binary. Star deltas from the earlier snapshot are unverified and are
**not** used to rank anything below. Ideas already shipped in Wave 5/6 and the
explicit skip list are treated as closed; the comparison is about what remains.

Sources used most are listed at the end.

## Five-project comparison

| Project | Distinctive ideas | Portable to argentgo? | Why / why not |
|---|---|---|---|
| **dsh (DeepSeek Harness)** | "Model-visible means logged": every request reconstructable from an append-only event log; failed/retried/cancelled attempts kept as non-history records; versioned JSONL generations. A single session-title provider seam. | **Yes for the ideas, no for the mechanism.** | More JSONL record types and a local title heuristic fit stdlib. Its Cordis profiles/bundles/plugin tree is the already-rejected plugin-graph direction. |
| **Pi** | Extended prompt-cache retention (`PI_CACHE_RETENTION`: 1h Anthropic / 24h OpenAI hints); deliberately minimal core with prompt templates, `/name`, `/tree`, `/clone` and steering-queue recovery on abort. | **Yes for cache hints and local UX.** | Provider cache markers are just HTTP/JSON fields in `internal/llm`; npm packages, hosted session sharing and the bundled Node runtime are not portable. |
| **Codex CLI** | Named profiles that group approval policy + sandbox mode + provider/model; ZDR `disable_response_storage`; sandbox recommendation derived from repo state. | **Mostly already present.** | argentgo has profiles, three sandbox modes, `trust_project` and all three wire protocols. Platform Seatbelt/Landlock sandboxes stay skipped (no OS primitives outside Go's stdlib). |
| **OpenCode** | Markdown commands with `$ARGUMENTS` / `$1..$N` expansion, per-command `model`/`agent` frontmatter and hot reload; short-lived one-time `pair` links for its local web UI. | **Yes for command templates.** | Commands are markdown files plus stdlib text parsing. Pairing is optional: the GUI token + asset-cookie fix already shipped, so it is not a priority. SQLite/LSP-via-server/plugins are skipped or already present. |
| **Hermes Agent** | `/retry` and `/undo` for the last turn, interrupt-and-redirect, `/insights`; local `import-agent` with `--dry-run` preview before writing. | **Yes for undo/retry and dry-run import.** | Both are local file/session operations. Messaging gateways, FTS5, Honcho user modelling and VPS/serverless backends are explicitly out. The self-improving loop (skills, memory nudges, past-session search) is already shipped or skipped. |

## Ranked slices

### 1. Turn checkpoint — `/undo` and `/retry`

- **Outcome:** after a turn that wrote files, `/undo` restores only the files that turn changed; `/retry` forks at that turn's parent and re-runs the same prompt and settings without destroying the original branch. Both are offered in CLI, TUI and GUI.
- **Acceptance:** a checkpoint manifest in the session directory records relative paths plus SHA-256 of the pre-turn bytes; restore touches only manifest entries; restore is a mutating action that goes through policy → approval → audit; a conflicting manual edit is reported instead of silently overwritten; `read-only` sandbox makes undo a no-op with a clear message; retry creates a child with a `parent` link and leaves the source session byte-identical.
- **Boundary tests:** overlapping edit after checkpoint (conflict path); symlink/absolute/`..` escape outside the workspace; UTF-8 filenames; aborted turn still records a partial manifest; compaction preserves checkpoint references; audit redaction applies to manifest paths.
- **Effort:** **L**.
- **Motivation:** Hermes' `/retry` + `/undo` and interrupt-and-redirect, combined with dsh's fork-at-a-turn-boundary.

### 2. Prompt-cache-aware request assembly and cache usage

- **Outcome:** the stable system prompt and tool-schema prefix are marked cacheable on providers that support it; later turns reuse the cached prefix; `usage` reports cached vs fresh tokens; `--no-prompt-cache` opts out.
- **Acceptance:** cache markers emitted only on routes that advertise support, with correct wire shape for Anthropic Messages, OpenAI chat completions and Responses; no marker on local/open-compatible endpoints unless configured; cached tokens are counted once in budget and cost; eval scenarios and offline provider behaviour are unchanged.
- **Boundary tests:** table-driven serialization per protocol; mocked SSE usage containing cached tokens; opt-out; zero-cache response; cancellation mid-stream leaves no partial accounting.
- **Effort:** **M**.
- **Motivation:** Pi's extended prompt-cache retention.

### 3. Durable attempt records for failed, retried and cancelled steps

- **Outcome:** `sessions --grep` and `replay` can show failed, retried, cancelled and stream-error attempts, while the model sees only committed messages.
- **Acceptance:** new append-only `attempt` record with reason and redacted error class; derived model history excludes attempts; compaction retains them; older JSONL sessions load unchanged; privacy/redaction rules run before the record is written.
- **Boundary tests:** append-only and forward/backward compatibility; retry-then-success ordering; redaction of error text; UTF-8-safe truncation; grep only ever returns redacted snippets.
- **Effort:** **M**.
- **Motivation:** dsh's "model-visible means logged" rule and its split between committed messages and retained failed attempts.

### 4. Markdown command templates with positional expansion

- **Outcome:** user/project markdown commands support `$ARGUMENTS` and `$1..$N` with quote grouping, optional `model`/`agent` frontmatter, project-over-global precedence, and reload on save. This extends the existing slash-command system; it does not re-add `/review`.
- **Acceptance:** expansion is table-driven; missing positions become empty strings; the highest-numbered position consumes the remaining arguments; invalid frontmatter fails with file and line; built-in commands are unchanged; no shell-block execution is added in this slice.
- **Boundary tests:** global vs project precedence, quoted and unicode arguments, no shell side effects, path escape in command discovery, no network calls.
- **Effort:** **S**.
- **Motivation:** OpenCode's markdown command format.

### 5. Deterministic session titles

- **Outcome:** `sessions`, TUI and GUI show a short readable title derived from the first user prompt; an explicit `/name`-style override wins.
- **Acceptance:** generated locally with no model call and no network; redaction applied before storage; truncation at a rune boundary; forks inherit or regenerate deliberately; stored as session meta; untitled sessions gain a title lazily on next read.
- **Boundary tests:** long/unicode/secret-like prompts, empty first prompt, compactor preservation, fork behaviour, no network access.
- **Effort:** **S**.
- **Motivation:** dsh's session-title provider seam and Pi's `/name`.

## Do not do

- **Explicit skips (closed, do not re-propose):** C-ABI plugin loader, tree-sitter AST, Rhai scripts, device control, telemetry, plugin marketplace, Node/Cordis plugin graphs, SQLite/FTS5, remote sandboxes, JS code-mode VMs, hosted catalogs. Also keep the earlier rejections: OS sandbox primitives outside Go's stdlib, Honcho-style user modelling and chat-platform bots.
- **Already shipped (do not re-propose):** `/review`; `sessions --grep`; `sessions tree/export`; shell timeout → background jobs; self-authored `SKILL.md`; TUI tasks/workspace/plan panels; GUI memory/usage commands; MCP per-server permissions; policy/hooks/compaction wiring; GUI asset-auth fix; frontend stream batching and visual redesign; Windows-only release workflow, one-click publisher with dry-run/privacy/consistency gates, local install/uninstall scripts and CHANGELOG-derived release notes.
- Do not add a third-party Go module, cgo, a second binary or a hosted service to make any slice cheaper. Do not edit `docs/PARITY.md` statuses without running the corresponding test.

## Release gate

1. `go vet ./...`, `go test ./...`, `gofmt -l .` clean; `GOOS=linux go build ./...` succeeds.
2. `argent eval evals/core.json` passes through the scripted provider and the real agent loop, no network.
3. `argent gui` end to end: stream → approval → tool → `turn_done`, including the undo/retry and title affordances.
4. Each slice lands with its boundary tests; protocol changes include per-protocol wire tests.
5. Privacy scan: no keys, tokens, real emails or personal paths in tracked files or history; local paths rendered as `<local-checkout>` before push.
6. Push with the pen name recorded in the writer workspace as commit author; no other personal information.

## Sources

- DeepSeek Harness README and architecture: https://github.com/deepseek-ai/deepseek-harness/blob/master/README.md and https://github.com/DeepSeek-ai/deepseek-harness/blob/master/docs/architecture.md
- Pi coding agent README and terminal usage: https://github.com/earendil-works/pi/blob/main/packages/coding-agent/README.md and https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/usage.md
- Codex CLI README and configuration docs: https://github.com/openai/codex/blob/main/README.md and https://github.com/openai/codex/blob/main/docs/config.md
- OpenCode intro and commands docs: https://opencode.ai/docs/ and https://opencode.ai/docs/commands/
- Hermes Agent README: https://github.com/NousResearch/hermes-agent/blob/main/README.md
