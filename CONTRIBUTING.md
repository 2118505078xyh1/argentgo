# Contributing to argentgo

Thanks for your interest in improving argentgo. This project is a personal,
stdlib-only Go coding-agent harness with a native MyGo desktop client. We
welcome bug reports, feature proposals, documentation fixes and code PRs.

## Ground rules

- **Core stays stdlib-only.** `internal/{agent,session,llm,tools,security,...}`
  must not gain third-party dependencies. The only exception is `native/`,
  which uses `github.com/egoist/mygo` for the desktop UI.
- **No cgo.** Everything must cross-compile with `GOOS=linux go build ./...`.
- **Windows host** is the release target; Linux cross-build must stay green.
- **No secrets, tokens, personal emails or local paths** in commits, issues,
  PRs or release notes.
- Be respectful. See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

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
