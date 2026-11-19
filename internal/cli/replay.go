package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/2118505078xyh1/argent-go/internal/server"
)

// runReplay implements `replay [id] [--json]`. With no id it replays the most
// recently updated session.
func runReplay(app App, _ context.Context, args []string) int {
	id := ""
	asJSON := false
	for _, arg := range args {
		switch {
		case arg == "-h" || arg == "--help":
			replayUsage(app.IO.Out)
			return 0
		case arg == "--json":
			asJSON = true
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(app.IO.Err, "argent: unknown flag %q\n", arg)
			replayUsage(app.IO.Err)
			return 2
		default:
			if id != "" {
				fmt.Fprintf(app.IO.Err, "argent: unexpected argument %q\n", arg)
				replayUsage(app.IO.Err)
				return 2
			}
			id = arg
		}
	}

	store := app.sessionStore()
	title := ""
	if id == "" {
		infos, err := store.List()
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		if len(infos) == 0 {
			fmt.Fprintln(app.IO.Out, "no sessions recorded")
			return 0
		}
		sort.SliceStable(infos, func(i, j int) bool { return infos[i].Updated.After(infos[j].Updated) })
		id, title = infos[0].ID, infos[0].Title
	}
	events, err := store.Load(id)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if asJSON {
		for _, event := range events {
			line, err := json.Marshal(event)
			if err != nil {
				fmt.Fprintln(app.IO.Err, "error:", err)
				return 1
			}
			fmt.Fprintln(app.IO.Out, string(line))
		}
		return 0
	}
	fmt.Fprintf(app.IO.Out, "== %s %s ==\n", id, title)
	for _, event := range events {
		if line := replayLine(event); line != "" {
			fmt.Fprintln(app.IO.Out, line)
		}
	}
	return 0
}

// replayLine renders one session record as a transcript line.
func replayLine(event server.SessionEvent) string {
	payload := map[string]any{}
	if len(event.Data) > 0 {
		_ = json.Unmarshal(event.Data, &payload)
	}
	text := firstText(payload, "text", "prompt", "content", "message")
	switch event.Kind {
	case "turn":
		return "" // accounting only
	case "tool":
		name := firstText(payload, "name", "tool")
		output := firstText(payload, "output", "result")
		if text == "" {
			text = output
		}
		if name == "" {
			name = "tool"
		}
		return "tool " + name + "> " + clipTranscript(text)
	case "summary", "compaction":
		return "summary> " + clipTranscript(text)
	}
	if text == "" {
		return ""
	}
	role := event.Kind
	if role == "" || role == "message" {
		if value, ok := payload["role"].(string); ok && value != "" {
			role = value
		}
	}
	return role + "> " + clipTranscript(text)
}

func firstText(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func clipTranscript(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > 500 {
		return text[:499] + "…"
	}
	return text
}

func replayUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent replay [<id>] [--json]

Prints a stored session transcript. Without an id the most recently updated
session is replayed. --json prints the raw record lines instead.
`)
}
