# Package-manager recipes

This directory contains community package-manager **recipes** for installing
argentgo. They are templates, not published artifacts: the project does **not**
maintain or host a catalog entry in Scoop, Chocolatey or Homebrew. A maintainer
(or downstream packager) copies a recipe into the relevant bucket / package
repo / tap and fills in the placeholders at release time. Nothing here depends
on a hosted catalog owned by this project.

Public repository: <https://github.com/2118505078xyh1/argentgo>

## Files

| File | Package manager | Platform | What it installs |
| --- | --- | --- | --- |
| `scoop/argentgo.json` | Scoop | Windows (amd64) | Downloads and unzips the release zip; shims `argent.exe` and `argent-native.exe`. |
| `choco/argentgo.nuspec` | Chocolatey | Windows (amd64) | NuGet package metadata (dependency-free); the version must match the release. |
| `choco/tools/chocolateyinstall.ps1` | Chocolatey | Windows (amd64) | Downloads the release zip with `Install-ChocolateyZipPackage` and verifies its SHA-256. |
| `homebrew/argentgo.rb` | Homebrew | macOS / Linux | Builds the CLI/TUI from source with `go install`. The native desktop GUI is Windows-only and is not included. |

All recipes use only public GitHub Releases URLs. There are no credentials,
tokens or private paths.

## Placeholder convention

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

## How a maintainer fills each file

1. Run `powershell -File scripts/build-release.ps1` (or take the already-published
   release assets). Note the version and the `checksums.txt` hash for the
   Windows amd64 zip.
2. **Scoop** — replace `{{VERSION}}` and `{{SHA256}}` in
   `scoop/argentgo.json`. The `hash` field is the value from `checksums.txt`, so
   Scoop verifies the download. The manifest also carries `checkver` /
   `autoupdate` blocks: Scoop's own `checkver` reads the GitHub releases (no
   project-hosted catalog needed), and `autoupdate` rewrites the URL and pulls
   the new hash from `checksums.txt` via regex. JSON cannot contain comments,
   so this explanation lives here in the README.
3. **Chocolatey** — replace `{{VERSION}}` in `argentgo.nuspec` and both
   `{{VERSION}}` / `{{SHA256}}` in `choco/tools/chocolateyinstall.ps1`, then run
   `choco pack`. The install script passes the hash from `checksums.txt` to
   `Install-ChocolateyZipPackage` as `checksum64` / `checksumType64`, so a
   tampered download fails. The package has no dependencies.
4. **Homebrew** — replace `{{VERSION}}` in `homebrew/argentgo.rb` and publish to
   a tap. `go install github.com/2118505078xyh1/argentgo/cmd/argent@{{VERSION}}`
   requires a version the Go toolchain accepts; release tags are `v`-prefixed,
   so fill that occurrence with the tag form (e.g. `v0.1.1`) if your tag is
   `v0.1.1`. The `url` field already points at that tag's source archive.

## Privacy note

These recipes reference only the public repository and public release assets.
No API keys, tokens (`sk-`, `ghp_`, ...), e-mail addresses or private/local
filesystem paths are embedded. Keep it that way: every value is either a public
URL or a release-derived version/hash.

## Validation

After substituting placeholders, validate before publishing:

```bash
python -m json.tool packaging/scoop/argentgo.json >/dev/null
python -c "import xml.etree.ElementTree as ET; ET.parse('packaging/choco/argentgo.nuspec')"
ruby -c packaging/homebrew/argentgo.rb
```
