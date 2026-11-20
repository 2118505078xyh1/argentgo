# CI contract

This repository has three workflows with non-overlapping triggers. Together they
are the release gate: a change is "green" only when every workflow that applies
to it passes.

## Workflows and when they run

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

## What each job proves

### `ci.yml`

- **`go` (windows-latest)** — source is gofmt-clean, vet-clean, all tests pass,
  and the shipped `windows/amd64` binary builds with `CGO_ENABLED=0`. It also
  cross-compiles to `linux/amd64`, which was the original spot-check of the
  no-cgo promise.
- **`race` (ubuntu-latest)** — `go test -race` passes for every package except
  `internal/pty` (see the rationale below). Linux runners ship gcc, which the
  race detector needs.

### `matrix.yml`

- **`crossbuild`** — `go build ./...` with `CGO_ENABLED=0` for all five targets
  below. Building `./...` includes `native/`, so an accidental cgo dependency or
  a platform-only import fails the job. No artifact is uploaded: a successful
  build *is* the proof of portability.
- **`unit`** — `CGO_ENABLED=0 go test ./...` on a stock Linux runner, proving the
  unit suite needs no C toolchain.
- **`race`** — mirrors `ci.yml`'s race gate with a generous `-timeout 20m`, so a
  hung test fails instead of blocking the job.
- **`archguard`** — `go test ./internal/archguard -count=1` (see below).

### `release.yml`

- **`verify` / `race`** — mirror the `ci.yml` gates so a tag cannot release code
  that would fail CI.
- **`release`** — only after both gates pass; builds with `-trimpath` and
  `CGO_ENABLED=0`, verifies the artifacts, then publishes them.

## No-cgo cross-build matrix

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

## Race gate and the pty exclusion

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

## Architecture guard

`internal/archguard` encodes the dependency rules from `AGENTS.md` as a test:
the core stays standard-library-only, forbidden import edges are rejected, and
no new module is introduced. It is the machine-checkable form of the layering
rules. `-count=1` disables the Go test cache so the invariant is actually
evaluated on every pull request rather than reused from a cached result.

## Detecting flakes locally with `scripts/flake-guard.ps1`

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

## What "green CI" means

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
