## What does this PR do?

A concise description of the change and why it is needed.

## Related issue

Fixes #___   (or "No issue — small cleanup")

## Type of change

- [ ] Bug fix
- [ ] New feature
- [ ] Documentation
- [ ] Refactor / test / tooling

## Verification

- [ ] `go build ./...`
- [ ] `GOOS=linux go build ./...` (cross-build)
- [ ] `go vet ./...`
- [ ] `gofmt -l .` prints nothing
- [ ] `go test ./... -count=1`
- [ ] `go build -o argent-native.exe ./native` (if the desktop client changed)

Describe any manual verification (e.g. ran `argent --version`, clicked through
the native window, checked a checkpoint round-trip).

## Constraints checklist

- [ ] No new dependency in the core packages (stdlib-only)
- [ ] No cgo; cross-build passes
- [ ] No secrets, tokens, personal emails or local paths added
- [ ] Tests added at the boundary changed

## Notes for reviewers

Anything that needs special attention, known limitations, or follow-up work.
Maintainers may ask for changes, split the PR, or decline it if it conflicts
with the single-binary / offline-first design.
