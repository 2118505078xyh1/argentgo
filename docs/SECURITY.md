# Security model

`argentgo` treats every tool call as untrusted until it has passed a short,
deterministic chain in `internal/security`. The chain is deliberately small and
stdlib-only so its behavior can be reviewed end to end. It is a set of
guardrails for an operator running an agent on their own machine, not a sandbox
against a determined attacker.

## The chain: policy → approval → audit

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

## Sandbox modes

`--sandbox-mode` selects the file-tool policy via `security.FromString`:

| Mode | Behavior |
| --- | --- |
| `read-only` | Drops every mutating tool; only the known safe tools remain. |
| `workspace-write` (default) | Writes are allowed but bounded to the workspace and any `--add-dir` write roots. |
| `danger-full-access` | Removes the write-root boundary. Policy rules still apply. |

`InWriteRoots` resolves both the candidate path and every root before comparing
them, so a symlink or Windows junction inside a root cannot redirect a write
outside it. An unresolvable path is a denial, not a pass.

## Path boundaries

Every tool path is normalized (backslashes, case, leading slashes) and matched
with `GlobMatch`, which supports `*` within a segment, `**` across segments and
`?` for one character. Literal patterns use prefix semantics. `Policy.checkPath`
additionally rejects relative `..` escapes and checks the canonical on-disk
location, so a link that points outside the workspace is denied even when no
explicit deny rule is configured. Permission rules use `WildcardMatch` with
deny-wins evaluation for safety.

## Invisible Unicode

Text shown to the operator is sanitized before an approval prompt is printed.
`ContainsInvisibleTags` detects Unicode Tag Block characters (Plane 14), which
render as nothing and can hide an injection payload inside readable text.
`SanitizeInvisible` removes those tags, reports whether any were found so the
prompt can warn, and escapes bidi/format controls with `EscapeBidi` so the
operator sees them as `\uXXXX` sequences. Only the prompt copy is sanitized:
the executed arguments are never modified, so display and execution do not
silently diverge.

## Command guard

`FindFileEditingCommand` is a token-aware classifier used in plan mode. It masks
quoted strings, backtick substitutions, heredoc bodies and shell comments
before looking for POSIX, cmd.exe and PowerShell file mutators, subcommands such
as `git push`, and non-temp output redirection. It is conservative: read-only
commands (`ls`, `cat`, `git status`, `go test`, `Get-Content`, ...) must not be
flagged, while a mutating command may occasionally slip through and be caught
by the normal policy/approval path instead.

## Threat model

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

## Validation and audit

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

### Notes on the allowlists

- The dependency allowlist contains `github.com/egoist/mygo` **and** its pinned
  transitive modules, because `go list -m all` reports the whole module graph.
  The invariant it enforces is "no module outside this reviewed set"; the
  separate `go list -deps ./internal/...` check enforces "the core imports only
  the standard library". Adding a dependency is a deliberate review step that
  must update the allowlist in the audit script.
- The privacy scan allowlists two synthetic credential fixtures embedded in
  unit tests, keyed by exact file path and matched value. New matches — even in
  those same files — fail the audit. Do not add real credentials to source.
