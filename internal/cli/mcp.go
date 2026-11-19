package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/2118505078xyh1/argent-go/internal/config"
)

// mcpServerView is one configured MCP server, as printed by `argent mcp`.
type mcpServerView struct {
	Name      string   `json:"name"`
	Transport string   `json:"transport"`
	Command   string   `json:"command,omitempty"`
	Args      []string `json:"args,omitempty"`
	URL       string   `json:"url,omitempty"`
}

// runMCP implements `mcp [--json] [-C root]`. It reads the merged config only;
// no server is contacted.
func runMCP(app App, _ context.Context, args []string) int {
	cwd, rest, err := splitRoot(args)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argent:", err)
		return 2
	}
	asJSON := false
	for _, arg := range rest {
		switch {
		case arg == "-h" || arg == "--help":
			mcpUsage(app.IO.Out)
			return 0
		case arg == "--json":
			asJSON = true
		default:
			fmt.Fprintf(app.IO.Err, "argent: unexpected argument %q\n", arg)
			mcpUsage(app.IO.Err)
			return 2
		}
	}
	root, err := workspaceRoot(app, cwd)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	cfg, err := config.Load(root, app.env)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	servers := mcpServersFromConfig(cfg.Raw["mcpServers"])

	if asJSON {
		data, err := json.MarshalIndent(servers, "", "  ")
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintln(app.IO.Out, string(data))
		return 0
	}
	if len(servers) == 0 {
		sources := strings.Join(cfg.Sources, ", ")
		if sources == "" {
			sources = "the default config"
		}
		fmt.Fprintf(app.IO.Out, "no MCP servers configured (sources: %s)\n", sources)
		return 0
	}
	fmt.Fprintf(app.IO.Out, "%-16s %-8s %s\n", "server", "transport", "target")
	for _, server := range servers {
		target := server.URL
		if target == "" {
			target = server.Command
			if len(server.Args) > 0 {
				target += " " + strings.Join(server.Args, " ")
			}
		}
		fmt.Fprintf(app.IO.Out, "%-16s %-8s %s\n", server.Name, server.Transport, target)
	}
	return 0
}

// mcpServersFromConfig accepts the `mcpServers` object shape and a list form.
func mcpServersFromConfig(raw any) []mcpServerView {
	var servers []mcpServerView
	add := func(name string, body map[string]any) {
		if strings.TrimSpace(name) == "" {
			return
		}
		view := mcpServerView{Name: name}
		view.Command, _ = body["command"].(string)
		view.URL, _ = body["url"].(string)
		view.Transport, _ = body["transport"].(string)
		for _, arg := range listOfStrings(body["args"]) {
			view.Args = append(view.Args, arg)
		}
		if view.Transport == "" {
			if view.Command != "" {
				view.Transport = "stdio"
			} else {
				view.Transport = "http"
			}
		}
		servers = append(servers, view)
	}

	switch value := raw.(type) {
	case map[string]any:
		for name, entry := range value {
			if body, ok := entry.(map[string]any); ok {
				add(name, body)
			}
		}
	case []any:
		for index, entry := range value {
			body, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			name, _ := body["name"].(string)
			if name == "" {
				name = fmt.Sprintf("server-%d", index+1)
			}
			add(name, body)
		}
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	return servers
}

func listOfStrings(raw any) []string {
	switch value := raw.(type) {
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	case []string:
		return append([]string(nil), value...)
	}
	return nil
}

func mcpUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent mcp [--json] [-C <dir>]

Lists the MCP servers declared under `+"`mcpServers`"+` in the merged config. No
server is contacted; use the agent itself to connect.
`)
}
