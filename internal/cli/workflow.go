package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/workflow"
)

// runWorkflow implements `workflow <list|run|show>`. Definitions are JSON files
// under <cwd>/.argent/workflows or <storage>/workflows; `run` also accepts a
// direct path. Every prompt step is driven by the injected App.NewRunner.
func runWorkflow(app App, ctx context.Context, args []string) int {
	if len(args) == 0 {
		workflowUsage(app.IO.Err)
		return 2
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "-h", "--help":
		workflowUsage(app.IO.Out)
		return 0
	case "list":
		return workflowList(app, args[1:])
	case "show":
		return workflowShow(app, args[1:])
	case "run":
		return workflowRun(app, ctx, args[1:])
	default:
		fmt.Fprintf(app.IO.Err, "argent: unknown workflow action %q\n", args[0])
		workflowUsage(app.IO.Err)
		return 2
	}
}

type workflowEntry struct {
	path string
	def  *workflow.Definition
}

// workflowDirs is where definitions are discovered: the workspace first, then
// the user-level storage directory.
func workflowDirs(app App, cwd string) []string {
	dirs := make([]string, 0, 2)
	if root, err := workspaceRoot(app, cwd); err == nil {
		dirs = append(dirs, filepath.Join(root, ".argent", "workflows"))
	}
	dirs = append(dirs, filepath.Join(app.storageDir(), "workflows"))
	return dirs
}

func workflowEntries(app App, cwd string) ([]workflowEntry, error) {
	var out []workflowEntry
	seen := map[string]bool{}
	for _, dir := range workflowDirs(app, cwd) {
		items, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("workflow: read %s: %w", dir, err)
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Name() < items[j].Name() })
		for _, item := range items {
			if item.IsDir() || !strings.HasSuffix(strings.ToLower(item.Name()), ".json") {
				continue
			}
			path := filepath.Join(dir, item.Name())
			key := filepath.Clean(path)
			if seen[key] {
				continue
			}
			seen[key] = true
			def, err := workflow.LoadDefinition(path)
			if err != nil {
				return nil, err
			}
			out = append(out, workflowEntry{path: path, def: def})
		}
	}
	return out, nil
}

func workflowList(app App, args []string) int {
	cwd, rest, err := splitRoot(args)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argent:", err)
		return 2
	}
	asJSON := false
	for _, arg := range rest {
		switch arg {
		case "--json":
			asJSON = true
		default:
			fmt.Fprintf(app.IO.Err, "argent: unexpected argument %q\n", arg)
			workflowUsage(app.IO.Err)
			return 2
		}
	}
	entries, err := workflowEntries(app, cwd)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if asJSON {
		type row struct {
			Name  string `json:"name"`
			Steps int    `json:"steps"`
			Path  string `json:"path"`
		}
		rows := make([]row, 0, len(entries))
		for _, entry := range entries {
			rows = append(rows, row{Name: entry.def.Name, Steps: len(entry.def.Steps), Path: entry.path})
		}
		data, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			fmt.Fprintln(app.IO.Err, "error:", err)
			return 1
		}
		fmt.Fprintln(app.IO.Out, string(data))
		return 0
	}
	if len(entries) == 0 {
		fmt.Fprintln(app.IO.Out, "no workflow definitions found under .argent/workflows or "+filepath.Join(app.storageDir(), "workflows"))
		return 0
	}
	for _, entry := range entries {
		fmt.Fprintf(app.IO.Out, "%s\t%d steps\t%s\n", entry.def.Name, len(entry.def.Steps), entry.path)
	}
	return 0
}

func workflowShow(app App, args []string) int {
	cwd, rest, err := splitRoot(args)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argent:", err)
		return 2
	}
	if len(rest) != 1 {
		workflowUsage(app.IO.Err)
		return 2
	}
	target := rest[0]
	entries, err := workflowEntries(app, cwd)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	var found *workflowEntry
	if _, statErr := os.Stat(target); statErr == nil {
		def, loadErr := workflow.LoadDefinition(target)
		if loadErr != nil {
			fmt.Fprintln(app.IO.Err, "error:", loadErr)
			return 1
		}
		found = &workflowEntry{path: target, def: def}
	} else {
		for i := range entries {
			name := strings.TrimSuffix(filepath.Base(entries[i].path), ".json")
			if entries[i].def.Name == target || name == target {
				found = &entries[i]
				break
			}
		}
	}
	if found == nil {
		fmt.Fprintf(app.IO.Err, "error: no workflow named %q\n", target)
		return 1
	}
	fmt.Fprintf(app.IO.Out, "workflow %s (%s)\n", found.def.Name, found.path)
	for i, step := range found.def.Steps {
		action := step.Prompt
		if step.Tool != "" {
			action = "tool " + step.Tool + " " + step.Args
		}
		fmt.Fprintf(app.IO.Out, "  %d. %s\t%s\n", i+1, step.Name, strings.TrimSpace(action))
	}
	return 0
}

// runnerPrompter adapts the injected Runner to workflow.Prompter. The workflow
// package only needs the final answer; streamed chunks are intentionally
// discarded so one step produces one result line.
type runnerPrompter struct{ runner Runner }

func (p runnerPrompter) Prompt(ctx context.Context, text string) (string, error) {
	return p.runner.Prompt(ctx, text, nil)
}

func workflowRun(app App, ctx context.Context, args []string) int {
	cwd, rest, err := splitRoot(args)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "argent:", err)
		return 2
	}
	path := ""
	input := ""
	values := make(map[string]string)
	for i := 0; i < len(rest); i++ {
		switch {
		case rest[i] == "--input":
			if i+1 >= len(rest) {
				fmt.Fprintln(app.IO.Err, "argent: --input needs a value")
				return 2
			}
			i++
			input = rest[i]
		case strings.HasPrefix(rest[i], "--input="):
			input = strings.TrimPrefix(rest[i], "--input=")
		case rest[i] == "--param":
			if i+1 >= len(rest) {
				fmt.Fprintln(app.IO.Err, "argent: --param needs a key=value value")
				return 2
			}
			i++
			key, value, ok := parseWorkflowParam(app, rest[i])
			if !ok {
				return 2
			}
			values[key] = value
		case strings.HasPrefix(rest[i], "--param="):
			key, value, ok := parseWorkflowParam(app, strings.TrimPrefix(rest[i], "--param="))
			if !ok {
				return 2
			}
			values[key] = value
		case strings.HasPrefix(rest[i], "-"):
			fmt.Fprintf(app.IO.Err, "argent: unexpected argument %q\n", rest[i])
			workflowUsage(app.IO.Err)
			return 2
		default:
			switch {
			case path == "":
				path = rest[i]
			case input == "":
				input = rest[i]
			default:
				input += " " + rest[i]
			}
		}
	}
	if path == "" {
		fmt.Fprintln(app.IO.Err, "error: workflow run needs a definition JSON path")
		return 2
	}
	def, err := workflow.LoadDefinition(path)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	// Resolve before building a runner: a missing required value blocks the
	// run here, and defaults/value caps follow the workflow package's rules.
	params, err := workflow.ResolveParameters(*def, values)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	root, err := workspaceRoot(app, cwd)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	runner, err := app.newRunner(&Options{Cwd: root, Mode: ModeText, MaxTurns: 12})
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	executor := &workflow.Executor{Prompter: runnerPrompter{runner: runner}}
	run, err := executor.RunWithParams(ctx, def, input, params)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	for _, step := range run.Steps {
		switch step.Status {
		case workflow.StatusOK:
			fmt.Fprintf(app.IO.Out, "ok   %s\t%s\n", step.Name, step.Output)
		case workflow.StatusSkipped:
			fmt.Fprintf(app.IO.Out, "skip %s\t%s\n", step.Name, step.Error)
		default:
			fmt.Fprintf(app.IO.Out, "FAIL %s\t%s\n", step.Name, step.Error)
		}
	}
	fmt.Fprintf(app.IO.Out, "workflow %s: %s\n", run.Name, run.Status)
	if !run.Succeeded() {
		return 1
	}
	return 0
}

// parseWorkflowParam splits one --param token into a declaration key and a
// value. Empty values are legal: ResolveParameters turns them into the default
// or a missing-required error. Keys are trimmed just like declaration keys.
func parseWorkflowParam(app App, token string) (key, value string, ok bool) {
	key, value, found := strings.Cut(token, "=")
	key = strings.TrimSpace(key)
	if !found || key == "" {
		fmt.Fprintf(app.IO.Err, "argent: --param needs key=value (got %q)\n", token)
		return "", "", false
	}
	return key, value, true
}

func workflowUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent workflow list|show|run ...

  workflow list [-C dir] [--json]        list workflow JSON definitions
  workflow show <name|path> [-C dir]     print the steps of one definition
  workflow run <file.json> [--input s] [--param key=value]
                                         execute a definition with the agent runner
`)
}
