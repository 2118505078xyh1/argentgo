# Installing argentgo

argentgo ships two Windows binaries from the same release:

- `argent.exe` — the CLI, TUI and agent engine (built from `./cmd/argent`).
- `argent-native.exe` — the MyGo desktop chat window (built from `./native`).

The desktop client **stays on the latest release**: keep both binaries from the
same zip so their versions match. There is no installer service, telemetry or
background process.

All downloads and release notes are on the public GitHub page:
<https://github.com/2118505078xyh1/argentgo/releases>.

## Requirements

| Install method | Requirement |
|---|---|
| Prebuilt zip (recommended) | Windows amd64; PowerShell (or Git Bash for `sha256sum`). No runtime needed. |
| `go install` from source | Go 1.27.3 or newer (pinned as `go 1.27.3` in `go.mod`), network access to fetch the module. Builds entirely from source. |
| Native desktop from source | A local checkout, Go 1.27.3+, and Windows (the desktop client is Windows-only). |

## Prebuilt binary (Windows, quickest path)

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

## PowerShell one-liner installer

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

## From source (`go install`)

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

## Building the native desktop frontend from source

From a local checkout:

```powershell
go build -o argent-native.exe ./native
```

`native/` is the single place that depends on the MyGo module
(`github.com/egoist/mygo`, already pinned in `go.mod`); the engine core stays
stdlib-only and cgo-free. `argent gui` launches this binary from the engine's
directory and drives it over the existing `--rpc` transport. The desktop client
is Windows-only. See [docs/DISTRIBUTION.md](DISTRIBUTION.md) for the two-binary
architecture.

## Updating

- **Prebuilt:** download the latest zip and `checksums.txt`, verify, and replace
  **both** binaries together. Re-running the installer updates `argent.exe`
  only, so copy the matching `argent-native.exe` as well.
- **From source:** re-run
  `go install github.com/2118505078xyh1/argentgo/cmd/argent@latest` and rebuild
  `./native` from the updated checkout.

## Uninstalling

```powershell
powershell -ExecutionPolicy Bypass -File scripts/uninstall-win.ps1 `
    [-InstallDir "$env:LOCALAPPDATA\argentgo\bin"]
```

The script removes only the installed `argent.exe` (and an empty install
directory). User data under `~/.argentgo` — sessions, config, hooks and audit
logs — is preserved. For manual/zip installs, delete the extracted directory;
for `go install`, delete the binary from `$(go env GOPATH)/bin`. Remove
`argent-native.exe` separately if you installed the desktop frontend.
