package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/skills"
)

// runSkills 实现 `skills [--json] [-C root]`。
func runSkills(app App, _ context.Context, args []string) int {
	cwd, rest, err := splitRoot(args)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argent:", err)
		return 2
	}
	asJSON := false
	for _, arg := range rest {
		switch {
		case arg == "-h" || arg == "--help":
			skillsUsage(app.IO.Out)
			return 0
		case arg == "--json":
			asJSON = true
		default:
			fmt.Fprintf(app.IO.Err, "argent: unexpected argument %q\n", arg)
			skillsUsage(app.IO.Err)
			return 2
		}
	}
	root, err := workspaceRoot(app, cwd)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	registry := skills.Discover(skills.DefaultRoots(root, homeFromEnv(app.env)))
	all := registry.All()
	if asJSON {
		entries := make([]skills.Entry, 0, len(all))
		for _, skill := range all {
			entries = append(entries, skill.Entry())
		}
		data, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintln(app.IO.Out, string(data))
		return 0
	}
	if len(all) == 0 {
		fmt.Fprintf(app.IO.Out, "no skills found under %s\n", strings.Join(skills.DefaultRoots(root, homeFromEnv(app.env)), ", "))
		return 0
	}
	for _, skill := range all {
		fmt.Fprintf(app.IO.Out, "%s\t%s\t%s\n", skill.Name, skill.Description, skill.Path)
	}
	return 0
}

func skillsUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent skills [--json] [-C <dir>]

Lists the SKILL.md documents discovered under .argent/skills and
~/.argentgo/skills. The same registry backs the use_skill tool.
`)
}
