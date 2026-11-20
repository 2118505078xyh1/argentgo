# Release workflow validation (local harness)

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

## How to run

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

## What the harness validates

### 1. Static structure of `release.yml` (parsed as text)

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

### 2. Local artifact verification

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

### 3. Optional YAML syntax check

If `python` is on `PATH`, the harness additionally tries
`yaml.safe_load` on `release.yml` and reports the result. A missing Python or
missing PyYAML is reported as `SKIP`, never `FAIL` — the textual assertions are
authoritative. A genuine parse error is reported as a failure.

## What only a real tag push can validate

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

## Release job graph

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

## Privacy and safety

- No secrets, tokens, credentials or private filesystem paths are embedded in
  the harness or this document. The token appears only as the literal YAML
  placeholder `${{ secrets.GITHUB_TOKEN }}` used for the assertion, and its
  value is never read or printed.
- The harness never contacts the network and never calls `gh`.
- It writes only to a uniquely named temp directory and deletes it in
  `finally`; it does not touch `dist/` or the working tree.

## Failure triage

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
