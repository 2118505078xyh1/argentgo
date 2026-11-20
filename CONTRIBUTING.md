# Contributing to argentgo

Thanks for your interest in improving argentgo. This project is a personal,
stdlib-only Go coding-agent harness with a native MyGo desktop client. We
welcome bug reports, feature proposals, documentation fixes and code PRs.

This single document also carries the community Code of Conduct, the security
model, the CI contract, the architecture dependency guard, the maintainer
release checklist and the local release-validation harness — all formerly
standalone files, kept here as titled sections below.

## Ground rules

- **Core stays stdlib-only.** `internal/{agent,session,llm,tools,security,...}`
  must not gain third-party dependencies. The only exception is `native/`,
  which uses `github.com/egoist/mygo` for the desktop UI.
- **No cgo.** Everything must cross-compile with `GOOS=linux go build ./...`.
- **Windows host** is the release target; Linux cross-build must stay green.
- **No secrets, tokens, personal emails or local paths** in commits, issues,
  PRs or release notes.
- Be respectful. See [Code of Conduct](#code-of-conduct).

## Reporting a bug

Open an issue using the **Bug report** template. Include:

- argentgo version (`argent --version`) and OS/Go versions.
- Minimal reproduction (CLI command, or steps in the native window).
- Expected vs actual behavior, and the relevant log lines (redact secrets).
- For crashes: what you were doing and whether a session file was left open.

## Proposing a feature

Open an issue using the **Feature request** template. Explain the problem, the
proposed behavior, and why it fits a local, offline-first, single-binary tool.
Large ideas are welcome but may be split into smaller PRs.

## Development setup

```powershell
$env:Path = "$env:USERPROFILE\.tools\go\go\bin;$env:Path"   # if Go not on PATH
go build ./...                        # host
$env:GOOS='linux'; go build ./...; Remove-Item Env:GOOS     # cross-build
go vet ./...
gofmt -l .                            # must print nothing
go test ./... -count=1
go build -o argent-native.exe ./native
```

Run the CLI with `argent --help`. The native client is built from `native/`
and speaks to the engine over the existing stdio `--rpc` transport.

## Pull requests

1. Fork the repo and create a topic branch from `main`.
2. Make a focused change with tests at the boundary you touch. Name tests after
   behavior (`TestUndoRefusesConflictingManualEdits`), table-driven where useful.
3. Run the full verification above; CI runs the same on Linux and Windows.
4. Open a PR using the template. Link the issue it fixes (`Fixes #123`).
5. Keep commits focused; use `<area>: <imperative summary>` (e.g.
   `session: reject torn JSONL tail`).

A maintainer will review. We may ask for changes, split the PR, or decline it
if it conflicts with the stdlib-only/no-cgo/single-binary constraints. Once
merged, your contribution appears in the next release notes and your commit
is attributed to you.

## What we are unlikely to accept

Items deliberately out of scope for this personal harness: C-ABI plugin
loaders, tree-sitter AST, Rhai scripting, device control, telemetry, plugin
marketplaces, plugin graphs, SQLite/FTS5, remote sandboxes and hosted
catalogs. Check with an issue before investing in these.

## Release process

Maintainers tag `vX.Y.Z` and publish a Windows zip + `checksums.txt` through
`.github/workflows/release.yml`. Contributors do not need to touch release
scripts.

## Code of Conduct

### Our pledge

We pledge to make participation in the argentgo community a harassment-free
experience for everyone, regardless of age, body size, disability, ethnicity,
sex characteristics, gender identity and expression, level of experience,
education, socio-economic status, nationality, personal appearance, race,
religion, or sexual identity and orientation.

### Our standards

Examples of behavior that contributes to a positive environment:

- Using welcoming and inclusive language.
- Being respectful of differing viewpoints and experiences.
- Gracefully accepting constructive criticism.
- Focusing on what is best for the community.
- Showing empathy towards other community members.

Examples of unacceptable behavior:

- The use of sexualized language or imagery and unwelcome sexual attention.
- Trolling, insulting or derogatory comments, and personal or political attacks.
- Public or private harassment.
- Publishing others' private information, such as a physical or electronic
  address, without explicit permission.
- Other conduct which could reasonably be considered inappropriate in a
  professional setting.

### Enforcement responsibilities

Project maintainers are responsible for clarifying and enforcing these
standards and will take appropriate and fair corrective action in response to
any behavior that they deem inappropriate, threatening, offensive or harmful.

### Scope

This Code of Conduct applies within all community spaces, and also applies
when an individual is officially representing the project in public spaces.

### Enforcement

Instances of abusive, harassing or otherwise unacceptable behavior may be
reported to the project maintainers by opening a confidential issue or
contacting them through GitHub. All complaints will be reviewed and
investigated promptly and fairly. Maintainers are obligated to respect the
privacy and security of the reporter of any incident.

### Attribution

This Code of Conduct is adapted from the [Contributor Covenant][homepage],
version 2.1, available at
https://www.contributor-covenant.org/version/2/1/code_of_conduct.html.

[homepage]: https://www.contributor-covenant.org

## Security

`argentgo` treats every tool call as untrusted until it has passed a short,
deterministic chain in `internal/security`. The chain is deliberately small and
stdlib-only so its behavior can be reviewed end to end. It is a set of
guardrails for an operator running an agent on their own machine, not a sandbox
against a determined attacker.

### The chain: policy → approval → audit

`security.Chain.Check` evaluates one call in a fixed order:

1. **Optional plan-mode command guard.** When `PlanCommandGuard` is enabled, a
   mutating shell command is stopped *before* anything else with a recoverable
   error the model can read and retry. No approval prompt, no policy deny, no
   audit entry — the call never reaches the chain.
2. **Policy decides.** `Policy.Evaluate` applies deny/allow tool lists, path
   rules, command rules and an ordered rules table. A deny ends the run with a
   `StopError` that wraps `ErrDenied`, plus a finality sentence telling the
   model not to retry or route around the block.
3. **Approval asks.** If the policy says `Ask`, the `Approver` asks the operator.
   Only `y`/`yes` approves; EOF and empty answers deny. A refusal ends the run
   with `ErrRefused`, which is also a `StopError`. A nil approver denies.
4. **Audit records.** Every reached decision is appended to a JSONL audit file
   under a private directory. Audit is evidence, not a gate: a write failure is
   reported but never blocks the run.

Both a policy deny and an operator refusal are terminal; an approval denial is
turned into a tool error the model can read. Checkpoint restores go through the
same chain via `CheckRestore`, so an undo cannot bypass policy or approval.

**Redaction before evidence.** `Chain.record` calls `RedactJSON` on tool
arguments and `Redact` on reasons; `AuditLog.Record` redacts again before
writing. There is no path that writes unredacted arguments to the audit log.
`Redact` recognizes provider key prefixes, auth headers, key/value credentials,
URL userinfo and common token formats; `RedactPath` collapses the home directory
and credential-shaped path segments; `RedactJSON` scrubs values under
credential-named keys without flattening the document.

### Sandbox modes

`--sandbox-mode` selects the file-tool policy via `security.FromString`:

| Mode | Behavior |
| --- | --- |
| `read-only` | Drops every mutating tool; only the known safe tools remain. |
| `workspace-write` (default) | Writes are allowed but bounded to the workspace and any `--add-dir` write roots. |
| `danger-full-access` | Removes the write-root boundary. Policy rules still apply. |

`InWriteRoots` resolves both the candidate path and every root before comparing
them, so a symlink or Windows junction inside a root cannot redirect a write
outside it. An unresolvable path is a denial, not a pass.

### Path boundaries

Every tool path is normalized (backslashes, case, leading slashes) and matched
with `GlobMatch`, which supports `*` within a segment, `**` across segments and
`?` for one character. Literal patterns use prefix semantics. `Policy.checkPath`
additionally rejects relative `..` escapes and checks the canonical on-disk
location, so a link that points outside the workspace is denied even when no
explicit deny rule is configured. Permission rules use `WildcardMatch` with
deny-wins evaluation for safety.

### Invisible Unicode

Text shown to the operator is sanitized before an approval prompt is printed.
`ContainsInvisibleTags` detects Unicode Tag Block characters (Plane 14), which
render as nothing and can hide an injection payload inside readable text.
`SanitizeInvisible` removes those tags, reports whether any were found so the
prompt can warn, and escapes bidi/format controls with `EscapeBidi` so the
operator sees them as `\uXXXX` sequences. Only the prompt copy is sanitized:
the executed arguments are never modified, so display and execution do not
silently diverge.

### Command guard

`FindFileEditingCommand` is a token-aware classifier used in plan mode. It masks
quoted strings, backtick substitutions, heredoc bodies and shell comments
before looking for POSIX, cmd.exe and PowerShell file mutators, subcommands such
as `git push`, and non-temp output redirection. It is conservative: read-only
commands (`ls`, `cat`, `git status`, `go test`, `Get-Content`, ...) must not be
flagged, while a mutating command may occasionally slip through and be caught
by the normal policy/approval path instead.

### Threat model

**What this defends against**

- Accidental secret leakage into transcripts or the audit log (redaction).
- Lexical and symlink/junction path escapes from the workspace (path boundaries).
- Prompt injection hidden in invisible Unicode characters or bidi overrides
  (tag stripping and visible escapes in approval prompts).
- Destructive shell commands executed while the user believes they are in a
  read-only planning phase (the optional plan-mode command guard).
- Unattended mutation without an explicit decision (policy defaults, approval,
  terminal errors).

**What this does not defend against**

- **Malicious language servers or plugins.** MCP servers, LSP servers, hooks
  and plugins run with the agent's privileges. Their trust is established
  elsewhere (hook SHA-256 checks, manifest review); this chain only gates tool
  calls that reach it.
- **Compromised dependencies or a compromised Go toolchain.** The core imports
  only the standard library, but the native desktop shell depends on a third
  party UI module and its transitive tree. Supply-chain compromise is outside
  this chain's scope.
- **OS-level attacks:** root/admin compromise, kernel exploits, other local
  users, physical access, or malware already on the machine.
- **Determined prompt injection that stays visible.** If the operator can read
  the text and still approves it, the chain has done its job; it does not judge
  intent.
- **Network-level secrets management.** Tokens live in config or the
  environment; the audit scrubber reduces spread, it is not a secret store.

### Validation and audit

Run the reproducible audit (offline; Windows PowerShell):

```powershell
powershell -ExecutionPolicy Bypass -File scripts/security-audit.ps1
```

It runs, in order: `go vet ./...`, `gofmt -l .` (must be empty), `govulncheck
./...` when the tool is on PATH, the fuzz seed corpora via `go test
./internal/security -run Fuzz -count=1`, a `go list -m all` dependency
allowlist check, a stdlib-only check of `internal/` imports, and a privacy scan
of tracked source for token-like literals. A missing `govulncheck` prints an
install hint and is skipped; that is the only soft failure. Everything else
exits non-zero on a problem and the script prints a short summary.

Fuzzing itself is opt-in and uses the Go standard library's `testing.F`:

```powershell
go test ./internal/security -fuzz FuzzRedact -fuzztime=30s
go test ./internal/security -fuzz FuzzSanitizeInvisible -fuzztime=30s
```

The seed corpora in `internal/security/fuzz_test.go` are also executed as
ordinary tests, so a plain `go test ./internal/security` checks them without a
fuzzing engine.

#### Notes on the allowlists

- The dependency allowlist contains `github.com/egoist/mygo` **and** its pinned
  transitive modules, because `go list -m all` reports the whole module graph.
  The invariant it enforces is "no module outside this reviewed set"; the
  separate `go list -deps ./internal/...` check enforces "the core imports only
  the standard library". Adding a dependency is a deliberate review step that
  must update the allowlist in the audit script.
- The privacy scan allowlists two synthetic credential fixtures embedded in
  unit tests, keyed by exact file path and matched value. New matches — even in
  those same files — fail the audit. Do not add real credentials to source.

## CI

This repository has three workflows with non-overlapping triggers. Together they
are the release gate: a change is "green" only when every workflow that applies
to it passes.

### Workflows and when they run

| Workflow | Trigger | Purpose |
| --- | --- | --- |
| `ci.yml` | push to `main`/`master`, and every pull request | Fast gate on the shipped Windows target: `gofmt`, `go vet`, `go test`, no-cgo build, plus a Linux `-race` job. |
| `matrix.yml` | every pull request, and manual `workflow_dispatch` | End-to-end portability proof: no-cgo cross-builds, unit tests without cgo, the race gate, and the architecture guard. |
| `release.yml` | tags matching `v*` | Reproducible packaging, gated by its own `verify` and `race` jobs; publishes only after both pass. |

`matrix.yml` is intentionally **not** tag-triggered: `release.yml` owns tags, and
duplicating the matrix on a tag would spend runner time without adding a gate.
Run it manually from the Actions tab when you want the full portability signal
on a branch.

All three workflows use `permissions: contents: read` except the single
publishing job in `release.yml`, which needs write access to create the release.
No workflow in this repository requires user-supplied authentication.

### What each job proves

#### `ci.yml`

- **`go` (windows-latest)** — source is gofmt-clean, vet-clean, all tests pass,
  and the shipped `windows/amd64` binary builds with `CGO_ENABLED=0`. It also
  cross-compiles to `linux/amd64`, which was the original spot-check of the
  no-cgo promise.
- **`race` (ubuntu-latest)** — `go test -race` passes for every package except
  `internal/pty` (see the rationale below). Linux runners ship gcc, which the
  race detector needs.

#### `matrix.yml`

- **`crossbuild`** — `go build ./...` with `CGO_ENABLED=0` for all five targets
  below. Building `./...` includes `native/`, so an accidental cgo dependency or
  a platform-only import fails the job. No artifact is uploaded: a successful
  build *is* the proof of portability.
- **`unit`** — `CGO_ENABLED=0 go test ./...` on a stock Linux runner, proving the
  unit suite needs no C toolchain.
- **`race`** — mirrors `ci.yml`'s race gate with a generous `-timeout 20m`, so a
  hung test fails instead of blocking the job.
- **`archguard`** — `go test ./internal/archguard -count=1` (see below).

#### `release.yml`

- **`verify` / `race`** — mirror the `ci.yml` gates so a tag cannot release code
  that would fail CI.
- **`release`** — only after both gates pass; builds with `-trimpath` and
  `CGO_ENABLED=0`, verifies the artifacts, then publishes them.

### No-cgo cross-build matrix

| GOOS | GOARCH | Notes |
| --- | --- | --- |
| `windows` | `amd64` | The only distributed desktop target. |
| `linux` | `amd64` | Common CI host; already spot-checked in `ci.yml`. |
| `linux` | `arm64` | Proves the pure-Go core is architecture-independent. |
| `darwin` | `amd64` | Intel macOS portability. |
| `darwin` | `arm64` | Apple-silicon portability. |

Every entry runs with `CGO_ENABLED=0`, `GOOS` and `GOARCH` set, and
`fail-fast: false` so one failure never hides another. This makes the
`AGENTS.md` rule "everything must cross-compile with `GOOS=linux go build ./...`"
an enforced, multi-target contract instead of a local claim.

### Race gate and the pty exclusion

The race detector requires cgo, so both race jobs run on `ubuntu-latest` where
gcc is available (the Windows bundled toolchain is not a reliable race host).
The command excludes exactly one package:

```sh
go test -race $(go list ./... | grep -v '/internal/pty$')
```

`internal/pty` tests require a real controlling terminal with ConPTY semantics;
a headless CI runner cannot provide one. Excluding only that package keeps the
concurrency gate on every cross-platform package, and the Windows `ci.yml` job
still exercises `internal/pty` (without `-race`).

### Architecture guard

`internal/archguard` encodes the dependency rules from `AGENTS.md` as a test:
the core stays standard-library-only, forbidden import edges are rejected, and
no new module is introduced. It is the machine-checkable form of the layering
rules. `-count=1` disables the Go test cache so the invariant is actually
evaluated on every pull request rather than reused from a cached result.

### Detecting flakes locally with `scripts/flake-guard.ps1`

Wall-clock-sensitive tests (the TUI ctrl+c path, hook shell timeouts, job
process tests, agent timing logic) were historically flaky and only mitigated,
not monitored. The flake guard reruns those packages to expose instability:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/flake-guard.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/flake-guard.ps1 -Count 10
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/flake-guard.ps1 -Packages internal/hooks
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/flake-guard.ps1 -Race -Count 3
```

Parameters:

- `-Count` — repetitions per package (default `5`). Passed straight to
  `go test -count=N`, which bypasses the test cache.
- `-Packages` — package list (default `internal/tui`, `internal/hooks`,
  `internal/jobs`, `internal/agent`). Paths are relative to the repository root.
- `-Race` — adds `-race` and sets `CGO_ENABLED=1`. Off by default to keep runtime
  bounded; the CI race gate already covers concurrency.

Each package is run as `go test <pkg> -count=<Count> -timeout 300s`. Any failure
prints the failing package and its output and exits non-zero, so the script is
usable as a local pre-push check or in a scheduled job. It creates no temporary
files and prints total runtime at the end.

The script is deliberately not a required CI gate on every PR: multiplying the
full suite by N would make every pull request slow. Use it when a timing-related
test changes, when investigating a suspicious CI failure, or periodically as a
scheduled job.

### What "green CI" means

A change is ready to merge when:

1. `ci.yml` passes on the pull request (format, vet, tests, Windows build,
   Linux race gate including all non-pty packages).
2. `matrix.yml` passes on the same pull request: all five no-cgo cross-builds,
   the cgo-free unit suite, the race gate, and the architecture guard.
3. If `scripts/flake-guard.ps1` was run locally for touched timing-sensitive
   code, it reported PASS with no unstable packages.

For a release, `release.yml` additionally must pass its `verify` and `race`
gates and publish the expected artifacts before the tag is considered shipped.
A red matrix job is never acceptable just because `ci.yml` is green: the matrix
is the objective portability proof.

## Architecture dependency guard

Architecture dependency-guard test for `argentgo`. It turns the dependency
rules in the repository-level `AGENTS.md` into an automated check so a bad
import is caught by `go test` instead of by code review.

### What it enforces

The test (`internal/archguard/arch_test.go`) discovers the module path by reading
`go.mod` (it is never hardcoded), then walks every `.go` file under `internal/`
and `native/`, parsing imports with `go/parser` (`parser.ImportsOnly`). It asserts:

- **Stdlib-only core.** No file under `internal/**` may import a third-party
  module — an import whose first path segment contains a `.` (`github.com/...`,
  `golang.org/...`, `gopkg.in/...`). Imports of this repository's own module
  (`<module>/...`) are intra-repo references, not third party, and are allowed.
- **No cgo.** No file anywhere uses `import "C"`, so `GOOS=linux go build ./...`
  keeps working.
- **Layering (internal packages are libraries).**
  - `internal/tools` must not import `internal/agent`.
  - `internal/agent` must not import `internal/app` (`app` is the top-level
    wirer and is the only package allowed to import everything).
  - `internal/llm` is the wire layer and must not import `internal/agent`,
    `internal/tools`, or `internal/app`.
- **Single external exception.** `native/` is the only place allowed to use a
  third-party module, and that module is `github.com/egoist/mygo` (including
  its sub-packages such as `mygo/ui`). Any other external import in `native/`
  fails the test.

Notes on strictness / allow-list:

- All `.go` files are checked, including `_test.go`, platform/build-tag
  variants (`_windows.go`, `_linux.go`, …), and generated/mock sources. There
  are no silent exceptions.
- The archguard test itself imports only the standard library.
- The test is stdlib-only, uses no cgo, adds no module, and never compiles the
  packages it inspects — it only parses imports, so it is safe on any host and
  for cross-compilation.

### Why

`AGENTS.md` states these rules in prose. An executable guard prevents drift:
accidentally pulling e.g. a UUID or CLI library into `internal/`, adding cgo,
or letting the wire layer depend on the agent loop will break the build/test
immediately, preserving a dependency-free, portable core.

### Run

```powershell
$env:Path = "$env:USERPROFILE\.tools\go\go\bin;$env:Path"

go test ./internal/archguard -count=1   # architecture guard
go vet ./internal/archguard
gofmt -l internal/archguard             # must print nothing
```

## Release checklist

Internal engineering notes for people working on argentgo itself. **User-facing
documentation (install, quickstart, configuration, commands, troubleshooting)
lives in the root `README.md`** — do not duplicate it here. The `docs/` files are
for design decisions, parity tracking and release gates.

### Dependency policy

- The engine core (`internal/{agent,session,llm,tools,security,...}`) stays
  stdlib-only. `internal/app` wires everything; library packages take interfaces
  (`ToolRunner`, `Approver`, `Recorder`, `Chatter`) instead of importing up.
- `github.com/egoist/mygo` is the single explicit exception, used by `native/`
  for the GPU-rendered desktop shell. It talks to the engine over the stdlib
  `--rpc` transport. Its transitive requirements (`purego`, `golang.org/x/*`)
  are indirect dependencies of that one module, not new core dependencies.
- The only desktop frontend is `native/` (MyGo). The browser frontend and its
  HTTP+SSE bridge have been removed; `argent gui` launches `argent-native.exe`.
  `cmd/argent/gui.go` only resolves the engine and starts that binary.
- No cgo. Everything must cross-compile with `GOOS=linux go build ./...`.
- Do not add a dependency to save a few lines of code.

### CI gates

`.github/workflows/ci.yml` enforces on every PR and push to main:

| Gate | Why |
| --- | --- |
| `gofmt -l .` | Windows runners default to `core.autocrlf=true`; CI normalizes it first. |
| `go vet ./...` | Static correctness. |
| `go test ./...` | All package tests. |
| `go test -race ./...` | Concurrency is a release gate (session fd/ID indexes, broker channels, job registry). Runs on `windows-latest`, which ships MinGW for CGO. The local dev host has no gcc and skips this. |
| `build (no cgo)` | Produces the Windows/amd64 binary we actually ship. |
| `cross-compile (linux/amd64, no cgo)` | Verifies the `AGENTS.md` portability promise on every PR, not just locally. |
| `ui` (ubuntu) | `bun install --frozen-lockfile`, typecheck, test, build of `ui/`. |

Race and cross-compile were added after the fact because the original CI only
checked the shipped platform; concurrency and portability were validated by
hand. Keep them in the gate.

### Performance baseline

No `Benchmark*` functions exist yet; adding the bench gate before there is a
baseline would be a no-op. When a benchmark is added:

```powershell
go test -run=^$ -bench=. -benchmem ./internal/...
```

Record the numbers in this file (with Go version, OS and host) so a regression
is visible. Do not chase micro-optimizations without a measured baseline.

### Release gate

The authoritative release checklist is `docs/STATUS.md`; packaging is in
`.github/workflows/release.yml`. The operator ships **Windows/amd64 only**.
`scripts/build-release.ps1` is the single source of truth for artifacts and
SHA-256 checksums.

### Release checklist steps

Run in order; keep the version identical in every source location:

1. **Bump the version** in `internal/cli/cli.go` (`const Version`),
   `internal/mcp/mcp.go` (`clientVersion`) and `internal/lsp/client.go`
   (`clientInfo.version` in `initialize`). `scripts/build-release.ps1` reads only
   `cli.go`, so sync the other two by hand.
2. **Update `CHANGELOG.md`** with a `## v<version>` section; the consistency
   check rejects a release without it.
3. **Full verification** on a clean tree: `gofmt -l .`, `go vet ./...`,
   `go test ./...`, `go test -race ./...` (needs gcc/MinGW on the Windows host),
   and the no-cgo cross-compile `GOOS=linux GOARCH=amd64 go build ./...`. Also run
   `argent eval evals/core.json`, the `argent gui` end-to-end smoke test, and
   `scripts/privacy-scan.ps1` (no keys, tokens, real emails or local paths).
4. **Build artifacts** with
   `powershell -ExecutionPolicy Bypass -File scripts/build-release.ps1` — writes
   `dist/release/argent_<version>_windows_amd64.zip` (both binaries), its
   `.sha256` sidecar and `checksums.txt`. `scripts/publish-win.ps1 -DryRun`
   repeats every gate without pushing.
5. **Commit, tag, push**: focused commit, `git tag -a v<version>`, then push the
   commit and the tag. Authenticate through the normal git credential helper
   (`git credential fill`); no credential or token is stored in these docs or
   scripts. `scripts/publish-win.ps1` can perform the tag and push itself.
6. **Publish the release** — either create the GitHub Release manually and
   upload the zip plus `checksums.txt`, or let `scripts/publish-win.ps1` trigger
   the workflow and wait for it. A tag alone is not a release: the publisher
   fails if the published Release lacks either asset.
7. **Update the local desktop**: re-download the latest zip (or re-run
   `scripts/install-win.ps1`), verify, and replace **both** binaries together so
   the desktop client stays on the latest release.

## Release validation

`scripts/test-release-workflow.ps1` is a **local, offline** harness for
`.github/workflows/release.yml`. It closes the "release.yml is never validated"
gap by checking, on a developer machine, that the workflow is wired correctly
and that the artifact-verification step it runs actually succeeds on a
release-shaped artifact.

> **Honest scope statement:** this is a *partial* validation, not an end-to-end
> publish test. No GitHub Actions run, no `gh` CLI publish, no real tag and no
> real token are involved. A genuine end-to-end release still requires pushing
> a `v*` tag and letting GitHub Actions run `release.yml` with the automatically
> provided `GITHUB_TOKEN`. Passing this harness must not be read as "the
> release pipeline has been proven end-to-end".

### How to run

From the repository root, using the toolchain in `AGENTS.md`:

```powershell
$env:Path = "$env:USERPROFILE\.tools\go\go\bin;$env:Path"   # only needed for -FullBuild

# Default: fully offline, no Go build required.
powershell -ExecutionPolicy Bypass -File scripts/test-release-workflow.ps1

# Optional: also run the real scripts/build-release.ps1 into a temp dir.
powershell -ExecutionPolicy Bypass -File scripts/test-release-workflow.ps1 -FullBuild
```

Requirements for the default path: Windows PowerShell 5.1 or `pwsh`, and
nothing else. `-FullBuild` additionally needs a working Go toolchain and the
modules in `go.mod` already cached (otherwise it is not offline).

The script prints a `PASS`/`FAIL` line per assertion and a final summary with a
`RESULT: PASS` / `RESULT: FAIL` line. It exits `0` on success and `1` if any
assertion failed. All temp directories are removed in a `finally` block, even
when an assertion fails.

### What the harness validates

#### 1. Static structure of `release.yml` (parsed as text)

| Area | Assertions |
| --- | --- |
| Job graph | jobs `verify`, `race`, `release` all declared; `release` has `needs: [verify, race]` |
| Trigger | tag push trigger `tags: ['v*']` |
| Least privilege | top-level `permissions:` with `contents: read`; `release` job overrides with `permissions: contents: write` |
| Token wiring | `release` references `${{ secrets.GITHUB_TOKEN }}` and sets `GH_TOKEN` from it — asserted as a **literal placeholder string only**; the value is never read, resolved or printed |
| Quality gates | `verify` runs gofmt / `go vet` / `go test` with `CGO_ENABLED='0'`; `race` runs `go test -race` with `CGO_ENABLED='1'` |
| Local scripts called | `release` calls `check-release-consistency.ps1`, `build-release.ps1`, `verify-release.ps1`, `extract-release-notes.ps1` |
| Publish step | `release` runs `gh release create` with `--verify-tag`, then `gh release view` to confirm the two assets |

Each job-specific assertion is scoped to that job's block in the YAML, so a
setting on one job cannot satisfy a check meant for another.

#### 2. Local artifact verification

The workflow's "Verify release artifacts" step is reproduced against a temp
directory:

- **Default:** fabricate a `argent_<version>_windows_amd64.zip` containing
  `argent.exe` and `argent-native.exe` plus a matching sha256sum-style
  `checksums.txt` — the exact shape `build-release.ps1` produces.
- **`-FullBuild`:** run the real `scripts/build-release.ps1 -SkipChecks -Tag v<version>`
  into the temp dir first.

Then `scripts/verify-release.ps1 -ArtifactDir <temp> -Version <version>` is run
in a separate PowerShell process (exactly as the workflow invokes it) and must
exit `0`. That verifies zip presence, both binaries inside it, and the SHA-256
in `checksums.txt` against the actual bytes.

#### 3. Optional YAML syntax check

If `python` is on `PATH`, the harness additionally tries
`yaml.safe_load` on `release.yml` and reports the result. A missing Python or
missing PyYAML is reported as `SKIP`, never `FAIL` — the textual assertions are
authoritative. A genuine parse error is reported as a failure.

### What only a real tag push can validate

These are intentionally **not** claimed by the harness:

- GitHub Actions actually scheduling and running the `verify` and `race` jobs on
  `windows-latest` / `ubuntu-latest`, including toolchain setup and caching.
- The `release` job's `needs: [verify, race]` gating being honoured by Actions.
- `GITHUB_TOKEN` being issued, scoped to `contents: write`, and accepted by
  `gh release create`; no token value is ever accessed locally.
- `--verify-tag` resolving against a real pushed tag and the checked-out commit.
- The real `gh release create` call, network round-trip and the post-publish
  `gh release view` asset assertion.
- Version/tag consistency under a real tag (`check-release-consistency.ps1`),
  and the `native/` GUI binary built from that tag's source.

### Release job graph

```
push tag v*  (on.push.tags: ['v*'])
        |
        +-------------------+-------------------+
        |                                       |
   verify (windows-latest)                race (ubuntu-latest)
   gofmt / go vet / go test               go test -race (no internal/pty)
   build windows + linux, CGO=0           CGO=1
        |                                       |
        +-------------------+-------------------+
                            |
                            v
              release (windows-latest)
              needs: [verify, race]
              permissions: contents: write
                            |
   1. check-release-consistency.ps1 -Tag $RELEASE_TAG
   2. build-release.ps1 -Tag $RELEASE_TAG -SkipChecks
   3. verify-release.ps1 -ArtifactDir dist/release -Version $version
   4. extract-release-notes.ps1 -Tag $RELEASE_TAG -OutFile release-notes.md
   5. GH_TOKEN=secrets.GITHUB_TOKEN gh release create ... --verify-tag
   6. gh release view --json assets  (assert both assets present)
```

Steps 2–3 are the local part this harness reproduces. Steps 1 and 4–6 require
a real checkout/tag/token and are only structure-asserted here.

### Privacy and safety

- No secrets, tokens, credentials or private filesystem paths are embedded in
  the harness or this document. The token appears only as the literal YAML
  placeholder `${{ secrets.GITHUB_TOKEN }}` used for the assertion, and its
  value is never read or printed.
- The harness never contacts the network and never calls `gh`.
- It writes only to a uniquely named temp directory and deletes it in
  `finally`; it does not touch `dist/` or the working tree.

### Failure triage

- A static assertion fails: `release.yml` drifted from the gated layout. Fix the
  workflow, not the harness (the harness is intentionally strict).
- `verify-release.ps1 exited 0` fails: the fabricated/real artifact is not
  release-shaped; check the printed verifier output and the `Version` constant
  in `internal/cli/cli.go`.
- `-FullBuild` build step fails: check the Go toolchain, cached modules and
  `native/` dependencies; the default offline run remains the authoritative
  check for wiring.
- `python yaml.safe_load` reports a failure (not `SKIP`): `release.yml` has a
  real YAML syntax error that the textual checks cannot catch.
