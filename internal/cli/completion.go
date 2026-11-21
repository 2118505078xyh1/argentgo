package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// runCompletion 实现 `completion bash|zsh|powershell`。flag 和
// 命令列表来自实时解析器，因此新 flag 一经注册就会立即被补全。
func runCompletion(app App, _ context.Context, args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		completionUsage(app.IO.Out)
		return 0
	}
	if len(args) != 1 {
		completionUsage(app.IO.Err)
		return 2
	}
	flags := strings.Join(withPrefix(flagSurface(), "--"), " ")
	commands := strings.Join(subcommandNames(), " ")
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "bash":
		fmt.Fprintf(app.IO.Out, bashCompletion, flags, commands)
	case "zsh":
		fmt.Fprintf(app.IO.Out, zshCompletion, flags, commands)
	case "powershell", "pwsh":
		fmt.Fprintf(app.IO.Out, powershellCompletion, flags, commands)
	default:
		fmt.Fprintf(app.IO.Err, "argentgo: unknown shell %q (want bash, zsh or powershell)\n", args[0])
		completionUsage(app.IO.Err)
		return 2
	}
	return 0
}

func withPrefix(values []string, prefix string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, prefix+value)
	}
	return out
}

const bashCompletion = `_argentgo_complete() {
  local cur="${COMP_WORDS[COMP_CWORD]}"
  local flags="%s"
  local commands="%s"
  if [[ "$cur" == -* ]]; then
    COMPREPLY=( $(compgen -W "$flags" -- "$cur") )
    return
  fi
  COMPREPLY=( $(compgen -W "$commands" -- "$cur") )
}
complete -F _argentgo_complete argentgo
`

const zshCompletion = `#compdef argentgo
_argentgo() {
  local -a flags commands
  flags=(%s)
  commands=(%s)
  if [[ "$words[CURRENT]" == -* ]]; then
    compadd -- $flags
  else
    compadd -- $commands
  fi
}
_argentgo "$@"
`

const powershellCompletion = `Register-ArgumentCompleter -Native -CommandName argentgo -ScriptBlock {
  param($wordToComplete, $commandAst, $cursorPosition)
  $flags = '%s'.Split(' ')
  $commands = '%s'.Split(' ')
  ($flags + $commands) | Where-Object { $_ -like "$wordToComplete*" } | ForEach-Object {
    [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
  }
}
`

func completionUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo completion bash|zsh|powershell

Prints a completion script for the full flag surface and every subcommand.
Example: argentgo completion bash > ~/.local/share/bash-completion/completions/argentgo
`)
}
