# LSP integration

`internal/lsp` is a stdlib-only JSON-RPC client that speaks LSP over stdio to
external language-server processes. No language server is bundled and no Go
module is added for one: `gopls`, `rust-analyzer`, `typescript-language-server`
and `pyright-langserver` are ordinary programs found on `PATH` (see
`DefaultServers()` in `internal/lsp/servers.go`). A missing server never breaks
startup — it fails at first use with an actionable message.

## Implemented methods

The client in `internal/lsp` implements the following LSP requests. Every
`...At` helper is a convenience wrapper that opens the file first and then
calls the URI-based method.

| Method | Client helper(s) | File |
| --- | --- | --- |
| `initialize` / `initialized` + server→client request answers | `Start` / `initialize` / `answer` | `client.go` |
| `textDocument/didOpen`, `didChange`, `didSave`, `didClose` | `Open`, `Refresh`, `DidOpen`, `DidChange`, `DidSave`, `DidClose` | `documents.go` |
| `textDocument/publishDiagnostics` | `Diagnostics`, `DiagnosticsAll` | `documents.go` |
| `textDocument/definition` | `Definition`, `DefinitionAt` | `features.go` |
| `textDocument/hover` | `Hover`, `HoverAt` | `features.go` |
| `textDocument/documentSymbol` | `DocumentSymbol`, `DocumentSymbolAt` | `features_ide.go` |
| `textDocument/references` | `References`, `ReferencesAt` | `features_ide.go` |
| `textDocument/codeAction` | `CodeActions`, `CodeActionsAt` | `features_ide.go` |
| `textDocument/rename` | `Rename`, `RenameAt` | `features_ide.go` |
| `textDocument/formatting` | `Formatting`, `FormattingAt` | `features_ide.go` |
| `workspace/symbol` | `WorkspaceSymbols` | `features_ide.go` |
| `textDocument/semanticTokens/full` | `SemanticTokens`, `SemanticTokensAt` | `features_advanced.go` |
| `textDocument/inlayHint` | `InlayHints`, `InlayHintsAt` | `features_advanced.go` |
| `textDocument/codeLens`, `codeLens/resolve` | `CodeLens`, `CodeLensAt`, `ResolveCodeLens` | `features_advanced.go` |

The parsers deliberately accept the several wire shapes a method may return
(a single `Location` vs. an array, `Location` vs. `LocationLink`, `DocumentSymbol`
trees vs. flat `SymbolInformation`, markup as a string / `MarkupContent` / an
array), so results are normalized instead of silently dropped.

## Activation in the app (lazy, on-demand)

`internal/app/lsp_adapter.go` is the only bridge between the dormant client and
the six `lsp_*` tools in `internal/tools` (`tools` never imports `lsp`).

- **Nothing is spawned at startup or at toolset build time.** Wiring the
  adapter only makes the tools *callable*. A language server process is launched
  on the **first** `lsp_*` call that resolves to a file, using
  `Registry.ForPath(path)` to pick the server by file extension.
- One process-scoped `lspManager` (`sync.Once`) caches clients by
  `root + "|" + server.Name`, reuses them across runs, and uses a per-key
  barrier so concurrent calls spawn at most one process per key. A failed spawn
  is negatively cached (no retry storm); unsupported extensions return a clear
  error.
- The initialize handshake is bounded (~15s) and each request is bounded
  (~10s), so a broken server degrades to a tool error rather than hanging the
  run.

**Kill switch:** set `ARGENT_LSP=0` and `NewLspIdeAdapter` returns `nil`; the
`lsp_*` tools keep reporting "LSP IDE integration is not available" and no
process is ever spawned. Any other value (or unset) activates the adapter.

## Running the real-server integration test

`internal/lsp/gopls_integration_test.go` drives an **actual gopls** process over
stdio. It builds a throwaway Go module with a struct, a const, two functions and
a call site, then exercises definition, document symbols, references, hover,
workspace symbols and formatting.

```powershell
# needs gopls on PATH, or at %USERPROFILE%\go\bin\gopls.exe
$env:Path = "$env:USERPROFILE\.tools\go\go\bin;$env:Path"
go test ./internal/lsp -run TestIntegrationGopls -count=1 -v
```

- **Skips gracefully** when gopls is absent (not on `PATH` and no
  `%USERPROFILE%\go\bin\gopls.exe`), so CI without gopls stays green. It also
  skips under `go test -short`, and skips (rather than fails) if gopls is too
  slow to start or resolve the definition — this test is optional.
- The test locates gopls, prepends its directory to `PATH` with `t.Setenv` so
  `exec.Command("gopls")` in `lsp.Start` finds it, and sets `GOFLAGS=-mod=mod`
  and `GOTOOLCHAIN=local` to prevent network/toolchain downloads.
- A plain `go test ./internal/lsp` also runs it automatically whenever gopls is
  installed; a normal run completes in about one second on a warm cache.

## What is verified vs. best-effort

The test divides its assertions into two tiers so an optional external tool
cannot make the suite flaky:

- **Verified (hard assertions).** Once gopls answers, the test requires correct
  results: `DefinitionAt` at the call site must resolve to `main.go` at the
  declaration line (a location in the wrong file is a real finding reported
  with `t.Errorf`); `DocumentSymbolAt` must contain `RunService`, `Service`,
  `AppName`, `Help` and `main`; `ReferencesAt` must return at least the
  declaration and the call site. Diagnostics for the valid fixture must publish
  (its wait is best-effort, but a clean file is logged, not asserted on).
- **Best-effort (reported, never fails).** Hover content, `workspace/symbol`
  results and formatting edits are logged. Empty hover, a filtered workspace
  query, or zero formatting edits (the fixture is already gofmt-formatted) are
  valid outcomes and produce only `t.Log` output.

This distinction matters: environment failures skip; a successful response that
is clearly wrong is a genuine bug in the client and fails the test.

## Limitations

- **Advanced features depend on server capabilities.** Semantic tokens, inlay
  hints and code lenses are implemented and advertised in `initialize`, but
  whether they return anything is entirely up to the server's support and
  settings. The client keeps the raw delta-encoded semantic-token stream and
  leaves decoding to the UI. `ResolveCodeLens` cannot resolve lenses from
  servers that require opaque `data` to be echoed back, because the client does
  not retain that field.
- **No DAP, no tree-sitter, by design.** Debug Adapter Protocol and embedded
  syntax-tree parsing are explicitly out of scope (see `docs/NEXT_STEPS.md`);
  this project keeps the native shell dependency-free and drives everything
  over stdlib JSON-RPC.
- **Server is an external tool.** gopls (and the other default servers) are not
  installed or vendored by this repository. Version drift between servers can
  change the exact hover text, symbol names or formatting output; the hard
  assertions above are chosen to be stable across gopls versions.
