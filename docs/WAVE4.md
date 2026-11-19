# Wave 4 — eight parallel slices

Same hard rules as `MIGRATION.md`: stdlib only for Go (no third-party modules,
no cgo), no git commands, gofmt, tests beside source, **stay inside your dirs**.

| # | Slice | Directories (exclusive) |
|---|---|---|
| 1 | app flag semantics | `internal/app`, `internal/security` |
| 2 | TUI theme / colour | `internal/tui`, `internal/i18n` |
| 3 | fallback / secondary model | `internal/agent` |
| 4 | remaining subcommands | `internal/cli` |
| 5 | GUI backend | `internal/gui` |
| 6 | UI transport bridge | `ui/`, `scripts/` |
| 7 | browser app-window launcher | `internal/launcher` |
| 8 | documentation | `README.md`, `docs/` |

Contracts shared between slices:

```go
// internal/launcher (slice 7)
func Open(ctx context.Context, url, profileDir string) error // Edge/Chrome --app, fallback browser
func Name() string                                            // "edge" | "chrome" | "browser"

// internal/gui (slice 5)
type Options struct {
    Dir, Version, Model, Provider, Protocol, Locale, StorageDir string
    OnReady func(url string)
    NewRunner func() Runner
}
func Run(ctx context.Context, opts Options) error
```

Slice 4 does **not** implement the `gui` subcommand — the integrator adds it
once slices 4, 5 and 7 land. Slice 6 does not touch `internal/gui`; it only
rewrites the frontend transport.

Verification contract: every slice runs `go vet` and `go test` on **its own
packages only**. The integrator runs the full suite once all eight report.
