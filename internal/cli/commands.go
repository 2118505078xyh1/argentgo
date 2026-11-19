package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/2118505078xyh1/argentgo/internal/commands"
)

// runCommands implements `commands [--json] [-C root]`.
func runCommands(app App, _ context.Context, args []string) int {
	cwd, rest, err := splitRoot(args)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argent:", err)
		return 2
	}
	asJSON := false
	for _, arg := range rest {
		switch {
		case arg == "-h" || arg == "--help":
			commandsUsage(app.IO.Out)
			return 0
		case arg == "--json":
			asJSON = true
		default:
			fmt.Fprintf(app.IO.Err, "argent: unexpected argument %q\n", arg)
			commandsUsage(app.IO.Err)
			return 2
		}
	}
	root, err := workspaceRoot(app, cwd)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	set := commands.Discover(commands.DefaultSources(root, homeFromEnv(app.env)))
	list := set.List()
	if asJSON {
		data, err := json.MarshalIndent(list, "", "  ")
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintln(app.IO.Out, string(data))
		return 0
	}
	if len(list) == 0 {
		fmt.Fprintln(app.IO.Out, "no slash commands found under .argent/commands or ~/.argentgo/commands")
		return 0
	}
	for _, command := range list {
		hint := command.ArgumentHint
		if hint == "" {
			hint = "-"
		}
		fmt.Fprintf(app.IO.Out, "/%s\t%s\t%s\t%s\n", command.Name, hint, command.Description, command.Path)
	}
	return 0
}

func commandsUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent commands [--json] [-C <dir>]

Lists the markdown slash commands discovered under .argent/commands and
~/.argentgo/commands. Plugin-provided commands are namespaced plugin:name.
`)
}
