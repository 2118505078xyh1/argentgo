package cli

import (
	"fmt"
	"io"
)

// writeUsage 把命令 Usage 打印到 w。
func writeUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo [flags] <prompt>
       argentgo [flags] -            (prompt from stdin)
       argentgo <command> [args]

commands:
  sessions list|show|fidelity|checkout|fork-at|list-messages|health|tree|rm|find|discard-attempt  inspect stored sessions (tree: ASCII id/parent tree, marks active leaf)
    sessions undo <id> [--steps N] [-C <dir>]  revert the last N checkpoints in one atomic undo (default 1)
    sessions retry <id> [--steps N] [checkpoint-id]  fork at the N-th most recent checkpoint (default 1)
  replay <id>                   print a stored session transcript (--verify: structural diagnostics)
  usage                         summarise turns and tokens per session
  context                       approximate token breakdown of the model context
  doctor                        check config, storage, provider, git and Go
  login <provider>              store an API key in the config file
  auth                          show stored credential status
  logout [provider]             remove stored credentials
  models                        list provider templates
  mcp                           list configured MCP servers
  opt-ins                       list all opt-in switches and their current values
  skills                        list discovered skills
  commands                      list discovered slash commands
  hooks                         list hook documents and trust state
  policy                        show sandbox, approval and policy
  audit                         tail the audit log
  update                        check GitHub releases for a newer build
  completion bash|zsh|powershell  print a shell completion script
  tui                           run the full-screen terminal UI
  bug                           write a diagnostics dump
  eval <file>                   run scripted JSON eval scenarios
  config get|set                read or write dotted config keys
  version [--verbose]           print version or detailed build info
   workflow list|run|show       inspect and run workflow definitions
   telemetry status|off         report the personal-build telemetry preference
   attach <id> [-n N]           print the last N lines of a job log
   logs [<id>]                  list background jobs and their logs
   jobs                         alias for logs
   follow <id> [-n N]           tail a job log until interrupted
   stop <id>                    kill a background job
   board                        read-only view of jobs and automations

flags:
  -m, --model <id>              model id (default gpt-4o-mini)
      --provider <name>         provider template id (see `+"`argentgo models`"+`)
      --profile <name>          named config profile
      --list-profiles           list config profiles and exit
      --base-url <url>          provider base URL
      --api-key <key>           provider API key
      --protocol <name>         anthropic | openai | responses (default openai)
      --yolo                    auto-approve every tool call
      --yes                     auto-confirm prompts in non-interactive environments
      --max-turns <n>           maximum model turns (default 100)
      --max-parallel <n>        read-only tool workers per batch (0 default, 1 serial)
      --max-tokens <n>          stop after this many billable tokens
      --max-cost <usd>          stop after this estimated dollar spend
      --max-seconds <n>         stop after this many wall-clock seconds
      --price-input <usd>       USD per million prompt tokens
      --price-output <usd>      USD per million completion tokens
      --fallback-model <id>     model to retry once with when the primary fails
      --secondary-model <id>    model for delegated subagent tasks
      --thinking <level>        reasoning effort hint (low | medium | high | xhigh; 或 低/中/高/超高)
      --mode <mode>             text | json (default text)
      --json-lines              one-shot strict JSONL on stdout
      --tokens                  print the request payload breakdown and exit
  -C, --cwd <dir>               workspace root (default: current directory)
      --add-dir <dir>           extra writable workspace directory (repeatable)
      --sandbox-mode <mode>     read-only | workspace-write | danger-full-access
      --sandbox <mode>          alias for --sandbox-mode
      --approval <mode>         ask | never | auto (default ask)
      --trust-project           trust project-local resources for this run
      --session <id>            resume the session with this id
      --session-id <id>         alias for --session
      --at <entry>              resume --session at this entry id
      --resume                  resume the most recent session
      --undo                    restore files from the latest checkpoint and exit
      --retry                   fork the session at its latest checkpoint
      --title <text>            override the generated session title
      --continue                alias for --resume
      --continue-session        alias for --resume
      --list-sessions           list stored sessions and exit
      --list-models             list provider templates and exit
      --output-schema <file>    JSON Schema path, or inline schema text
      --plugins <name>          load this plugin by name (repeatable, `+"`all`"+`)
      --no-auto-compact         disable automatic compaction for this run
      --no-prompt-cache         disable provider prompt-cache markers for this run
      --no-color                disable ANSI colours
      --theme <name>            UI theme name
      --locale <locale>         output locale
      --tui                     run the full-screen terminal UI
      --worktree[=name]         run in a git worktree (not migrated)
      --background              run in the background (not migrated)
      --enable-browser          enable the browser tool (not migrated)
      --enable-desktop          enable desktop control (not migrated)
      --enable-service          enable the pty service tool (not migrated)
      --workflow-mode <mode>    dynamic workflow mode (not migrated)
      --workflow-resume         resume a workflow journal (not migrated)
      --workflow-journal <path> workflow journal path (not migrated)
      --gui                     launch the desktop window (not migrated)
  -c, --config <key=value>      config override, dotted keys nest (repeatable)
      --serve                   serve HTTP + SSE (/v1/prompt, /v1/abort, /v1/health)
      --serve-address <host>    HTTP bind address (default 127.0.0.1)
      --serve-port <port>       HTTP bind port (default 7777)
      --serve-token <token>     bearer token; mandatory for --serve
      --token <token>           alias for --serve-token
      --rpc                     newline-delimited JSON-RPC on stdio
      --acp                     ACP JSON-RPC on stdio
  -V, --version                 print version and exit
  -h, --help                    print this help
`)
}
