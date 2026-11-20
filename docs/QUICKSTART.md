# Quickstart (Windows, ~5 minutes)

This is the language-neutral, step-by-step path from "downloaded" to "first useful
answer". All commands are Windows/PowerShell unless noted. See
[INSTALL.md](INSTALL.md) for the full installation guide and
[API_REFERENCE.md](API_REFERENCE.md) for the complete command surface.

> `<your-key>` below is a placeholder. Replace it with your own API key and never
> commit it to version control.

## 1. Install

Fastest path — the prebuilt Windows zip:

1. Download `argent_<version>_windows_amd64.zip` and `checksums.txt` from the
   [latest release](https://github.com/2118505078xyh1/argentgo/releases/latest).
2. Verify the SHA-256 manifest (Git Bash: `sha256sum -c checksums.txt`; PowerShell:
   compare `Get-FileHash` output with `checksums.txt`).
3. Extract and smoke-test:

   ```powershell
   Expand-Archive .\argent_<version>_windows_amd64.zip -DestinationPath .
   .\argent.exe --version
   ```

Keep `argent.exe` and `argent-native.exe` in the **same directory** — `argent gui`
looks for the native binary beside the engine. The desktop client stays on the
latest release, so always replace both binaries together. From source, use
`go install github.com/2118505078xyh1/argentgo/cmd/argent@latest` (Go 1.27.3+).

## 2. Configure a provider

`argent login <provider>` stores a key in the config file
(`~/.argentgo/config.json`) and never echoes it back:

```powershell
argent login openai --api-key <your-key>   # explicit key
argent login openai                        # prompts on stdin, without echo
argent auth                                # show stored credential status
```

The key is read from `--api-key`, `<PROVIDER>_API_KEY`, `ARGENT_API_KEY`, or from
the interactive prompt. You can also skip `login` and set an environment
variable for the current shell:

```powershell
$env:OPENAI_API_KEY = "<your-key>"
```

Run `argent models` to list the built-in provider templates (`openai`,
`anthropic`, `deepseek`, `qwen`, …). Use `argent logout [provider]` to remove
stored credentials.

## 3. First prompt in the TUI

Start the full-screen terminal UI:

```powershell
argent tui
```

> `argent` with no prompt reads stdin and exits; it does not auto-open the TUI.
> Use `argent tui` (or `argent --tui`) for the interactive UI. For a one-shot
> answer, run `argent "your task"`.

Type a task and press **Enter** to send. Useful keys:

| Key | Action |
|---|---|
| `Enter` | send |
| `Alt+Enter` / `Ctrl+J` | newline |
| `PgUp` / `PgDn`, `Shift+Up` / `Shift+Down` | scroll |
| `Ctrl+C` | cancel the current turn; press again to quit |
| `/quit` or `/exit` | quit |

Type `/help` in the input box to display the in-app key list.

## 4. Native desktop window

```powershell
argent gui
```

This launches `argent-native.exe` from the engine's directory. The native client
drives the engine over the built-in `--rpc` transport — no browser, no WebView,
no HTTP service. It is Windows-only and requires `argent-native.exe` beside
`argent.exe`. See [FRONTEND.md](FRONTEND.md) and [DISTRIBUTION.md](DISTRIBUTION.md).

## 5. Switch model or provider

```powershell
argent models                                   # list templates
argent --provider deepseek "帮我写个函数"        # use a built-in template
argent -m gpt-4o "explain this diff"             # -m / --model overrides the model
```

Other useful switches: `--protocol anthropic|openai|responses`, `--base-url <url>`,
`--profile <name>`, and `-c/--config key=value` for dotted config overrides.
Settings resolve from flags → `ARGENT_*` environment → config files → defaults.

## 6. Approvals and sandbox mode

```powershell
argent --approval ask   "refactor this"   # default: prompt for approval
argent --approval auto  "refactor this"   # auto-approve every request
argent --approval never "refactor this"   # deny every approval request
argent --sandbox-mode read-only "audit this dir"
```

When an approval prompt appears: **y** allow, **n** deny, **a** allow for this
session, **esc** deny. `--yolo` is a shortcut for `auto`. Sandbox modes are
`read-only` (mutating tools removed), `workspace-write` (default, workspace
boundary enforced) and `danger-full-access` (boundary removed). Approval and
policy decisions are recorded in a redacted audit log under `~/.argentgo/audit/`.

## 7. Slash commands

Verified built-in TUI commands: `/help`, `/clear`, `/expand`, `/undo`, `/retry`,
`/name <title>`, `/quit` (alias `/exit`). `/undo` and `/retry` operate on the
latest SHA-256 checkpoint.

Markdown slash commands are also discovered from `.argent/commands` (project) and
`~/.argentgo/commands` (user); `argent commands` lists them. Their bodies expand
`$ARGUMENTS` / `$@` and `$1`…`$N`.

> There is **no** built-in `/compact` command. Context compaction is automatic;
> disable it for one run with `--no-auto-compact`.

## 8. Troubleshooting

```powershell
argent doctor                          # config, storage, provider, git, Go
argent doctor --base-url <url>         # probe a specific provider endpoint
argent bug                             # write a diagnostics dump
```

`doctor` prints `ok` / `warn` / `fail` per check. For install problems, re-read
[INSTALL.md](INSTALL.md); for exact flags and return values, see
[API_REFERENCE.md](API_REFERENCE.md).
