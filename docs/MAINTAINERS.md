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
