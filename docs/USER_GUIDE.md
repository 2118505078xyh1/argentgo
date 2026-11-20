# User guide

Installation, quickstart, distribution and package-manager recipes for argentgo.
Command flags, transports and tools are documented in the
[Technical reference](TECHNICAL.md); maintainer and release-gate notes live in
[CONTRIBUTING.md](../CONTRIBUTING.md).

## Install

argentgo ships two Windows binaries from the same release:

- `argent.exe` — the CLI, TUI and agent engine (built from `./cmd/argent`).
- `argent-native.exe` — the MyGo desktop chat window (built from `./native`).

The desktop client **stays on the latest release**: keep both binaries from the
same zip so their versions match. There is no installer service, telemetry or
background process.

All downloads and release notes are on the public GitHub page:
<https://github.com/2118505078xyh1/argentgo/releases>.

### Requirements

| Install method | Requirement |
|---|---|
| Prebuilt zip (recommended) | Windows amd64; PowerShell (or Git Bash for `sha256sum`). No runtime needed. |
| `go install` from source | Go 1.27.3 or newer (pinned as `go 1.27.3` in `go.mod`), network access to fetch the module. Builds entirely from source. |
| Native desktop from source | A local checkout, Go 1.27.3+, and Windows (the desktop client is Windows-only). |

### Prebuilt binary (Windows, quickest path)

1. Download the latest release assets from
   <https://github.com/2118505078xyh1/argentgo/releases/latest>:
   - `argent_<version>_windows_amd64.zip`
   - `checksums.txt`

2. Verify the zip against the SHA-256 manifest. `checksums.txt` is a
   `sha256sum`-style file (`<hash>  <filename>`), so in Git Bash:

   ```bash
   sha256sum -c checksums.txt
   ```

   In PowerShell, compare the file's hash with the manifest line yourself:

   ```powershell
   $zip = 'argent_<version>_windows_amd64.zip'
   $want = (Get-Content .\checksums.txt | Where-Object { $_ -match '\s' + [regex]::Escape($zip) + '$' } | Select-Object -First 1) -split '\s+'
   $got = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant()
   if ($got -ne $want[0]) { throw 'checksum mismatch' }
   ```

   The release notes also state the hash. A per-asset
   `argent_<version>_windows_amd64.zip.sha256` is produced by the build script
   for tools that take a single-hash file.

3. Extract and smoke-test:

   ```powershell
   Expand-Archive -LiteralPath '.\argent_<version>_windows_amd64.zip' -DestinationPath .
   .\argent.exe --version
   ```

   The zip extracts `argent.exe` and `argent-native.exe` at its root. **Keep the
   two files in the same directory**: `argent gui` looks for `argent-native.exe`
   beside `argent.exe` and fails if it is not there. Add that directory to your
   `PATH` to run `argent` from any shell.

### PowerShell one-liner installer

For a local checkout with a downloaded release zip, `scripts/install-win.ps1`
installs `argent.exe` into `%LOCALAPPDATA%\argentgo\bin` (override with
`-InstallDir`). Its documented usage is:

```powershell
powershell -ExecutionPolicy Bypass -File scripts/install-win.ps1 `
    [-ZipPath dist/release/argent_<version>_windows_amd64.zip] `
    [-InstallDir "$env:LOCALAPPDATA\argentgo\bin"]
```

A concrete one-liner after placing the zip next to the script's checkout:

```powershell
powershell -ExecutionPolicy Bypass -File scripts/install-win.ps1 -ZipPath '.\argent_<version>_windows_amd64.zip'
```

With no `-ZipPath`, the script looks for exactly one
`argent_*_windows_amd64.zip` in `dist/release` (where
`scripts/build-release.ps1` writes artifacts). It installs **only** the CLI
binary; sessions/config under `~/.argentgo` are untouched. For the desktop
window, copy `argent-native.exe` from the zip into the same install directory.

### From source (`go install`)

`cmd/argent` is a `main` package, so the standard Go install path is valid:

```powershell
go install github.com/2118505078xyh1/argentgo/cmd/argent@latest
```

This fetches the public module and **builds from source**; it needs the Go
toolchain (1.27.3+) and is not a prebuilt binary. The resulting `argent.exe` is
placed in `$GOBIN` if set, otherwise in `$(go env GOPATH)/bin` (check with
`go env GOPATH`). Verify with `argent.exe --version`.

`go install` installs only the CLI/engine. It does not build the desktop
frontend. It also works on non-Windows hosts for CLI/TUI use, but `argent gui`
requires Windows plus a separately built `argent-native.exe`.

### Building the native desktop frontend from source

From a local checkout:

```powershell
go build -o argent-native.exe ./native
```

`native/` is the single place that depends on the MyGo module
(`github.com/egoist/mygo`, already pinned in `go.mod`); the engine core stays
stdlib-only and cgo-free. `argent gui` launches this binary from the engine's
directory and drives it over the existing `--rpc` transport. The desktop client
is Windows-only. See [Distribution](#distribution) for the two-binary
architecture.

### Updating

- **Prebuilt:** download the latest zip and `checksums.txt`, verify, and replace
  **both** binaries together. Re-running the installer updates `argent.exe`
  only, so copy the matching `argent-native.exe` as well.
- **From source:** re-run
  `go install github.com/2118505078xyh1/argentgo/cmd/argent@latest` and rebuild
  `./native` from the updated checkout.

### Uninstalling

```powershell
powershell -ExecutionPolicy Bypass -File scripts/uninstall-win.ps1 `
    [-InstallDir "$env:LOCALAPPDATA\argentgo\bin"]
```

The script removes only the installed `argent.exe` (and an empty install
directory). User data under `~/.argentgo` — sessions, config, hooks and audit
logs — is preserved. For manual/zip installs, delete the extracted directory;
for `go install`, delete the binary from `$(go env GOPATH)/bin`. Remove
`argent-native.exe` separately if you installed the desktop frontend.

## Quickstart

This is the language-neutral, step-by-step path from "downloaded" to "first useful
answer". All commands are Windows/PowerShell unless noted. See
[Install](#install) for the full installation guide and
[API reference](TECHNICAL.md#api-reference) for the complete command surface.

> `<your-key>` below is a placeholder. Replace it with your own API key and never
> commit it to version control.

### 1. Install

Fastest path — the prebuilt Windows zip:

1. Download `argent_<version>_windows_amd64.zip` and `checksums.txt` from the
   [latest release](https://github.com/2118505078xyh1/argentgo/releases/latest).
2. Verify the SHA-256 manifest (Git Bash: `sha256sum -c checksums.txt`; PowerShell:
   compare `Get-FileHash` output with `checksums.txt`).
3. Extract and smoke-test:

   ```powershell
   Expand-Archive .\argent_<version>_windows_amd64.zip -DestinationPath .
   .\argent.exe --version
   ```

Keep `argent.exe` and `argent-native.exe` in the **same directory** — `argent gui`
looks for the native binary beside the engine. The desktop client stays on the
latest release, so always replace both binaries together. From source, use
`go install github.com/2118505078xyh1/argentgo/cmd/argent@latest` (Go 1.27.3+).

### 2. Configure a provider

`argent login <provider>` stores a key in the config file
(`~/.argentgo/config.json`) and never echoes it back:

```powershell
argent login openai --api-key <your-key>   # explicit key
argent login openai                        # prompts on stdin, without echo
argent auth                                # show stored credential status
```

The key is read from `--api-key`, `<PROVIDER>_API_KEY`, `ARGENT_API_KEY`, or from
the interactive prompt. You can also skip `login` and set an environment
variable for the current shell:

```powershell
$env:OPENAI_API_KEY = "<your-key>"
```

Run `argent models` to list the built-in provider templates (`openai`,
`anthropic`, `deepseek`, `qwen`, …). Use `argent logout [provider]` to remove
stored credentials.

### 3. First prompt in the TUI

Start the full-screen terminal UI:

```powershell
argent tui
```

> `argent` with no prompt reads stdin and exits; it does not auto-open the TUI.
> Use `argent tui` (or `argent --tui`) for the interactive UI. For a one-shot
> answer, run `argent "your task"`.

Type a task and press **Enter** to send. Useful keys:

| Key | Action |
|---|---|
| `Enter` | send |
| `Alt+Enter` / `Ctrl+J` | newline |
| `PgUp` / `PgDn`, `Shift+Up` / `Shift+Down` | scroll |
| `Ctrl+C` | cancel the current turn; press again to quit |
| `/quit` or `/exit` | quit |

Type `/help` in the input box to display the in-app key list.

### 4. Native desktop window

```powershell
argent gui
```

This launches `argent-native.exe` from the engine's directory. The native client
drives the engine over the built-in `--rpc` transport — no browser, no WebView,
no HTTP service. It is Windows-only and requires `argent-native.exe` beside
`argent.exe`. See [Frontend decision](TECHNICAL.md#frontend-decision) and
[Distribution](#distribution).

### 5. Switch model or provider

```powershell
argent models                                   # list templates
argent --provider deepseek "帮我写个函数"        # use a built-in template
argent -m gpt-4o "explain this diff"             # -m / --model overrides the model
```

Other useful switches: `--protocol anthropic|openai|responses`, `--base-url <url>`,
`--profile <name>`, and `-c/--config key=value` for dotted config overrides.
Settings resolve from flags → `ARGENT_*` environment → config files → defaults.

### 6. Approvals and sandbox mode

```powershell
argent --approval ask   "refactor this"   # default: prompt for approval
argent --approval auto  "refactor this"   # auto-approve every request
argent --approval never "refactor this"   # deny every approval request
argent --sandbox-mode read-only "audit this dir"
```

When an approval prompt appears: **y** allow, **n** deny, **a** allow for this
session, **esc** deny. `--yolo` is a shortcut for `auto`. Sandbox modes are
`read-only` (mutating tools removed), `workspace-write` (default, workspace
boundary enforced) and `danger-full-access` (boundary removed). Approval and
policy decisions are recorded in a redacted audit log under `~/.argentgo/audit/`.

### 7. Slash commands

Verified built-in TUI commands: `/help`, `/clear`, `/expand`, `/undo`, `/retry`,
`/name <title>`, `/quit` (alias `/exit`). `/undo` and `/retry` operate on the
latest SHA-256 checkpoint.

Markdown slash commands are also discovered from `.argent/commands` (project) and
`~/.argentgo/commands` (user); `argent commands` lists them. Their bodies expand
`$ARGUMENTS` / `$@` and `$1`…`$N`.

> There is **no** built-in `/compact` command. Context compaction is automatic;
> disable it for one run with `--no-auto-compact`.

### 8. Troubleshooting

```powershell
argent doctor                          # config, storage, provider, git, Go
argent doctor --base-url <url>         # probe a specific provider endpoint
argent bug                             # write a diagnostics dump
```

`doctor` prints `ok` / `warn` / `fail` per check. For install problems, re-read
[Install](#install); for exact flags and return values, see
[API reference](TECHNICAL.md#api-reference).

## Distribution

This section records how argentgo is packaged and shipped. Maintainer-facing
release gates live in
[CONTRIBUTING.md](../CONTRIBUTING.md#release-checklist).

### The two binaries

A release ships exactly two Windows executables, built from the same source
tree and the same version:

| Binary | Source | Role |
|---|---|---|
| `argent.exe` | `./cmd/argent` (`package main`, `func main`) | CLI, full-screen TUI and the agent engine. Also exposes the headless transports `--serve` (HTTP/SSE), `--rpc` and `acp`. Reports its version via `--version`. |
| `argent-native.exe` | `./native` | MyGo native desktop chat window, the only GUI frontend. Windows-only. |

The engine core is stdlib-only and cgo-free. `github.com/egoist/mygo` (pinned in
`go.mod`) is the single third-party dependency and is used only by `native/`.
The browser frontend has been removed.

### How they communicate (`--rpc`)

The two programs do not link together; they talk over an existing transport:

1. `argent gui` is detected in `cmd/argent/main.go`, which
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

### Release assets layout

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

### Checksums

SHA-256 is computed with `Get-FileHash` at build time. Verification options:

```bash
# Git Bash
sha256sum -c checksums.txt
```

```powershell
# PowerShell: compare against the manifest line for your zip
(Get-FileHash -LiteralPath '.\argent_<version>_windows_amd64.zip' -Algorithm SHA256).Hash.ToLowerInvariant()
```

See [Install](#install) for a complete verification example.

### Supported platforms

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

### Version locations

The version is duplicated in three source literals and must be bumped together:

| File | Constant / field |
|---|---|
| `internal/cli/cli.go` | `const Version` — reported by `--version`, `/v1/health` and ACP `initialize`; read by the release scripts. |
| `internal/mcp/mcp.go` | `clientVersion` — sent as MCP `clientInfo.version`. |
| `internal/lsp/client.go` | `version` in the LSP `clientInfo` of the `initialize` request. |

`scripts/build-release.ps1` reads the version **only** from `internal/cli/cli.go`
and validates `-Version` / `-Tag` against it, so a mismatch in the other two
files will not be caught by the packaging script. Keep all three in sync.

## Package managers

This section collects community package-manager **recipes** for installing
argentgo. They are templates, not published artifacts: the project does **not**
maintain or host a catalog entry in Scoop, Chocolatey or Homebrew. A maintainer
(or downstream packager) copies a recipe into the relevant bucket / package
repo / tap and fills in the placeholders at release time. Nothing here depends
on a hosted catalog owned by this project.

Public repository: <https://github.com/2118505078xyh1/argentgo>

### Files

| File | Package manager | Platform | What it installs |
| --- | --- | --- | --- |
| `packaging/scoop/argentgo.json` | Scoop | Windows (amd64) | Downloads and unzips the release zip; shims `argent.exe` and `argent-native.exe`. |
| `packaging/choco/argentgo.nuspec` | Chocolatey | Windows (amd64) | NuGet package metadata (dependency-free); the version must match the release. |
| `packaging/choco/tools/chocolateyinstall.ps1` | Chocolatey | Windows (amd64) | Downloads the release zip with `Install-ChocolateyZipPackage` and verifies its SHA-256. |
| `packaging/homebrew/argentgo.rb` | Homebrew | macOS / Linux | Builds the CLI/TUI from source with `go install`. The native desktop GUI is Windows-only and is not included. |

All recipes use only public GitHub Releases URLs. There are no credentials,
tokens or private paths.

### Placeholder convention

Recipes deliberately do **not** hardcode a current version. Release automation
replaces these placeholders before publishing a package:

- `{{VERSION}}` — the bare release version, e.g. `0.1.1` (no leading `v`).
  It is used in asset names (`argent_{{VERSION}}_windows_amd64.zip`) and in the
  release tag (`v{{VERSION}}`).
- `{{SHA256}}` — the lowercase hex SHA-256 of the Windows zip. Take it from the
  `checksums.txt` line whose second column is
  `argent_<version>_windows_amd64.zip` (the file `scripts/build-release.ps1`
  writes into `dist/release/`).

Example substitution for a `v0.1.1` release:

- URL:
  `https://github.com/2118505078xyh1/argentgo/releases/download/v0.1.1/argent_0.1.1_windows_amd64.zip`
- `{{VERSION}}` -> `0.1.1`
- `{{SHA256}}` -> the first column of the matching `checksums.txt` line

`scripts/build-release.ps1` prints the zip name and writes both the zip and
`dist/release/checksums.txt`. Read the bare version from its
`argent_<version>_windows_amd64.zip` output (it mirrors `cli.Version` in
`internal/cli/cli.go`), and read the hash from the matching `checksums.txt`
line; do not invent either value.

### How a maintainer fills each file

1. Run `powershell -File scripts/build-release.ps1` (or take the already-published
   release assets). Note the version and the `checksums.txt` hash for the
   Windows amd64 zip.
2. **Scoop** — replace `{{VERSION}}` and `{{SHA256}}` in
   `packaging/scoop/argentgo.json`. The `hash` field is the value from `checksums.txt`,
   so Scoop verifies the download. The manifest also carries `checkver` /
   `autoupdate` blocks: Scoop's own `checkver` reads the GitHub releases (no
   project-hosted catalog needed), and `autoupdate` rewrites the URL and pulls
   the new hash from `checksums.txt` via regex. JSON cannot contain comments,
   so this explanation lives here in this guide.
3. **Chocolatey** — replace `{{VERSION}}` in `packaging/choco/argentgo.nuspec` and both
   `{{VERSION}}` / `{{SHA256}}` in `packaging/choco/tools/chocolateyinstall.ps1`, then run
   `choco pack`. The install script passes the hash from `checksums.txt` to
   `Install-ChocolateyZipPackage` as `checksum64` / `checksumType64`, so a
   tampered download fails. The package has no dependencies.
4. **Homebrew** — replace `{{VERSION}}` in `packaging/homebrew/argentgo.rb` and publish to
   a tap. `go install github.com/2118505078xyh1/argentgo/cmd/argent@{{VERSION}}`
   requires a version the Go toolchain accepts; release tags are `v`-prefixed,
   so fill that occurrence with the tag form (e.g. `v0.1.1`) if your tag is
   `v0.1.1`. The `url` field already points at that tag's source archive.

### Privacy note

These recipes reference only the public repository and public release assets.
No API keys, tokens (`sk-`, `ghp_`, ...), e-mail addresses or private/local
filesystem paths are embedded. Keep it that way: every value is either a public
URL or a release-derived version/hash.

### Validation

After substituting placeholders, validate before publishing:

```bash
python -m json.tool packaging/scoop/argentgo.json >/dev/null
python -c "import xml.etree.ElementTree as ET; ET.parse('packaging/choco/argentgo.nuspec')"
ruby -c packaging/homebrew/argentgo.rb
```
