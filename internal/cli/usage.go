package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// sessionUsage is one session's accounted turns and tokens.
type sessionUsage struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	Turns            int    `json:"turns"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	ToolCalls        int    `json:"tool_calls"`
}

// runUsage implements `usage [--json]`: it sums the turn records the agent
// appended to each session file.
func runUsage(app App, _ context.Context, args []string) int {
	asJSON := false
	for _, arg := range args {
		switch {
		case arg == "-h" || arg == "--help":
			usageUsage(app.IO.Out)
			return 0
		case arg == "--json":
			asJSON = true
		default:
			fmt.Fprintf(app.IO.Err, "argent: unexpected argument %q\n", arg)
			usageUsage(app.IO.Err)
			return 2
		}
	}

	store := app.sessionStore()
	infos, err := store.List()
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	sortSessions(infos)

	rows := make([]sessionUsage, 0, len(infos))
	total := sessionUsage{ID: "total", Title: fmt.Sprintf("%d sessions", len(infos))}
	for _, info := range infos {
		events, err := store.Load(info.ID)
		if err != nil {
			fmt.Fprintf(app.IO.Err, "warning: %s: %v\n", info.ID, err)
			continue
		}
		row := sessionUsage{ID: info.ID, Title: info.Title}
		for _, event := range events {
			switch event.Kind {
			case "turn":
				var payload struct {
					Turns int `json:"turns"`
					Usage *struct {
						PromptTokens     int `json:"prompt_tokens"`
						CompletionTokens int `json:"completion_tokens"`
					} `json:"usage"`
				}
				if err := json.Unmarshal(event.Data, &payload); err != nil {
					continue
				}
				if payload.Turns > 0 {
					row.Turns += payload.Turns
				} else {
					row.Turns++
				}
				if payload.Usage != nil {
					row.PromptTokens += payload.Usage.PromptTokens
					row.CompletionTokens += payload.Usage.CompletionTokens
				}
			case "tool":
				row.ToolCalls++
			}
		}
		rows = append(rows, row)
		total.Turns += row.Turns
		total.PromptTokens += row.PromptTokens
		total.CompletionTokens += row.CompletionTokens
		total.ToolCalls += row.ToolCalls
	}

	if asJSON {
		data, err := json.MarshalIndent(map[string]any{"sessions": rows, "total": total}, "", "  ")
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintln(app.IO.Out, string(data))
		return 0
	}
	if len(rows) == 0 {
		fmt.Fprintln(app.IO.Out, "no sessions recorded")
		return 0
	}
	fmt.Fprintf(app.IO.Out, "%-24s %-6s %-10s %-10s %s\n", "session", "turns", "prompt", "completion", "tools")
	for _, row := range rows {
		fmt.Fprintf(app.IO.Out, "%-24s %-6d %-10d %-10d %d\n",
			clipSessionID(row.ID), row.Turns, row.PromptTokens, row.CompletionTokens, row.ToolCalls)
	}
	fmt.Fprintf(app.IO.Out, "%-24s %-6d %-10d %-10d %d\n",
		"total", total.Turns, total.PromptTokens, total.CompletionTokens, total.ToolCalls)
	return 0
}

func clipSessionID(id string) string {
	if len(id) <= 23 {
		return id
	}
	return id[:20] + "..."
}

func usageUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent usage [--json]

Summarises stored sessions: model turns, prompt and completion tokens, and tool
calls, read from the "turn" records each run appends.
`)
}
