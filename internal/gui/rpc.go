package gui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
)

// unsupported explains, per command, why the Go build cannot answer it yet.
// Unknown commands get their own message; neither case ever hangs the page.
var unsupported = map[string]string{
	"undo":              "the checkpoint store is not wired yet",
	"retry":             "the checkpoint store is not wired yet",
	"pty_spawn":         "the terminal needs a pty host, which is not wired yet",
	"pty_write":         "the terminal needs a pty host, which is not wired yet",
	"pty_read":          "the terminal needs a pty host, which is not wired yet",
	"pty_kill":          "the terminal needs a pty host, which is not wired yet",
	"pty_list":          "the terminal needs a pty host, which is not wired yet",
	"list_jobs":         "the background job registry is not wired yet",
	"start_job":         "the background job registry is not wired yet",
	"job_tail":          "the background job registry is not wired yet",
	"kill_job":          "the background job registry is not wired yet",
	"list_memory":       "the memory store is not wired yet",
	"add_memory":        "the memory store is not wired yet",
	"forget_memory":     "the memory store is not wired yet",
	"search_memory":     "the memory store is not wired yet",
	"list_automations":  "the automation scheduler is not wired yet",
	"create_automation": "the automation scheduler is not wired yet",
	"toggle_automation": "the automation scheduler is not wired yet",
	"delete_automation": "the automation scheduler is not wired yet",
	"run_automation":    "the automation scheduler is not wired yet",
	"automation_log":    "the automation scheduler is not wired yet",
	"preview_schedule":  "the automation scheduler is not wired yet",
	"usage_summary":     "the usage ledger is not wired yet",
}

// unsupportedError keeps the original per-command message for nil backends.
func unsupportedError(name string) error {
	reason := unsupported[name]
	if reason == "" {
		reason = "no backend is configured"
	}
	return fmt.Errorf("not supported in the Go build: %s", reason)
}

// memoryStore and usageStore fall back to the stdlib defaults under StorageDir.
// The injected value always wins, so an integrator can replace either store.
func (b *bridge) memoryStore() Memory {
	if b.opts.Memory != nil {
		return b.opts.Memory
	}
	return NewFileMemory(b.configRoot())
}

func (b *bridge) usageStore() Usage {
	if b.opts.Usage != nil {
		return b.opts.Usage
	}
	return NewFileUsage(b.configRoot())
}

// workflowStore and documentStore fall back to stdlib workspace scanners, just
// like memory and usage fall back to file stores under the config root.
func (b *bridge) workflowStore() Workflows {
	if b.opts.Workflows != nil {
		return b.opts.Workflows
	}
	return NewFileWorkflows(b.dir)
}

func (b *bridge) documentStore() Documents {
	if b.opts.Documents != nil {
		return b.opts.Documents
	}
	return NewFileDocuments(b.dir)
}

// args is the decoded JSON body of one rpc call.
type args map[string]json.RawMessage

func parseArgs(body []byte) (args, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return args{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var parsed args
	if err := decoder.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if parsed == nil {
		parsed = args{}
	}
	return parsed, nil
}

func (a args) str(key string) string {
	raw, ok := a[key]
	if !ok {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

func (a args) boolean(key string) bool {
	raw, ok := a[key]
	if !ok {
		return false
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	return value
}

func (a args) integer(key string) int64 {
	raw, ok := a[key]
	if !ok {
		return 0
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		if value, err := number.Int64(); err == nil {
			return value
		}
		if value, err := number.Float64(); err == nil {
			return int64(value)
		}
	}
	var float float64
	if err := json.Unmarshal(raw, &float); err != nil {
		return 0
	}
	return int64(float)
}

// rpc answers POST /rpc/{command}: Tauri's invoke() with a different carrier.
func (b *bridge) rpc(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/rpc/")
	if name == "" || strings.Contains(name, "/") {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown command"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read body: " + err.Error()})
		return
	}
	arguments, err := parseArgs(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	result, err := b.dispatch(r.Context(), name, arguments)
	if err != nil {
		// Tauri rejected the invoke promise with the error string; the page's
		// bridge turns this body back into a rejected promise.
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// dispatch is the command table. Every command returns either a JSON result or
// an error the page shows in its error bar.
func (b *bridge) dispatch(ctx context.Context, name string, a args) (any, error) {
	switch name {
	case "get_state":
		return b.getState(), nil
	case "new_tab":
		return b.startTab()
	case "activate_tab":
		return nil, b.activateTab(a.str("sid"))
	case "close_tab":
		return nil, b.closeTab(a.str("sid"))
	case "send_prompt":
		return nil, b.command(a.str("sid"), Command{Kind: CommandPrompt, Text: a.str("text")})
	case "steer":
		text := a.str("text")
		if strings.TrimSpace(text) == "" {
			return nil, errors.New("steering text is empty")
		}
		return nil, b.command(a.str("sid"), Command{Kind: CommandSteer, Text: text})
	case "pause_session":
		return nil, b.command(a.str("sid"), Command{Kind: CommandPause, Paused: true})
	case "resume_session":
		return nil, b.command(a.str("sid"), Command{Kind: CommandResume, Paused: false})
	case "abort":
		return nil, b.command(a.str("sid"), Command{Kind: CommandAbort})
	case "new_session":
		return nil, b.command(a.str("sid"), Command{Kind: CommandNewSession})
	case "open_session":
		return nil, b.command(a.str("sid"), Command{Kind: CommandOpenSession, Path: a.str("path")})
	case "rename_session":
		return nil, b.command(a.str("sid"), Command{
			Kind:  CommandRenameSession,
			Path:  a.str("path"),
			Title: a.str("title"),
		})
	case "delete_session":
		return nil, b.command(a.str("sid"), Command{Kind: CommandDeleteSession, Path: a.str("path")})
	case "compact":
		return nil, b.command(a.str("sid"), Command{Kind: CommandCompact})
	case "rewind":
		return nil, b.command(a.str("sid"), Command{Kind: CommandRewind, Steps: int(a.integer("steps"))})
	case "undo":
		if b.opts.History == nil {
			return nil, unsupportedError("undo")
		}
		return nil, b.opts.History.Undo(ctx, a.str("sid"), int(a.integer("steps")))
	case "retry":
		if b.opts.History == nil {
			return nil, unsupportedError("retry")
		}
		return nil, b.opts.History.Retry(ctx, a.str("sid"), int(a.integer("steps")))
	case "set_model":
		return nil, b.command(a.str("sid"), Command{Kind: CommandSetModel, Model: a.str("model")})
	case "set_thinking":
		return nil, b.command(a.str("sid"), Command{Kind: CommandSetThinking, Level: a.str("level")})
	case "answer_approval":
		t, err := b.tab(a.str("sid"))
		if err != nil {
			return nil, err
		}
		return nil, t.answer(uint64(a.integer("id")), a.boolean("allow"))
	case "allow_tool":
		t, err := b.tab(a.str("sid"))
		if err != nil {
			return nil, err
		}
		t.allow(a.str("name"))
		return nil, nil
	case "list_files":
		return listFiles(b.dir, a.str("query")), nil
	case "search_sessions":
		return b.searchSessions(a.str("query"), int(a.integer("limit")))
	case "import_session_text":
		if b.opts.Sessions == nil {
			return nil, fmt.Errorf("not supported in the Go build: no session store configured")
		}
		file, err := b.opts.Sessions.Import(a.str("text"), a.str("title"))
		if err != nil {
			return nil, err
		}
		return file, nil
	case "get_telemetry_preferences":
		return b.telemetryPreferences(), nil
	case "set_telemetry_preferences":
		return nil, b.setTelemetryPreferences(a.boolean("enabled"), a.boolean("consent"))
	case "git_status":
		return b.gitStatus(ctx)
	case "git_diff":
		return b.gitDiff(ctx, a.str("path"))
	case "list_workspace_files":
		return b.listWorkspaceFiles(), nil
	case "read_workspace_file":
		return b.readWorkspaceFile(a.str("path"))
	case "write_workspace_file":
		return nil, b.writeWorkspaceFile(a.str("path"), a.str("content"))
	case "delete_workspace_file":
		return nil, b.deleteWorkspaceFile(a.str("path"))
	case "upload_workspace_file":
		return b.uploadWorkspaceFile(a.str("name"), a.str("base64Data"))
	case "list_mcp_servers":
		return b.listMCPServers(), nil
	case "list_plugins":
		return b.listPlugins(), nil

	// stdlib-backed defaults; never "not supported" even with no injection.
	case "list_workflows":
		return b.workflowStore().List()
	case "list_documents":
		return b.documentStore().List()
	case "read_document":
		return b.documentStore().Read(a.str("path"))
	case "list_memory":
		return b.memoryStore().List()
	case "add_memory":
		return nil, b.memoryStore().Add(a.str("scope"), a.str("key"), a.str("value"))
	case "forget_memory":
		return nil, b.memoryStore().Forget(a.str("scope"), a.str("key"))
	case "search_memory":
		return b.memoryStore().Search(a.str("query"))
	case "usage_summary":
		return b.usageStore().Summary()

	// injected-only backends: nil returns a clear error, never a fake success.
	case "list_jobs":
		if b.opts.Jobs == nil {
			return nil, unsupportedError("list_jobs")
		}
		return b.opts.Jobs.List()
	case "start_job":
		if b.opts.Jobs == nil {
			return nil, unsupportedError("start_job")
		}
		return b.opts.Jobs.Start(a.str("command"))
	case "job_tail":
		if b.opts.Jobs == nil {
			return nil, unsupportedError("job_tail")
		}
		return b.opts.Jobs.Tail(a.str("id"), int(a.integer("lines")))
	case "kill_job":
		if b.opts.Jobs == nil {
			return nil, unsupportedError("kill_job")
		}
		return nil, b.opts.Jobs.Stop(a.str("id"))

	case "list_automations":
		if b.opts.Automation == nil {
			return nil, unsupportedError("list_automations")
		}
		return b.opts.Automation.List()
	case "create_automation":
		if b.opts.Automation == nil {
			return nil, unsupportedError("create_automation")
		}
		return nil, b.opts.Automation.Create(a.str("name"), a.str("schedule"), a.str("prompt"))
	case "toggle_automation":
		if b.opts.Automation == nil {
			return nil, unsupportedError("toggle_automation")
		}
		return nil, b.opts.Automation.Toggle(a.str("id"))
	case "delete_automation":
		if b.opts.Automation == nil {
			return nil, unsupportedError("delete_automation")
		}
		return nil, b.opts.Automation.Delete(a.str("id"))
	case "run_automation":
		if b.opts.Automation == nil {
			return nil, unsupportedError("run_automation")
		}
		return nil, b.opts.Automation.Run(a.str("id"))
	case "automation_log":
		if b.opts.Automation == nil {
			return nil, unsupportedError("automation_log")
		}
		return b.opts.Automation.Log(a.str("id"))
	case "preview_schedule":
		if b.opts.Automation == nil {
			return nil, unsupportedError("preview_schedule")
		}
		return b.opts.Automation.Preview(a.str("schedule"))

	case "pty_spawn":
		if b.opts.PTY == nil {
			return nil, unsupportedError("pty_spawn")
		}
		return b.opts.PTY.Spawn(a.str("command"), int(a.integer("cols")), int(a.integer("rows")))
	case "pty_write":
		if b.opts.PTY == nil {
			return nil, unsupportedError("pty_write")
		}
		return nil, b.opts.PTY.Write(a.str("id"), a.str("text"))
	case "pty_read":
		if b.opts.PTY == nil {
			return nil, unsupportedError("pty_read")
		}
		return b.opts.PTY.Read(a.str("id"))
	case "pty_kill":
		if b.opts.PTY == nil {
			return nil, unsupportedError("pty_kill")
		}
		return nil, b.opts.PTY.Kill(a.str("id"))
	case "pty_list":
		if b.opts.PTY == nil {
			return nil, unsupportedError("pty_list")
		}
		return b.opts.PTY.List()
	}
	if reason, ok := unsupported[name]; ok {
		return nil, fmt.Errorf("not supported in the Go build: %s", reason)
	}
	return nil, fmt.Errorf("unknown command %q", name)
}

// getState reproduces the Tauri payload: active sid, its last ready frame and
// the release channel.
func (b *bridge) getState() map[string]any {
	b.mu.Lock()
	sid := b.active
	ready := b.ready[sid]
	b.mu.Unlock()
	channel := strings.TrimSpace(os.Getenv("ARGENT_RELEASE_CHANNEL"))
	if channel == "" {
		channel = "stable"
	}
	var readyValue any
	if ready != nil {
		readyValue = ready
	}
	return map[string]any{"sid": sid, "ready": readyValue, "channel": channel}
}

// searchSessions delegates to the configured store; an empty query is cheap to
// answer locally, like the Rust build did.
func (b *bridge) searchSessions(query string, limit int) ([]SearchHit, error) {
	if strings.TrimSpace(query) == "" {
		return []SearchHit{}, nil
	}
	if b.opts.Sessions == nil {
		return nil, fmt.Errorf("not supported in the Go build: no session store configured")
	}
	if limit > 100 {
		limit = 100
	}
	hits, err := b.opts.Sessions.Search(strings.TrimSpace(query), limit)
	if err != nil {
		return nil, err
	}
	if hits == nil {
		hits = []SearchHit{}
	}
	for i := range hits {
		hits[i].Snippet = redactSecrets(hits[i].Snippet)
	}
	return hits, nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
}
