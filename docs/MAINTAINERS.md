# Maintainer notes

Internal engineering notes for people working on argentgo itself. **User-facing
documentation (install, quickstart, configuration, commands, troubleshooting)
lives in the root `README.md`** — do not duplicate it here. The `docs/` files are
for design decisions, parity tracking and release gates.

## Dependency policy

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

## CI gates

`.github/workflows/ci.yml` enforces on every PR and push to main:

| Gate | Why |
|---|---|
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

## Performance baseline

No `Benchmark*` functions exist yet; adding the bench gate before there is a
baseline would be a no-op. When a benchmark is added:

```powershell
go test -run=^$ -bench=. -benchmem ./internal/...
```

Record the numbers in this file (with Go version, OS and host) so a regression
is visible. Do not chase micro-optimizations without a measured baseline.

## Release gate

The authoritative release checklist is `docs/ROADMAP.md`; packaging is in
`.github/workflows/release.yml`. The operator ships **Windows/amd64 only**.
`scripts/build-release.ps1` is the single source of truth for artifacts and
SHA-256 checksums.

## Release checklist

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
