# 100-item TODO — post v0.1.1

Rules: stdlib-only Go, no cgo, no new third-party Go module, no hosted service.
Already shipped and explicit skips are closed; see `docs/NEXT_STEPS.md`.
Each agent owns only its listed paths and must not edit `internal/app` or
`docs/PARITY.md` unless its prompt explicitly says so. Integration in
`internal/app` is done after all agents report.

## A. Release, scripts, docs and QA — owner: release agent

- [x] A01 Fix ubuntu-latest release workflow build step that failed on v0.1.1.
- [x] A02 Ensure `gh release create` publishes a real Release with Windows zip and checksums.
- [x] A03 Keep `scripts/publish-win.ps1` failing hard when no Release appears.
- [x] A04 Keep `scripts/build-release.ps1` Windows/amd64-only with exactly one zip.
- [x] A05 Fix CI gofmt failure caused by line endings.
- [x] A06 Diagnose and fix CI ubuntu test failure in owned workflow if script-related.
- [x] A07 Publish script must validate clean `main` before any build.
- [x] A08 Publish script must validate tag against `internal/cli/cli.go` Version.
- [x] A09 Publish script must require a matching CHANGELOG section.
- [x] A10 Add local artifact cleanup after successful publish.
- [x] A11 Generate release notes from the CHANGELOG section.
- [x] A12 Verify checksums inside the release workflow before upload.
- [x] A13 Add `scripts/install-win.ps1` for local installation.
- [x] A14 Add `scripts/uninstall-win.ps1` for local removal.
- [x] A15 Add a release dry-run mode that validates but does not push.
- [x] A16 Update `docs/ARCHITECTURE.md` with the final release flow.
- [x] A17 Update `docs/FRONTEND.md` with desktop install and icon notes.
- [x] A18 Add a privacy scan script under `scripts/`.
- [x] A19 Add a script to check Version/tag/CHANGELOG consistency.
- [x] A20 Document the one-command Windows publish flow in `README.md`.

## B. Session, CLI and slash commands — owner: session agent

- [x] B01 Add a session checkpoint manifest type with relative paths and SHA-256.
- [x] B02 Record pre-turn bytes for files changed in a turn.
- [x] B03 Implement `/undo` restoring only manifest entries.
- [x] B04 Detect conflicting manual edits and refuse to overwrite.
- [x] B05 Implement `/retry` as a fork from the checkpoint parent.
- [x] B06 Add an append-only `attempt` JSONL record type.
- [x] B07 Record failed, retried, cancelled and stream-error attempts.
- [x] B08 Derive model history from committed records only.
- [x] B09 Preserve attempt records through compaction.
- [x] B10 Generate deterministic session titles from the first prompt.
- [x] B11 Redact secrets before storing generated titles.
- [x] B12 Add `/name` override for session titles.
- [x] B13 Make `sessions --grep` show redacted attempt snippets.
- [x] B14 Make `sessions tree` show fork/retry links.
- [x] B15 Include attempt records in offline HTML export.
- [x] B16 Expand `$ARGUMENTS` in markdown commands.
- [x] B17 Expand `$1..$N` with quote grouping in markdown commands.
- [x] B18 Parse `model` and `agent` frontmatter in slash commands.
- [x] B19 Add CLI flags for undo, retry and title override.
- [x] B20 Add boundary tests: path escapes, UTF-8 names, aborted turns, old JSONL compatibility.

## C. LLM, providers and eval — owner: provider agent

- [x] C01 Define a prompt-cache capability on the `Chatter` boundary.
- [x] C02 Emit Anthropic `cache_control` markers on the stable prefix.
- [x] C03 Emit OpenAI-compatible cached-prefix markers where supported.
- [x] C04 Emit Responses API cached-prefix markers where supported.
- [x] C05 Add `--no-prompt-cache` option plumbing in `internal/llm` options.
- [x] C06 Count cached tokens separately from fresh tokens.
- [x] C07 Count cached tokens once in budget and cost.
- [x] C08 Add table-driven serialization tests per protocol.
- [x] C09 Add mocked SSE usage tests containing cached tokens.
- [x] C10 Add zero-cache response tests.
- [x] C11 Add cancellation-mid-stream accounting tests.
- [x] C12 Add provider template capability field for cache support.
- [x] C13 Keep local/open-compatible endpoints cache-free by default.
- [x] C14 Add an eval scenario for cached usage reporting.
- [x] C15 Add an eval scenario for prompt-cache opt-out.
- [x] C16 Ensure offline scripted-provider eval stays unchanged.
- [x] C17 Add stream-error attempt hooks for the session layer.
- [x] C18 Redact provider error bodies before logging.
- [x] C19 Normalize model IDs across provider templates.
- [x] C20 Add provider header tests for auth and cache markers.

## D. Agent loop, tools and security — owner: runtime agent

- [x] D01 Emit a checkpoint-ready event before mutating tool batches.
- [x] D02 Capture tool mutation paths for checkpoint manifests.
- [x] D03 Route undo/restore through policy → approval → audit.
- [x] D04 Redact manifest paths in audit records.
- [x] D05 Strengthen repeated-tool-call dedupe tests.
- [x] D06 Test parallel read-only tool batches.
- [x] D07 Accept `old_text`/`new_text` aliases in `multi_edit`.
- [x] D08 Add a report hook for `background_after_ms` wiring in app.
- [x] D09 Improve `tool_search` ranking and description matching.
- [x] D10 Validate `submit_plan` steps before writing plan.json.
- [x] D11 Add `current_time` IANA timezone boundary tests.
- [x] D12 Add `web_search` unconfigured-error tests.
- [x] D13 Add `repo_map` large-repo truncation tests.
- [x] D14 Extend policy config tests for camelCase and snake_case keys.
- [x] D15 Extend hook trust-store tests for tampered files.
- [x] D16 Test PreToolUse exit-2 blocking before tool execution.
- [x] D17 Test PostToolUseFailure error visibility.
- [x] D18 Test compaction preserving failures and next steps.
- [x] D19 Test cancellation within one turn.
- [x] D20 Test steering injection at tool boundaries.

## E. TUI, GUI and frontend — owner: UI agent

- [x] E01 Add `/undo` and `/retry` affordances to TUI.
- [x] E02 Show attempt/failure badges in TUI transcript.
- [x] E03 Show generated session titles in TUI sidebar.
- [x] E04 Show markdown command argument hints in TUI help.
- [x] E05 Show cached-token counts in TUI status line.
- [x] E06 Add undo/retry buttons to GUI transcript actions.
- [x] E07 Show attempt badges in GUI tool cards.
- [x] E08 Add rename/title edit in GUI sidebar.
- [x] E09 Show cached-token usage in GUI status bar.
- [x] E10 Add command-template suggestions to GUI palette.
- [x] E11 Add keyboard shortcut help overlay polish.
- [x] E12 Add reduced-motion verification to UI tests.
- [x] E13 Polish light-theme contrast for bubbles and tool cards.
- [x] E14 Improve empty state with quick actions.
- [x] E15 Persist tool-card expanded/collapsed state per tab.
- [x] E16 Add SSE reconnect with backoff in GUI frontend.
- [x] E17 Improve approval queue UX with pending count and focus.
- [x] E18 Add asset-cookie regression test in GUI backend.
- [x] E19 Wire remaining GUI injected interfaces where stdlib defaults exist.
- [x] E20 Add a GUI screenshot/E2E script under a temp-safe location.
