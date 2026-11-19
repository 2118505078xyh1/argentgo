# argent-go

Personal coding-agent harness in Go. Rewrite of `argent` (Rust) with the same
shape: stream, run tools, append to a session log, repeat.

Slice 1 (this commit):

- OpenAI-compatible `/chat/completions` with SSE streaming and tool calls
- agent loop with turn limit and tool-result truncation
- tools: `read`, `write`, `edit`, `bash`, `ls`, `grep`, `find`
- workspace root check for every path; mutating tools need `--yolo`
- JSONL session log under `~/.argent-go/sessions`
- stdlib only, no third-party modules

## Build and run

```bash
go build ./cmd/argent
./argent -m gpt-4o-mini "read go.mod and summarise it"
```

Configuration (first match wins):

| Setting | Flag | Env | Default |
|---|---|---|---|
| base URL | `--base-url` | `ARGENT_BASE_URL`, `OPENAI_BASE_URL` | `https://api.openai.com/v1` |
| API key | `--api-key` | `ARGENT_API_KEY`, `OPENAI_API_KEY` | empty |
| model | `-m` | `ARGENT_MODEL` | `gpt-4o-mini` |

Local llama.cpp works the same way:

```bash
./argent --base-url http://127.0.0.1:8080/v1 -m spark-4b "hello"
```

## Flags

| Flag | Meaning |
|---|---|
| `--yolo` | allow mutating tools (write/edit/bash) |
| `--max-turns N` | stop after N model turns (default 12) |
| `--mode json` | emit JSONL events instead of plain text |
| `--cwd DIR` | workspace root (default: current directory) |

## Status

First slice only: OpenAI-compatible wire protocol, no Anthropic client yet, no
approval prompt, no plugins/MCP. Tests cover the loop and the filesystem tools.
