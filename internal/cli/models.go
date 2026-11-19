package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/provider"
)

// runModels implements `models [--json]`: the provider template table, without
// touching the network.
func runModels(app App, _ context.Context, args []string) int {
	asJSON := false
	for _, arg := range args {
		switch {
		case arg == "-h" || arg == "--help":
			modelsUsage(app.IO.Out)
			return 0
		case arg == "--json":
			asJSON = true
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(app.IO.Err, "argent: unknown flag %q\n", arg)
			modelsUsage(app.IO.Err)
			return 2
		default:
			fmt.Fprintf(app.IO.Err, "argent: unexpected argument %q\n", arg)
			modelsUsage(app.IO.Err)
			return 2
		}
	}

	templates := provider.List()
	if asJSON {
		data, err := json.MarshalIndent(templates, "", "  ")
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintln(app.IO.Out, string(data))
		return 0
	}
	fmt.Fprintf(app.IO.Out, "%-12s %-24s %-38s %s\n", "provider", "protocol", "base URL", "default model")
	for _, template := range templates {
		fmt.Fprintf(app.IO.Out, "%-12s %-24s %-38s %s\n",
			template.Name, template.Protocol, template.BaseURL, template.DefaultModel)
	}
	return 0
}

func modelsUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent models [--json]

Lists the built-in provider templates: the id to pass to --provider, the wire
protocol, the API root and the default model. Credentials are read from each
provider's API key environment variable.
`)
}
