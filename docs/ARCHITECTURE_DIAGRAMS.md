# Architecture diagrams

Companion to [ARCHITECTURE.md](ARCHITECTURE.md). The diagrams below are Mermaid
(fences are plain text) and mirror what the code and the `internal/archguard`
test actually enforce. They are a map, not a second specification: when a diagram
and the source disagree, the source wins.

## 1. Package layers and dependency rules

`internal/archguard` parses every `.go` import and checks: stdlib-only core, no
cgo, one-way layering, and a single third-party exception in `native/`.

```mermaid
flowchart TB
  subgraph surfaces["Surfaces"]
    CLI["cmd/argent + internal/cli<br/>flags and dispatch"]
    TUI["internal/tui<br/>ANSI full screen"]
    SRV["internal/server<br/>serve / rpc / acp"]
    NAT["native/<br/>MyGo desktop UI<br/>ONLY third-party dep: egoist/mygo"]
  end

  subgraph wirer["Wiring — the only package allowed to import everything"]
    APP["internal/app<br/>Resolve, Execute, adapters"]
  end

  subgraph loop["Agent layer"]
    AGENT["internal/agent<br/>model + tool loop"]
    TOOLS["internal/tools<br/>registry + built-ins"]
  end

  subgraph wire["Wire layer"]
    LLM["internal/llm<br/>chat / anthropic / responses"]
  end

  subgraph core["Core — stdlib only, no cgo"]
    SEC["security / session"]
    BASE["config / i18n / memory / hooks"]
    UTIL["proc / pty / jobs / skills / plugins / lsp / mcp"]
  end

  CLI --> APP
  TUI --> APP
  SRV --> APP
  APP --> AGENT
  APP --> TOOLS
  APP --> LLM
  AGENT --> LLM
  AGENT -. "interfaces only<br/>ToolRunner / Chatter / Recorder" .-> TOOLS
  TOOLS --> LLM
  AGENT --> SEC
  AGENT --> BASE
  APP --> CORE_LINK["all core packages"]
  CORE_LINK --> SEC
  CORE_LINK --> BASE
  CORE_LINK --> UTIL
  NAT -. "spawns argent --rpc<br/>no engine import" .-> SRV

  X1["FORBIDDEN: tools to agent"]:::bad
  X2["FORBIDDEN: agent to app"]:::bad
  X3["FORBIDDEN: llm to agent / tools / app"]:::bad
  X4["FORBIDDEN: third-party import inside internal/**"]:::bad

  classDef bad fill:#ffe6e6,stroke:#cc0000,color:#900;
```

Notes: `internal/tools` never imports `internal/agent`; `internal/agent` never
imports `internal/app`; `internal/llm` imports neither the loop nor tools;
`internal/app` is the sole wirer. `native/` is the only module that may depend on
`github.com/egoist/mygo` and reaches the engine over stdio JSON-RPC instead of
linking it.

## 2. Request flow — prompt to events

One turn, from any surface, through the single loop. `app.Execute` injects every
collaborator; the transports and TUI are interchangeable.

```mermaid
sequenceDiagram
  participant U as User / Editor
  participant F as cli / tui / native
  participant A as app.Execute
  participant G as agent.Runner
  participant L as llm Chatter
  participant T as tools.Toolset
  participant C as security.Chain
  participant S as session JSONL

  U->>F: prompt text
  F->>A: Prompt with resolved options
  A->>S: open session, register sync hooks
  A->>G: Run with injected interfaces
  loop each model turn
    G->>L: Chat messages + tool specs
    L-->>G: streamed text deltas and tool calls
    G-->>F: agent.Event text_delta
    loop each tool call
      G->>T: Run name and args
      T->>C: guard Check policy then approval then audit
      C-->>T: allow or StopError
      T-->>G: tool output
      G-->>F: agent.Event tool_start / tool_end
    end
    G->>S: Recorder Append turn and tool records
  end
  G->>S: Append final turn record
  G-->>A: Result text and usage
  A-->>F: final answer
  F-->>U: text chunks, SSE frames or prompt/delta
```

`--serve` emits `data:` SSE frames, `--rpc` emits `prompt/delta` notifications
plus the final `{"text":...}` result, and `acp` emits `session/update`. All three
forward the same streamed text supplied by the loop's `onText` callback.

## 3. Session tree, compaction and checkpoints

A session is one append-only JSONL file. Records carry `id` / `parent`, so a
conversation is the path from root to the active leaf and forks never rewrite
history.

```mermaid
flowchart LR
  H["header record"] --> M1["user + assistant + tool records<br/>linked by id / parent"]
  M1 --> LEAF["active leaf<br/>last record in file order"]

  M1 --> C["compaction record<br/>summary + dropped + keep<br/>files / failures / nextSteps"]
  C --> KEEP["kept suffix<br/>tool-pair safe cut"]

  M1 --> CP["checkpoint record<br/>pre-turn bytes + sha256<br/>parent = leaf before turn"]
  CP --> UNDO["undo: restore files<br/>refuse if changed since<br/>restore guarded by security chain"]
  CP --> FORK["retry: fork a new attempt<br/>record from checkpoint parent"]
  FORK --> LEAF2["new active leaf"]
```

`Compact(keep)` and `CompactBudget(budget)` both append a `compaction` record;
`adjustCut` keeps a tool call and its result on the same side. Checkpoints are
captured before a mutating batch and are recorded through the same
policy → approval → audit chain as a tool call.

## 4. LSP activation — lazy spawn and cached clients

Nothing is spawned at startup. The six `lsp_*` tools are always advertised; the
app installs an adapter, and a language server is only launched on the first
tool call for a given root and language.

```mermaid
flowchart TD
  CALL["model calls an lsp_* tool<br/>for example lsp_symbols"] --> AD{"adapter wired?<br/>ARGENT_LSP not 0"}
  AD -- no --> NA["tool reports<br/>not available, no failure"]
  AD -- yes --> PATH["adapter.clientForPath<br/>resolve path against root"]
  PATH --> REG["lsp.Registry.ForPath<br/>map extension to server"]
  REG -- none --> ERR["unsupported extension error"]
  REG -- found --> MGR["process-wide lspManager.clientFor<br/>key = root plus server name"]
  MGR --> Q{"cached client?"}
  Q -- yes --> REUSE["reuse client"]
  Q -- no --> Q2{"spawn in flight?"}
  Q2 -- yes --> WAIT["wait on per-key barrier, recheck"]
  WAIT --> Q
  Q2 -- no --> SPAWN["lsp.Start with 15s handshake timeout"]
  SPAWN -- fails --> NEG["cache failure<br/>no retry on every call"]
  SPAWN -- ok --> CACHE["cache client by key"]
  REUSE --> DTO["map lsp types to tools DTOs<br/>10s call timeout"]
  CACHE --> DTO
  DTO --> OUT["edits returned as text<br/>never applied by preview tools"]
```

The manager is a process-level singleton (`sync.Once`), keyed by workspace root
plus server name, so clients survive across runs and multiple write roots.
Built-in mappings cover gopls, rust-analyzer, TypeScript and pyright; a missing
binary degrades to an actionable error rather than a stall.

## 5. Distribution and install flow (Windows)

```mermaid
flowchart TB
  SRC["source tree<br/>cmd/argent, native/<br/>stdlib core + mygo in native only"]
  VER["internal/cli/cli.go<br/>Version is the single source of truth"]
  BUILD["scripts/build-release.ps1<br/>CGO_ENABLED=0, GOOS=windows,<br/>GOARCH=amd64, -trimpath"]
  BIN["argent.exe + argent-native.exe<br/>GUI subsystem build"]
  ZIP["argent_VERSION_windows_amd64.zip"]
  SUMS["checksums.txt + per-zip .sha256<br/>SHA-256"]
  PUB["scripts/publish-win.ps1<br/>clean main, consistency check, push tag"]
  CI[".github/workflows/release.yml<br/>ubuntu-latest + pwsh"]
  REL["GitHub Release with assets<br/>public releases page"]
  INST["scripts/install-win.ps1<br/>default LOCALAPPDATA/argentgo/bin"]
  GOI["go install .../cmd/argent@latest<br/>CLI only, any host with Go"]

  SRC --> VER --> BUILD --> BIN
  BIN --> ZIP --> SUMS --> PUB
  PUB --> CI --> REL
  REL --> INST
  REL --> GOI
  INST --> BIN
```

Windows/amd64 is the only published binary target. The tag alone is not a
release: the publish script waits for a published Release carrying both the zip
and the checksum manifest. `go install` produces only the CLI/engine; the desktop
client is built from `./native` and ships in the same zip as `argent.exe`.
