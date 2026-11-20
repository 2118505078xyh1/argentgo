# archguard

Architecture dependency-guard test for `argentgo`. It turns the dependency
rules in the repository-level `AGENTS.md` into an automated check so a bad
import is caught by `go test` instead of by code review.

## What it enforces

The test (`arch_test.go`) discovers the module path by reading `go.mod` (it is
never hardcoded), then walks every `.go` file under `internal/` and `native/`,
parsing imports with `go/parser` (`parser.ImportsOnly`). It asserts:

- **Stdlib-only core.** No file under `internal/**` may import a third-party
  module — an import whose first path segment contains a `.` (`github.com/...`,
  `golang.org/...`, `gopkg.in/...`). Imports of this repository's own module
  (`<module>/...`) are intra-repo references, not third party, and are allowed.
- **No cgo.** No file anywhere uses `import "C"`, so `GOOS=linux go build ./...`
  keeps working.
- **Layering (internal packages are libraries).**
  - `internal/tools` must not import `internal/agent`.
  - `internal/agent` must not import `internal/app` (`app` is the top-level
    wirer and is the only package allowed to import everything).
  - `internal/llm` is the wire layer and must not import `internal/agent`,
    `internal/tools`, or `internal/app`.
- **Single external exception.** `native/` is the only place allowed to use a
  third-party module, and that module is `github.com/egoist/mygo` (including
  its sub-packages such as `mygo/ui`). Any other external import in `native/`
  fails the test.

Notes on strictness / allow-list:

- All `.go` files are checked, including `_test.go`, platform/build-tag
  variants (`_windows.go`, `_linux.go`, …), and generated/mock sources. There
  are no silent exceptions.
- The archguard test itself imports only the standard library.
- The test is stdlib-only, uses no cgo, adds no module, and never compiles the
  packages it inspects — it only parses imports, so it is safe on any host and
  for cross-compilation.

## Why

`AGENTS.md` states these rules in prose. An executable guard prevents drift:
accidentally pulling e.g. a UUID or CLI library into `internal/`, adding cgo,
or letting the wire layer depend on the agent loop will break the build/test
immediately, preserving a dependency-free, portable core.

## Run

```powershell
$env:Path = "$env:USERPROFILE\.tools\go\go\bin;$env:Path"

go test ./internal/archguard -count=1   # architecture guard
go vet ./internal/archguard
gofmt -l internal/archguard             # must print nothing
```
