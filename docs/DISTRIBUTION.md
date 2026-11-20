# Distribution architecture

This file records how argentgo is packaged and shipped. Installation steps live
in [docs/INSTALL.md](INSTALL.md); maintainer-facing release gates live in
[docs/MAINTAINERS.md](MAINTAINERS.md).

## The two binaries

A release ships exactly two Windows executables, built from the same source
tree and the same version:

| Binary | Source | Role |
|---|---|---|
| `argent.exe` | `./cmd/argent` (`package main`, `func main`) | CLI, full-screen TUI and the agent engine. Also exposes the headless transports `--serve` (HTTP/SSE), `--rpc` and `acp`. Reports its version via `--version`. |
| `argent-native.exe` | `./native` | MyGo native desktop chat window, the only GUI frontend. Windows-only. |

The engine core is stdlib-only and cgo-free. `github.com/egoist/mygo` (pinned in
`go.mod`) is the single third-party dependency and is used only by `native/`.
The browser frontend has been removed.

## How they communicate (`--rpc`)

The two programs do not link together; they talk over an existing transport:

1. `argent gui` (or `argent --gui`) is detected in `cmd/argent/main.go`, which
   calls `runGUI` in `cmd/argent/gui.go`. That resolves config, opens the
   session store and refuses to run on non-Windows hosts.
2. It looks for `argent-native.exe` **next to the running `argent.exe`**
   (`os.Executable()`'s directory) and starts it with `ARGENT_EXE` set to the
   engine path and `ARGENT_CWD` set to the workspace root. `ARGENT_CWD` also
   becomes the child's working directory.
3. `native/main.go` reads `ARGENT_EXE`; when present, `native/bridge.go` spawns
   `argent --rpc` with the workspace as its working directory. When absent, the
   native window runs a canned demo instead of a real engine.
4. `--rpc` is newline-delimited JSON-RPC 2.0 over stdio (`internal/server/rpc.go`).
   Methods include `prompt`, `prompt/delta` streaming notifications, `abort`,
   `session.list` and `session.load`. The native UI sends prompts and renders
   deltas; it never imports engine packages, so the core stays stdlib-only.

## Release assets layout

Releases are published on the public GitHub page:
<https://github.com/2118505078xyh1/argentgo/releases>. A tagged commit is **not**
a release by itself: `scripts/publish-win.ps1` waits for a published Release
that has both required assets and fails if only a tag exists.

Per release:

```text
argent_<version>_windows_amd64.zip     # contains argent.exe + argent-native.exe at the root
argent_<version>_windows_amd64.zip.sha256  # single-hash sidecar
checksums.txt                          # sha256sum-style manifest for all zips
```

- `scripts/build-release.ps1` is the single source of truth for artifacts. It
  builds `./cmd/argent` and `./native` with `CGO_ENABLED=0`, `GOOS=windows`,
  `GOARCH=amd64` and `-trimpath`, then zips both binaries together.
- The zip is a GUI-subsystem build (`-H=windowsgui`): no black console window
  from a desktop shortcut, while CLI output still works from an existing
  terminal.
- `checksums.txt` is UTF-8 without BOM with LF endings so `sha256sum -c`
  accepts it. Each line is `<sha256>  <filename>`. The combined manifest also
  drives the updater's automatic verification; the per-zip `.sha256` sidecar is
  for tools that expect one hash per asset.
- The release notes restate the hash. `scripts/publish-win.ps1` can publish via
  the repository's CI workflow using its built-in credentials, or the release
  can be created manually with the same two assets uploaded.

## Checksums

SHA-256 is computed with `Get-FileHash` at build time. Verification options:

```bash
# Git Bash
sha256sum -c checksums.txt
```

```powershell
# PowerShell: compare against the manifest line for your zip
(Get-FileHash -LiteralPath '.\argent_<version>_windows_amd64.zip' -Algorithm SHA256).Hash.ToLowerInvariant()
```

See [docs/INSTALL.md](INSTALL.md) for a complete verification example.

## Supported platforms

- **Windows amd64 is the only published binary target.** `scripts/build-release.ps1`
  defines a single `windows/amd64` target, and `scripts/publish-win.ps1` requires
  a `argent_*_windows_amd64.zip` asset.
- There is **no cgo**, so `GOOS=linux go build ./...` cross-compiles successfully
  and CI verifies it on every PR. No Linux binary or other platform zip is
  published.
- `go install github.com/2118505078xyh1/argentgo/cmd/argent@latest` works on any
  host with Go 1.27.3+, but it installs only the CLI/engine. The desktop client
  is Windows-only and must be built as `./native` or taken from the release zip.
- The desktop client stays on the latest release: install both binaries from
  the same zip rather than mixing versions.

## Version locations

The version is duplicated in three source literals and must be bumped together:

| File | Constant / field |
|---|---|
| `internal/cli/cli.go` | `const Version` — reported by `--version`, `/v1/health` and ACP `initialize`; read by the release scripts. |
| `internal/mcp/mcp.go` | `clientVersion` — sent as MCP `clientInfo.version`. |
| `internal/lsp/client.go` | `version` in the LSP `clientInfo` of the `initialize` request. |

`scripts/build-release.ps1` reads the version **only** from `internal/cli/cli.go`
and validates `-Version` / `-Tag` against it, so a mismatch in the other two
files will not be caught by the packaging script. Keep all three in sync.
